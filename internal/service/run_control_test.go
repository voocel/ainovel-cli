package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/activity"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// TestRunActivityRequiresAttachedHub 守卫活动通道的可选装配：未装配时订阅方
// 得到 ok=false（TUI 静默降级为纯轮询），装配后发布-唤醒-整读贯通。
func TestRunActivityRequiresAttachedHub(t *testing.T) {
	api := New(openServiceStore(t))
	if _, _, ok := api.SubscribeRunActivity("book-1"); ok {
		t.Fatal("subscribe without hub must report ok=false")
	}
	if _, ok := api.RunActivity("book-1"); ok {
		t.Fatal("activity without hub must report ok=false")
	}
	hub := activity.NewHub()
	api.AttachActivityFeed(hub)
	wake, cancel, ok := api.SubscribeRunActivity("book-1")
	if !ok || wake == nil {
		t.Fatal("subscribe with hub must work")
	}
	defer cancel()
	hub.Publish(activity.Event{ProjectID: "book-1", RunID: "run-1", Kind: activity.ToolStart, Tool: "authority_read"})
	<-wake
	if snapshot, ok := api.RunActivity("book-1"); !ok || snapshot.RunID != "run-1" {
		t.Fatalf("activity snapshot = %#v ok=%v", snapshot, ok)
	}
}

func TestNewIDIsUniqueAndPrefixed(t *testing.T) {
	now := serviceTime()
	first, second := NewID("book", now), NewID("book", now)
	if !strings.HasPrefix(first, "book-20260818-") || first == second {
		t.Fatalf("ids = %q, %q", first, second)
	}
}

func TestPauseAndCancelCreationRun(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	api := New(authorityStore)
	now := serviceTime()
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "control-book", ChangeID: "create-control", UserID: "user-1",
		Reason: "运行控制测试", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	runID := ensureServiceTestRun(t, ctx, authorityStore, "control-book", now)

	paused, err := api.PauseCreationRun(ctx, runID, now.Add(time.Minute))
	if err != nil || paused.State != domain.RunPaused {
		t.Fatalf("pause = %#v, %v", paused, err)
	}
	again, err := api.PauseCreationRun(ctx, runID, now.Add(2*time.Minute))
	if err != nil || again.State != domain.RunPaused {
		t.Fatalf("pause idempotent = %#v, %v", again, err)
	}
	cancelled, err := api.CancelCreationRun(ctx, runID, now.Add(3*time.Minute))
	if err != nil || cancelled.State != domain.RunCancelled {
		t.Fatalf("cancel = %#v, %v", cancelled, err)
	}
	if _, err := api.PauseCreationRun(ctx, runID, now.Add(4*time.Minute)); err == nil {
		t.Fatal("pausing a cancelled run must fail")
	}
	// 终态落点仍要能被界面呈现：Latest 返回已取消的一轮，Active 则不再返回。
	latest, ok, err := api.LatestCreationRun(ctx, "control-book")
	if err != nil || !ok || latest.State != domain.RunCancelled {
		t.Fatalf("latest run = %#v ok=%v err=%v", latest, ok, err)
	}
	if _, active, err := api.ActiveCreationRun(ctx, "control-book"); err != nil || active {
		t.Fatalf("cancelled run should not be active: active=%v err=%v", active, err)
	}
}

// TestPauseDuringDriveStopsAfterCurrentStep 守卫写作中控制（页面设计 §5 p/x）：
// 驱动循环执行期间用户暂停，当前步骤完成后优雅收束、不再创建新任务；续跑到完成。
func TestPauseDuringDriveStopsAfterCurrentStep(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "pause-mid-book", UserID: "user-1", Premise: "写作中暂停测试",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	executor.onExecute = func(operation domain.Operation) {
		if operation.Kind == domain.OperationWriteChapter {
			if _, err := api.PauseCreationRun(ctx, operation.RunID, serviceTime().Add(time.Minute)); err != nil {
				t.Errorf("pause during drive: %v", err)
			}
		}
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	if result.RunState != domain.RunPaused {
		t.Fatalf("run state = %v, want paused", result.RunState)
	}
	// 暂停时正在写的第一章这一步已完成入库，之后没有开启新章。
	project, err := api.Project(ctx, "pause-mid-book", domain.InitialRevision)
	if err != nil || len(project.Manuscript) != 1 {
		t.Fatalf("manuscript after pause = %d, %v", len(project.Manuscript), err)
	}
	callsAtPause := executor.calls

	executor.onExecute = nil
	command.CreatedAt = serviceTime().Add(time.Hour)
	result, err = api.QuickWrite(ctx, command)
	if err != nil || result.RunState != domain.RunCompleted {
		t.Fatalf("resume result = %#v, %v", result, err)
	}
	if executor.calls == callsAtPause {
		t.Fatal("resume did not execute remaining work")
	}
}

// TestCancelDuringDriveWinsOverWaitingSettle 守卫乐观并发的裁决优先级：任务执行
// 期间用户取消，协调器收束时不得把 Run 改回等待态，也不得报状态冲突。
func TestCancelDuringDriveWinsOverWaitingSettle(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "cancel-mid-book", UserID: "user-1", Premise: "写作中取消测试",
		Chapters: 1, Approval: domain.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	executor.onExecute = func(operation domain.Operation) {
		if operation.Kind == domain.OperationDevelopPlan {
			if _, err := api.CancelCreationRun(ctx, operation.RunID, serviceTime().Add(time.Minute)); err != nil {
				t.Errorf("cancel during drive: %v", err)
			}
		}
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	if result.RunState != domain.RunCancelled {
		t.Fatalf("run state = %v, want cancelled", result.RunState)
	}
}

func TestImportNewProjectCreatesBookFromProjection(t *testing.T) {
	ctx := context.Background()
	api := New(openServiceStore(t))
	now := serviceTime()
	origin, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "origin-book", ChangeID: "create-origin", UserID: "user-1",
		Reason: "导出源", Draft: testProjectDraft(), CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("create origin: %v", err)
	}
	projection, err := api.ExportProject(ctx, origin.ID, origin.Revision)
	if err != nil {
		t.Fatalf("export origin: %v", err)
	}
	projection.ProjectID = "copied-book"

	proposal, err := api.ImportNewProject(ctx, "import-copy", "user-1", "导入作品文件", projection, now.Add(time.Minute))
	if err != nil || proposal.ApprovalState != domain.ApprovalPending {
		t.Fatalf("import proposal = %#v, %v", proposal, err)
	}
	if _, err := api.Approve(ctx, proposal.ID, "user-1", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("approve import: %v", err)
	}
	copied, err := api.Project(ctx, "copied-book", domain.InitialRevision)
	if err != nil || len(copied.Plan) != len(origin.Plan) || len(copied.Canon) != len(origin.Canon) {
		t.Fatalf("copied = %#v, %v", copied, err)
	}
}

func TestDeleteProjectRemovesAllDataAndAllowsRecreate(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	// 跑完两本书让全链数据（蓝图/正文/Canon/审阅/Run/事件/工作区）落库，
	// 删除一本必须不伤及另一本。
	for _, projectID := range []string{"delete-book", "keep-book"} {
		if _, err := api.QuickWrite(ctx, QuickWriteCommand{
			ProjectID: projectID, UserID: "user-1", Premise: "删除功能测试",
			Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
		}); err != nil {
			t.Fatalf("quick write %s: %v", projectID, err)
		}
	}
	if err := api.DeleteProject(ctx, "delete-book"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	projects, err := api.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].ID != "keep-book" {
		t.Fatalf("projects after delete = %#v, %v", projects, err)
	}
	if _, ok, err := api.LatestCreationRun(ctx, "delete-book"); err != nil || ok {
		t.Fatalf("deleted project still has runs: ok=%v err=%v", ok, err)
	}
	if project, err := api.Project(ctx, "keep-book", 0); err != nil || len(project.Manuscript) != 2 {
		t.Fatalf("keep-book after delete = %#v, %v", project, err)
	}
	// 同 ID 从零重跑必须成功：任何表有残留都会撞唯一键或幂等键。
	if _, err := api.QuickWrite(ctx, QuickWriteCommand{
		ProjectID: "delete-book", UserID: "user-1", Premise: "重新来过",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
	if err := api.DeleteProject(ctx, "ghost-book"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting missing project = %v, want ErrNotFound", err)
	}
}

func TestDeleteProjectRejectsRunningRun(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	api := New(authorityStore)
	now := serviceTime()
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "busy-book", ChangeID: "create-busy", UserID: "user-1",
		Reason: "删除守卫测试", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	runID := ensureServiceTestRun(t, ctx, authorityStore, "busy-book", now)
	if err := api.DeleteProject(ctx, "busy-book"); !errors.Is(err, store.ErrStateConflict) {
		t.Fatalf("delete with running run = %v, want ErrStateConflict", err)
	}
	if _, err := api.CancelCreationRun(ctx, runID, now.Add(time.Minute)); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if err := api.DeleteProject(ctx, "busy-book"); err != nil {
		t.Fatalf("delete after cancel: %v", err)
	}
}

// TestRunOperationsListsCreationOrder 守卫诊断下钻的数据源（M3）：按创建序
// 返回本轮全部 Operation——run 事件表只有编排流水，真实错误在 Operation 里。
func TestRunOperationsListsCreationOrder(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	result, err := api.QuickWrite(ctx, QuickWriteCommand{
		ProjectID: "ops-book", UserID: "user-1", Premise: "诊断下钻测试",
		Chapters: 1, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	})
	if err != nil || result.RunState != domain.RunCompleted {
		t.Fatalf("quick write = %#v, %v", result, err)
	}
	operations, err := api.RunOperations(ctx, result.RunID)
	if err != nil || len(operations) < 2 {
		t.Fatalf("run operations = %d, %v", len(operations), err)
	}
	if operations[0].Kind != domain.OperationDevelopPlan {
		t.Fatalf("first operation kind = %v", operations[0].Kind)
	}
	for _, operation := range operations {
		if operation.RunID != result.RunID {
			t.Fatalf("operation %s belongs to run %s", operation.ID, operation.RunID)
		}
	}
}

// TestOperationToolIssuesExtractsSelfCorrectedErrors 守卫诊断补全（Codex 复审）：
// 成功任务自纠过的工具报错要能从已持久化消息里确定性提取——role=tool 且
// is_error 的消息配上 assistant 消息 tool_call 的 id→name 映射。
func TestOperationToolIssuesExtractsSelfCorrectedErrors(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	result, err := api.QuickWrite(ctx, QuickWriteCommand{
		ProjectID: "issues-book", UserID: "user-1", Premise: "工具报错提取测试",
		Chapters: 1, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	})
	if err != nil || result.RunState != domain.RunCompleted {
		t.Fatalf("quick write = %#v, %v", result, err)
	}
	operations, err := api.RunOperations(ctx, result.RunID)
	if err != nil || len(operations) == 0 {
		t.Fatalf("run operations = %d, %v", len(operations), err)
	}
	operationID := operations[0].ID
	appendCommitted := func(index int, payload string) {
		t.Helper()
		if _, err := api.store.AppendOperationEvent(ctx, domain.OperationEvent{
			OperationID: operationID, StepID: "agent.message", Attempt: 1,
			IdempotencyKey: fmt.Sprintf("issues:%d", index),
			Kind:           "agent.message_committed", Payload: []byte(payload), CreatedAt: serviceTime(),
		}); err != nil {
			t.Fatalf("append event %d: %v", index, err)
		}
	}
	// 错误结果的 text 是 json.Marshal 过的字符串字面量（capability 落库形态）。
	appendCommitted(0, `{"role":"assistant","content":[{"type":"tool_call","tool_call":{"id":"call-9","name":"workspace_put_chapter","args":{}}}]}`)
	appendCommitted(1, `{"role":"tool","content":[{"type":"text","text":"\"expected_version 不匹配\""}],"metadata":{"tool_call_id":"call-9","is_error":true}}`)
	appendCommitted(2, `{"role":"tool","content":[{"type":"text","text":"{\"ok\":true}"}],"metadata":{"tool_call_id":"call-9"}}`)
	issues, err := api.OperationToolIssues(ctx, operationID)
	if err != nil {
		t.Fatalf("tool issues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %#v, want 1", issues)
	}
	// 外层引号已解掉，呈现原始错误文本。
	if issues[0].Tool != "workspace_put_chapter" || issues[0].Err != "expected_version 不匹配" {
		t.Fatalf("issue = %#v", issues[0])
	}
	// 形状漂移显式报错（Debug-First），不静默少报。
	appendCommitted(3, `{"role":"tool","content":"形状不符","metadata":{"is_error":true}}`)
	if _, err := api.OperationToolIssues(ctx, operationID); err == nil ||
		!strings.Contains(err.Error(), "decode committed message") {
		t.Fatalf("shape drift must fail loud, got %v", err)
	}
}
