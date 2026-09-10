package operation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

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

	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	operation := domain.Operation{
		ID: "write-1", Kind: domain.OperationWriteChapter, Target: target,
		State: domain.OperationQueued, RunID: createEngineTestRun(t, ctx, authorityStore, target.ID, now),
		Snapshot: engineSnapshot(input, 1, domain.ApprovalAuto),
		Input:    input, CreatedAt: now, UpdatedAt: now,
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

func (e *neverExecutor) Identity() string { return testExecutor }

func (e *neverExecutor) Execute(context.Context, domain.Operation) (domain.OperationOutcome, error) {
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
			input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
			operation := domain.Operation{
				ID: "write-constrained", Kind: domain.OperationWriteChapter, Target: target,
				State: domain.OperationQueued,
				RunID: createEngineTestRun(t, ctx, authorityStore, target.ID, now), Snapshot: engineSnapshot(input, 1, domain.ApprovalAuto),
				Input: input, CreatedAt: now, UpdatedAt: now,
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
		ID: "chapter-2", PlanNodeID: "chapter-plan-2", Number: 2, Title: "第二章", Author: domain.AuthorAI,
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
	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	operation := domain.Operation{
		ID: "long-call", Kind: domain.OperationWriteChapter,
		Target:   domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"},
		State:    domain.OperationQueued,
		RunID:    createEngineTestRun(t, ctx, authorityStore, "book-1", now),
		Snapshot: engineSnapshot(input, 1, domain.ApprovalManual),
		Input:    input, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimNextOperationForExecutor(ctx, "worker-1", testExecutor, lease, now)
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

// 引擎不再加载 Execution Profile（D45）：执行器按快照自行取配置，这里的
// ConfigDigest 无需对应任何记录。
const testExecutor = "test.executor@1"

func engineSnapshot(input json.RawMessage, base domain.Revision, policy domain.ApprovalPolicy) domain.ExecutionSnapshot {
	return domain.ExecutionSnapshot{
		Executor: testExecutor, BaseRevision: base, InputDigest: domain.Digest(input),
		ConfigDigest: "profile", ApprovalPolicy: policy,
	}
}

type manuscriptExecutor struct{}

func (manuscriptExecutor) Identity() string { return testExecutor }

func (manuscriptExecutor) Execute(_ context.Context, operation domain.Operation) (domain.OperationOutcome, error) {
	chapter, err := json.Marshal(domain.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门", Author: domain.AuthorAI,
		Blocks: []domain.ManuscriptBlock{{ID: "chapter-1-block-1", Text: "他在山门前作出选择。"}},
	})
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	canon, err := json.Marshal(domain.CanonFact{
		ID: "chapter-1-outcome", Kind: domain.CanonEvent, SubjectID: "hero",
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
		{domain.DocumentRef{Kind: domain.DocumentEntity, ID: "hero"}, domain.Entity{ID: "hero", Kind: domain.EntityCharacter, Name: "主角"}},
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
		Goal: domain.NovelGoal{Premise: "测试创作", TargetChapters: 3}.Goal(),
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

// artifactExecutor 只产出工件：先发布对象，再交元数据；crashAfterPublish 让首次
// 执行在对象落盘后、元数据提交前失败，模拟崩溃窗口。
type artifactExecutor struct {
	store             *store.Store
	crashAfterPublish bool
	calls             int
	basis             domain.EvidenceBasis
}

func (artifactExecutor) Identity() string { return testExecutor }

func (e *artifactExecutor) Execute(_ context.Context, operation domain.Operation) (domain.OperationOutcome, error) {
	e.calls++
	writer, err := e.store.NewArtifactWriter(operation.ID, operation.Attempt)
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	writer.Write([]byte("封面"))
	digest, size, err := writer.Publish()
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	if e.crashAfterPublish && e.calls == 1 {
		return domain.OperationOutcome{}, errors.New("crashed after publishing the object")
	}
	basis := e.basis
	if basis.Equal(domain.EvidenceBasis{}) {
		basis, err = domain.OperationBasis(operation)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
	}
	return domain.OperationOutcome{Artifacts: []domain.Artifact{{
		ID: domain.ArtifactID(operation.ID, "cover"), ProjectID: operation.Target.ID, Digest: digest, MediaType: "image/png",
		Size: size, Basis: basis, OperationID: operation.ID, Attempt: operation.Attempt, CreatedAt: operation.CreatedAt,
	}}}, nil
}

// TestArtifactWithUntruthfulBasisFails 守护 D48：产出声明的基线必须在启动快照上
// 属实，引用不存在的文档即失败，不落元数据。
func TestArtifactWithUntruthfulBasisFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	operation := createAssetOperation(t, ctx, authorityStore, now)
	executor := &artifactExecutor{store: authorityStore, basis: domain.EvidenceBasis{
		Documents: []domain.DocumentBasis{{Ref: domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, Revision: 1}, {Ref: domain.DocumentRef{Kind: domain.DocumentEntity, ID: "missing"}, Revision: 1}},
	}}
	result, err := NewEngine(authorityStore).RunNext(ctx, executor, "worker-1", time.Minute, now.Add(time.Minute))
	if !errors.Is(err, domain.ErrInvalid) || result.Operation.State != domain.OperationFailed {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if _, err := authorityStore.GetArtifact(ctx, domain.ArtifactID(operation.ID, "cover")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("artifact metadata must not persist, got %v", err)
	}
}

func createAssetOperation(t *testing.T, ctx context.Context, authorityStore *store.Store, now time.Time) domain.Operation {
	t.Helper()
	intent, _ := json.Marshal(domain.Intent{Premise: "封面生成"})
	commitUserChange(t, ctx, authorityStore, domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}, "seed-asset", now, domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, Operation: domain.PatchPut, Content: intent})
	input := json.RawMessage(`{"target":{"kind":"intent","id":"root"},"role":"cover","basis":{"documents":[{"ref":{"kind":"intent","id":"root"},"revision":1}]}}`)
	operation, err := authorityStore.CreateOperation(ctx, domain.Operation{
		ID: "asset-1", Kind: domain.OperationGenerateAsset,
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"},
		State:  domain.OperationQueued, RunID: createEngineTestRun(t, ctx, authorityStore, "book-1", now),
		Snapshot: engineSnapshot(input, 1, domain.ApprovalAuto),
		Input:    input, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create asset operation: %v", err)
	}
	return operation
}

func TestArtifactOnlyOutcomeConcludesSucceeded(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	operation := createAssetOperation(t, ctx, authorityStore, now)
	executor := &artifactExecutor{store: authorityStore}
	result, err := NewEngine(authorityStore).RunNext(ctx, executor, "worker-1", time.Minute, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Operation.State != domain.OperationSucceeded || len(result.Artifacts) != 1 || result.Proposal.ID != "" {
		t.Fatalf("result = %#v", result)
	}
	stored, err := authorityStore.GetArtifact(ctx, domain.ArtifactID(operation.ID, "cover"))
	if err != nil || stored.Attempt != 1 || stored.Digest != result.Artifacts[0].Digest {
		t.Fatalf("stored artifact = %#v, %v", stored, err)
	}
	if content, err := authorityStore.ReadArtifact(stored.Digest); err != nil || string(content) != "封面" {
		t.Fatalf("object = %q, %v", content, err)
	}
}

func TestCrashAfterArtifactPublishBeforeMetadataCommitRecoversByReexecution(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	operation := createAssetOperation(t, ctx, authorityStore, now)
	executor := &artifactExecutor{store: authorityStore, crashAfterPublish: true}
	engine := NewEngine(authorityStore)
	if result, err := engine.RunNext(ctx, executor, "worker-1", time.Minute, now.Add(time.Minute)); err == nil || result.Operation.State != domain.OperationFailed {
		t.Fatalf("first run = %#v, %v; want failed after crash", result, err)
	}
	if _, err := authorityStore.GetArtifact(ctx, domain.ArtifactID(operation.ID, "cover")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("metadata must not exist after crash: %v", err)
	}
	if _, err := authorityStore.TransitionOperation(ctx, operation.ID, domain.OperationFailed, domain.OperationQueued, "resume", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	result, err := engine.RunNext(ctx, executor, "worker-1", time.Minute, now.Add(3*time.Minute))
	if err != nil || result.Operation.State != domain.OperationSucceeded || executor.calls != 2 {
		t.Fatalf("second run = %#v, %v, calls = %d", result, err, executor.calls)
	}
	stored, err := authorityStore.GetArtifact(ctx, domain.ArtifactID(operation.ID, "cover"))
	if err != nil || stored.Attempt != 2 {
		t.Fatalf("stored artifact = %#v, %v", stored, err)
	}
	// 同内容重发布落到同一对象：崩溃不留下第二份副本。
	if content, err := authorityStore.ReadArtifact(stored.Digest); err != nil || string(content) != "封面" {
		t.Fatalf("object = %q, %v", content, err)
	}
}

// commitUserChange 以用户身份在当前 Revision 上提交补丁，返回新 Revision。
func commitUserChange(t *testing.T, ctx context.Context, authorityStore *store.Store, target domain.AuthorityTarget, id string, now time.Time, patches ...domain.Patch) domain.Revision {
	t.Helper()
	engine := change.New(authorityStore)
	current, err := authorityStore.CurrentRevision(ctx, target)
	if errors.Is(err, store.ErrNotFound) {
		current = domain.InitialRevision
	} else if err != nil {
		t.Fatalf("current revision: %v", err)
	}
	proposal, err := engine.Prepare(ctx, domain.Proposal{
		ID: id, Target: target, BaseRevision: current, Author: domain.Author{Kind: domain.AuthorUser, ID: "user-1"},
		Reason: id, Patches: patches, ApprovalState: domain.ApprovalPending, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("prepare %s: %v", id, err)
	}
	proposal, err = change.Decide(proposal, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, now)
	if err != nil {
		t.Fatalf("approve %s: %v", id, err)
	}
	committed, err := engine.Commit(ctx, proposal)
	if err != nil {
		t.Fatalf("commit %s: %v", id, err)
	}
	return committed.NewRevision
}

func lockPatch(t *testing.T, ref domain.DocumentRef) domain.Patch {
	t.Helper()
	content, err := json.Marshal(domain.OwnershipRule{Target: ref, Control: domain.ControlLocked})
	if err != nil {
		t.Fatalf("marshal ownership: %v", err)
	}
	return domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentOwnership, ID: ref.Key()}, Operation: domain.PatchPut, Content: content}
}

// TestFinalizeRelocatesControlOnlyDrift：提案已落盘、收尾前用户锁定了不相干设定，
// 提案按 D51 重定位到当前 Revision 提交；任务不作废，启动快照保持原值。
func TestFinalizeRelocatesControlOnlyDrift(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	intent, _ := json.Marshal(domain.Intent{Premise: "凡人修仙"})
	commitUserChange(t, ctx, authorityStore, target, "seed", now, domain.Patch{
		Document: domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, Operation: domain.PatchPut, Content: intent,
	})
	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	operation := domain.Operation{
		ID: "write-1", Kind: domain.OperationWriteChapter, Target: target,
		State: domain.OperationQueued, RunID: createEngineTestRun(t, ctx, authorityStore, target.ID, now),
		Snapshot: engineSnapshot(input, 1, domain.ApprovalAuto),
		Input:    input, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	running, err := authorityStore.ClaimNextOperation(ctx, "worker-before-crash", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	planContent, _ := json.Marshal(domain.PlanNode{ID: "vol-1", Kind: domain.PlanVolume, Title: "第一卷", Summary: "凡人踏入修行"})
	if _, err := change.New(authorityStore).Prepare(ctx, domain.Proposal{
		ID: running.ID + "-proposal", OperationID: running.ID, Target: target, BaseRevision: 1,
		Author: domain.Author{Kind: domain.AuthorAI, ID: "writer.compose@1"}, Reason: "agent candidate",
		Patches:       []domain.Patch{{Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "vol-1"}, Operation: domain.PatchPut, Content: planContent}},
		ApprovalState: domain.ApprovalPending, CreatedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("prepare operation proposal: %v", err)
	}
	// 等待收尾期间用户锁定 Intent：用户专属变化，Revision 2。
	commitUserChange(t, ctx, authorityStore, target, "lock-intent", now.Add(3*time.Second), lockPatch(t, domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}))
	if _, err := authorityStore.RecoverExpiredOperations(ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("recover crashed operation: %v", err)
	}
	executor := &neverExecutor{}
	result, err := NewEngine(authorityStore).RunNext(ctx, executor, "worker-after-crash", time.Minute, now.Add(3*time.Minute))
	if err != nil || executor.called {
		t.Fatalf("resume: %v, executed again = %v", err, executor.called)
	}
	if result.Operation.State != domain.OperationSucceeded || result.ChangeSet == nil ||
		result.ChangeSet.BaseRevision != 2 || result.ChangeSet.NewRevision != 3 || result.Operation.Snapshot.BaseRevision != 1 {
		t.Fatalf("result = %#v", result)
	}
	stored, err := authorityStore.GetProposalByOperation(ctx, running.ID)
	if err != nil || stored.BaseRevision != 2 || stored.ApprovalState != domain.ApprovalApproved {
		t.Fatalf("stored proposal = %#v, %v", stored, err)
	}
}

// driftingExecutor 在执行期间锁定设定（可重定位），又在合规分析期间改动实体（不可重定位）。
type driftingExecutor struct {
	passingManuscriptExecutor
	t      *testing.T
	store  *store.Store
	target domain.AuthorityTarget
	now    time.Time
}

func (e *driftingExecutor) Execute(ctx context.Context, operation domain.Operation) (domain.OperationOutcome, error) {
	commitUserChange(e.t, ctx, e.store, e.target, "lock-intent", e.now, lockPatch(e.t, domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}))
	return e.passingManuscriptExecutor.Execute(ctx, operation)
}

func (e *driftingExecutor) AnalyzeSemanticCompliance(
	ctx context.Context,
	operation domain.Operation,
	proposal domain.Proposal,
	constraints []domain.OwnershipRule,
) (domain.SemanticComplianceReport, error) {
	hero, _ := json.Marshal(domain.Entity{ID: "hero", Kind: domain.EntityCharacter, Name: "主角二号"})
	commitUserChange(e.t, ctx, e.store, e.target, "rename-hero", e.now, domain.Patch{
		Document: domain.DocumentRef{Kind: domain.DocumentEntity, ID: "hero"}, Operation: domain.PatchPut, Content: hero,
	})
	return e.passingManuscriptExecutor.AnalyzeSemanticCompliance(ctx, operation, proposal, constraints)
}

// TestCommitConflictAfterRelocationGoesStale：重定位之后、落库之前又有内容提交，
// 任务转 stale（后继继承工作区）而不是 failed。
func TestCommitConflictAfterRelocationGoesStale(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-constraints"}
	seedConstrainedProject(t, ctx, authorityStore, target, now)
	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	if _, err := authorityStore.CreateOperation(ctx, domain.Operation{
		ID: "write-constrained", Kind: domain.OperationWriteChapter, Target: target, State: domain.OperationQueued,
		RunID: createEngineTestRun(t, ctx, authorityStore, target.ID, now), Snapshot: engineSnapshot(input, 1, domain.ApprovalAuto),
		Input: input, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	executor := &driftingExecutor{t: t, store: authorityStore, target: target, now: now.Add(time.Second)}
	result, err := NewEngine(authorityStore).RunNext(ctx, executor, "worker-1", time.Minute, now.Add(time.Minute))
	if !errors.Is(err, store.ErrRevisionConflict) || result.Operation.State != domain.OperationStale {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if revision, err := authorityStore.CurrentRevision(ctx, target); err != nil || revision != 3 {
		t.Fatalf("revision = %d, %v; the stale task must not commit", revision, err)
	}
}
