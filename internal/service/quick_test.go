package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestQuickWriteUsesSamePersistentOperationPipelineAndResumesIdempotently(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	executor := &scriptedQuickExecutor{now: serviceTime(), authorityStore: authorityStore}
	api := NewWithExecutor(authorityStore, executor)
	api.now = serviceTime
	command := QuickWriteCommand{
		ProjectID: "quick-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	if result.Revision != 5 || len(result.Chapters) != 3 || executor.calls != 5 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
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
	repeated, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("resume completed quick write: %v", err)
	}
	if repeated.Revision != result.Revision || executor.calls != 5 {
		t.Fatalf("repeated = %#v, executor calls = %d", repeated, executor.calls)
	}
}

func TestCompletionContractRejectsExtraPlanChapters(t *testing.T) {
	project := ProjectSnapshot{
		Plan: []domain.PlanNode{
			{ID: "chapter-1", Kind: domain.PlanChapter, Order: 1, Title: "第一章", Summary: "开端"},
			{ID: "chapter-2", Kind: domain.PlanChapter, Order: 2, Title: "第二章", Summary: "推进"},
		},
		Manuscript: []domain.ManuscriptChapter{
			{ID: "manuscript-1", PlanNodeID: "chapter-1", Number: 1, Title: "第一章", Blocks: []domain.ManuscriptBlock{{ID: "block-1", Text: "正文"}}},
			{ID: "manuscript-2", PlanNodeID: "chapter-2", Number: 2, Title: "第二章", Blocks: []domain.ManuscriptBlock{{ID: "block-2", Text: "正文"}}},
		},
	}
	goal := domain.CreationRunGoal{Premise: "一章故事", TargetChapters: 1}
	if unmet := completionUnmet(project, goal); !strings.Contains(unmet, "蓝图有 2 章") {
		t.Fatalf("completion mismatch = %q", unmet)
	}
}

func TestQuickWriteManualApprovalWaitsForUserAndResumes(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "manual-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, Approval: domain.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}

	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write under manual approval: %v", err)
	}
	if result.RunID != "run:manual-book:1" || result.RunState != domain.RunWaitingUser ||
		result.WaitingOperationID != runQuickID(result.RunID, "plan") || executor.calls != 1 {
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
	waiting, ok, err := api.WaitingProposal(ctx, result.RunID)
	if err != nil || !ok || waiting.ID != result.WaitingOperationID+"-proposal" {
		t.Fatalf("waiting proposal = %#v ok=%v err=%v", waiting, ok, err)
	}

	proposals := []string{
		runQuickID(result.RunID, "plan") + "-proposal",
		runQuickID(result.RunID, "chapter", "chapter-plan-1") + "-proposal",
		runQuickID(result.RunID, "chapter", "chapter-plan-2") + "-proposal",
	}
	for round, proposalID := range proposals {
		at := command.CreatedAt.Add(time.Duration(round+1) * time.Hour)
		if _, err := api.Approve(ctx, proposalID, command.UserID, at); err != nil {
			t.Fatalf("approve %s: %v", proposalID, err)
		}
		command.CreatedAt = at.Add(time.Minute)
		if result, err = api.QuickWrite(ctx, command); err != nil {
			t.Fatalf("resume after approving %s: %v", proposalID, err)
		}
	}
	if result.RunState != domain.RunCompleted || result.Revision != 4 || len(result.Chapters) != 2 || executor.calls != 4 {
		t.Fatalf("final = %#v, executor calls = %d", result, executor.calls)
	}
}

func TestQuickWriteMilestonePresetPausesOnlyAtMilestones(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "milestone-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, Approval: domain.ApprovalMilestone,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}

	// 蓝图是重大变化，等待确认；例行章节（正文 + 随章事实）自动通过。
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write under milestone approval: %v", err)
	}
	if result.RunState != domain.RunWaitingUser || executor.calls != 1 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	if _, err := api.Approve(ctx, runQuickID(result.RunID, "plan")+"-proposal", command.UserID, command.CreatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
	if result, err = api.QuickWrite(ctx, command); err != nil {
		t.Fatalf("resume after plan approval: %v", err)
	}
	if result.RunState != domain.RunCompleted || result.Revision != 5 || executor.calls != 5 {
		t.Fatalf("final = %#v, executor calls = %d", result, executor.calls)
	}
}

func TestQuickWriteRejectedChapterIsRewrittenBySuccessor(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	authorityStore := openServiceStore(t)
	executor.authorityStore = authorityStore
	api := NewWithExecutor(authorityStore, executor)
	api.now = serviceTime
	command := QuickWriteCommand{
		ProjectID: "reject-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 1, Approval: domain.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	if _, err := api.QuickWrite(ctx, command); err != nil {
		t.Fatalf("quick write: %v", err)
	}
	if _, err := api.Approve(ctx, runQuickID("run:reject-book:1", "plan")+"-proposal", command.UserID, command.CreatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("write chapter candidate: %v", err)
	}
	if result.RunState != domain.RunWaitingUser || executor.calls != 2 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}

	// 否决候选后再次发起：后继带着工作区与否决理由重写，而不是原地续跑或直接失败。
	feedback := "结尾太仓促，请补全告别场景"
	chapterOperationID := runQuickID(result.RunID, "chapter", "chapter-plan-1")
	if _, err := api.Reject(ctx, chapterOperationID+"-proposal", command.UserID, feedback, command.CreatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("reject chapter: %v", err)
	}
	command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
	if result, err = api.QuickWrite(ctx, command); err != nil {
		t.Fatalf("rewrite after rejection: %v", err)
	}
	successor := chapterOperationID + ":r2"
	if result.RunState != domain.RunWaitingUser || executor.calls != 3 ||
		len(result.Chapters) != 1 || result.Chapters[0].OperationID != successor {
		t.Fatalf("rewrite = %#v, executor calls = %d", result, executor.calls)
	}
	// 否决理由入账为该章作用域的 Directive（§4.9），并随当前快照进入后继任务输入。
	project, err := api.Project(ctx, command.ProjectID, 0)
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
	if _, err := api.Reject(ctx, chapterOperationID+"-proposal", command.UserID, "另一条理由", command.CreatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("repeat reject: %v", err)
	}
	if again, err := api.Project(ctx, command.ProjectID, 0); err != nil || again.Revision != 3 || len(again.Directives) != 1 {
		t.Fatalf("repeated rejection must not add directives: rev=%d err=%v", again.Revision, err)
	}
	if _, err := api.Approve(ctx, successor+"-proposal", command.UserID, command.CreatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("approve rewrite: %v", err)
	}
	command.CreatedAt = command.CreatedAt.Add(2 * time.Hour)
	if result, err = api.QuickWrite(ctx, command); err != nil {
		t.Fatalf("finish after rewrite: %v", err)
	}
	if result.RunState != domain.RunCompleted || result.Revision != 4 {
		t.Fatalf("final = %#v", result)
	}
}

func TestQuickWriteMidRunTighteningMigratesInFlightWork(t *testing.T) {
	// S9：全自动写作中途用户锁定设定，基线推进使在途章节失效；
	// 后继继承血统在新边界下续写，整轮创作不中止、不换命令入口。
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	executor := &scriptedQuickExecutor{now: serviceTime()}
	executor.authorityStore = authorityStore
	api := NewWithExecutor(authorityStore, executor)
	api.now = serviceTime
	command := QuickWriteCommand{
		ProjectID: "tighten-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	executor.onExecute = func(operation domain.Operation) {
		if operation.ID != runQuickID("run:tighten-book:1", "chapter", "chapter-plan-1") {
			return
		}
		if _, err := api.SetOwnership(ctx, SetOwnershipCommand{
			ProjectID: "tighten-book", ChangeID: "mid-run-lock", UserID: "user-1",
			Target:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-1"},
			Control: domain.ControlLocked, Reason: "写作中途锁定关键节点",
			CreatedAt: command.CreatedAt.Add(30 * time.Minute),
		}); err != nil {
			t.Fatalf("mid-run lock: %v", err)
		}
	}
	// 收紧立即生效：在途第 1 章失效，后继继承血统；因缺少语义合规证据，
	// auto 保守升级为安全暂停等待裁决，而不是整轮失败。
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across mid-run tightening: %v", err)
	}
	successor := runQuickID(result.RunID, "chapter", "chapter-plan-1") + ":r2"
	if result.RunState != domain.RunWaitingUser || executor.calls != 3 ||
		result.WaitingOperationID != successor {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}

	// 用户裁决后同一入口继续：第 2 章同样在锁定约束下暂停，逐稿放行直至完成。
	for _, proposalID := range []string{
		successor + "-proposal",
		runQuickID(result.RunID, "chapter", "chapter-plan-2") + "-proposal",
	} {
		command.CreatedAt = command.CreatedAt.Add(time.Hour)
		if _, err := api.Approve(ctx, proposalID, command.UserID, command.CreatedAt); err != nil {
			t.Fatalf("approve %s: %v", proposalID, err)
		}
		command.CreatedAt = command.CreatedAt.Add(time.Minute)
		if result, err = api.QuickWrite(ctx, command); err != nil {
			t.Fatalf("resume after approving %s: %v", proposalID, err)
		}
	}
	if result.RunState != domain.RunCompleted || result.Revision != 5 || executor.calls != 5 ||
		result.Chapters[0].OperationID != successor {
		t.Fatalf("final = %#v, executor calls = %d", result, executor.calls)
	}
}

func TestQuickWriteExtendsGoalWithRollingPlan(t *testing.T) {
	// D5+D2：完本后把目标从 3 章提到 5 章——Intent 走用户提案更新，
	// 蓝图用 revise_plan 增量扩窗（保持已有节点稳定），续写到新目标。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "grow-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	first, err := api.QuickWrite(ctx, command)
	if err != nil || first.RunState != domain.RunCompleted || first.Revision != 5 {
		t.Fatalf("first run = %#v, %v", first, err)
	}

	command.Chapters = 5
	command.CreatedAt = command.CreatedAt.Add(time.Hour)
	grown, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("grow goal: %v", err)
	}
	// 修订序列：意图更新 6、扩窗 7、第 4 章 8、第 5 章 9；审阅不产生修订。
	// 调用序列：已写窗口先过阶段审阅（先审后扩）再扩窗、写两章、终审，共 +5。
	if grown.RunState != domain.RunCompleted || grown.Revision != 9 ||
		grown.RunID != "run:grow-book:2" || len(grown.Chapters) != 5 || executor.calls != 10 {
		t.Fatalf("grown = %#v, executor calls = %d", grown, executor.calls)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
	if err != nil || project.Intent.TargetChapters != 5 || len(project.Manuscript) != 5 {
		t.Fatalf("project = %#v, %v", project, err)
	}

	// Run 血统（§6.3）：创建事件 + 阶段审阅、扩窗、两章、终审的 operation_created 事件。
	events, err := api.CreationRunEvents(ctx, "run:grow-book:2")
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	kinds := make([]string, len(events))
	for index, event := range events {
		kinds[index] = event.Kind
	}
	want := []string{
		domain.RunEventCreated, domain.RunEventOperationCreated, domain.RunEventOperationCreated,
		domain.RunEventOperationCreated, domain.RunEventOperationCreated, domain.RunEventOperationCreated,
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("run event kinds = %v, want %v", kinds, want)
	}
}

func TestQuickWriteApprovalTighteningMidRunTakesEffectImmediately(t *testing.T) {
	// S9 的调严审批半边：auto 起书，第 1 章执行期间用户把策略调成 manual；
	// 在途章失效，后继按权威新策略等待裁决（D23 收紧立即生效），同一入口继续。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "policy-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	executor.onExecute = func(operation domain.Operation) {
		if operation.ID != runQuickID("run:policy-book:1", "chapter", "chapter-plan-1") {
			return
		}
		if _, err := api.SetApprovalPolicy(ctx, SetApprovalPolicyCommand{
			ProjectID: "policy-book", ChangeID: "tighten", UserID: "user-1",
			Policy: domain.ApprovalManual, Reason: "写作中途调严审批",
			CreatedAt: command.CreatedAt.Add(30 * time.Minute),
		}); err != nil {
			t.Fatalf("tighten approval mid-run: %v", err)
		}
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across approval tightening: %v", err)
	}
	successor := runQuickID(result.RunID, "chapter", "chapter-plan-1") + ":r2"
	if result.RunState != domain.RunWaitingUser || result.WaitingOperationID != successor || executor.calls != 3 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	for _, proposalID := range []string{
		successor + "-proposal",
		runQuickID(result.RunID, "chapter", "chapter-plan-2") + "-proposal",
	} {
		command.CreatedAt = command.CreatedAt.Add(time.Hour)
		if _, err := api.Approve(ctx, proposalID, command.UserID, command.CreatedAt); err != nil {
			t.Fatalf("approve %s: %v", proposalID, err)
		}
		command.CreatedAt = command.CreatedAt.Add(time.Minute)
		if result, err = api.QuickWrite(ctx, command); err != nil {
			t.Fatalf("resume after approving %s: %v", proposalID, err)
		}
	}
	if result.RunState != domain.RunCompleted || result.Revision != 5 || executor.calls != 5 {
		t.Fatalf("final = %#v, executor calls = %d", result, executor.calls)
	}
}

func TestQuickWriteBlockedReviewTriggersRewriteAndReReview(t *testing.T) {
	// 完成契约（§6.4）：审阅给出阻塞发现 → 按意见重写该章 → Revision 推进使
	// 旧裁定自动失效 → 再审阅通过后才 completed；队列为空绝不等于完成。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: serviceTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 1,
	}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "review-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across blocked review: %v", err)
	}
	// 规划 1 + 两章 2 + 首轮审阅（阻塞）1 + 重写 1 + 再审阅（通过）1 = 6 次。
	if result.RunState != domain.RunCompleted || result.Revision != 5 || executor.calls != 6 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
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
}

func TestQuickWriteDirectiveAddedMidRunReachesSuccessorAndGatesReview(t *testing.T) {
	// S12 + §4.9：写作中途用户提出要求，基线推进使在途章节失效，后继按当前
	// 快照拿到含该要求的任务输入；审阅必须逐条核验，未满足即阻塞并重写。
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	executor := &scriptedQuickExecutor{
		now: serviceTime(), blockChapter: "chapter-chapter-plan-2", unmetDirective: "hook", unmetRemaining: 1,
	}
	executor.authorityStore = authorityStore
	api := NewWithExecutor(authorityStore, executor)
	api.now = serviceTime
	command := QuickWriteCommand{
		ProjectID: "directive-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	executor.onExecute = func(operation domain.Operation) {
		if operation.ID != runQuickID("run:directive-book:1", "chapter", "chapter-plan-1") {
			return
		}
		if _, err := api.AddDirective(ctx, AddDirectiveCommand{
			ProjectID: "directive-book", ChangeID: "mid-run-hook", UserID: "user-1", DirectiveID: "hook",
			Scope: "from_chapter:1", Text: "每章结尾留钩子", Reason: "写作中途提出要求",
			CreatedAt: command.CreatedAt.Add(30 * time.Minute),
		}); err != nil {
			t.Fatalf("mid-run directive: %v", err)
		}
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across mid-run directive: %v", err)
	}
	// 规划 1 + 第 1 章（失效）1 + 后继 1 + 第 2 章 1 + 审阅（要求未满足）1 + 重写 1 + 再审阅 1 = 7 次。
	if result.RunState != domain.RunCompleted || result.Revision != 6 || executor.calls != 7 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	successor := runQuickID(result.RunID, "chapter", "chapter-plan-1") + ":r2"
	operation, err := authorityStore.GetOperation(ctx, successor)
	if err != nil {
		t.Fatalf("read successor: %v", err)
	}
	var input struct {
		Directives []domain.Directive `json:"directives"`
	}
	if err := json.Unmarshal(operation.Input, &input); err != nil {
		t.Fatalf("decode successor input: %v", err)
	}
	if len(input.Directives) != 1 || input.Directives[0].ID != "hook" {
		t.Fatalf("successor input = %s", operation.Input)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
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
	// 先审后扩（§6.3）：写满一个规划窗口后先做阶段审阅，通过才扩展下一段
	// 计划，且审阅意见随扩窗输入进入下一段规划。
	ctx := context.Background()
	note := "主角动机要在下一段更明确"
	executor := &scriptedQuickExecutor{now: serviceTime(), passNote: note}
	api := newQuickTestService(t, executor)
	var extendInputs []string
	executor.onExecute = func(operation domain.Operation) {
		if operation.Kind == domain.OperationRevisePlan {
			extendInputs = append(extendInputs, string(operation.Input))
		}
	}
	command := QuickWriteCommand{
		ProjectID: "window-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 5, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write with rolling window: %v", err)
	}
	// 规划 1 + 首窗三章 3 + 阶段审阅 1 + 扩窗 1 + 两章 2 + 终审 1 = 9 次。
	if result.RunState != domain.RunCompleted || result.Revision != 8 ||
		len(result.Chapters) != 5 || executor.calls != 9 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	if len(extendInputs) != 1 || !strings.Contains(extendInputs[0], note) {
		t.Fatalf("extend inputs = %v, want stage review note %q forwarded", extendInputs, note)
	}
}

func TestQuickWriteWindowReviewBlockedTriggersRewriteBeforeExtending(t *testing.T) {
	// 阶段审阅给出阻塞发现时不得扩窗：先按意见重写、再审阅通过后才继续规划。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: serviceTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 1,
	}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "window-block-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 5, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across blocked window review: %v", err)
	}
	// 规划 1 + 三章 3 + 阶段审阅（阻塞）1 + 重写 1 + 再审阅 1 + 扩窗 1 + 两章 2 + 终审 1 = 11 次。
	if result.RunState != domain.RunCompleted || result.Revision != 9 || executor.calls != 11 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
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

func TestQuickWriteExhaustedRepairBudgetWaitsAndRaisedBudgetResumes(t *testing.T) {
	// 自动修订预算是显式 RunStrategy（§6.3）：预算用尽落 waiting_user 而不是
	// 静默降低审阅标准；用户中途调高预算后，同一入口从落点继续到完成。
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: serviceTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 3,
	}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "budget-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write until budget exhausted: %v", err)
	}
	// 规划 1 + 两章 2 + 审阅/重写交替（阻塞 3 次、重写 2 次消耗预算 2）= 8 次。
	if result.RunState != domain.RunWaitingUser || executor.calls != 8 ||
		!strings.Contains(result.RunReason, "自动修订预算") {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	// 预算用尽的等待不携带稿件：决定卡只展示原因与调预算选项。
	if _, ok, err := api.WaitingProposal(ctx, result.RunID); err != nil || ok {
		t.Fatalf("budget waiting should carry no proposal: ok=%v err=%v", ok, err)
	}
	run, err := api.CreationRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("read waiting run: %v", err)
	}
	strategy := run.Strategy
	strategy.AutoRepairBudget = 3
	updated, err := api.UpdateCreationRunStrategy(ctx, run.ID, strategy, serviceTime().Add(time.Minute))
	if err != nil {
		t.Fatalf("raise repair budget: %v", err)
	}
	if updated.Strategy.AutoRepairBudget != 3 {
		t.Fatalf("updated strategy = %#v", updated.Strategy)
	}
	resume := command
	resume.CreatedAt = serviceTime().Add(2 * time.Minute)
	result, err = api.QuickWrite(ctx, resume)
	if err != nil {
		t.Fatalf("resume after raising budget: %v", err)
	}
	// 续跑：第三次重写 1 + 终审通过 1 = 10 次。
	if result.RunState != domain.RunCompleted || executor.calls != 10 {
		t.Fatalf("resumed result = %#v, executor calls = %d", result, executor.calls)
	}
}

type scriptedQuickExecutor struct {
	now            time.Time
	calls          int
	authorityStore *store.Store
	onExecute      func(operation domain.Operation)
	// blockChapter/blockRemaining：让若干次审阅对指定正文章节给出阻塞发现，
	// 用来验证审阅-重写闭环。
	blockChapter   string
	blockRemaining int
	// passNote：审阅通过时附带的 note 级发现，用来验证意见流进下一段规划。
	passNote string
	// unmetDirective/unmetRemaining：让若干次审阅把指定要求核验为未满足，
	// 阻塞发现落在 blockChapter，用来验证要求核验驱动的重写闭环。
	unmetDirective string
	unmetRemaining int
	// rewrites：按章记录重写轮次，让 canon 前值在多轮重写间保持连续。
	rewrites map[string]int
}

func newQuickTestService(t *testing.T, executor *scriptedQuickExecutor) *Service {
	t.Helper()
	authorityStore := openServiceStore(t)
	executor.authorityStore = authorityStore
	api := NewWithExecutor(authorityStore, executor)
	api.now = serviceTime
	return api
}

func (e *scriptedQuickExecutor) ModelConfigDigest() string { return "model" }

func (e *scriptedQuickExecutor) Execute(
	ctx context.Context,
	operation domain.Operation,
	_ prompt.Compiled,
) (domain.OperationOutcome, error) {
	e.calls++
	if e.onExecute != nil {
		e.onExecute(operation)
	}
	var patches []domain.Patch
	switch operation.Kind {
	case domain.OperationDevelopPlan:
		var input struct {
			RequestedChapters int `json:"requested_chapters"`
		}
		if err := json.Unmarshal(operation.Input, &input); err != nil {
			return domain.OperationOutcome{}, err
		}
		values := []domain.PlanNode{
			{ID: "volume-1", Kind: domain.PlanVolume, Order: 1, Title: "余信", Summary: "完成亡者留下的托付"},
			{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Order: 1, Title: "启程", Summary: "邮差追索身份与收信人"},
		}
		for number := 1; number <= input.RequestedChapters; number++ {
			values = append(values, domain.PlanNode{
				ID: fmt.Sprintf("chapter-plan-%d", number), Kind: domain.PlanChapter,
				ParentID: "arc-1", Order: number, Title: fmt.Sprintf("第%d章", number),
				Summary: fmt.Sprintf("送出第%d封关键书信", number),
			})
		}
		for _, value := range values {
			content, err := json.Marshal(value)
			if err != nil {
				return domain.OperationOutcome{}, err
			}
			patches = append(patches, domain.Patch{
				Document:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: value.ID},
				Operation: domain.PatchPut, Content: content,
			})
		}
	case domain.OperationRevisePlan:
		var input struct {
			ExistingChapters  int `json:"existing_chapters"`
			RequestedChapters int `json:"requested_chapters"`
		}
		if err := json.Unmarshal(operation.Input, &input); err != nil {
			return domain.OperationOutcome{}, err
		}
		for number := input.ExistingChapters + 1; number <= input.RequestedChapters; number++ {
			node := domain.PlanNode{
				ID: fmt.Sprintf("chapter-plan-%d", number), Kind: domain.PlanChapter,
				ParentID: "arc-1", Order: number, Title: fmt.Sprintf("第%d章", number),
				Summary: fmt.Sprintf("送出第%d封关键书信", number),
			}
			content, err := json.Marshal(node)
			if err != nil {
				return domain.OperationOutcome{}, err
			}
			patches = append(patches, domain.Patch{
				Document:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: node.ID},
				Operation: domain.PatchPut, Content: content,
			})
		}
	case domain.OperationWriteChapter:
		var input struct {
			ChapterPlanID string `json:"chapter_plan_id"`
			ChapterNumber int    `json:"chapter_number"`
		}
		if err := json.Unmarshal(operation.Input, &input); err != nil {
			return domain.OperationOutcome{}, err
		}
		chapter := domain.ManuscriptChapter{
			ID: "chapter-" + input.ChapterPlanID, PlanNodeID: input.ChapterPlanID,
			Number: input.ChapterNumber, Title: fmt.Sprintf("第%d章", input.ChapterNumber),
			Blocks: []domain.ManuscriptBlock{{
				ID:   fmt.Sprintf("chapter-%d-block-1", input.ChapterNumber),
				Text: fmt.Sprintf("第%d章正文", input.ChapterNumber),
			}},
		}
		content, err := json.Marshal(chapter)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		fact := domain.CanonFact{
			ID: chapter.ID + "-outcome", Kind: domain.CanonEvent, SubjectID: chapter.ID,
			Predicate: "event.chapter_outcome", Value: json.RawMessage(fmt.Sprintf("%q", chapter.Title)),
			SourceChapterID: chapter.ID,
		}
		canon, err := json.Marshal(fact)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		patches = []domain.Patch{
			{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}, Operation: domain.PatchPut, Content: content},
			{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: fact.ID}, Operation: domain.PatchPut, Content: canon},
		}
	case domain.OperationReviewRange:
		var input struct {
			Range struct {
				ChapterIDs []string `json:"chapter_ids"`
			} `json:"range"`
			VerifyIntent bool               `json:"verify_intent"`
			Directives   []domain.Directive `json:"directives"`
		}
		if err := json.Unmarshal(operation.Input, &input); err != nil {
			return domain.OperationOutcome{}, err
		}
		verdict := domain.ReviewVerdict{
			Status: domain.ReviewPass, Revision: operation.Snapshot.BaseRevision,
			ChapterIDs: input.Range.ChapterIDs, ReviewKey: "scripted-review",
			Findings: []domain.ReviewFinding{},
		}
		if input.VerifyIntent {
			verdict.Intent = &domain.IntentVerification{RequiredPresent: true, ForbiddenAbsent: true, EndingConsistent: true}
		}
		for _, directive := range input.Directives {
			check := domain.DirectiveVerification{DirectiveID: directive.ID, Satisfied: true}
			if directive.ID == e.unmetDirective && e.unmetRemaining > 0 {
				e.unmetRemaining--
				check.Satisfied, check.Note = false, "结尾没有留下钩子"
				verdict.Status = domain.ReviewBlocked
				verdict.Findings = append(verdict.Findings, domain.ReviewFinding{
					ChapterID: e.blockChapter, Severity: domain.FindingBlocking, Note: "按要求补上结尾钩子：" + directive.Text,
				})
			}
			verdict.Directives = append(verdict.Directives, check)
		}
		if e.blockRemaining > 0 && slices.Contains(input.Range.ChapterIDs, e.blockChapter) {
			e.blockRemaining--
			verdict.Status = domain.ReviewBlocked
			verdict.Findings = append(verdict.Findings, domain.ReviewFinding{
				ChapterID: e.blockChapter, Severity: domain.FindingBlocking, Note: "结尾太仓促，补全告别场景",
			})
		}
		if e.passNote != "" && verdict.Status == domain.ReviewPass {
			verdict.Findings = append(verdict.Findings, domain.ReviewFinding{
				ChapterID: input.Range.ChapterIDs[0], Severity: domain.FindingNote, Note: e.passNote,
			})
		}
		findings, err := json.Marshal(verdict.Findings)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		if _, err := e.authorityStore.PutWorkspaceArtifact(ctx, domain.WorkspaceArtifact{
			OperationID: operation.ID, Key: verdict.ReviewKey,
			MediaType: domain.ReviewArtifactMediaType, Content: findings,
			UpdatedAt: e.now.Add(time.Duration(e.calls) * time.Second),
		}, 0, operation.Attempt); err != nil {
			return domain.OperationOutcome{}, err
		}
		payload, err := json.Marshal(verdict)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		return domain.OperationOutcome{Verdict: payload}, nil
	case domain.OperationRewriteChapter:
		var input struct {
			ChapterID string   `json:"chapter_id"`
			Findings  []string `json:"findings"`
		}
		if err := json.Unmarshal(operation.Input, &input); err != nil {
			return domain.OperationOutcome{}, err
		}
		planID := strings.TrimPrefix(input.ChapterID, "chapter-")
		var number int
		if _, err := fmt.Sscanf(planID, "chapter-plan-%d", &number); err != nil {
			return domain.OperationOutcome{}, fmt.Errorf("unexpected rewrite target %q: %w", input.ChapterID, err)
		}
		chapter := domain.ManuscriptChapter{
			ID: input.ChapterID, PlanNodeID: planID,
			Number: number, Title: fmt.Sprintf("第%d章", number),
			Blocks: []domain.ManuscriptBlock{{
				ID:   fmt.Sprintf("chapter-%d-block-1", number),
				Text: fmt.Sprintf("第%d章正文（按审阅意见重写：%s）", number, strings.Join(input.Findings, "；")),
			}},
		}
		content, err := json.Marshal(chapter)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		if e.rewrites == nil {
			e.rewrites = map[string]int{}
		}
		round := e.rewrites[chapter.ID]
		e.rewrites[chapter.ID] = round + 1
		fact := domain.CanonFact{
			ID: chapter.ID + "-outcome", Kind: domain.CanonEvent, SubjectID: chapter.ID,
			Predicate:       "event.chapter_outcome",
			PreviousValue:   json.RawMessage(fmt.Sprintf("%q", chapter.Title+strings.Repeat("（重写）", round))),
			Value:           json.RawMessage(fmt.Sprintf("%q", chapter.Title+strings.Repeat("（重写）", round+1))),
			SourceChapterID: chapter.ID,
		}
		canon, err := json.Marshal(fact)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		patches = []domain.Patch{
			{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}, Operation: domain.PatchPut, Content: content},
			{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: fact.ID}, Operation: domain.PatchPut, Content: canon},
		}
	default:
		return domain.OperationOutcome{}, fmt.Errorf("unexpected operation kind %s", operation.Kind)
	}
	return domain.OperationOutcome{Proposal: &domain.Proposal{
		ID: operation.ID + "-proposal", OperationID: operation.ID, Target: operation.Target,
		BaseRevision: operation.Snapshot.BaseRevision,
		Author:       domain.Author{Kind: domain.AuthorAI, ID: operation.Snapshot.WorkerProfileVersion},
		Reason:       "quick write candidate", Patches: patches,
		ApprovalState: domain.ApprovalPending, CreatedAt: e.now.Add(time.Duration(e.calls) * time.Second),
	}}, nil
}
