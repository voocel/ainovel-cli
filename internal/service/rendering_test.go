package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// renderingDeriver 是第二种目标（D49 稳定性判据）：为每个章节计划节点渲染一份
// 衍生资产；附件缺失或其工件基线已失效即重生，全部有效即完成。它只读快照与证据。
type renderingDeriver struct{}

type renderingGoal struct {
	Role string `json:"role"`
}

const goalRendering domain.GoalKind = "rendering"

func (renderingDeriver) ValidateGoal(payload json.RawMessage) error {
	var goal renderingGoal
	if err := domain.DecodeStrict(payload, &goal); err != nil || goal.Role == "" {
		return fmt.Errorf("rendering goal requires a role: %w", domain.ErrInvalid)
	}
	return nil
}

func (renderingDeriver) Next(project ProjectSnapshot, run domain.CreationRun, evidence runEvidence) (step, error) {
	var goal renderingGoal
	if err := domain.DecodeStrict(run.Goal.Payload, &goal); err != nil {
		return step{}, err
	}
	valid := make(map[domain.ArtifactRef]bool, len(evidence.artifacts))
	for _, artifact := range evidence.artifacts {
		valid[artifact.Ref()] = true
	}
	attached := make(map[string]domain.Attachment, len(project.Attachments))
	for _, attachment := range project.Attachments {
		attached[attachment.ID] = attachment
	}
	for _, node := range renderingTargets(project) {
		target := domain.DocumentRef{Kind: domain.DocumentPlan, ID: node.ID}
		if attachment, ok := attached[goal.Role+":"+target.Key()]; ok && valid[attachment.Artifact] {
			continue
		}
		work, err := renderingWork(project, run, goal.Role, target)
		if err != nil {
			return step{}, err
		}
		return step{Work: &work}, nil
	}
	return step{Done: fmt.Sprintf("%d 个节点的%s已全部渲染", len(renderingTargets(project)), goal.Role)}, nil
}

func renderingTargets(project ProjectSnapshot) []domain.PlanNode {
	var nodes []domain.PlanNode
	for _, node := range project.Plan {
		if node.Kind == domain.PlanChapter {
			nodes = append(nodes, node)
		}
	}
	slices.SortFunc(nodes, func(left, right domain.PlanNode) int { return left.Order - right.Order })
	return nodes
}

// renderingWork 的槽位按目标节点的最后变化 revision 命名：源变了就是新槽位。
func renderingWork(project ProjectSnapshot, run domain.CreationRun, role string, target domain.DocumentRef) (workItem, error) {
	basis, err := basisFor(project, []domain.DocumentRef{target}, nil)
	if err != nil {
		return workItem{}, err
	}
	revision := strconv.FormatInt(int64(project.Index[target.Key()].Revision), 10)
	return workItem{
		ID: runQuickID(run.ID, "asset", target.ID, "r"+revision), Kind: domain.OperationGenerateAsset,
		Input: domain.GenerateAssetInput{Target: target, Role: role, Basis: basis},
		Reasons: workReasons{
			Waiting: target.ID + " 的资产已生成，等你确认后继续",
			Failure: target.ID + " 的资产这次没能生成",
			Stuck:   target.ID + " 的资产任务已结束但没有挂上附件，需要人工检查",
		},
	}, nil
}

const renderingProject = "render-book"

// newRenderingService 装配三章的作品与外部执行器，并登记渲染目标的推导器。
func newRenderingService(t *testing.T, approval domain.ApprovalPolicy) (*Service, *externalGenerationExecutor) {
	t.Helper()
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	executor := &externalGenerationExecutor{store: authorityStore, service: &fakeGenerationService{recoverable: true}, now: serviceTime()}
	api := NewWithExecutors(authorityStore, ExecutorSet{External: executor})
	api.now = serviceTime
	api.derivers[goalRendering] = renderingDeriver{}
	draft := ProjectDraft{
		Intent: domain.Intent{Premise: "三章的书", TargetChapters: 3}, Approval: approval,
		Plan: []domain.PlanNode{
			{ID: "volume-1", Kind: domain.PlanVolume, Order: 1, Title: "卷一", Summary: "开端"},
			{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Order: 1, Title: "弧一", Summary: "启程"},
		},
	}
	for number := 1; number <= 3; number++ {
		draft.Plan = append(draft.Plan, domain.PlanNode{
			ID: "chapter-plan-" + strconv.Itoa(number), Kind: domain.PlanChapter, ParentID: "arc-1",
			Order: number, Title: "第" + strconv.Itoa(number) + "章", Summary: "推进",
		})
	}
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: renderingProject, ChangeID: "create", UserID: "user-1", Reason: "建书", Draft: draft, CreatedAt: serviceTime(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return api, executor
}

func startRenderingRun(t *testing.T, api *Service, sequence int, at time.Time) domain.CreationRun {
	t.Helper()
	strategy := domain.CreationRunStrategy{PlanWindowChapters: 1, ReviewCadence: domain.ReviewPerPlanWindow, AutoRepairBudget: 0}
	preset, err := domain.NewCreationRunPreset("rendering", domain.ApprovalAuto, strategy)
	if err != nil {
		t.Fatalf("preset: %v", err)
	}
	run, err := api.StartCreationRun(context.Background(), StartCreationRunCommand{
		RunID: fmt.Sprintf("run:%s:%d", renderingProject, sequence), ProjectID: renderingProject,
		Goal:     domain.CreationRunGoal{Kind: goalRendering, Payload: json.RawMessage(`{"role":"cover"}`)},
		Strategy: strategy, Preset: preset, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("start rendering run: %v", err)
	}
	return run
}

func renderingCommand(at time.Time) QuickWriteCommand {
	return QuickWriteCommand{ProjectID: renderingProject, WorkerID: "render-worker", LeaseDuration: time.Minute, CreatedAt: at}
}

// attachmentsByTarget 取当前附件到工件引用的映射，键为目标节点 ID。
func attachmentsByTarget(project ProjectSnapshot) map[string]domain.ArtifactRef {
	refs := make(map[string]domain.ArtifactRef, len(project.Attachments))
	for _, attachment := range project.Attachments {
		refs[attachment.Target.ID] = attachment.Artifact
	}
	return refs
}

func TestRenderingRunRecoversExternalJobAcrossLeaseExpiry(t *testing.T) {
	// 验收切片 2：外部异步任务在首个节点渲染中崩溃（已提交、租约过期），恢复后按
	// RequestID 取回结果而不重提；每个节点各得一份附件，Run 完成。
	ctx := context.Background()
	api, executor := newRenderingService(t, domain.ApprovalAuto)
	run := startRenderingRun(t, api, 1, serviceTime())
	project, err := api.Project(ctx, renderingProject, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	first, err := renderingWork(project, run, "cover", domain.DocumentRef{Kind: domain.DocumentPlan, ID: "chapter-plan-1"})
	if err != nil {
		t.Fatalf("first work: %v", err)
	}
	command, err := workCommand(api.runBaseCommand(run, renderingCommand(serviceTime())), first, serviceTime())
	if err != nil {
		t.Fatalf("first command: %v", err)
	}
	command.RunID = run.ID
	if _, err := api.StartOperation(ctx, command); err != nil {
		t.Fatalf("start first asset: %v", err)
	}
	claimed, err := api.store.ClaimOperationForExecutor(ctx, first.ID, "crashed-worker", executor.Identity(), time.Minute, serviceTime())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := executor.submit(ctx, claimed, first.Input.(domain.GenerateAssetInput), 0); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := api.RecoverOperations(ctx, serviceTime().Add(2*time.Minute)); err != nil {
		t.Fatalf("recover expired: %v", err)
	}

	outcome, err := api.driveCreationRun(ctx, run, renderingCommand(serviceTime().Add(3*time.Minute)))
	if err != nil || outcome.run.State != domain.RunCompleted {
		t.Fatalf("outcome = %#v, %v", outcome.run, err)
	}
	refs := attachmentsByTarget(outcome.project)
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
	if err != nil || recovered.Attempt != 2 || recovered.State != domain.OperationSucceeded {
		t.Fatalf("recovered first asset = %#v, %v", recovered, err)
	}
}

func TestRenderingRunRegeneratesOnlyAffectedTargetAfterSourceChange(t *testing.T) {
	// 验收切片 3：改一个源节点后只重生受影响的一份成果；manual 审批先等用户再交付；
	// 完成绑定新 Revision；旧工件行保留，GC 不删被引用对象。
	ctx := context.Background()
	api, executor := newRenderingService(t, domain.ApprovalAuto)
	outcome, err := api.driveCreationRun(ctx, startRenderingRun(t, api, 1, serviceTime()), renderingCommand(serviceTime()))
	if err != nil || outcome.run.State != domain.RunCompleted {
		t.Fatalf("initial render = %#v, %v", outcome.run, err)
	}
	before := attachmentsByTarget(outcome.project)

	edited, err := json.Marshal(domain.PlanNode{
		ID: "chapter-plan-2", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 2, Title: "第2章", Summary: "改写后的推进",
	})
	if err != nil {
		t.Fatalf("encode plan: %v", err)
	}
	at := serviceTime().Add(time.Hour)
	if _, err := api.commitUserProposal(ctx, domain.Proposal{
		ID: "edit-plan-2", Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: renderingProject},
		BaseRevision: outcome.project.Revision, Author: domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, Reason: "改第二章计划",
		Patches:       []domain.Patch{{Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "chapter-plan-2"}, Operation: domain.PatchPut, Content: edited}},
		ApprovalState: domain.ApprovalPending, CreatedAt: at,
	}, at); err != nil {
		t.Fatalf("edit plan: %v", err)
	}
	if _, err := api.SetApprovalPolicy(ctx, SetApprovalPolicyCommand{
		ProjectID: renderingProject, ChangeID: "manual", UserID: "user-1", Policy: domain.ApprovalManual, Reason: "交付前逐份确认", CreatedAt: at.Add(time.Minute),
	}); err != nil {
		t.Fatalf("set approval: %v", err)
	}

	run := startRenderingRun(t, api, 2, at.Add(2*time.Minute))
	outcome, err = api.driveCreationRun(ctx, run, renderingCommand(at.Add(3*time.Minute)))
	if err != nil || outcome.run.State != domain.RunWaitingUser || outcome.waiting == "" {
		t.Fatalf("regeneration = %#v, %v", outcome.run, err)
	}
	operations, err := api.RunOperations(ctx, run.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != domain.OperationGenerateAsset {
		t.Fatalf("run operations = %#v, %v", operations, err)
	}
	if input, err := domain.TaskInputAs[domain.GenerateAssetInput](operations[0]); err != nil || input.Target.ID != "chapter-plan-2" {
		t.Fatalf("affected target = %#v, %v", input, err)
	}
	if _, err := api.Approve(ctx, outcome.waiting+"-proposal", "user-1", at.Add(4*time.Minute)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	outcome, err = api.driveCreationRun(ctx, outcome.run, renderingCommand(at.Add(5*time.Minute)))
	if err != nil || outcome.run.State != domain.RunCompleted || outcome.run.CompletedRevision != outcome.project.Revision {
		t.Fatalf("delivery = %#v, %v", outcome.run, err)
	}
	after := attachmentsByTarget(outcome.project)
	if after["chapter-plan-1"] != before["chapter-plan-1"] || after["chapter-plan-3"] != before["chapter-plan-3"] || after["chapter-plan-2"] == before["chapter-plan-2"] {
		t.Fatalf("attachments before = %#v, after = %#v", before, after)
	}
	if executor.service.submits != 4 {
		t.Fatalf("submits = %d, want 3 + 1", executor.service.submits)
	}
	artifacts, err := api.Artifacts(ctx, renderingProject)
	if err != nil || len(artifacts) != 4 {
		t.Fatalf("artifact rows = %d, %v", len(artifacts), err)
	}
	for _, ref := range after {
		if !slices.ContainsFunc(artifacts, func(artifact domain.Artifact) bool { return artifact.Ref() == ref }) {
			t.Fatalf("attachment %v has no artifact row", ref)
		}
	}
	if removed, err := api.CollectArtifactGarbage(ctx, 0); err != nil || removed != 0 {
		t.Fatalf("gc removed %d referenced objects, %v", removed, err)
	}
	for _, artifact := range artifacts {
		if _, err := api.store.ReadArtifact(artifact.Digest); err != nil {
			t.Fatalf("object %s must survive gc: %v", artifact.ID, err)
		}
	}
}

func TestOneServiceRunsNovelThenExternalGeneration(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	llm := &scriptedQuickExecutor{now: serviceTime(), authorityStore: authorityStore}
	external := &externalGenerationExecutor{store: authorityStore, service: &fakeGenerationService{recoverable: true}, now: serviceTime()}
	api := NewWithExecutors(authorityStore, ExecutorSet{LLM: llm, External: external})
	api.now = serviceTime
	api.derivers[goalRendering] = renderingDeriver{}
	result, err := api.QuickWrite(ctx, QuickWriteCommand{ProjectID: renderingProject, UserID: "user-1", Premise: "一段旅程", Chapters: 3, WorkerID: "mixed-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime()})
	if err != nil || result.RunState != domain.RunCompleted {
		t.Fatalf("novel=%#v, err=%v", result, err)
	}
	llmCalls := llm.calls
	run := startRenderingRun(t, api, 2, serviceTime().Add(time.Hour))
	outcome, err := api.driveCreationRun(ctx, run, renderingCommand(serviceTime().Add(time.Hour)))
	if err != nil || outcome.run.State != domain.RunCompleted || external.service.submits != 3 || llm.calls != llmCalls || len(outcome.project.Attachments) != 3 {
		t.Fatalf("render=%#v err=%v submits=%d llm calls=%d", outcome.run, err, external.service.submits, llm.calls)
	}
	operations, err := api.RunOperations(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if operation.Snapshot.Executor != external.Identity() {
			t.Fatalf("wrong frozen executor: %q", operation.Snapshot.Executor)
		}
	}
}
