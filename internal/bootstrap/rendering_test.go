package bootstrap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	evidencereader "github.com/voocel/ainovel-cli/internal/app/evidence"
	novelapp "github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	tasks "github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	runs "github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/operation"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// The second application registers through public assembly contracts. Its state
// adapter loads only project documents and generic artifact/check evidence.
type renderingEvidence struct {
	artifacts []model.Artifact
	checks    []evidencereader.Check
}

type renderingPolicy interface {
	ValidateGoal(json.RawMessage) error
	Next(projectdoc.Snapshot, model.CreationRun, renderingEvidence) (runs.Step, error)
}

type renderingAdapter struct {
	projects *projectdoc.Repository
	evidence *evidencereader.Reader
	policy   renderingPolicy
}

func (g renderingAdapter) ValidateGoal(payload json.RawMessage) error {
	return g.policy.ValidateGoal(payload)
}
func (g renderingAdapter) Next(ctx context.Context, run model.CreationRun) (runs.Decision, error) {
	project, err := g.projects.Project(ctx, run.ProjectID, 0)
	if err != nil {
		return runs.Decision{}, err
	}
	artifacts, err := g.evidence.Artifacts(ctx, project.ID, project.Revision)
	if err != nil {
		return runs.Decision{}, err
	}
	checks, err := g.evidence.Checks(ctx, project.ID, project.Revision, model.OperationInspectAsset)
	if err != nil {
		return runs.Decision{}, err
	}
	next, err := g.policy.Next(project, run, renderingEvidence{artifacts: artifacts, checks: checks})
	return runs.Decision{Revision: project.Revision, Step: next}, err
}

func newMediaApp(st *store.Store, executors tasks.ExecutorSet, policy renderingPolicy, contracts ...operation.VerdictContract) *testApp {
	changes := change.New(st)
	adapter := renderingAdapter{projects: projectdoc.New(st, changes), evidence: evidencereader.New(st, changes, operation.NewEngine(st, changes, contracts...)), policy: policy}
	return newTestApp(st, bootstrap.Options{Executors: executors, Contracts: contracts, Goals: map[model.GoalKind]runs.Goal{goalRendering: adapter}, Now: testTime})
}

func mediaBasis(project projectdoc.Snapshot, target model.DocumentRef) (model.EvidenceBasis, error) {
	// Inspection depends on the selected attachment and the source documents behind
	// it. A still-existing attachment alone cannot keep an old inspection valid.
	basis := model.EvidenceBasis{}
	visited := make(map[string]bool)
	queue := []model.DocumentRef{target}
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if visited[ref.Key()] {
			continue
		}
		visited[ref.Key()] = true
		entry, ok := project.Index[ref.Key()]
		if !ok {
			return model.EvidenceBasis{}, fmt.Errorf("media source %s is absent: %w", ref.Key(), model.ErrInvalid)
		}
		basis.Documents = append(basis.Documents, model.DocumentBasis{Ref: ref, Revision: entry.Revision})
		queue = append(queue, entry.Dependencies...)
	}
	return basis.Normalize(), nil
}

func renderingSnapshot(t *testing.T, api *testApp) projectdoc.Snapshot {
	t.Helper()
	snapshot, err := api.Projects.Project(context.Background(), renderingProject, 0)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// renderingDeriver 是第二种目标（D49 稳定性判据）：为每个章节计划节点渲染一份
// 衍生资产；附件缺失或其工件基线已失效即重生，全部有效即完成。它只读快照与证据。
type renderingDeriver struct{}

type renderingGoal struct {
	Role string `json:"role"`
}

const goalRendering model.GoalKind = "rendering"

func (renderingDeriver) ValidateGoal(payload json.RawMessage) error {
	var goal renderingGoal
	if err := model.DecodeStrict(payload, &goal); err != nil || goal.Role == "" {
		return fmt.Errorf("rendering goal requires a role: %w", model.ErrInvalid)
	}
	return nil
}

func (renderingDeriver) Next(project projectdoc.Snapshot, run model.CreationRun, evidence renderingEvidence) (runs.Step, error) {
	var goal renderingGoal
	if err := model.DecodeStrict(run.Goal.Payload, &goal); err != nil {
		return runs.Step{}, err
	}
	valid := make(map[model.ArtifactRef]bool, len(evidence.artifacts))
	for _, artifact := range evidence.artifacts {
		valid[artifact.Ref()] = true
	}
	attached := make(map[string]model.Attachment, len(project.Attachments))
	for _, attachment := range project.Attachments {
		attached[attachment.ID] = attachment
	}
	for _, node := range renderingTargets(project) {
		target := model.DocumentRef{Kind: model.DocumentPlan, ID: node.ID}
		if attachment, ok := attached[goal.Role+":"+target.Key()]; ok && valid[attachment.Artifact] {
			continue
		}
		work, err := renderingWork(project, run, goal.Role, target)
		if err != nil {
			return runs.Step{}, err
		}
		return runs.Step{Work: &work}, nil
	}
	return runs.Step{Done: fmt.Sprintf("%d 个节点的%s已全部渲染", len(renderingTargets(project)), goal.Role)}, nil
}

func renderingTargets(project projectdoc.Snapshot) []model.PlanNode {
	var nodes []model.PlanNode
	for _, node := range project.Plan {
		if node.Kind == model.PlanChapter {
			nodes = append(nodes, node)
		}
	}
	slices.SortFunc(nodes, func(left, right model.PlanNode) int { return left.Order - right.Order })
	return nodes
}

// renderingWork 的槽位按目标节点的最后变化 revision 命名：源变了就是新槽位。
func renderingWork(project projectdoc.Snapshot, run model.CreationRun, role string, target model.DocumentRef) (runs.WorkItem, error) {
	basis, err := mediaBasis(project, target)
	if err != nil {
		return runs.WorkItem{}, err
	}
	revision := strconv.FormatInt(int64(project.Index[target.Key()].Revision), 10)
	return runs.WorkItem{
		ID: run.ID + ":asset:" + target.ID + ":r" + revision, Kind: model.OperationGenerateAsset,
		Input: model.GenerateAssetInput{Target: target, Role: role, Basis: basis},
		Reasons: runs.WorkReasons{
			Waiting: target.ID + " 的资产已生成，等你确认后继续",
			Failure: target.ID + " 的资产这次没能生成",
			Stuck:   target.ID + " 的资产任务已结束但没有挂上附件，需要人工检查",
		},
	}, nil
}

const renderingProject = "render-book"

// newRenderingApp 装配三章的作品与外部执行器，并登记渲染目标的推导器。
func newRenderingApp(t *testing.T, approval model.ApprovalPolicy) (*testApp, *externalGenerationExecutor) {
	t.Helper()
	ctx := context.Background()
	authorityStore := openTestStore(t)
	executor := &externalGenerationExecutor{store: authorityStore, service: &fakeGenerationService{recoverable: true}, now: testTime()}
	api := newMediaApp(authorityStore, tasks.ExecutorSet{External: executor}, renderingDeriver{})
	draft := projectdoc.ProjectDraft{
		Intent: model.Intent{Premise: "三章的书", TargetChapters: 3}, Approval: approval,
		Plan: []model.PlanNode{
			{ID: "volume-1", Kind: model.PlanVolume, Order: 1, Title: "卷一", Summary: "开端"},
			{ID: "arc-1", Kind: model.PlanArc, ParentID: "volume-1", Order: 1, Title: "弧一", Summary: "启程"},
		},
	}
	for number := 1; number <= 3; number++ {
		draft.Plan = append(draft.Plan, model.PlanNode{
			ID: "chapter-plan-" + strconv.Itoa(number), Kind: model.PlanChapter, ParentID: "arc-1",
			Order: number, Title: "第" + strconv.Itoa(number) + "章", Summary: "推进",
		})
	}
	if _, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
		ProjectID: renderingProject, ChangeID: "create", UserID: "user-1", Reason: "建书", Draft: draft, CreatedAt: testTime(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return api, executor
}

func startRenderingRun(t *testing.T, api *testApp, sequence int, at time.Time) model.CreationRun {
	t.Helper()
	strategy := model.CreationRunStrategy{PlanWindowChapters: 1, ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: 0}
	preset, err := model.NewCreationRunPreset("rendering", model.ApprovalAuto, strategy)
	if err != nil {
		t.Fatalf("preset: %v", err)
	}
	run, err := api.Runs.StartCreationRun(context.Background(), runs.StartCreationRunCommand{
		RunID: fmt.Sprintf("run:%s:%d", renderingProject, sequence), ProjectID: renderingProject,
		Goal:     model.CreationRunGoal{Kind: goalRendering, Payload: json.RawMessage(`{"role":"cover"}`)},
		Strategy: strategy, Preset: preset, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("start rendering run: %v", err)
	}
	return run
}

func renderingBase() tasks.StartOperationCommand {
	return tasks.StartOperationCommand{ProjectID: renderingProject, ConfigDigest: "render-v1"}
}

func renderingTasks(api *testApp) runs.Tasks {
	return api.Tasks.ForCreation(renderingBase())
}

func renderingCommand(at time.Time) runs.DriveCommand {
	return runs.DriveCommand{WorkerID: "render-worker", LeaseDuration: time.Minute, CreatedAt: at}
}

// attachmentsByTarget 取当前附件到工件引用的映射，键为目标节点 ID。
func attachmentsByTarget(project projectdoc.Snapshot) map[string]model.ArtifactRef {
	refs := make(map[string]model.ArtifactRef, len(project.Attachments))
	for _, attachment := range project.Attachments {
		refs[attachment.Target.ID] = attachment.Artifact
	}
	return refs
}

func TestRenderingRunRecoversExternalJobAcrossLeaseExpiry(t *testing.T) {
	// 验收切片 2：外部异步任务在首个节点渲染中崩溃（已提交、租约过期），恢复后按
	// RequestID 取回结果而不重提；每个节点各得一份附件，Run 完成。
	ctx := context.Background()
	api, executor := newRenderingApp(t, model.ApprovalAuto)
	run := startRenderingRun(t, api, 1, testTime())
	project, err := api.Projects.Project(ctx, renderingProject, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	first, err := renderingWork(project, run, "cover", model.DocumentRef{Kind: model.DocumentPlan, ID: "chapter-plan-1"})
	if err != nil {
		t.Fatalf("first work: %v", err)
	}
	command, err := tasks.WorkCommand(renderingBase(), first, testTime())
	if err != nil {
		t.Fatalf("first command: %v", err)
	}
	command.RunID = run.ID
	if _, err := api.Tasks.StartOperation(ctx, command); err != nil {
		t.Fatalf("start first asset: %v", err)
	}
	claimed, err := api.store.ClaimOperationForExecutor(ctx, first.ID, "crashed-worker", executor.Identity(), time.Minute, testTime())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := executor.submit(ctx, claimed, first.Input.(model.GenerateAssetInput), 0); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := api.Tasks.RecoverOperations(ctx, testTime().Add(2*time.Minute)); err != nil {
		t.Fatalf("recover expired: %v", err)
	}

	outcome, err := api.Runs.Drive(ctx, run, renderingTasks(api), renderingCommand(testTime().Add(3*time.Minute)))
	if err != nil || outcome.Run.State != model.RunCompleted {
		t.Fatalf("outcome = %#v, %v", outcome.Run, err)
	}
	refs := attachmentsByTarget(renderingSnapshot(t, api))
	if len(refs) != 3 || executor.service.submits != 3 || executor.calls != 3 {
		t.Fatalf("attachments = %#v, submits = %d, calls = %d", refs, executor.service.submits, executor.calls)
	}
	for target, ref := range refs {
		content, err := api.store.ReadArtifact(ref.Digest)
		if err != nil || string(content) != "rendered:"+target {
			t.Fatalf("object for %s = %q, %v", target, content, err)
		}
	}
	recovered, err := api.store.GetOperation(ctx, first.ID)
	if err != nil || recovered.Attempt != 2 || recovered.State != model.OperationSucceeded {
		t.Fatalf("recovered first asset = %#v, %v", recovered, err)
	}
}

func TestRenderingRunRegeneratesOnlyAffectedTargetAfterSourceChange(t *testing.T) {
	// 验收切片 3：改一个源节点后只重生受影响的一份成果；manual 审批先等用户再交付；
	// 完成绑定新 Revision；旧工件行保留，GC 不删被引用对象。
	ctx := context.Background()
	api, executor := newRenderingApp(t, model.ApprovalAuto)
	outcome, err := api.Runs.Drive(ctx, startRenderingRun(t, api, 1, testTime()), renderingTasks(api), renderingCommand(testTime()))
	if err != nil || outcome.Run.State != model.RunCompleted {
		t.Fatalf("initial render = %#v, %v", outcome.Run, err)
	}
	before := attachmentsByTarget(renderingSnapshot(t, api))

	edited, err := json.Marshal(model.PlanNode{
		ID: "chapter-plan-2", Kind: model.PlanChapter, ParentID: "arc-1", Order: 2, Title: "第2章", Summary: "改写后的推进",
	})
	if err != nil {
		t.Fatalf("encode plan: %v", err)
	}
	at := testTime().Add(time.Hour)
	if _, err := api.changes.CommitUser(ctx, model.Proposal{
		ID: "edit-plan-2", Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: renderingProject},
		BaseRevision: outcome.Revision, Author: model.Author{Kind: model.AuthorUser, ID: "user-1"}, Reason: "改第二章计划",
		Patches:       []model.Patch{{Document: model.DocumentRef{Kind: model.DocumentPlan, ID: "chapter-plan-2"}, Operation: model.PatchPut, Content: edited}},
		ApprovalState: model.ApprovalPending, CreatedAt: at,
	}, at); err != nil {
		t.Fatalf("edit plan: %v", err)
	}
	if _, err := api.Projects.SetApprovalPolicy(ctx, projectdoc.SetApprovalPolicyCommand{
		ProjectID: renderingProject, ChangeID: "manual", UserID: "user-1", Policy: model.ApprovalManual, Reason: "交付前逐份确认", CreatedAt: at.Add(time.Minute),
	}); err != nil {
		t.Fatalf("set approval: %v", err)
	}

	run := startRenderingRun(t, api, 2, at.Add(2*time.Minute))
	outcome, err = api.Runs.Drive(ctx, run, renderingTasks(api), renderingCommand(at.Add(3*time.Minute)))
	if err != nil || outcome.Run.State != model.RunWaitingUser || outcome.Waiting == "" {
		t.Fatalf("regeneration = %#v, %v", outcome.Run, err)
	}
	operations, err := api.Runs.RunOperations(ctx, run.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != model.OperationGenerateAsset {
		t.Fatalf("run operations = %#v, %v", operations, err)
	}
	if input, err := model.TaskInputAs[model.GenerateAssetInput](operations[0]); err != nil || input.Target.ID != "chapter-plan-2" {
		t.Fatalf("affected target = %#v, %v", input, err)
	}
	if _, err := api.Decisions.Approve(ctx, outcome.Waiting+"-proposal", "user-1", at.Add(4*time.Minute)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	outcome, err = api.Runs.Drive(ctx, outcome.Run, renderingTasks(api), renderingCommand(at.Add(5*time.Minute)))
	if err != nil || outcome.Run.State != model.RunCompleted || outcome.Run.CompletedRevision != outcome.Revision {
		t.Fatalf("delivery = %#v, %v", outcome.Run, err)
	}
	after := attachmentsByTarget(renderingSnapshot(t, api))
	if after["chapter-plan-1"] != before["chapter-plan-1"] || after["chapter-plan-3"] != before["chapter-plan-3"] || after["chapter-plan-2"] == before["chapter-plan-2"] {
		t.Fatalf("attachments before = %#v, after = %#v", before, after)
	}
	if executor.service.submits != 4 {
		t.Fatalf("submits = %d, want 3 + 1", executor.service.submits)
	}
	artifacts, err := api.Resources.Artifacts(ctx, renderingProject)
	if err != nil || len(artifacts) != 4 {
		t.Fatalf("artifact rows = %d, %v", len(artifacts), err)
	}
	for _, ref := range after {
		if !slices.ContainsFunc(artifacts, func(artifact model.Artifact) bool { return artifact.Ref() == ref }) {
			t.Fatalf("attachment %v has no artifact row", ref)
		}
	}
	if removed, err := api.Resources.CollectArtifactGarbage(ctx, 0); err != nil || removed != 0 {
		t.Fatalf("gc removed %d referenced objects, %v", removed, err)
	}
	for _, artifact := range artifacts {
		if _, err := api.store.ReadArtifact(artifact.Digest); err != nil {
			t.Fatalf("object %s must survive gc: %v", artifact.ID, err)
		}
	}
}

func TestOneApplicationRunsNovelThenExternalGeneration(t *testing.T) {
	ctx := context.Background()
	authorityStore := openTestStore(t)
	llm := &scriptedQuickExecutor{now: testTime(), authorityStore: authorityStore}
	external := &externalGenerationExecutor{store: authorityStore, service: &fakeGenerationService{recoverable: true}, now: testTime()}
	api := newMediaApp(authorityStore, tasks.ExecutorSet{LLM: llm, External: external}, renderingDeriver{})
	result, err := api.Novels.QuickWrite(ctx, novelapp.QuickWriteCommand{ProjectID: renderingProject, UserID: "user-1", Premise: "一段旅程", Chapters: 3, WorkerID: "mixed-worker", LeaseDuration: time.Minute, CreatedAt: testTime()})
	if err != nil || result.RunState != model.RunCompleted {
		t.Fatalf("novel=%#v, err=%v", result, err)
	}
	llmCalls := llm.calls
	run := startRenderingRun(t, api, 2, testTime().Add(time.Hour))
	outcome, err := api.Runs.Drive(ctx, run, renderingTasks(api), renderingCommand(testTime().Add(time.Hour)))
	if err != nil || outcome.Run.State != model.RunCompleted || external.service.submits != 3 || llm.calls != llmCalls || len(renderingSnapshot(t, api).Attachments) != 3 {
		t.Fatalf("render=%#v err=%v submits=%d llm calls=%d", outcome.Run, err, external.service.submits, llm.calls)
	}
	operations, err := api.Runs.RunOperations(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if operation.Snapshot.Executor != external.Identity() {
			t.Fatalf("wrong frozen executor: %q", operation.Snapshot.Executor)
		}
	}
}
