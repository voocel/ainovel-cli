package operation

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestRunNextRecoversCommittedProposalWithoutExecutingAgain(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	changeEngine := change.New(authorityStore)
	seedContent, _ := json.Marshal(domain.Intent{Premise: "凡人修仙"})
	seed := domain.Proposal{
		ID: "seed", Target: target, Author: domain.Author{Kind: domain.AuthorUser, ID: "user-1"},
		Reason: "seed", Patches: []domain.Patch{{
			Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
			Operation: domain.PatchPut, Content: seedContent,
		}}, ApprovalState: domain.ApprovalPending, CreatedAt: now,
	}
	seed, err = changeEngine.Prepare(ctx, seed)
	if err != nil {
		t.Fatalf("prepare seed: %v", err)
	}
	seed, err = change.Decide(seed, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, now)
	if err != nil {
		t.Fatalf("approve seed: %v", err)
	}
	if _, err := changeEngine.Commit(ctx, seed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	worker, err := prompt.BuiltinWorkerProfile("writer.compose")
	if err != nil {
		t.Fatalf("load writer capability: %v", err)
	}
	compiled, err := prompt.NewRegistry(authorityStore).Reload(ctx, prompt.CompileRequest{
		ProjectID: "book-1", CoreProtocolVersion: "core-v1", Worker: worker,
		Intent: domain.Intent{Premise: "凡人修仙"}, StoryContext: json.RawMessage(`{"revision":1}`),
		Task: json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`), BaseRevision: 1, ProjectOverlayRevision: 1,
		ModelConfigDigest: "model", ApprovalPolicy: domain.ApprovalAuto, ApprovalPolicyDigest: "auto",
	}, now)
	if err != nil {
		t.Fatalf("compile execution profile: %v", err)
	}
	operation := domain.Operation{
		ID: "write-1", Kind: domain.OperationWriteChapter, Target: target,
		State: domain.OperationQueued, RunID: createEngineTestRun(t, ctx, authorityStore, target.ID, now),
		Snapshot: compiled.Snapshot,
		Input:    json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`), CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	running, err := authorityStore.ClaimNextOperation(ctx, "worker-before-crash", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	planContent, _ := json.Marshal(domain.PlanNode{
		ID: "vol-1", Kind: domain.PlanVolume, Order: 0, Title: "第一卷", Summary: "凡人踏入修行",
	})
	proposal := domain.Proposal{
		ID: running.ID + "-proposal", OperationID: running.ID,
		Target: target, BaseRevision: 1,
		Author: domain.Author{Kind: domain.AuthorAI, ID: "writer.compose@1"},
		Reason: "agent candidate", Patches: []domain.Patch{{
			Document:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: "vol-1"},
			Operation: domain.PatchPut, Content: planContent,
		}}, ApprovalState: domain.ApprovalPending, CreatedAt: now.Add(2 * time.Second),
	}
	proposal, err = changeEngine.Prepare(ctx, proposal)
	if err != nil {
		t.Fatalf("prepare operation proposal: %v", err)
	}
	proposal, err = change.Decide(
		proposal, domain.ApprovalApproved,
		domain.Author{Kind: domain.AuthorSystem, ID: "approval-policy:auto"}, now.Add(3*time.Second),
	)
	if err != nil {
		t.Fatalf("approve operation proposal: %v", err)
	}
	if _, err := changeEngine.Commit(ctx, proposal); err != nil {
		t.Fatalf("commit operation proposal: %v", err)
	}
	if _, err := authorityStore.RecoverExpiredOperations(ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("recover crashed operation: %v", err)
	}

	executor := &neverExecutor{}
	result, err := NewEngine(authorityStore).RunNext(ctx, executor, "worker-after-crash", time.Minute, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("resume operation: %v", err)
	}
	if executor.called {
		t.Fatal("executor ran again after its proposal was already committed")
	}
	if result.Operation.State != domain.OperationSucceeded || result.ChangeSet == nil || result.ChangeSet.NewRevision != 2 {
		t.Fatalf("result = %#v", result)
	}
}

type neverExecutor struct {
	called bool
}

func (e *neverExecutor) ModelConfigDigest() string { return "model" }

func (e *neverExecutor) Execute(context.Context, domain.Operation, prompt.Compiled) (domain.OperationOutcome, error) {
	e.called = true
	panic("executor must not run when the operation proposal is already committed")
}

func TestAutoApprovalRequiresIndependentSemanticComplianceForConstrainedStory(t *testing.T) {
	for _, test := range []struct {
		name      string
		executor  Executor
		wantState domain.OperationState
		wantRev   domain.Revision
		wantCheck domain.SemanticComplianceStatus
	}{
		{name: "analyzer unavailable", executor: manuscriptExecutor{}, wantState: domain.OperationAwaitingApproval, wantRev: 1, wantCheck: domain.SemanticComplianceUnavailable},
		{name: "analyzer passes", executor: passingManuscriptExecutor{}, wantState: domain.OperationSucceeded, wantRev: 2, wantCheck: domain.SemanticCompliancePass},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
			authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer authorityStore.Close()
			target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-constraints"}
			seedConstrainedProject(t, ctx, authorityStore, target, now)
			worker, err := prompt.BuiltinWorkerProfile("writer.compose")
			if err != nil {
				t.Fatalf("load writer capability: %v", err)
			}
			compiled, err := prompt.NewRegistry(authorityStore).Reload(ctx, prompt.CompileRequest{
				ProjectID: target.ID, CoreProtocolVersion: "core-v1", Worker: worker,
				Intent: domain.Intent{Premise: "凡人守住底线"},
				Ownership: []domain.OwnershipRule{{
					Target: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-bottom-line"}, Control: domain.ControlLocked,
				}},
				StoryContext: json.RawMessage(`{"revision":1}`),
				Task:         json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
				BaseRevision: 1, ProjectOverlayRevision: 1, ModelConfigDigest: "model",
				ApprovalPolicy: domain.ApprovalAuto, ApprovalPolicyDigest: "auto",
			}, now)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			operation := domain.Operation{
				ID: "write-constrained", Kind: domain.OperationWriteChapter, Target: target,
				State: domain.OperationQueued,
				RunID: createEngineTestRun(t, ctx, authorityStore, target.ID, now), Snapshot: compiled.Snapshot,
				Input: json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`), CreatedAt: now, UpdatedAt: now,
			}
			if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
				t.Fatalf("create operation: %v", err)
			}
			result, err := NewEngine(authorityStore).RunNext(ctx, test.executor, "worker-1", time.Minute, now.Add(time.Minute))
			if err != nil {
				t.Fatalf("run operation: %v", err)
			}
			if result.Operation.State != test.wantState {
				t.Fatalf("state = %s, want %s", result.Operation.State, test.wantState)
			}
			var report domain.SemanticComplianceReport
			if err := json.Unmarshal(result.Proposal.Impact.Compliance, &report); err != nil {
				t.Fatalf("decode compliance report: %v", err)
			}
			if report.Status != test.wantCheck {
				t.Fatalf("semantic status = %s, want %s", report.Status, test.wantCheck)
			}
			revision, err := authorityStore.CurrentRevision(ctx, target)
			if err != nil || revision != test.wantRev {
				t.Fatalf("revision = %d, %v; want %d", revision, err, test.wantRev)
			}
		})
	}
}

func TestMilestoneProposalClassification(t *testing.T) {
	volume, err := json.Marshal(domain.PlanNode{ID: "volume-2", Kind: domain.PlanVolume, Title: "远行", Summary: "进入新阶段"})
	if err != nil {
		t.Fatalf("marshal volume: %v", err)
	}
	chapter, err := json.Marshal(domain.PlanNode{ID: "chapter-plan-2", Kind: domain.PlanChapter, ParentID: "arc-1", Title: "第二章", Summary: "继续前行"})
	if err != nil {
		t.Fatalf("marshal chapter: %v", err)
	}
	if !milestoneProposal(domain.Proposal{Patches: []domain.Patch{{
		Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "volume-2"}, Operation: domain.PatchPut, Content: volume,
	}}}) {
		t.Fatal("volume change was not classified as a milestone")
	}
	if milestoneProposal(domain.Proposal{Patches: []domain.Patch{{
		Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "chapter-plan-2"}, Operation: domain.PatchPut, Content: chapter,
	}}}) {
		t.Fatal("chapter-only plan change was classified as a milestone")
	}
	if !milestoneProposal(domain.Proposal{Patches: []domain.Patch{{
		Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-state"}, Operation: domain.PatchDelete,
	}}}) {
		t.Fatal("canon change was not classified as a milestone")
	}

	// 例行推进：章节正文与随章 Canon Delta 不构成 milestone，否则中间档塌缩为 manual。
	chapterContent, err := json.Marshal(domain.ManuscriptChapter{
		ID: "chapter-2", PlanNodeID: "chapter-plan-2", Number: 2, Title: "第二章",
		Blocks: []domain.ManuscriptBlock{{ID: "p-1", Text: "旅程继续。"}},
	})
	if err != nil {
		t.Fatalf("marshal chapter content: %v", err)
	}
	routineDelta, err := json.Marshal(domain.CanonFact{
		ID: "hero-position", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.position",
		Value: json.RawMessage(`"官道"`), SourceChapterID: "chapter-2",
	})
	if err != nil {
		t.Fatalf("marshal routine delta: %v", err)
	}
	if milestoneProposal(domain.Proposal{Patches: []domain.Patch{
		{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-2"}, Operation: domain.PatchPut, Content: chapterContent},
		{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-position"}, Operation: domain.PatchPut, Content: routineDelta},
	}}) {
		t.Fatal("routine chapter canon delta was classified as a milestone")
	}
	// 不随章的独立 Canon 修改仍是 milestone。
	standalone, err := json.Marshal(domain.CanonFact{
		ID: "world-rule-1", Kind: domain.CanonWorldRule, SubjectID: "world", Predicate: "rule.magic",
		Value: json.RawMessage(`"灵气复苏"`),
	})
	if err != nil {
		t.Fatalf("marshal standalone canon: %v", err)
	}
	if !milestoneProposal(domain.Proposal{Patches: []domain.Patch{{
		Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "world-rule-1"}, Operation: domain.PatchPut, Content: standalone,
	}}}) {
		t.Fatal("standalone canon change was not classified as a milestone")
	}
}

func TestLongCallRenewsOperationLease(t *testing.T) {
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	now := time.Now().UTC()
	lease := 300 * time.Millisecond
	operation := domain.Operation{
		ID: "long-call", Kind: domain.OperationWriteChapter,
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"},
		State:  domain.OperationQueued,
		RunID:  createEngineTestRun(t, ctx, authorityStore, "book-1", now),
		Snapshot: domain.ExecutionSnapshot{
			ExecutionProfileDigest: "profile", BaseRevision: 1,
			CoreProtocolVersion: "core-v1", WorkerProfileVersion: "writer.compose@1",
			ToolSchemaDigest: "tools", PromptDigest: "prompt", ModelConfigDigest: "model",
			ApprovalPolicy: domain.ApprovalManual, ApprovalPolicyDigest: "manual",
		},
		Input: json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`), CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimNextOperationForModel(ctx, "worker-1", "model", lease, now)
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	if err := NewEngine(authorityStore).withLease(ctx, operation, "worker-1", lease, func(context.Context) error {
		time.Sleep(450 * time.Millisecond)
		return nil
	}); err != nil {
		t.Fatalf("run with lease: %v", err)
	}
	stored, err := authorityStore.GetOperation(ctx, operation.ID)
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if stored.LeaseUntil == nil || !stored.LeaseUntil.After(operation.LeaseUntil.Add(100*time.Millisecond)) {
		t.Fatalf("lease was not renewed: initial=%v current=%v", operation.LeaseUntil, stored.LeaseUntil)
	}
}

type manuscriptExecutor struct{}

func (manuscriptExecutor) ModelConfigDigest() string { return "model" }

func (manuscriptExecutor) Execute(_ context.Context, operation domain.Operation, _ prompt.Compiled) (domain.OperationOutcome, error) {
	chapter, err := json.Marshal(domain.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门",
		Blocks: []domain.ManuscriptBlock{{ID: "chapter-1-block-1", Text: "他在山门前作出选择。"}},
	})
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	canon, err := json.Marshal(domain.CanonFact{
		ID: "chapter-1-outcome", Kind: domain.CanonEvent, SubjectID: "chapter-1",
		Predicate: "event.chapter_outcome", Value: json.RawMessage(`"通过山门选择"`), SourceChapterID: "chapter-1",
	})
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	return domain.OperationOutcome{Proposal: &domain.Proposal{
		ID: operation.ID + "-proposal", OperationID: operation.ID,
		Target: operation.Target, BaseRevision: operation.Snapshot.BaseRevision,
		Author: domain.Author{Kind: domain.AuthorAI, ID: "writer.compose@1"},
		Reason: "write chapter", Patches: []domain.Patch{
			{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-1"}, Operation: domain.PatchPut, Content: chapter},
			{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "chapter-1-outcome"}, Operation: domain.PatchPut, Content: canon},
		}, ApprovalState: domain.ApprovalPending, CreatedAt: time.Date(2026, 8, 18, 12, 1, 1, 0, time.UTC),
	}}, nil
}

type passingManuscriptExecutor struct{ manuscriptExecutor }

func (passingManuscriptExecutor) AnalyzeSemanticCompliance(
	context.Context,
	domain.Operation,
	domain.Proposal,
	[]domain.OwnershipRule,
) (domain.SemanticComplianceReport, error) {
	return domain.SemanticComplianceReport{Status: domain.SemanticCompliancePass}, nil
}

func seedConstrainedProject(
	t *testing.T,
	ctx context.Context,
	authorityStore *store.Store,
	target domain.AuthorityTarget,
	now time.Time,
) {
	t.Helper()
	values := []struct {
		ref   domain.DocumentRef
		value any
	}{
		{domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, domain.Intent{Premise: "凡人守住底线"}},
		{domain.DocumentRef{Kind: domain.DocumentPlan, ID: "volume-1"}, domain.PlanNode{ID: "volume-1", Kind: domain.PlanVolume, Title: "入道", Summary: "进入山门"}},
		{domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-1"}, domain.PlanNode{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "接受考验"}},
		{domain.DocumentRef{Kind: domain.DocumentPlan, ID: "chapter-plan-1"}, domain.PlanNode{ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Title: "第一章", Summary: "抵达山门"}},
		{domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-bottom-line"}, domain.CanonFact{ID: "hero-bottom-line", Kind: domain.CanonWorldRule, SubjectID: "hero", Predicate: "rule.bottom_line", Value: json.RawMessage(`"不伤无辜"`)}},
		{domain.DocumentRef{Kind: domain.DocumentOwnership, ID: "canon:hero-bottom-line"}, domain.OwnershipRule{Target: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-bottom-line"}, Control: domain.ControlLocked}},
	}
	patches := make([]domain.Patch, 0, len(values))
	for _, value := range values {
		content, err := json.Marshal(value.value)
		if err != nil {
			t.Fatalf("marshal seed %s: %v", value.ref.Key(), err)
		}
		patches = append(patches, domain.Patch{Document: value.ref, Operation: domain.PatchPut, Content: content})
	}
	engine := change.New(authorityStore)
	proposal, err := engine.Prepare(ctx, domain.Proposal{
		ID: "seed-constraints", Target: target, Author: domain.Author{Kind: domain.AuthorUser, ID: "user-1"},
		Reason: "seed", Patches: patches, ApprovalState: domain.ApprovalPending, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("prepare seed: %v", err)
	}
	proposal, err = change.Decide(proposal, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, now)
	if err != nil {
		t.Fatalf("approve seed: %v", err)
	}
	if _, err := engine.Commit(ctx, proposal); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

func createEngineTestRun(
	t *testing.T,
	ctx context.Context,
	authorityStore *store.Store,
	projectID string,
	now time.Time,
) string {
	t.Helper()
	run := domain.CreationRun{
		ID: "run:" + projectID, ProjectID: projectID,
		Goal: domain.CreationRunGoal{Premise: "测试创作", TargetChapters: 3},
		Strategy: domain.CreationRunStrategy{
			PlanWindowChapters: 3, ReviewCadence: domain.ReviewPerPlanWindow, AutoRepairBudget: 3,
		},
		Preset: domain.CreationRunPreset{
			Source: "test", Digest: "test-preset", Approval: domain.ApprovalAuto,
		},
		State: domain.RunRunning, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateCreationRun(ctx, run); err != nil {
		t.Fatalf("create operation test run: %v", err)
	}
	return run.ID
}
