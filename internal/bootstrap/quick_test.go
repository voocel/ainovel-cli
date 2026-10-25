package bootstrap_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/decision"
	novelapp "github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

func TestQuickWriteUsesSamePersistentOperationPipelineAndResumesIdempotently(t *testing.T) {
	ctx := context.Background()
	authorityStore := openTestStore(t)
	executor := &scriptedQuickExecutor{now: testTime(), authorityStore: authorityStore}
	api := newTestAppWithExecutor(authorityStore, executor)
	api.now = testTime
	command := novelapp.QuickWriteCommand{
		ProjectID: "quick-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	if result.Revision != 5 || len(result.Chapters) != 3 || executor.calls != 5 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read quick project: %v", err)
	}
	if len(project.Manuscript) != 3 {
		t.Fatalf("manuscript chapters = %d, want 3", len(project.Manuscript))
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	if strings.Contains(strings.ToLower(string(payload)), "proposal") || strings.Contains(strings.ToLower(string(payload)), "change_set") {
		t.Fatalf("quick result leaked internal change protocol: %s", payload)
	}

	command.CreatedAt = command.CreatedAt.Add(time.Hour)
	repeated, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("resume completed quick write: %v", err)
	}
	if repeated.Revision != result.Revision || executor.calls != 5 {
		t.Fatalf("repeated = %#v, executor calls = %d", repeated, executor.calls)
	}
}

func TestQuickWriteManualApprovalWaitsForUserAndResumes(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "manual-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, Approval: model.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}

	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write under manual approval: %v", err)
	}
	if result.RunID != "run:manual-book:1" || result.RunState != model.RunWaitingUser ||
		result.WaitingOperationID != planID(result.RunID, command.Chapters) || executor.calls != 1 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode waiting result: %v", err)
	}
	if strings.Contains(strings.ToLower(string(payload)), "proposal") || strings.Contains(strings.ToLower(string(payload)), "change_set") {
		t.Fatalf("waiting result leaked internal change protocol: %s", payload)
	}
	// 恢复落点（workbench §4）：等待裁决时可按 Run 定位待批稿件重建决定卡。
	waiting, ok, err := api.Runs.WaitingProposal(ctx, result.RunID)
	if err != nil || !ok || waiting.ID != result.WaitingOperationID+"-proposal" {
		t.Fatalf("waiting proposal = %#v ok=%v err=%v", waiting, ok, err)
	}

	proposals := []string{
		planID(result.RunID, command.Chapters) + "-proposal",
		runQuickID(result.RunID, "chapter", "chapter-plan-1") + "-proposal",
		runQuickID(result.RunID, "chapter", "chapter-plan-2") + "-proposal",
	}
	for round, proposalID := range proposals {
		at := command.CreatedAt.Add(time.Duration(round+1) * time.Hour)
		if _, err := api.Decisions.Approve(ctx, proposalID, command.UserID, at); err != nil {
			t.Fatalf("approve %s: %v", proposalID, err)
		}
		command.CreatedAt = at.Add(time.Minute)
		if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
			t.Fatalf("resume after approving %s: %v", proposalID, err)
		}
	}
	if result.RunState != model.RunCompleted || result.Revision != 4 || len(result.Chapters) != 2 || executor.calls != 4 {
		t.Fatalf("final = %#v, executor calls = %d", result, executor.calls)
	}
}

// 稿件等确认时用户又提了要求（§5.5 第 4 条）：与这一章不相干的不动它，照旧等确认；落在这
// 一章上的使它过时，续跑时转 stale，由后继带着新要求重写，不会一直停在过时的稿件上。
func TestQuickWriteDraftOutdatedWhileWaitingIsRedoneBySuccessor(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "outdated-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, Approval: model.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Decisions.Approve(ctx, planID(result.RunID, command.Chapters)+"-proposal", command.UserID, testTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	draft := runQuickID(result.RunID, "chapter", "chapter-plan-1")
	note := func(id string, from, to int, at time.Time) {
		t.Helper()
		if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
			ProjectID: command.ProjectID, ChangeID: id, UserID: command.UserID, DirectiveID: id,
			Scope: model.DirectiveScopeChapters(from, to), Text: id, Reason: "用户的要求", CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		command.CreatedAt = at.Add(time.Minute)
		if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
			t.Fatal(err)
		}
	}
	command.CreatedAt = testTime().Add(2 * time.Hour)
	if result, err = api.Novels.QuickWrite(ctx, command); err != nil || result.WaitingOperationID != draft {
		t.Fatalf("chapter 1 draft = %#v, %v", result, err)
	}
	calls := executor.calls

	note("later", 2, 2, testTime().Add(3*time.Hour))
	if result.WaitingOperationID != draft || executor.calls != calls {
		t.Fatalf("a requirement on chapter 2 must leave the chapter 1 draft waiting: %#v, calls %d", result, executor.calls)
	}
	note("opening", 1, 1, testTime().Add(4*time.Hour))
	if result.RunState != model.RunWaitingUser || result.WaitingOperationID != draft+":redo2" || executor.calls != calls+1 {
		t.Fatalf("an outdated draft must be redone by its successor: %#v, calls %d", result, executor.calls)
	}
	if old, err := api.Tasks.Operation(ctx, draft); err != nil || old.State != model.OperationStale {
		t.Fatalf("outdated draft = %s, %v", old.State, err)
	}
	successor, err := api.Tasks.Operation(ctx, draft+":redo2")
	if err != nil {
		t.Fatal(err)
	}
	var input model.WriteChapterInput
	if err := json.Unmarshal(successor.Input, &input); err != nil || !slices.ContainsFunc(input.Directives, func(d model.Directive) bool { return d.ID == "opening" }) {
		t.Fatalf("successor input = %s, %v", successor.Input, err)
	}
}

// 退回蓝图的理由交给重拟的规划（D80）：后继规划的输入带着它，规划才能照着重做。
func TestQuickWriteRejectedBlueprintIsReplannedWithTheReason(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "replan-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, Approval: model.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	blueprint := planID(result.RunID, command.Chapters)
	const reason = "第 1 章从局长的视角写"
	if _, err := api.Decisions.Reject(ctx, decision.RejectCommand{ProposalID: blueprint + "-proposal", UserID: command.UserID, Reason: reason, At: testTime().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	command.CreatedAt = testTime().Add(2 * time.Hour)
	if result, err = api.Novels.QuickWrite(ctx, command); err != nil || result.WaitingOperationID != blueprint+":redo2" {
		t.Fatalf("replan = %#v, %v", result, err)
	}
	successor, err := api.Tasks.Operation(ctx, blueprint+":redo2")
	if err != nil {
		t.Fatal(err)
	}
	var input model.DevelopPlanInput
	if err := json.Unmarshal(successor.Input, &input); err != nil || !slices.ContainsFunc(input.PendingRequirements, func(text string) bool { return strings.HasPrefix(text, reason) }) {
		t.Fatalf("successor input = %s, %v", successor.Input, err)
	}
}

func TestQuickWriteMilestonePresetPausesOnlyAtMilestones(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "milestone-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, Approval: model.ApprovalMilestone,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}

	// 蓝图是重大变化，等待确认；例行章节（正文 + 随章事实）自动通过。
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write under milestone approval: %v", err)
	}
	if result.RunState != model.RunWaitingUser || executor.calls != 1 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	if _, err := api.Decisions.Approve(ctx, planID(result.RunID, command.Chapters)+"-proposal", command.UserID, command.CreatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
	if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatalf("resume after plan approval: %v", err)
	}
	if result.RunState != model.RunCompleted || result.Revision != 5 || executor.calls != 5 {
		t.Fatalf("final = %#v, executor calls = %d", result, executor.calls)
	}
}

func TestQuickWriteRejectedChapterIsRewrittenBySuccessor(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	authorityStore := openTestStore(t)
	executor.authorityStore = authorityStore
	api := newTestAppWithExecutor(authorityStore, executor)
	api.now = testTime
	command := novelapp.QuickWriteCommand{
		ProjectID: "reject-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 1, Approval: model.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	if _, err := api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatalf("quick write: %v", err)
	}
	if _, err := api.Decisions.Approve(ctx, planID("run:reject-book:1", command.Chapters)+"-proposal", command.UserID, command.CreatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("write chapter candidate: %v", err)
	}
	if result.RunState != model.RunWaitingUser || executor.calls != 2 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}

	// 否决候选后再次发起：后继带着工作区与否决理由重写，而不是原地续跑或直接失败。
	feedback := "结尾太仓促，请补全告别场景"
	chapterOperationID := runQuickID(result.RunID, "chapter", "chapter-plan-1")
	if _, err := api.Decisions.Reject(ctx, decision.RejectCommand{ProposalID: chapterOperationID + "-proposal", UserID: command.UserID, Reason: feedback, At: command.CreatedAt.Add(time.Hour)}); err != nil {
		t.Fatalf("reject chapter: %v", err)
	}
	command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
	if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatalf("rewrite after rejection: %v", err)
	}
	successor := chapterOperationID + ":redo2"
	if result.RunState != model.RunWaitingUser || executor.calls != 3 ||
		len(result.Chapters) != 1 || result.Chapters[0].OperationID != successor {
		t.Fatalf("rewrite = %#v, executor calls = %d", result, executor.calls)
	}
	// 否决理由入账为该章作用域的 Directive（§4.9），并随当前快照进入后继任务输入。
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if len(project.Directives) != 1 || project.Directives[0].Text != feedback ||
		project.Directives[0].Scope != "plan_node:chapter-plan-1" || project.Revision != 3 {
		t.Fatalf("directives = %#v at revision %d", project.Directives, project.Revision)
	}
	successorOperation, err := authorityStore.GetOperation(ctx, successor)
	if err != nil {
		t.Fatalf("read successor: %v", err)
	}
	if !strings.Contains(string(successorOperation.Input), feedback) {
		t.Fatalf("successor input lacks rejection directive: %s", successorOperation.Input)
	}
	if _, err := api.Decisions.Reject(ctx, decision.RejectCommand{ProposalID: chapterOperationID + "-proposal", UserID: command.UserID, Reason: "另一条理由", At: command.CreatedAt.Add(time.Hour)}); err != nil {
		t.Fatalf("repeat reject: %v", err)
	}
	if again, err := api.Projects.Project(ctx, command.ProjectID, 0); err != nil || again.Revision != 3 || len(again.Directives) != 1 {
		t.Fatalf("repeated rejection must not add directives: rev=%d err=%v", again.Revision, err)
	}
	if _, err := api.Decisions.Approve(ctx, successor+"-proposal", command.UserID, command.CreatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("approve rewrite: %v", err)
	}
	command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
	if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatalf("finish after rewrite: %v", err)
	}
	if result.RunState != model.RunCompleted || result.Revision != 4 {
		t.Fatalf("final = %#v", result)
	}
}

func TestQuickWriteMidRunTighteningMigratesInFlightWork(t *testing.T) {
	// S9 + D51：全自动写作中途用户锁定设定，在途章节的提案重定位到新基线、
	// 在新边界下裁决；整轮创作不中止、不重跑模型、不换命令入口。
	ctx := context.Background()
	authorityStore := openTestStore(t)
	executor := &scriptedQuickExecutor{now: testTime()}
	executor.authorityStore = authorityStore
	api := newTestAppWithExecutor(authorityStore, executor)
	api.now = testTime
	command := novelapp.QuickWriteCommand{
		ProjectID: "tighten-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	executor.onExecute = func(operation model.Operation) {
		if operation.ID != runQuickID("run:tighten-book:1", "chapter", "chapter-plan-1") {
			return
		}
		if _, err := api.Projects.SetOwnership(ctx, projectdoc.SetOwnershipCommand{
			ProjectID: "tighten-book", ChangeID: "mid-run-lock", UserID: "user-1",
			Target:  model.DocumentRef{Kind: model.DocumentPlan, ID: "arc-1"},
			Control: model.ControlLocked, Reason: "写作中途锁定关键节点",
			CreatedAt: command.CreatedAt.Add(30 * time.Minute),
		}); err != nil {
			t.Fatalf("mid-run lock: %v", err)
		}
	}
	// 收紧立即生效：在途第 1 章的提案重定位到锁定后的 Revision；因缺少语义合规
	// 证据，auto 保守升级为安全暂停等待裁决，而不是整轮失败或作废重跑。
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across mid-run tightening: %v", err)
	}
	chapterOne := runQuickID(result.RunID, "chapter", "chapter-plan-1")
	if result.RunState != model.RunWaitingUser || executor.calls != 2 ||
		result.WaitingOperationID != chapterOne {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}

	// 用户裁决后同一入口继续：第 2 章同样在锁定约束下暂停，逐稿放行直至完成。
	for _, proposalID := range []string{
		chapterOne + "-proposal",
		runQuickID(result.RunID, "chapter", "chapter-plan-2") + "-proposal",
	} {
		command.CreatedAt = command.CreatedAt.Add(time.Hour)
		if _, err := api.Decisions.Approve(ctx, proposalID, command.UserID, command.CreatedAt); err != nil {
			t.Fatalf("approve %s: %v", proposalID, err)
		}
		command.CreatedAt = command.CreatedAt.Add(time.Minute)
		if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
			t.Fatalf("resume after approving %s: %v", proposalID, err)
		}
	}
	if result.RunState != model.RunCompleted || result.Revision != 5 || executor.calls != 4 ||
		result.Chapters[0].OperationID != chapterOne {
		t.Fatalf("final = %#v, executor calls = %d", result, executor.calls)
	}
}

func TestQuickWriteExtendsGoalWithRollingPlan(t *testing.T) {
	// D5+D2+D63：完本后把篇幅从 3 章提到 5 章——只改运行目标、不写 Intent（已有审阅证据
	// 不失效），蓝图用 revise_plan 增量扩窗（保持已有节点稳定），续写到新篇幅。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "grow-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	first, err := api.Novels.QuickWrite(ctx, command)
	if err != nil || first.RunState != model.RunCompleted || first.Revision != 5 {
		t.Fatalf("first run = %#v, %v", first, err)
	}

	command.Chapters = 5
	command.CreatedAt = command.CreatedAt.Add(time.Hour)
	grown, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("grow goal: %v", err)
	}
	// 修订序列：扩窗 6、第 4 章 7、第 5 章 8；审阅不产生修订。
	// 调用序列：前三章的窗口审阅仍有效（篇幅不进证据基线），扩窗、写两章、末窗审阅，共 +4。
	if grown.RunState != model.RunCompleted || grown.Revision != 8 ||
		grown.RunID != "run:grow-book:2" || len(grown.Chapters) != 5 || executor.calls != 9 {
		t.Fatalf("grown = %#v, executor calls = %d", grown, executor.calls)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil || len(project.Manuscript) != 5 {
		t.Fatalf("project = %#v, %v", project, err)
	}
	// 续跑不传篇幅沿用上一轮（修复旧版把已有作品重置为 3 章）：只做完成验证，不再调用模型。
	command.Chapters = novelapp.KeepChapters
	command.CreatedAt = command.CreatedAt.Add(time.Hour)
	kept, err := api.Novels.QuickWrite(ctx, command)
	if err != nil || kept.RunState != model.RunCompleted || len(kept.Chapters) != 5 || executor.calls != 9 {
		t.Fatalf("kept = %#v, %v, executor calls = %d", kept, err, executor.calls)
	}
	run, err := api.Runs.CreationRun(ctx, kept.RunID)
	if goal, decodeErr := model.DecodeNovelGoal(run.Goal); err != nil || decodeErr != nil || goal.TargetChapters != 5 {
		t.Fatalf("continued goal = %+v, %v %v", goal, err, decodeErr)
	}

	// Run 血统（§6.3）：创建事件 + 扩窗、两章、末窗审阅的 operation_created 事件。
	events, err := api.Runs.CreationRunEvents(ctx, "run:grow-book:2")
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	kinds := make([]string, len(events))
	for index, event := range events {
		kinds[index] = event.Kind
	}
	want := []string{
		model.RunEventCreated, model.RunEventOperationCreated, model.RunEventOperationCreated,
		model.RunEventOperationCreated, model.RunEventOperationCreated,
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("run event kinds = %v, want %v", kinds, want)
	}
}

func TestQuickWriteApprovalTighteningMidRunTakesEffectImmediately(t *testing.T) {
	// S9 的调严审批半边：auto 起书，第 1 章执行期间用户把策略调成 manual；
	// 在途章的提案重定位后按权威新策略等待裁决（D23 收紧立即生效），同一入口继续。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "policy-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	executor.onExecute = func(operation model.Operation) {
		if operation.ID != runQuickID("run:policy-book:1", "chapter", "chapter-plan-1") {
			return
		}
		if _, err := api.Projects.SetApprovalPolicy(ctx, projectdoc.SetApprovalPolicyCommand{
			ProjectID: "policy-book", ChangeID: "tighten", UserID: "user-1",
			Policy: model.ApprovalManual, Reason: "写作中途调严审批",
			CreatedAt: command.CreatedAt.Add(30 * time.Minute),
		}); err != nil {
			t.Fatalf("tighten approval mid-run: %v", err)
		}
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across approval tightening: %v", err)
	}
	chapterOne := runQuickID(result.RunID, "chapter", "chapter-plan-1")
	if result.RunState != model.RunWaitingUser || result.WaitingOperationID != chapterOne || executor.calls != 2 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	for _, proposalID := range []string{
		chapterOne + "-proposal",
		runQuickID(result.RunID, "chapter", "chapter-plan-2") + "-proposal",
	} {
		command.CreatedAt = command.CreatedAt.Add(time.Hour)
		if _, err := api.Decisions.Approve(ctx, proposalID, command.UserID, command.CreatedAt); err != nil {
			t.Fatalf("approve %s: %v", proposalID, err)
		}
		command.CreatedAt = command.CreatedAt.Add(time.Minute)
		if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
			t.Fatalf("resume after approving %s: %v", proposalID, err)
		}
	}
	if result.RunState != model.RunCompleted || result.Revision != 5 || executor.calls != 4 {
		t.Fatalf("final = %#v, executor calls = %d", result, executor.calls)
	}
}

// 已知阻塞先修完（D68）：一次审阅阻塞两章，两章都重写后再复审，复审只对改动章下结论。
func TestQuickWriteRewritesAllKnownBlockingBeforeReReview(t *testing.T) {
	ctx := context.Background()
	first, second, third := "chapter-chapter-plan-1", "chapter-chapter-plan-2", "chapter-chapter-plan-3"
	executor := &scriptedQuickExecutor{now: testTime(), blockOnce: []string{first, third}}
	api := newQuickTestApp(t, executor)
	result, err := api.Novels.QuickWrite(ctx, novelapp.QuickWriteCommand{
		ProjectID: "recheck-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	})
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	// 规划 1 + 三章 3 + 首审（阻塞 1、3 章）1 + 重写 2 + 复审 1 = 8 次。
	if result.RunState != model.RunCompleted || executor.calls != 8 || len(executor.reviews) != 2 {
		t.Fatalf("result = %#v, calls = %d, reviews = %d", result, executor.calls, len(executor.reviews))
	}
	recheck := executor.reviews[1]
	var prior []string
	for _, finding := range recheck.PriorFindings {
		prior = append(prior, finding.ChapterID)
	}
	if !slices.Equal(recheck.Reviewed, []string{second}) || !slices.Equal(prior, []string{first, third}) {
		t.Fatalf("recheck input = %+v", recheck)
	}
}

func TestQuickWriteBlockedReviewTriggersRewriteAndReReview(t *testing.T) {
	// 完成契约（§6.4）：审阅给出阻塞发现 → 按意见重写该章 → Revision 推进使
	// 旧裁定自动失效 → 再审阅通过后才 completed；队列为空绝不等于完成。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: testTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 1,
	}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "review-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across blocked review: %v", err)
	}
	// 规划 1 + 两章 2 + 首轮审阅（阻塞）1 + 重写 1 + 再审阅（通过）1 = 6 次。
	if result.RunState != model.RunCompleted || result.Revision != 5 || executor.calls != 6 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	rewritten := false
	for _, chapter := range project.Manuscript {
		if chapter.ID == "chapter-chapter-plan-2" {
			rewritten = strings.Contains(chapter.Blocks[0].Text, "按审阅意见重写")
		}
	}
	if !rewritten {
		t.Fatalf("chapter 2 was not rewritten from review findings: %#v", project.Manuscript)
	}
	// 生产里程碑取自落盘的记录（D77）：入稿、改写入稿、两次审阅结论与完成。
	snap, err := api.Workbench.WorkbenchSnapshot(ctx, command.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var milestones []string
	for _, milestone := range snap.Milestones {
		milestones = append(milestones, milestone.Text)
	}
	if len(milestones) != 6 {
		t.Fatalf("milestones = %q", milestones)
	}
	for _, want := range []string{"第 1 章《", "第 2 章《", "》改写入稿", "审阅第 1–2 章：未通过 · 1 处未解决", "审阅第 1–2 章：通过", "全书 2 章完成"} {
		if !slices.ContainsFunc(milestones, func(text string) bool { return strings.Contains(text, want) }) {
			t.Fatalf("milestones missing %q: %q", want, milestones)
		}
	}
}

func TestQuickWriteDirectiveAddedMidRunReachesSuccessorAndGatesReview(t *testing.T) {
	// S12 + §4.9：写作中途用户提出要求，基线推进使在途章节失效，后继按当前
	// 快照拿到含该要求的任务输入；审阅必须逐条核验，未满足即阻塞并重写。
	ctx := context.Background()
	authorityStore := openTestStore(t)
	executor := &scriptedQuickExecutor{
		now: testTime(), blockChapter: "chapter-chapter-plan-2", unmetDirective: "hook", unmetRemaining: 1,
	}
	executor.authorityStore = authorityStore
	api := newTestAppWithExecutor(authorityStore, executor)
	api.now = testTime
	command := novelapp.QuickWriteCommand{
		ProjectID: "directive-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	executor.onExecute = func(operation model.Operation) {
		if operation.ID != runQuickID("run:directive-book:1", "chapter", "chapter-plan-1") {
			return
		}
		if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
			ProjectID: "directive-book", ChangeID: "mid-run-hook", UserID: "user-1", DirectiveID: "hook",
			Scope: "from_chapter:1", Text: "每章结尾留钩子", Reason: "写作中途提出要求",
			CreatedAt: command.CreatedAt.Add(30 * time.Minute),
		}); err != nil {
			t.Fatalf("mid-run directive: %v", err)
		}
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across mid-run directive: %v", err)
	}
	// 规划 1 + 第 1 章（失效）1 + 后继 1 + 第 2 章 1 + 审阅（要求未满足）1 + 重写 1 + 再审阅 1 = 7 次。
	if result.RunState != model.RunCompleted || result.Revision != 6 || executor.calls != 7 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	successor := runQuickID(result.RunID, "chapter", "chapter-plan-1") + ":redo2"
	operation, err := authorityStore.GetOperation(ctx, successor)
	if err != nil {
		t.Fatalf("read successor: %v", err)
	}
	var input struct {
		Directives []model.Directive `json:"directives"`
	}
	if err := json.Unmarshal(operation.Input, &input); err != nil {
		t.Fatalf("decode successor input: %v", err)
	}
	if len(input.Directives) != 1 || input.Directives[0].ID != "hook" {
		t.Fatalf("successor input = %s", operation.Input)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	for _, chapter := range project.Manuscript {
		if chapter.ID == "chapter-chapter-plan-2" && !strings.Contains(chapter.Blocks[0].Text, "按审阅意见重写") {
			t.Fatalf("chapter 2 was not rewritten for the unmet directive: %#v", chapter)
		}
	}
}

func TestQuickWriteReviewsWindowBeforeExtendingPlan(t *testing.T) {
	// 先审后扩（§6.3）：写满一个规划窗口后先做窗口审阅，通过才扩展下一段
	// 计划，且审阅意见随扩窗输入进入下一段规划。
	ctx := context.Background()
	note := "主角动机要在下一段更明确"
	executor := &scriptedQuickExecutor{now: testTime(), passNote: note}
	api := newQuickTestApp(t, executor)
	var extendInputs []string
	executor.onExecute = func(operation model.Operation) {
		if operation.Kind == model.OperationRevisePlan {
			extendInputs = append(extendInputs, string(operation.Input))
		}
	}
	command := novelapp.QuickWriteCommand{
		ProjectID: "window-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 5, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write with rolling window: %v", err)
	}
	// 规划 1 + 首窗三章 3 + 首窗审阅 1 + 扩窗 1 + 两章 2 + 末窗审阅 1 = 9 次。
	if result.RunState != model.RunCompleted || result.Revision != 8 ||
		len(result.Chapters) != 5 || executor.calls != 9 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	if len(extendInputs) != 1 || !strings.Contains(extendInputs[0], note) {
		t.Fatalf("extend inputs = %v, want stage review note %q forwarded", extendInputs, note)
	}
	// 工作台与协调器同口径（D62）：两个窗口各自的当前裁定都呈现，不只取最新一份。
	snapshot, err := api.Workbench.WorkbenchSnapshot(ctx, "window-book")
	if err != nil {
		t.Fatalf("workbench snapshot: %v", err)
	}
	if len(snapshot.Findings) != 2 || snapshot.Findings[0].ChapterID != "chapter-chapter-plan-1" || snapshot.Findings[1].ChapterID != "chapter-chapter-plan-4" {
		t.Fatalf("workbench findings = %+v, want one per window", snapshot.Findings)
	}
}

// TestQuickWriteReviewCostIsLinear 是 D62 的成本证据：20 章按 3 章窗口审阅，每章只审一次
// （原累计范围为 3+6+…+20 章次）。
func TestQuickWriteReviewCostIsLinear(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	reviewed := 0
	executor.onExecute = func(operation model.Operation) {
		if operation.Kind != model.OperationReviewRange {
			return
		}
		input, err := model.TaskInputAs[model.ReviewRangeInput](operation)
		if err != nil {
			t.Fatalf("review input: %v", err)
		}
		reviewed += len(input.ChapterIDs)
	}
	result, err := api.Novels.QuickWrite(ctx, novelapp.QuickWriteCommand{
		ProjectID: "long-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 20, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	})
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	// 规划 1 + 20 章 + 7 次窗口审阅 + 6 次扩窗 = 34 次。
	if result.RunState != model.RunCompleted || len(result.Chapters) != 20 || reviewed != 20 || executor.calls != 34 {
		t.Fatalf("result = %#v, reviewed chapters = %d, executor calls = %d", result, reviewed, executor.calls)
	}
}

func TestQuickWriteWindowReviewBlockedTriggersRewriteBeforeExtending(t *testing.T) {
	// 窗口审阅给出阻塞发现时不得扩窗：先按意见重写、再审阅通过后才继续规划。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: testTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 1,
	}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "window-block-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 5, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across blocked window review: %v", err)
	}
	// 规划 1 + 三章 3 + 窗口审阅（阻塞）1 + 重写 1 + 再审阅 1 + 扩窗 1 + 两章 2 + 末窗审阅 1 = 11 次。
	if result.RunState != model.RunCompleted || result.Revision != 9 || executor.calls != 11 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	rewritten := false
	for _, chapter := range project.Manuscript {
		if chapter.ID == "chapter-chapter-plan-2" {
			rewritten = strings.Contains(chapter.Blocks[0].Text, "按审阅意见重写")
		}
	}
	if !rewritten {
		t.Fatalf("chapter 2 was not rewritten before extending: %#v", project.Manuscript)
	}
}

func TestQuickWriteExhaustedRepairBudgetMarksAndMovesOn(t *testing.T) {
	// 按审阅标准判的阻塞，自动修订用尽后标记在章上接着写（D94）：不停下等用户；预算按章
	// 的连续自动修订计，新开一轮也不让标记过的章重来。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: testTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 3,
	}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "budget-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write until budget exhausted: %v", err)
	}
	// 规划 1 + 两章 2 + 审阅/重写交替（阻塞 3 次、重写 2 次用尽预算）= 8 次，之后标记完成。
	if result.RunState != model.RunCompleted || executor.calls != 8 ||
		result.RunReason != "全书 2 章完成；第 2 章自动修订后仍有审阅意见，已标记" {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	snapshot, err := api.Workbench.WorkbenchSnapshot(ctx, command.ProjectID)
	if err != nil || len(snapshot.Findings) != 1 || snapshot.Findings[0].ChapterID != "chapter-chapter-plan-2" ||
		snapshot.Findings[0].Severity != model.FindingBlocking {
		t.Fatalf("marked findings = %#v, %v", snapshot.Findings, err)
	}
	// 标记与推导同一份规则；时间线在放下它的那次审阅处交代“已标记，接着写”。
	if want := []novelapp.Mark{{ChapterID: "chapter-chapter-plan-2", Number: 2, Repairs: 2}}; !slices.Equal(snapshot.Marks, want) {
		t.Fatalf("marks = %#v", snapshot.Marks)
	}
	var timeline []string
	for _, milestone := range snapshot.Milestones {
		timeline = append(timeline, milestone.Text)
	}
	marked := slices.Index(timeline, "第 2 章《第2章》修订 2 次仍未改好，已标记，接着写")
	if marked < 1 || timeline[marked-1] != "审阅第 1–2 章：未通过 · 1 处未解决" {
		t.Fatalf("timeline = %q", timeline)
	}
	resume := command
	resume.CreatedAt = testTime().Add(time.Minute)
	if result, err = api.Novels.QuickWrite(ctx, resume); err != nil || result.RunState != model.RunCompleted ||
		result.RunID != "run:budget-book:2" || executor.calls != 8 {
		t.Fatalf("a new run must not rewrite the marked chapter again: %#v calls=%d err=%v", result, executor.calls, err)
	}
}

func TestQuickWriteUnmetRequirementWaitsAndReworkResumes(t *testing.T) {
	// 违反用户要求的阻塞，自动修订用尽后仍等用户（D94）：照常续跑停在原处；用户说清怎么改
	// 返工这一章，预算重新起算，从落点写到完成。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: testTime(), blockChapter: "chapter-chapter-plan-2", unmetDirective: "hook", unmetRemaining: 3,
	}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "unmet-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	executor.onExecute = func(operation model.Operation) {
		if operation.ID != runQuickID("run:unmet-book:1", "chapter", "chapter-plan-1") {
			return
		}
		executor.onExecute = nil
		if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
			ProjectID: "unmet-book", ChangeID: "hook", UserID: "user-1", DirectiveID: "hook",
			Scope: "from_chapter:1", Text: "每章结尾留钩子", Reason: "写作中途提出要求",
			CreatedAt: command.CreatedAt.Add(30 * time.Minute),
		}); err != nil {
			t.Fatalf("mid-run directive: %v", err)
		}
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write until budget exhausted: %v", err)
	}
	// 规划 1 + 第 1 章（失效）1 + 后继 1 + 第 2 章 1 + 审阅/重写交替（未满足 3 次、重写 2 次）5 = 9 次。
	if result.RunState != model.RunWaitingUser || executor.calls != 9 ||
		!strings.Contains(result.RunReason, "第 2 章《第2章》自动修订了 2 次仍没做到你的要求") {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	// 预算用尽的等待不携带稿件；卡住的那一章由推导器报给工作台。
	if _, ok, err := api.Runs.WaitingProposal(ctx, result.RunID); err != nil || ok {
		t.Fatalf("budget waiting should carry no proposal: ok=%v err=%v", ok, err)
	}
	run, err := api.Runs.CreationRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("read waiting run: %v", err)
	}
	project, err := api.Projects.Project(ctx, "unmet-book", 0)
	if err != nil {
		t.Fatal(err)
	}
	if held, err := api.Reviews.RepairHold(ctx, project, run); err != nil || held != "chapter-chapter-plan-2" {
		t.Fatalf("repair hold = %q, %v", held, err)
	}
	resume := command
	resume.CreatedAt = testTime().Add(time.Hour)
	if result, err = api.Novels.QuickWrite(ctx, resume); err != nil || result.RunState != model.RunWaitingUser || executor.calls != 9 {
		t.Fatalf("plain resume must stay on the exhausted chapter: %#v calls=%d err=%v", result, executor.calls, err)
	}
	if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
		ProjectID: "unmet-book", ChangeID: "rework", UserID: "user-1", DirectiveID: "rework",
		Scope: model.DirectiveScopeChapters(2, 2), Text: "结尾停在信封上那行没写完的地址", Rework: true,
		Reason: "说清怎么改", CreatedAt: testTime().Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("rework directive: %v", err)
	}
	resume.CreatedAt = testTime().Add(3 * time.Hour)
	result, err = api.Novels.QuickWrite(ctx, resume)
	// 续跑：返工 1 + 末窗审阅通过 1 = 11 次。
	if err != nil || result.RunState != model.RunCompleted || executor.calls != 11 {
		t.Fatalf("resumed result = %#v, executor calls = %d, %v", result, executor.calls, err)
	}
}

// 第二次自动修订做到一半进程没了（D63）：续跑时那次修订还在队列里，接着把它做完，不能当成
// 预算已用尽去等用户、把它丢在队列里。
func TestQuickWriteResumesARepairCutShortByACrash(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 2}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "crash-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	crash := errors.New("进程没了")
	rewrites := 0
	executor.onExecute = func(operation model.Operation) {
		if operation.Kind == model.OperationRewriteChapter {
			if rewrites++; rewrites == 2 {
				executor.onExecute = nil
				panic(crash)
			}
		}
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != crash {
				panic(recovered)
			}
		}()
		api.Novels.QuickWrite(ctx, command)
	}()
	resume := command
	resume.CreatedAt = testTime().Add(3 * time.Minute)
	result, err := api.Novels.QuickWrite(ctx, resume)
	// 规划 1 + 两章 2 + 审阅/重写交替（阻塞 2 次）4 + 崩溃的那次重写重做 1 + 末窗审阅通过 1 = 9 次。
	if err != nil || result.RunState != model.RunCompleted || executor.calls != 9 {
		t.Fatalf("resumed result = %#v, executor calls = %d, %v", result, executor.calls, err)
	}
}

type scriptedQuickExecutor struct {
	now            time.Time
	calls          int
	authorityStore *store.Store
	onExecute      func(operation model.Operation)
	// fail 非空时每次执行都以它失败，用来验证失败的去向与旁白。
	fail error
	// blockChapter/blockRemaining：让若干次审阅对指定正文章节给出阻塞发现，
	// 用来验证审阅-重写闭环。
	blockChapter   string
	blockRemaining int
	// blockOnce：第一次审阅对这些章同时给出阻塞发现；reviews 记录每次审阅的输入。
	// 已审且正文未变的章（D68）只作上下文，脚本同样不对它们判阻塞。
	blockOnce []string
	reviews   []model.ReviewRangeInput
	// passNote：审阅通过时附带的 note 级发现，用来验证意见流进下一段规划。
	passNote string
	// unmetDirective/unmetRemaining：让若干次审阅把指定要求核验为未满足，
	// 阻塞发现落在 blockChapter，用来验证要求核验驱动的重写闭环。
	unmetDirective string
	unmetRemaining int
	// rewrites：按章记录重写轮次，让 canon 前值在多轮重写间保持连续。
	rewrites map[string]int
	// finale/scaleMax/raiseTo：篇幅交给 AI 时的规划（D63）——写罗盘（预计默认 50），
	// 蓝图够到 finale 章时声明收官；raiseTo 非零时扩窗上调预计（D73）。
	finale, scaleMax, raiseTo int
	// batch：每次规划追加的章数（D67 由规划者决定），0 取 3。
	batch int
}

func newQuickTestApp(t *testing.T, executor *scriptedQuickExecutor) *testApp {
	t.Helper()
	authorityStore := openTestStore(t)
	executor.authorityStore = authorityStore
	api := newTestAppWithExecutor(authorityStore, executor)
	api.now = testTime
	return api
}

func (e *scriptedQuickExecutor) Identity() string { return prompt.ExecutorIdentity }

// planCompass 返回本次规划的末章与罗盘补丁：罗盘总带一条长线（D91），固定篇幅时只写终局。
// 章数由"模型"自定（D67）：每次追加 batch 章，不越过固定篇幅与启动时的预计篇幅，至少追加
// 一章；蓝图够到 finale 章时声明收官。
func (e *scriptedQuickExecutor) planCompass(fixed, existing int, extending bool) (int, []model.Patch, error) {
	batch := cmp.Or(e.batch, 3)
	compass := model.Compass{Ending: "送完最后一封信", Threads: []model.StoryThread{{ID: "t1", Name: "信", Question: "最后一封信能否送到"}}}
	last := min(existing+batch, fixed)
	if fixed == 0 {
		compass.ScaleMax = cmp.Or(e.scaleMax, 50)
		last = max(existing+1, min(existing+batch, compass.ScaleMax))
		if extending && e.raiseTo > 0 {
			compass.ScaleMax = e.raiseTo
		}
		if e.finale > 0 && last >= e.finale {
			last, compass.Final = e.finale, e.finale
		}
	}
	content, err := json.Marshal(compass)
	if err != nil {
		return 0, nil, err
	}
	return last, []model.Patch{{
		Document: model.DocumentRef{Kind: model.DocumentCompass, ID: model.SingletonDocumentID}, Operation: model.PatchPut, Content: content,
	}}, nil
}

func (e *scriptedQuickExecutor) Execute(ctx context.Context, operation model.Operation) (model.OperationOutcome, error) {
	e.calls++
	if e.onExecute != nil {
		e.onExecute(operation)
	}
	if e.fail != nil {
		return model.OperationOutcome{}, e.fail
	}
	var patches []model.Patch
	switch operation.Kind {
	case model.OperationDevelopPlan:
		input, err := model.TaskInputAs[model.DevelopPlanInput](operation)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		// 规划提案同时登记主角实体：随章 Canon Delta 的主体必须是已登记实体（D35）。
		hero, err := json.Marshal(model.Entity{ID: "hero", Kind: model.EntityCharacter, Name: "邮差"})
		if err != nil {
			return model.OperationOutcome{}, err
		}
		patches = append(patches, model.Patch{
			Document: model.DocumentRef{Kind: model.DocumentEntity, ID: "hero"}, Operation: model.PatchPut, Content: hero,
		})
		values := []model.PlanNode{
			{ID: "volume-1", Kind: model.PlanVolume, Order: 1, Title: "余信", Summary: "完成亡者留下的托付"},
			{ID: "arc-1", Kind: model.PlanArc, ParentID: "volume-1", Order: 1, Title: "启程", Summary: "邮差追索身份与收信人"},
		}
		last, compass, err := e.planCompass(input.FixedChapters, 0, false)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		patches = append(patches, compass...)
		for number := 1; number <= last; number++ {
			values = append(values, model.PlanNode{
				ID: fmt.Sprintf("chapter-plan-%d", number), Kind: model.PlanChapter, Turn: "转折",
				ParentID: "arc-1", Order: number, Title: fmt.Sprintf("第%d章", number),
				Summary: fmt.Sprintf("送出第%d封关键书信", number),
			})
		}
		for _, value := range values {
			content, err := json.Marshal(value)
			if err != nil {
				return model.OperationOutcome{}, err
			}
			patches = append(patches, model.Patch{
				Document:  model.DocumentRef{Kind: model.DocumentPlan, ID: value.ID},
				Operation: model.PatchPut, Content: content,
			})
		}
	case model.OperationRevisePlan:
		input, err := model.TaskInputAs[model.RevisePlanInput](operation)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		last, compass, err := e.planCompass(input.FixedChapters, input.ExistingChapters, true)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		patches = append(patches, compass...)
		for number := input.ExistingChapters + 1; number <= last; number++ {
			node := model.PlanNode{
				ID: fmt.Sprintf("chapter-plan-%d", number), Kind: model.PlanChapter, Turn: "转折",
				ParentID: "arc-1", Order: number, Title: fmt.Sprintf("第%d章", number),
				Summary: fmt.Sprintf("送出第%d封关键书信", number),
			}
			content, err := json.Marshal(node)
			if err != nil {
				return model.OperationOutcome{}, err
			}
			patches = append(patches, model.Patch{
				Document:  model.DocumentRef{Kind: model.DocumentPlan, ID: node.ID},
				Operation: model.PatchPut, Content: content,
			})
		}
	case model.OperationWriteChapter:
		input, err := model.TaskInputAs[model.WriteChapterInput](operation)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		number, err := e.chapterNumber(ctx, operation, input.ChapterPlanID)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		chapter := model.ManuscriptChapter{
			ID: "chapter-" + input.ChapterPlanID, PlanNodeID: input.ChapterPlanID,
			Number: number, Author: model.AuthorAI,
			Blocks: []model.ManuscriptBlock{{
				ID:   fmt.Sprintf("chapter-%d-block-1", number),
				Text: fmt.Sprintf("第%d章正文", number),
			}},
		}
		content, err := json.Marshal(chapter)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		fact := model.CanonFact{
			ID: chapter.ID + "-outcome", Kind: model.CanonEvent, SubjectID: "hero",
			Predicate: "event.chapter_outcome", Value: json.RawMessage(fmt.Sprintf(`"第%d章"`, number)),
			SourceChapterID: chapter.ID,
		}
		canon, err := json.Marshal(fact)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		patches = []model.Patch{
			{Document: model.DocumentRef{Kind: model.DocumentManuscript, ID: chapter.ID}, Operation: model.PatchPut, Content: content},
			{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: fact.ID}, Operation: model.PatchPut, Content: canon},
		}
	case model.OperationReviewRange:
		input, err := model.TaskInputAs[model.ReviewRangeInput](operation)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		e.reviews = append(e.reviews, input)
		verdict := model.ReviewVerdict{
			Revision:   operation.Snapshot.BaseRevision,
			ChapterIDs: input.ChapterIDs, ReviewKey: "scripted-review", Basis: input.Basis,
			Findings: []model.ReviewFinding{},
		}
		blockable := func(id string) bool {
			return slices.Contains(input.ChapterIDs, id) && !slices.Contains(input.Reviewed, id)
		}
		// 脚本化审阅对投递的要求一律判满足（settle 项因此也有结论），指定要求按次数判违反。
		for _, requirement := range input.Requirements {
			check := model.RequirementCheck{ID: requirement.ID, Status: model.CheckSatisfied}
			if requirement.ID == "directive:"+e.unmetDirective && e.unmetRemaining > 0 {
				e.unmetRemaining--
				check.Status, check.Note = model.CheckViolated, "结尾没有留下钩子"
				verdict.Findings = append(verdict.Findings, model.ReviewFinding{
					ChapterID: e.blockChapter, Severity: model.FindingBlocking, Note: "按要求补上结尾钩子：" + requirement.Text,
					Requirement: requirement.ID,
				})
			}
			verdict.Checks = append(verdict.Checks, check)
		}
		if e.blockRemaining > 0 && blockable(e.blockChapter) {
			e.blockRemaining--
			verdict.Findings = append(verdict.Findings, model.ReviewFinding{
				ChapterID: e.blockChapter, Severity: model.FindingBlocking, Note: "结尾太仓促，补全告别场景",
			})
		}
		for _, id := range e.blockOnce {
			if blockable(id) {
				verdict.Findings = append(verdict.Findings, model.ReviewFinding{ChapterID: id, Severity: model.FindingBlocking, Note: "人物前后矛盾"})
			}
		}
		e.blockOnce = nil
		if e.passNote != "" && verdict.Blocking() == 0 {
			verdict.Findings = append(verdict.Findings, model.ReviewFinding{
				ChapterID: input.ChapterIDs[0], Severity: model.FindingNote, Note: e.passNote,
			})
		}
		findings, err := json.Marshal(verdict.Findings)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		if _, err := e.authorityStore.PutWorkspaceArtifact(ctx, model.WorkspaceArtifact{
			OperationID: operation.ID, Key: verdict.ReviewKey,
			MediaType: model.ReviewArtifactMediaType, Content: findings,
			UpdatedAt: e.now.Add(time.Duration(e.calls) * time.Second),
		}, nil, operation.Attempt); err != nil {
			return model.OperationOutcome{}, err
		}
		payload, err := json.Marshal(verdict)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		return model.OperationOutcome{Verdict: payload}, nil
	case model.OperationRewriteChapter:
		input, err := model.TaskInputAs[model.RewriteChapterInput](operation)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		number, err := e.chapterNumber(ctx, operation, input.ChapterPlanID)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		if patches, err = e.rewrite(input.ChapterID, input.ChapterPlanID, number, "按审阅意见重写："+strings.Join(input.Findings, "；")); err != nil {
			return model.OperationOutcome{}, err
		}
	case model.OperationReworkChapter:
		input, err := model.TaskInputAs[model.ReworkChapterInput](operation)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		number, err := e.chapterNumber(ctx, operation, input.ChapterPlanID)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		if patches, err = e.rewrite(input.ChapterID, input.ChapterPlanID, number, "按返工要求重写"); err != nil {
			return model.OperationOutcome{}, err
		}
	case model.OperationReviseCanon:
		input, err := model.TaskInputAs[model.ReviseCanonInput](operation)
		if err != nil {
			return model.OperationOutcome{}, err
		}
		// 待核验事实原样确认；未入账的章补记一条。
		for _, id := range input.FactIDs {
			stored, err := e.authorityStore.GetDocument(ctx, operation.Target, model.DocumentRef{Kind: model.DocumentCanon, ID: id}, operation.Snapshot.BaseRevision)
			if err != nil {
				return model.OperationOutcome{}, err
			}
			var fact model.CanonFact
			if err := json.Unmarshal(stored.Content, &fact); err != nil {
				return model.OperationOutcome{}, err
			}
			fact.PreviousValue = fact.Value
			content, err := json.Marshal(fact)
			if err != nil {
				return model.OperationOutcome{}, err
			}
			patches = append(patches, model.Patch{Document: stored.Document, Operation: model.PatchPut, Content: content})
		}
		if len(input.FactIDs) == 0 {
			content, err := json.Marshal(model.CanonFact{
				ID: input.ChapterID + "-outcome", Kind: model.CanonEvent, SubjectID: "hero",
				Predicate: "event.chapter_outcome", Value: json.RawMessage(`"补账"`), SourceChapterID: input.ChapterID,
			})
			if err != nil {
				return model.OperationOutcome{}, err
			}
			patches = append(patches, model.Patch{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: input.ChapterID + "-outcome"}, Operation: model.PatchPut, Content: content})
		}
	default:
		return model.OperationOutcome{}, fmt.Errorf("unexpected operation kind %s", operation.Kind)
	}
	return model.OperationOutcome{Proposal: &model.Proposal{
		ID: operation.ID + "-proposal", OperationID: operation.ID, Target: operation.Target,
		BaseRevision: operation.Snapshot.BaseRevision,
		Author:       model.Author{Kind: model.AuthorAI, ID: "scripted.writer@1"},
		Reason:       "quick write candidate", Patches: patches,
		ApprovalState: model.ApprovalPending, CreatedAt: e.now.Add(time.Duration(e.calls) * time.Second),
	}}, nil
}

// rewrite 是重写一章的提交：正文写明为什么重写，随章事实按轮次连续更新。
// chapterNumber 是章节计划在任务基线上的位置（D66）。
func (e *scriptedQuickExecutor) chapterNumber(ctx context.Context, operation model.Operation, planID string) (int, error) {
	documents, err := e.authorityStore.ListDocuments(ctx, operation.Target, model.DocumentPlan, operation.Snapshot.BaseRevision)
	if err != nil {
		return 0, err
	}
	plan := make([]model.PlanNode, len(documents))
	for i, document := range documents {
		if err := json.Unmarshal(document.Content, &plan[i]); err != nil {
			return 0, err
		}
	}
	return slices.IndexFunc(model.ChapterPlansInOrder(plan), func(node model.PlanNode) bool { return node.ID == planID }) + 1, nil
}

func (e *scriptedQuickExecutor) rewrite(chapterID, planID string, number int, note string) ([]model.Patch, error) {
	content, err := json.Marshal(model.ManuscriptChapter{
		ID: chapterID, PlanNodeID: planID, Number: number, Author: model.AuthorAI,
		Blocks: []model.ManuscriptBlock{{ID: fmt.Sprintf("chapter-%d-block-1", number), Text: fmt.Sprintf("第%d章正文（%s）", number, note)}},
	})
	if err != nil {
		return nil, err
	}
	if e.rewrites == nil {
		e.rewrites = map[string]int{}
	}
	round := e.rewrites[chapterID]
	e.rewrites[chapterID] = round + 1
	fact := model.CanonFact{
		ID: chapterID + "-outcome", Kind: model.CanonEvent, SubjectID: "hero",
		Predicate:       "event.chapter_outcome",
		PreviousValue:   json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("第%d章", number)+strings.Repeat("（重写）", round))),
		Value:           json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("第%d章", number)+strings.Repeat("（重写）", round+1))),
		SourceChapterID: chapterID,
	}
	canon, err := json.Marshal(fact)
	if err != nil {
		return nil, err
	}
	return []model.Patch{
		{Document: model.DocumentRef{Kind: model.DocumentManuscript, ID: chapterID}, Operation: model.PatchPut, Content: content},
		{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: fact.ID}, Operation: model.PatchPut, Content: canon},
	}, nil
}

// D67：规划者一次铺满固定篇幅，提交边界照收；短章在审阅容量内同窗审完即完成。
func TestQuickWritePlannerMayLayOutTheWholeBook(t *testing.T) {
	executor := &scriptedQuickExecutor{now: testTime(), batch: 5}
	api := newQuickTestApp(t, executor)
	result, err := api.Novels.QuickWrite(context.Background(), novelapp.QuickWriteCommand{
		ProjectID: "whole-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 5, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	})
	// 规划 1 + 五章 5 + 同窗审阅 1 = 7 次。
	if err != nil || result.RunState != model.RunCompleted || len(result.Chapters) != 5 || executor.calls != 7 {
		t.Fatalf("result = %#v, executor calls = %d, err = %v", result, executor.calls, err)
	}
}

func TestQuickWriteUnrelatedDirectiveKeepsWindowVerdictValid(t *testing.T) {
	// D48：审阅证据按基线失效，不按整本 Revision。扩窗时用户对第 5 章提要求，
	// 1–3 章的窗口审阅证据仍然有效、不重审；对第 2 章提要求则必须重审。
	ctx := context.Background()
	run := func(projectID, scope string) (novelapp.QuickWriteResult, *scriptedQuickExecutor) {
		t.Helper()
		executor := &scriptedQuickExecutor{now: testTime()}
		api := newQuickTestApp(t, executor)
		command := novelapp.QuickWriteCommand{
			ProjectID: projectID, UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
			Chapters: 5, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
		}
		executor.onExecute = func(operation model.Operation) {
			if operation.ID != runQuickID("run:"+projectID+":1", "plan", "extend", "3", "f5:s0:e0") {
				return
			}
			if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
				ProjectID: projectID, ChangeID: "mid-run-directive", UserID: "user-1", DirectiveID: "late",
				Scope: scope, Text: "结尾要有告别场景", Reason: "扩窗时提出要求",
				CreatedAt: command.CreatedAt.Add(30 * time.Minute),
			}); err != nil {
				t.Fatalf("mid-run directive: %v", err)
			}
		}
		result, err := api.Novels.QuickWrite(ctx, command)
		if err != nil {
			t.Fatalf("quick write %s: %v", projectID, err)
		}
		return result, executor
	}
	reviewed := func(executor *scriptedQuickExecutor, runID string, revision model.Revision) bool {
		t.Helper()
		_, ok := findReview(t, executor.authorityStore, runID, revision)
		return ok
	}
	// 规划 1 + 三章 3 + 窗口审阅 1 + 扩窗（提案重定位，D51）1 + 两章 2 + 末窗审阅 1 = 9 次；
	// 要求提交后（Revision 6）没有新的窗口审阅。
	unrelated, executor := run("late-directive-book", "from_chapter:5")
	if unrelated.RunState != model.RunCompleted || unrelated.Revision != 9 || executor.calls != 9 ||
		reviewed(executor, unrelated.RunID, 6) {
		t.Fatalf("unrelated directive result = %#v, executor calls = %d", unrelated, executor.calls)
	}
	// 覆盖第 2 章的要求改变了窗口裁定的作用域基线：扩窗失效、重审一次再扩，共 11 次。
	related, executor := run("early-directive-book", "chapter_range:2-2")
	if related.RunState != model.RunCompleted || related.Revision != 9 || executor.calls != 11 ||
		!reviewed(executor, related.RunID, 6) {
		t.Fatalf("related directive result = %#v, executor calls = %d", related, executor.calls)
	}
}

// TestQuickWriteAcceptedFindingClearsMarkAndWithdrawRestoresIt 是 D43 在标记上的闭环（D94）：
// 预算用尽标记完成 → 用户接受阻塞发现，标记消失、全书算通过；撤回后标记恢复，新一轮
// 不因此再重写。
func TestQuickWriteAcceptedFindingClearsMarkAndWithdrawRestoresIt(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: testTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 3,
	}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "accept-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil || result.RunState != model.RunCompleted || executor.calls != 8 || !strings.Contains(result.RunReason, "已标记") {
		t.Fatalf("result = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
	snapshot, err := api.Workbench.WorkbenchSnapshot(ctx, command.ProjectID)
	if err != nil || len(snapshot.Findings) != 1 || snapshot.Findings[0].Severity != model.FindingBlocking {
		t.Fatalf("findings before adjudication = %#v, %v", snapshot.Findings, err)
	}
	accept := novelapp.AddAdjudicationCommand{
		ProjectID: command.ProjectID, ChangeID: "accept-1", UserID: "user-1",
		Findings: []string{snapshot.Findings[0].ID}, Reason: "仓促的告别也是一种风格", CreatedAt: testTime().Add(time.Minute),
	}
	accepted, err := api.Reviews.AddAdjudication(ctx, accept)
	if err != nil || len(accepted.Adjudications) != 1 || accepted.Adjudications[0].Finding != accept.Findings[0] ||
		accepted.Adjudications[0].Basis.Equal(model.EvidenceBasis{}) {
		t.Fatalf("accepted = %#v, %v", accepted, err)
	}
	accept.CreatedAt = testTime().Add(2 * time.Minute)
	if again, err := api.Reviews.AddAdjudication(ctx, accept); err != nil || again.Revision != accepted.Revision {
		t.Fatalf("retry must be idempotent: %#v, %v", again, err)
	}
	if snapshot, err = api.Workbench.WorkbenchSnapshot(ctx, command.ProjectID); err != nil ||
		len(snapshot.Findings) != 0 || len(snapshot.Adjudications) != 1 {
		t.Fatalf("snapshot after adjudication = findings %#v, adjudications %#v, %v", snapshot.Findings, snapshot.Adjudications, err)
	}
	resume := command
	resume.CreatedAt = testTime().Add(3 * time.Minute)
	if result, err = api.Novels.QuickWrite(ctx, resume); err != nil || result.RunState != model.RunCompleted ||
		result.RunReason != "全书 2 章完成并通过审阅" || executor.calls != 8 {
		t.Fatalf("resume after adjudication = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
	if _, err := api.Reviews.WithdrawAdjudication(ctx, novelapp.WithdrawAdjudicationCommand{
		ProjectID: command.ProjectID, ChangeID: "withdraw-1", UserID: "user-1",
		AdjudicationID: "accept-1:1", Reason: "还是改一下", CreatedAt: testTime().Add(4 * time.Minute),
	}); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, err := api.Reviews.WithdrawAdjudication(ctx, novelapp.WithdrawAdjudicationCommand{
		ProjectID: command.ProjectID, ChangeID: "withdraw-2", UserID: "user-1",
		AdjudicationID: "accept-1:1", Reason: "再撤一次", CreatedAt: testTime().Add(5 * time.Minute),
	}); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("second withdraw err = %v", err)
	}
	if snapshot, err = api.Workbench.WorkbenchSnapshot(ctx, command.ProjectID); err != nil ||
		len(snapshot.Findings) != 1 || len(snapshot.Adjudications) != 0 {
		t.Fatalf("snapshot after withdrawal = findings %#v, adjudications %#v, %v", snapshot.Findings, snapshot.Adjudications, err)
	}
	// 新一轮：标记恢复，这一章的自动修订早已用尽，不再重写。
	resume.CreatedAt = testTime().Add(6 * time.Minute)
	if result, err = api.Novels.QuickWrite(ctx, resume); err != nil || result.RunState != model.RunCompleted ||
		result.RunID != "run:accept-book:3" || !strings.Contains(result.RunReason, "已标记") || executor.calls != 8 {
		t.Fatalf("rerun after withdrawal = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
}

// TestAdjudicationExpiresWhenChapterChanges：裁决绑定被接受的基线（D43/D48），
// 用户改了该章正文，裁决与裁定一起失效。
func TestAdjudicationExpiresWhenChapterChanges(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: testTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 3,
	}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "expire-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	if result, err := api.Novels.QuickWrite(ctx, command); err != nil || result.RunState != model.RunCompleted {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	snapshot, err := api.Workbench.WorkbenchSnapshot(ctx, command.ProjectID)
	if err != nil || len(snapshot.Findings) != 1 {
		t.Fatalf("findings = %#v, %v", snapshot.Findings, err)
	}
	if _, err := api.Reviews.AddAdjudication(ctx, novelapp.AddAdjudicationCommand{
		ProjectID: command.ProjectID, ChangeID: "accept-1", UserID: "user-1",
		Findings: []string{snapshot.Findings[0].ID}, Reason: "接受", CreatedAt: testTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	projection, err := api.Projects.ExportProject(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for index := range projection.Manuscript {
		if projection.Manuscript[index].ID == "chapter-chapter-plan-2" {
			projection.Manuscript[index].Blocks[0].Text += "（用户手工润色）"
		}
	}
	proposal, err := api.Projects.ImportProject(ctx, "edit-chapter-2", "user-1", "手工润色", projection, testTime().Add(2*time.Minute))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := api.Decisions.Approve(ctx, proposal.ID, "user-1", testTime().Add(3*time.Minute)); err != nil {
		t.Fatalf("approve import: %v", err)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if len(project.Adjudications) != 1 {
		t.Fatalf("adjudication records = %#v", project.Adjudications)
	}
	valid, err := api.Reviews.ValidAdjudications(ctx, project)
	if err != nil || len(valid) != 0 {
		t.Fatalf("valid adjudications after chapter edit = %#v, %v", valid, err)
	}
	if snapshot, err = api.Workbench.WorkbenchSnapshot(ctx, command.ProjectID); err != nil || len(snapshot.Adjudications) != 0 {
		t.Fatalf("workbench adjudications after edit = %#v, %v", snapshot.Adjudications, err)
	}
}

// TestQuickWriteDisjointDirectiveMidRunRelocatesInFlightChapter 是 S12 的重定位半边（D51）：
// 写第 2 章时用户给第 5 章提要求，第 2 章的提案重定位后照常提交，没有后继、不重跑模型；
// 要求按当前快照进入第 5 章的任务输入。
func TestQuickWriteDisjointDirectiveMidRunRelocatesInFlightChapter(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "disjoint-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 5, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	executor.onExecute = func(operation model.Operation) {
		if operation.ID != runQuickID("run:disjoint-book:1", "chapter", "chapter-plan-2") {
			return
		}
		if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
			ProjectID: "disjoint-book", ChangeID: "late-farewell", UserID: "user-1", DirectiveID: "farewell",
			Scope: "from_chapter:5", Text: "结尾要有告别场景", Reason: "写作中途提出要求",
			CreatedAt: command.CreatedAt.Add(30 * time.Minute),
		}); err != nil {
			t.Fatalf("mid-run directive: %v", err)
		}
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	// 规划 1 + 三章 3 + 窗口审阅 1 + 扩窗 1 + 两章 2 + 末窗审阅 1 = 9 次，第 2 章没有后继。
	if result.RunState != model.RunCompleted || result.Revision != 9 || executor.calls != 9 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	chapterTwo := runQuickID(result.RunID, "chapter", "chapter-plan-2")
	if _, err := executor.authorityStore.GetOperation(ctx, chapterTwo+":redo2"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("chapter 2 must not need a successor, got %v", err)
	}
	proposal, err := api.Decisions.ProposalForOperation(ctx, chapterTwo)
	if err != nil || proposal.BaseRevision != 4 || proposal.ApprovalState != model.ApprovalApproved {
		t.Fatalf("relocated chapter 2 proposal = %#v, %v", proposal, err)
	}
	operation, err := executor.authorityStore.GetOperation(ctx, runQuickID(result.RunID, "chapter", "chapter-plan-5"))
	if err != nil {
		t.Fatalf("read chapter 5 operation: %v", err)
	}
	input, err := model.TaskInputAs[model.WriteChapterInput](operation)
	if err != nil || len(input.Directives) != 1 || input.Directives[0].ID != "farewell" {
		t.Fatalf("chapter 5 input = %#v, %v", input, err)
	}
}

// TestQuickWriteDriftDuringReviewFollowsBasis：审阅期间的漂移按裁定基线判定（D48/D51）——
// 锁定设定不作废裁定，覆盖范围内章节的要求才使审阅失效并重审。
func TestQuickWriteDriftDuringReviewFollowsBasis(t *testing.T) {
	ctx := context.Background()
	run := func(projectID string, drift func(api *testApp, at time.Time)) (novelapp.QuickWriteResult, *scriptedQuickExecutor) {
		t.Helper()
		executor := &scriptedQuickExecutor{now: testTime()}
		api := newQuickTestApp(t, executor)
		command := novelapp.QuickWriteCommand{
			ProjectID: projectID, UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
			Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
		}
		executor.onExecute = func(operation model.Operation) {
			if reviewAt(operation.ID, "run:"+projectID+":1", 4) {
				drift(api, command.CreatedAt.Add(30*time.Minute))
			}
		}
		result, err := api.Novels.QuickWrite(ctx, command)
		if err != nil {
			t.Fatalf("quick write %s: %v", projectID, err)
		}
		return result, executor
	}
	// 规划 1 + 两章 2 + 末窗审阅 1 = 4 次：审阅期间锁定设定，裁定照常生效。
	locked, executor := run("locked-review-book", func(api *testApp, at time.Time) {
		if _, err := api.Projects.SetOwnership(ctx, projectdoc.SetOwnershipCommand{
			ProjectID: "locked-review-book", ChangeID: "review-lock", UserID: "user-1",
			Target: model.DocumentRef{Kind: model.DocumentPlan, ID: "arc-1"}, Control: model.ControlLocked,
			Reason: "审阅中途锁定", CreatedAt: at,
		}); err != nil {
			t.Fatalf("lock during review: %v", err)
		}
	})
	if locked.RunState != model.RunCompleted || executor.calls != 4 {
		t.Fatalf("locked result = %#v, executor calls = %d", locked, executor.calls)
	}
	// 覆盖第 1 章起的要求改变了裁定的作用域基线：审阅失效、重审一次，共 5 次。
	related, executor := run("related-review-book", func(api *testApp, at time.Time) {
		if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
			ProjectID: "related-review-book", ChangeID: "review-hook", UserID: "user-1", DirectiveID: "hook",
			Scope: "from_chapter:1", Text: "每章结尾留钩子", Reason: "审阅中途提出要求", CreatedAt: at,
		}); err != nil {
			t.Fatalf("directive during review: %v", err)
		}
	})
	if related.RunState != model.RunCompleted || executor.calls != 5 {
		t.Fatalf("related result = %#v, executor calls = %d", related, executor.calls)
	}
	stale, ok := findReview(t, executor.authorityStore, related.RunID, 4)
	if !ok || stale.State != model.OperationStale {
		t.Fatalf("first review = %#v, found %v; want stale", stale, ok)
	}
}

// TestQuickWriteUserEditedChapterIsVerifiedBeforeNextWrite 是 D41 第 3 条的闭环：用户改了
// 第 1 章正文后再续写，先核验第 1 章的来源事实，再重审、扩窗、写第 4 章；核验后缺口消失。
func TestQuickWriteUserEditedChapterIsVerifiedBeforeNextWrite(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "edited-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil || result.RunState != model.RunCompleted || result.Revision != 5 || executor.calls != 5 {
		t.Fatalf("first run = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
	projection, err := api.Projects.ExportProject(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for index := range projection.Manuscript {
		if projection.Manuscript[index].ID == "chapter-chapter-plan-1" {
			projection.Manuscript[index].Blocks[0].Text += "（用户改写了开头）"
		}
	}
	proposal, err := api.Projects.ImportProject(ctx, "edit-chapter-1", "user-1", "手工润色", projection, testTime().Add(time.Hour))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := api.Decisions.Approve(ctx, proposal.ID, "user-1", testTime().Add(time.Hour+time.Minute)); err != nil {
		t.Fatalf("approve import: %v", err)
	}
	snapshot, err := api.Workbench.WorkbenchSnapshot(ctx, command.ProjectID)
	if err != nil || len(snapshot.Briefs[1].Facts) != 1 || !snapshot.Briefs[1].Facts[0].Pending {
		t.Fatalf("chapter 1 facts after edit = %#v, %v", snapshot.Briefs[1].Facts, err)
	}
	// 续写到 4 章：核验 1 + 重审（第 1 章变化使其窗口裁定失效）1 + 扩窗 1 + 第 4 章 1 + 末窗审阅 1 = 10 次。
	command.Chapters, command.CreatedAt = 4, testTime().Add(2*time.Hour)
	if result, err = api.Novels.QuickWrite(ctx, command); err != nil || result.RunState != model.RunCompleted || executor.calls != 10 {
		t.Fatalf("second run = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
	verify, err := executor.authorityStore.GetOperation(ctx, runQuickID(result.RunID, "canon", "chapter-chapter-plan-1", "r6"))
	if err != nil || verify.State != model.OperationSucceeded {
		t.Fatalf("canon verification = %#v, %v", verify, err)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if gaps := novelapp.CanonGaps(project); len(gaps) != 0 {
		t.Fatalf("gaps after verification = %#v", gaps)
	}
}

// D63：一句话交给 AI 定篇幅——首次规划给出罗盘，扩窗时蓝图够到收官章便声明收官；
// 末窗必须对收官要求下结论，完成章数等于收官承诺。续跑沿用"交给 AI"。
func TestQuickWriteAILengthCompletesAtTheCommittedFinale(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime(), finale: 5}
	api := newQuickTestApp(t, executor)
	var finale []model.Requirement
	executor.onExecute = func(operation model.Operation) {
		if operation.Kind != model.OperationReviewRange {
			return
		}
		input, err := model.TaskInputAs[model.ReviewRangeInput](operation)
		if err != nil {
			t.Fatalf("review input: %v", err)
		}
		for _, requirement := range input.Requirements {
			if strings.HasPrefix(requirement.ID, "book:finale:") {
				finale = append(finale, requirement)
			}
		}
	}
	command := novelapp.QuickWriteCommand{
		ProjectID: "ai-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: novelapp.KeepChapters, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	// 规划、三章、窗口审阅、扩窗（收官于 5）、两章、末窗审阅。
	if result.RunState != model.RunCompleted || result.RunReason != "全书 5 章完成并通过审阅" || len(result.Chapters) != 5 || executor.calls != 9 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	if len(finale) != 1 || !finale[0].Settle || !strings.Contains(finale[0].Text, "送完最后一封信") {
		t.Fatalf("finale requirement = %+v", finale)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil || project.Compass == nil || project.Compass.Final != 5 {
		t.Fatalf("compass = %+v, %v", project.Compass, err)
	}
	run, err := api.Runs.CreationRun(ctx, result.RunID)
	if goal, decodeErr := model.DecodeNovelGoal(run.Goal); err != nil || decodeErr != nil || goal.TargetChapters != 0 {
		t.Fatalf("AI length goal = %+v, %v %v", goal, err, decodeErr)
	}
}

// 续写（D63）：完本后不给章数，撤回收官承诺、篇幅交回 AI；扩窗文案点明已写末章是结局，
// AI 重新收官后按新的全书章数完成。续写不能同时固定章数。
func TestQuickWriteExtendHandsTheContinuationToAI(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime(), finale: 5}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "sequel-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: novelapp.KeepChapters, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	if first, err := api.Novels.QuickWrite(ctx, command); err != nil || first.RunState != model.RunCompleted || executor.calls != 9 {
		t.Fatalf("first run = %#v, %v", first, err)
	}
	command.Extend, command.Chapters = true, 7
	if _, err := api.Novels.QuickWrite(ctx, command); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("extend with fixed chapters = %v, want ErrInvalid", err)
	}

	var goals []string
	executor.onExecute = func(operation model.Operation) {
		if operation.Kind == model.OperationRevisePlan {
			input, err := model.TaskInputAs[model.RevisePlanInput](operation)
			if err != nil {
				t.Fatalf("revise input: %v", err)
			}
			goals = append(goals, input.Goal)
		}
	}
	executor.finale = 7
	command.Chapters = novelapp.KeepChapters
	command.CreatedAt = command.CreatedAt.Add(time.Hour)
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	// 前五章的审阅仍有效（罗盘不进证据基线）：扩窗（收官于 7）、第 6、7 章、完成前同窗
	// 审第 6–7 章（窗口按容量切，D67），共 +4。
	if result.RunState != model.RunCompleted || result.RunReason != "全书 7 章完成并通过审阅" || len(result.Chapters) != 7 || executor.calls != 13 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	if len(goals) != 1 || !strings.Contains(goals[0], "第 5 章已作为全书结局写成，这是续写") {
		t.Fatalf("continuation goals = %q", goals)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil || project.Compass == nil || project.Compass.Final != 7 {
		t.Fatalf("compass = %+v, %v", project.Compass, err)
	}
}

// D73 篇幅护栏：开放期扩窗逼近预计篇幅时 AI 上调 scale_max——护栏以内自行继续；超过
// 护栏时 auto 档下也要等用户裁决，批准后按新预计继续，直到收官。
func TestQuickWriteAIScaleRaiseWaitsForUserBeyondAutonomy(t *testing.T) {
	for _, c := range []struct {
		name    string
		raiseTo int
		waits   bool
	}{
		{"within-ceiling", 8, false},
		{"beyond-ceiling", model.CompassAutonomyCeiling + 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			executor := &scriptedQuickExecutor{now: testTime(), finale: 5, scaleMax: 4, raiseTo: c.raiseTo}
			api := newQuickTestApp(t, executor)
			command := novelapp.QuickWriteCommand{
				ProjectID: "raise-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
				Chapters: 0, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
			}
			result, err := api.Novels.QuickWrite(ctx, command)
			if err != nil {
				t.Fatalf("quick write: %v", err)
			}
			if c.waits {
				// 预计 4 章：扩窗把预计提到护栏以上，等用户裁决。
				raise := runQuickID(result.RunID, "plan", "extend", "3", "f0:s4:e0")
				if result.RunState != model.RunWaitingUser || result.WaitingOperationID != raise || executor.calls != 6 {
					t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
				}
				if _, err := api.Decisions.Approve(ctx, raise+"-proposal", command.UserID, command.CreatedAt.Add(time.Hour)); err != nil {
					t.Fatalf("approve raise: %v", err)
				}
				command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
				if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
					t.Fatalf("resume after raise: %v", err)
				}
			}
			// 第 4 章、审第 4 章、扩窗收官于 5、第 5 章、审第 5 章。
			if result.RunState != model.RunCompleted || len(result.Chapters) != 5 || executor.calls != 11 {
				t.Fatalf("final = %#v, executor calls = %d", result, executor.calls)
			}
			project, err := api.Projects.Project(ctx, command.ProjectID, 0)
			if err != nil || project.Compass == nil || project.Compass.ScaleMax != c.raiseTo || project.Compass.Final != 5 {
				t.Fatalf("compass = %+v, %v", project.Compass, err)
			}
		})
	}
}

// D71：否决扩写方案的理由只作用于还没写的章节。已审窗口的要求作用域不变，续跑不重审
// 已写章节；重新规划的任务带着这条理由。
func TestRejectingExtensionPlanKeepsWrittenReviews(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime(), finale: 5, scaleMax: 4, raiseTo: model.CompassAutonomyCeiling + 1}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "reject-plan-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	raise := runQuickID(result.RunID, "plan", "extend", "3", "f0:s4:e0")
	if err != nil || result.WaitingOperationID != raise || len(executor.reviews) != 1 {
		t.Fatalf("raise wait = %#v, reviews = %d, %v", result, len(executor.reviews), err)
	}
	feedback := "下一段放慢，先把邮差的身世交代清楚"
	if _, err := api.Decisions.Reject(ctx, decision.RejectCommand{ProposalID: raise + "-proposal", UserID: command.UserID, Reason: feedback, At: command.CreatedAt.Add(time.Hour)}); err != nil {
		t.Fatalf("reject raise: %v", err)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil || len(project.Directives) != 1 || project.Directives[0].Scope != "from_chapter:4" {
		t.Fatalf("rejection directive = %#v, %v", project.Directives, err)
	}

	calls := executor.calls
	command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
	if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatalf("replan: %v", err)
	}
	// 重新规划再次上调上限而等待：其间只有这一次规划，第 1–3 章的审阅仍然有效。
	if result.WaitingOperationID == "" || executor.calls != calls+1 || len(executor.reviews) != 1 {
		t.Fatalf("replan = %#v, calls %d → %d, reviews = %d", result, calls, executor.calls, len(executor.reviews))
	}
	replan, err := api.store.GetOperation(ctx, result.WaitingOperationID)
	if err != nil || !strings.Contains(string(replan.Input), feedback) {
		t.Fatalf("replan input lacks the rejection feedback: %s, %v", replan.Input, err)
	}
}

// D63：等待 AI 上调上限时用户改为固定篇幅——罗盘的收官章数对齐到固定值（上下文只有一个
// 篇幅口径），按新篇幅续写；被取代的旧规划稿不再作为等待稿件呈现。
func TestQuickWriteFixingLengthSupersedesWaitingRaise(t *testing.T) {
	// 暂停保留运行等待的任务（D64）：续跑时照样核对并作废被取代的上调。
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "paused"}[paused], func(t *testing.T) {
			testFixingLengthSupersedesWaitingRaise(t, paused)
		})
	}
}

func testFixingLengthSupersedesWaitingRaise(t *testing.T, paused bool) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: testTime(), scaleMax: 4, raiseTo: model.CompassAutonomyCeiling + 1,
		blockChapter: "chapter-chapter-plan-4", unmetDirective: "hook", unmetRemaining: 10,
	}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "fix-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 0, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	raise := runQuickID("run:fix-book:1", "plan", "extend", "3", "f0:s4:e0")
	if err != nil || result.WaitingOperationID != raise {
		t.Fatalf("raise wait = %#v, %v", result, err)
	}
	if paused {
		run, err := api.Runs.PauseCreationRun(ctx, result.RunID, command.CreatedAt.Add(time.Minute))
		if err != nil || run.State != model.RunPaused || run.WaitingOperationID != raise {
			t.Fatalf("paused run = %+v, %v", run, err)
		}
	}

	if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
		ProjectID: "fix-book", ChangeID: "hook", UserID: "user-1", DirectiveID: "hook",
		Scope: "from_chapter:4", Text: "每章结尾留钩子", Reason: "等待时提出要求", CreatedAt: command.CreatedAt.Add(30 * time.Minute),
	}); err != nil {
		t.Fatalf("directive: %v", err)
	}
	command.Chapters, command.CreatedAt = 5, command.CreatedAt.Add(time.Hour)
	if result, err = api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatalf("fix length: %v", err)
	}
	// 第 4 章一直没做到要求：2 次自动重写用尽后等用户（D94），这次等待不带稿件。
	if result.RunState != model.RunWaitingUser || !strings.Contains(result.RunReason, "自动修订了 2 次仍没做到你的要求") {
		t.Fatalf("budget wait = %#v", result)
	}
	// 被取代的上调转为 stale（D64）：不再作为等待稿件呈现，旧稿也不能再被批准入账。
	if stale, err := api.store.GetOperation(ctx, raise); err != nil || stale.State != model.OperationStale {
		t.Fatalf("superseded raise = %+v, %v", stale, err)
	}
	if _, ok, err := api.Runs.WaitingProposal(ctx, result.RunID); err != nil || ok {
		t.Fatalf("superseded raise must not surface as the waiting proposal: ok=%v err=%v", ok, err)
	}
	superseded, err := api.store.GetProposalByOperation(ctx, raise)
	if err != nil {
		t.Fatalf("raise proposal: %v", err)
	}
	if _, err := api.Decisions.Approve(ctx, superseded.ID, "user-1", command.CreatedAt.Add(time.Minute)); err == nil {
		t.Fatal("a superseded raise must not be approved")
	}
	if rejected, err := api.Decisions.Reject(ctx, decision.RejectCommand{ProposalID: superseded.ID, UserID: "user-1", Reason: "不再需要", At: command.CreatedAt.Add(time.Minute)}); err != nil || rejected.ApprovalState != model.ApprovalRejected {
		t.Fatalf("rejecting a superseded raise = %+v, %v", rejected, err)
	}
	// 固定篇幅后篇幅只有用户一个口径（D70）：罗盘撤下 AI 的上限与收官，只留终局。
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil || project.Compass == nil || project.Compass.Final != 0 || project.Compass.ScaleMax != 0 ||
		project.Compass.Ending == "" || len(project.Manuscript) != 5 {
		t.Fatalf("fixed-length compass = %+v chapters=%d, %v", project.Compass, len(project.Manuscript), err)
	}
}
