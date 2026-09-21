package decision

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// stubAnalyzer 返回预设的语义影响报告；用例在建提案前改 report。
type stubAnalyzer struct{ report change.SemanticImpactReport }

func (a *stubAnalyzer) Analyze(context.Context, model.Proposal, change.StructuralImpact) (json.RawMessage, error) {
	return json.Marshal(a.report)
}

// boundLLM 只让 task.Manager.HasLLM 为真，不执行任务。
type boundLLM struct{}

func (boundLLM) Identity() string { return "llm.agent@1" }
func (boundLLM) Execute(context.Context, model.Operation) (model.OperationOutcome, error) {
	return model.OperationOutcome{}, errors.New("not executed in decision tests")
}

func conflictReport() change.SemanticImpactReport {
	return change.SemanticImpactReport{
		Status: change.SemanticImpactConflict,
		Findings: []change.SemanticImpactFinding{{
			Document: &model.DocumentRef{Kind: model.DocumentManuscript, ID: "chapter-1"}, Explanation: "第一章已经用行动落实了旧底线",
		}},
		Options: []change.ResolutionOption{
			{Strategy: change.ResolutionRewriteAffected, ChapterIDs: []string{"chapter-1"}, Explanation: "同步重写第一章"},
			{Strategy: change.ResolutionReinterpretFuture, Explanation: "保留旧章并在后文解释变化"},
			{Strategy: change.ResolutionAbandon, Explanation: "放弃本次事实变更"},
		},
	}
}

type reviewFixture struct {
	ctx      context.Context
	store    *store.Store
	projects *projectdoc.Repository
	review   *Review
	analyzer *stubAnalyzer
	now      time.Time
}

const (
	fixtureProject = "book"
	fixtureRun     = "run-1"
	fixtureUser    = "user-1"
)

// newReviewFixture 建库并种一部作品：revision 1 是设定，revision 2 加入用户写的第一章，
// 另有一个 running 的创作 Run 供受影响重写归属。
func newReviewFixture(t *testing.T, withModel bool) *reviewFixture {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "decision.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	analyzer := &stubAnalyzer{report: conflictReport()}
	changes := change.NewWithSemanticAnalyzer(s, analyzer)
	projects := projectdoc.New(s, changes)
	var executors task.ExecutorSet
	if withModel {
		executors.LLM = boundLLM{}
	}
	f := &reviewFixture{
		ctx: ctx, store: s, projects: projects, analyzer: analyzer,
		review: New(s, changes, projects, task.New(s, nil, executors, nil)),
		now:    time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC),
	}
	if _, err := projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
		ProjectID: fixtureProject, ChangeID: "create", UserID: fixtureUser, Reason: "创建作品", CreatedAt: f.now,
		Draft: projectdoc.ProjectDraft{
			Intent: model.Intent{Premise: "一个凡人进入修仙宗门"},
			Plan: []model.PlanNode{
				{ID: "volume-1", Kind: model.PlanVolume, Title: "入道", Summary: "进入修行世界"},
				{ID: "arc-1", Kind: model.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "通过山门考验"},
				{ID: "chapter-plan-1", Kind: model.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "抵达山门"},
			},
			Entities: []model.Entity{{ID: "hero", Kind: model.EntityCharacter, Name: "主角"}},
			Canon: []model.CanonFact{{
				ID: "hero-bottom-line", Kind: model.CanonWorldRule, SubjectID: "hero", Predicate: "rule.bottom_line", Value: json.RawMessage(`"绝不滥杀"`),
			}},
		},
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	f.commitEdit(t, "add-chapter", func(projection *projectdoc.ProjectProjection) {
		projection.Manuscript = append(projection.Manuscript, model.ManuscriptChapter{
			ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "第一章", Author: model.AuthorUser,
			Blocks: []model.ManuscriptBlock{{ID: "block-1", Text: "他在山门前放下了刀。"}},
		})
	})
	if _, err := s.CreateCreationRun(ctx, model.CreationRun{
		ID: fixtureRun, ProjectID: fixtureProject,
		Goal:     model.NovelGoal{Premise: "测试创作", TargetChapters: 3}.Goal(),
		Strategy: model.CreationRunStrategy{PlanWindowChapters: 3, ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: 3},
		Preset:   model.CreationRunPreset{Source: "test", Digest: "test-preset", Approval: model.ApprovalAuto},
		State:    model.RunRunning, CreatedAt: f.now, UpdatedAt: f.now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	return f
}

func (f *reviewFixture) export(t *testing.T) projectdoc.ProjectProjection {
	t.Helper()
	projection, err := f.projects.ExportProject(f.ctx, fixtureProject, 0)
	if err != nil {
		t.Fatalf("export project: %v", err)
	}
	return projection
}

// commitEdit 以用户身份直接提交一次改动，推进作品 Revision。
func (f *reviewFixture) commitEdit(t *testing.T, changeID string, edit func(*projectdoc.ProjectProjection)) {
	t.Helper()
	projection := f.export(t)
	edit(&projection)
	proposal, err := f.projects.ImportProject(f.ctx, changeID, fixtureUser, changeID, projection, f.now)
	if err != nil {
		t.Fatalf("import %s: %v", changeID, err)
	}
	if _, err := f.review.Approve(f.ctx, proposal.ID, fixtureUser, f.now); err != nil {
		t.Fatalf("approve %s: %v", changeID, err)
	}
}

// conflict 建一个改动角色底线、带语义冲突报告的待裁决提案。
func (f *reviewFixture) conflict(t *testing.T, id string) model.Proposal {
	t.Helper()
	projection := f.export(t)
	projection.Canon[0].Value = json.RawMessage(`"可以为目的伤害无辜"`)
	proposal, err := f.projects.ImportProjectWithSemantic(f.ctx, id, fixtureUser, "改变角色底线", projection, f.now)
	if err != nil {
		t.Fatalf("prepare semantic proposal: %v", err)
	}
	if proposal.BaseRevision != 2 || len(proposal.Impact.Semantic) == 0 {
		t.Fatalf("semantic proposal = %#v", proposal)
	}
	return proposal
}

func (f *reviewFixture) command(proposalID string, strategy change.ResolutionStrategy) ResolveProposalCommand {
	return ResolveProposalCommand{
		ProposalID: proposalID, UserID: fixtureUser, Strategy: string(strategy), Reason: " 放弃改动 ",
		RunID: fixtureRun, CreatedAt: f.now.Add(5 * time.Minute),
	}
}

func (f *reviewFixture) assertState(t *testing.T, proposalID string, state model.ApprovalState, revision model.Revision) {
	t.Helper()
	stored, err := f.store.GetProposal(f.ctx, proposalID)
	if err != nil || stored.ApprovalState != state {
		t.Fatalf("proposal state = %q (%v), want %q", stored.ApprovalState, err, state)
	}
	current, err := f.store.CurrentRevision(f.ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: fixtureProject})
	if err != nil || current != revision {
		t.Fatalf("current revision = %d (%v), want %d", current, err, revision)
	}
}

// rewriteOperation 预先登记受影响重写任务，模拟上一次裁决已建任务但调用方重试。
func (f *reviewFixture) rewriteOperation(t *testing.T, proposalID string, input model.RewriteAffectedInput, baseRevision model.Revision) model.Operation {
	t.Helper()
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := f.store.CreateOperation(f.ctx, model.Operation{
		ID: proposalID + ":rewrite-affected", Kind: model.OperationRewriteAffected,
		Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: fixtureProject},
		State:  model.OperationQueued, RunID: fixtureRun, Input: payload,
		Snapshot: model.ExecutionSnapshot{
			Executor: "llm.agent@1", BaseRevision: baseRevision, InputDigest: model.Digest(payload),
			ConfigDigest: "profile", ApprovalPolicy: model.ApprovalManual,
		},
		CreatedAt: f.now, UpdatedAt: f.now,
	})
	if err != nil {
		t.Fatalf("create rewrite operation: %v", err)
	}
	return operation
}

func TestResolveProposalRejectsIncompleteCommand(t *testing.T) {
	f := newReviewFixture(t, true)
	proposal := f.conflict(t, "change-bottom-line")
	cases := map[string]struct {
		edit func(*ResolveProposalCommand)
		want error
	}{
		"missing proposal": {func(c *ResolveProposalCommand) { c.ProposalID = "" }, model.ErrInvalid},
		"missing user":     {func(c *ResolveProposalCommand) { c.UserID = "" }, model.ErrInvalid},
		"missing time":     {func(c *ResolveProposalCommand) { c.CreatedAt = time.Time{} }, model.ErrInvalid},
		"unknown proposal": {func(c *ResolveProposalCommand) { c.ProposalID = "ghost" }, model.ErrNotFound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			command := f.command(proposal.ID, change.ResolutionAbandon)
			tc.edit(&command)
			if _, err := f.review.ResolveProposal(f.ctx, command); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			f.assertState(t, proposal.ID, model.ApprovalPending, 2)
		})
	}
}

func TestResolveProposalRequiresSemanticConflict(t *testing.T) {
	f := newReviewFixture(t, true)
	plain, err := f.projects.ImportProject(f.ctx, "plain-edit", fixtureUser, "无语义报告", func() projectdoc.ProjectProjection {
		projection := f.export(t)
		projection.Plan[2].Summary = "冒雨抵达山门"
		return projection
	}(), f.now)
	if err != nil {
		t.Fatalf("prepare plain proposal: %v", err)
	}
	f.analyzer.report = change.SemanticImpactReport{Status: change.SemanticImpactConsistent}
	consistent := f.conflict(t, "consistent-edit")
	f.analyzer.report = conflictReport()
	conflict := f.conflict(t, "change-bottom-line")

	cases := map[string]struct {
		proposal string
		strategy change.ResolutionStrategy
		want     string
	}{
		"unknown strategy":          {conflict.ID, "merge", `unknown resolution strategy "merge"`},
		"proposal without report":   {plain.ID, change.ResolutionAbandon, "has no semantic impact report"},
		"consistent proposal":       {consistent.ID, change.ResolutionReinterpretFuture, "approve it directly"},
		"consistent proposal abort": {consistent.ID, change.ResolutionAbandon, "approve it directly"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.review.ResolveProposal(f.ctx, f.command(tc.proposal, tc.strategy))
			if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want ErrInvalid containing %q", err, tc.want)
			}
			f.assertState(t, tc.proposal, model.ApprovalPending, 2)
		})
	}
}

func TestResolveProposalAbandonRejectsWithReason(t *testing.T) {
	f := newReviewFixture(t, false)
	proposal := f.conflict(t, "change-bottom-line")
	command := f.command(proposal.ID, change.ResolutionAbandon)
	result, err := f.review.ResolveProposal(f.ctx, command)
	if err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if result.Strategy != "abandon" || result.ChangeSet != nil || len(result.Operations) != 0 || result.Proposal == nil {
		t.Fatalf("result = %#v", result)
	}
	rejected := *result.Proposal
	if rejected.ApprovalState != model.ApprovalRejected || rejected.DecisionReason != "放弃改动" ||
		rejected.DecidedBy == nil || rejected.DecidedBy.ID != fixtureUser || rejected.DecidedAt == nil || !rejected.DecidedAt.Equal(command.CreatedAt) {
		t.Fatalf("rejected proposal = %#v", rejected)
	}
	f.assertState(t, proposal.ID, model.ApprovalRejected, 2)
	if snapshot, err := f.projects.Project(f.ctx, fixtureProject, 0); err != nil || string(snapshot.Canon[0].Value) != `"绝不滥杀"` {
		t.Fatalf("canon after abandon = %s (%v)", snapshot.Canon[0].Value, err)
	}

	// 重复放弃幂等；放弃后再批准是状态冲突。
	again, err := f.review.ResolveProposal(f.ctx, command)
	if err != nil || again.Proposal == nil || again.Proposal.ApprovalState != model.ApprovalRejected || again.Proposal.DecisionReason != "放弃改动" {
		t.Fatalf("repeated abandon = %#v, %v", again, err)
	}
	if _, err := f.review.ResolveProposal(f.ctx, f.command(proposal.ID, change.ResolutionReinterpretFuture)); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("approve after reject: %v", err)
	}
}

func TestResolveProposalReinterpretFutureCommitsWithoutOperation(t *testing.T) {
	f := newReviewFixture(t, false)
	proposal := f.conflict(t, "change-bottom-line")
	result, err := f.review.ResolveProposal(f.ctx, f.command(proposal.ID, change.ResolutionReinterpretFuture))
	if err != nil {
		t.Fatalf("reinterpret future: %v", err)
	}
	if result.Strategy != "reinterpret_future" || result.Proposal != nil || len(result.Operations) != 0 || result.ChangeSet == nil {
		t.Fatalf("result = %#v", result)
	}
	if result.ChangeSet.ID != proposal.ID || result.ChangeSet.NewRevision != 3 || result.ChangeSet.ApprovalState != model.ApprovalApproved {
		t.Fatalf("change set = %#v", result.ChangeSet)
	}
	f.assertState(t, proposal.ID, model.ApprovalApproved, 3)
	snapshot, err := f.projects.Project(f.ctx, fixtureProject, 0)
	if err != nil || string(snapshot.Canon[0].Value) != `"可以为目的伤害无辜"` || string(snapshot.Canon[0].PreviousValue) != `"绝不滥杀"` {
		t.Fatalf("canon after commit = %#v (%v)", snapshot.Canon, err)
	}

	// 已批准的提案重复裁决返回同一 ChangeSet；再放弃是状态冲突。
	again, err := f.review.ResolveProposal(f.ctx, f.command(proposal.ID, change.ResolutionReinterpretFuture))
	if err != nil || again.ChangeSet == nil || again.ChangeSet.NewRevision != 3 {
		t.Fatalf("repeated resolution = %#v, %v", again, err)
	}
	if _, err := f.review.ResolveProposal(f.ctx, f.command(proposal.ID, change.ResolutionAbandon)); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("abandon after approve: %v", err)
	}
	f.assertState(t, proposal.ID, model.ApprovalApproved, 3)
}

func TestResolveProposalRewriteAffectedChecksPrerequisitesBeforeCommit(t *testing.T) {
	cases := map[string]struct {
		withModel bool
		edit      func(*ResolveProposalCommand)
		want      error
		message   string
	}{
		"no model":     {false, nil, model.ErrInvalid, "requires a configured model"},
		"no run":       {true, func(c *ResolveProposalCommand) { c.RunID = " " }, model.ErrInvalid, "requires a creation run"},
		"unknown pack": {true, func(c *ResolveProposalCommand) { c.Packs = []resource.PackRef{{ID: "ghost-pack", Revision: 1}} }, model.ErrNotFound, ""},
		"unknown profile": {true, func(c *ResolveProposalCommand) {
			c.CreatorProfiles = []resource.CreatorProfileRef{{ID: "ghost", Scope: "global", Revision: 1}}
		}, model.ErrNotFound, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newReviewFixture(t, tc.withModel)
			proposal := f.conflict(t, "change-bottom-line")
			command := f.command(proposal.ID, change.ResolutionRewriteAffected)
			if tc.edit != nil {
				tc.edit(&command)
			}
			_, err := f.review.ResolveProposal(f.ctx, command)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v, want %v containing %q", err, tc.want, tc.message)
			}
			f.assertState(t, proposal.ID, model.ApprovalPending, 2)
		})
	}
}

func TestResolveProposalRewriteAffectedAdoptsMatchingOperation(t *testing.T) {
	f := newReviewFixture(t, true)
	proposal := f.conflict(t, "change-bottom-line")
	existing := f.rewriteOperation(t, proposal.ID, model.RewriteAffectedInput{
		ChapterIDs: []string{"chapter-1"}, BaseRevision: 3, ResolutionProposalID: proposal.ID, Reason: "同步重写第一章",
	}, 3)
	result, err := f.review.ResolveProposal(f.ctx, f.command(proposal.ID, change.ResolutionRewriteAffected))
	if err != nil {
		t.Fatalf("rewrite affected: %v", err)
	}
	if result.ChangeSet == nil || result.ChangeSet.NewRevision != 3 || len(result.Operations) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := result.Operations[0]; got.ID != existing.ID || got.State != model.OperationQueued || got.RunID != fixtureRun {
		t.Fatalf("adopted operation = %#v", got)
	}
	f.assertState(t, proposal.ID, model.ApprovalApproved, 3)
}

func TestResolveProposalRewriteAffectedRejectsForeignOperation(t *testing.T) {
	cases := map[string]struct {
		input        model.RewriteAffectedInput
		baseRevision model.Revision
	}{
		"different chapters": {model.RewriteAffectedInput{ChapterIDs: []string{"chapter-2"}, BaseRevision: 3, ResolutionProposalID: "change-bottom-line", Reason: "x"}, 3},
		"different base":     {model.RewriteAffectedInput{ChapterIDs: []string{"chapter-1"}, BaseRevision: 2, ResolutionProposalID: "change-bottom-line", Reason: "x"}, 2},
		"other proposal":     {model.RewriteAffectedInput{ChapterIDs: []string{"chapter-1"}, BaseRevision: 3, ResolutionProposalID: "someone-else", Reason: "x"}, 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newReviewFixture(t, true)
			proposal := f.conflict(t, "change-bottom-line")
			f.rewriteOperation(t, proposal.ID, tc.input, tc.baseRevision)
			result, err := f.review.ResolveProposal(f.ctx, f.command(proposal.ID, change.ResolutionRewriteAffected))
			if !errors.Is(err, model.ErrIdempotencyConflict) {
				t.Fatalf("error = %v, want ErrIdempotencyConflict", err)
			}
			// 提案已经提交，冲突只阻止把陌生任务当作本次裁决的后续。
			if result.ChangeSet == nil || result.ChangeSet.NewRevision != 3 || len(result.Operations) != 0 {
				t.Fatalf("partial result = %#v", result)
			}
			f.assertState(t, proposal.ID, model.ApprovalApproved, 3)
		})
	}
}

func TestResolveProposalStaleProposalCannotBeApprovedButCanBeAbandoned(t *testing.T) {
	f := newReviewFixture(t, false)
	proposal := f.conflict(t, "change-bottom-line")
	f.commitEdit(t, "later-edit", func(projection *projectdoc.ProjectProjection) {
		projection.Plan[2].Summary = "冒雨抵达山门"
	})
	_, err := f.review.ResolveProposal(f.ctx, f.command(proposal.ID, change.ResolutionReinterpretFuture))
	if !errors.Is(err, model.ErrRevisionConflict) {
		t.Fatalf("stale approval: %v", err)
	}
	f.assertState(t, proposal.ID, model.ApprovalPending, 3)
	result, err := f.review.ResolveProposal(f.ctx, f.command(proposal.ID, change.ResolutionAbandon))
	if err != nil || result.Proposal == nil || result.Proposal.ApprovalState != model.ApprovalRejected {
		t.Fatalf("stale abandon = %#v, %v", result, err)
	}
	f.assertState(t, proposal.ID, model.ApprovalRejected, 3)
}
