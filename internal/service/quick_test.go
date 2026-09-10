package service

import (
	"context"
	"encoding/json"
	"errors"
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
	goal := domain.NovelGoal{Premise: "一章故事", TargetChapters: 1}
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
	// S9 + D51：全自动写作中途用户锁定设定，在途章节的提案重定位到新基线、
	// 在新边界下裁决；整轮创作不中止、不重跑模型、不换命令入口。
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
	// 收紧立即生效：在途第 1 章的提案重定位到锁定后的 Revision；因缺少语义合规
	// 证据，auto 保守升级为安全暂停等待裁决，而不是整轮失败或作废重跑。
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write across mid-run tightening: %v", err)
	}
	chapterOne := runQuickID(result.RunID, "chapter", "chapter-plan-1")
	if result.RunState != domain.RunWaitingUser || executor.calls != 2 ||
		result.WaitingOperationID != chapterOne {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}

	// 用户裁决后同一入口继续：第 2 章同样在锁定约束下暂停，逐稿放行直至完成。
	for _, proposalID := range []string{
		chapterOne + "-proposal",
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
	if result.RunState != domain.RunCompleted || result.Revision != 5 || executor.calls != 4 ||
		result.Chapters[0].OperationID != chapterOne {
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
	// 在途章的提案重定位后按权威新策略等待裁决（D23 收紧立即生效），同一入口继续。
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
	chapterOne := runQuickID(result.RunID, "chapter", "chapter-plan-1")
	if result.RunState != domain.RunWaitingUser || result.WaitingOperationID != chapterOne || executor.calls != 2 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	for _, proposalID := range []string{
		chapterOne + "-proposal",
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
	if result.RunState != domain.RunCompleted || result.Revision != 5 || executor.calls != 4 {
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

func (e *scriptedQuickExecutor) Identity() string { return prompt.ExecutorIdentity("model") }

func (e *scriptedQuickExecutor) ModelConfigDigest() string { return "model" }

func (e *scriptedQuickExecutor) Execute(ctx context.Context, operation domain.Operation) (domain.OperationOutcome, error) {
	e.calls++
	if e.onExecute != nil {
		e.onExecute(operation)
	}
	var patches []domain.Patch
	switch operation.Kind {
	case domain.OperationDevelopPlan:
		input, err := domain.TaskInputAs[domain.DevelopPlanInput](operation)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		// 规划提案同时登记主角实体：随章 Canon Delta 的主体必须是已登记实体（D35）。
		hero, err := json.Marshal(domain.Entity{ID: "hero", Kind: domain.EntityCharacter, Name: "邮差"})
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		patches = append(patches, domain.Patch{
			Document: domain.DocumentRef{Kind: domain.DocumentEntity, ID: "hero"}, Operation: domain.PatchPut, Content: hero,
		})
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
		input, err := domain.TaskInputAs[domain.RevisePlanInput](operation)
		if err != nil {
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
		input, err := domain.TaskInputAs[domain.WriteChapterInput](operation)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		chapter := domain.ManuscriptChapter{
			ID: "chapter-" + input.ChapterPlanID, PlanNodeID: input.ChapterPlanID,
			Number: input.ChapterNumber, Title: fmt.Sprintf("第%d章", input.ChapterNumber), Author: domain.AuthorAI,
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
			ID: chapter.ID + "-outcome", Kind: domain.CanonEvent, SubjectID: "hero",
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
		input, err := domain.TaskInputAs[domain.ReviewRangeInput](operation)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		verdict := domain.ReviewVerdict{
			Status: domain.ReviewPass, Revision: operation.Snapshot.BaseRevision,
			ChapterIDs: input.ChapterIDs, ReviewKey: "scripted-review", Basis: input.Basis,
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
					DirectiveID: directive.ID,
				})
			}
			verdict.Directives = append(verdict.Directives, check)
		}
		if e.blockRemaining > 0 && slices.Contains(input.ChapterIDs, e.blockChapter) {
			e.blockRemaining--
			verdict.Status = domain.ReviewBlocked
			verdict.Findings = append(verdict.Findings, domain.ReviewFinding{
				ChapterID: e.blockChapter, Severity: domain.FindingBlocking, Note: "结尾太仓促，补全告别场景",
			})
		}
		if e.passNote != "" && verdict.Status == domain.ReviewPass {
			verdict.Findings = append(verdict.Findings, domain.ReviewFinding{
				ChapterID: input.ChapterIDs[0], Severity: domain.FindingNote, Note: e.passNote,
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
		input, err := domain.TaskInputAs[domain.RewriteChapterInput](operation)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		chapter := domain.ManuscriptChapter{
			ID: input.ChapterID, PlanNodeID: input.ChapterPlanID,
			Number: input.ChapterNumber, Title: fmt.Sprintf("第%d章", input.ChapterNumber), Author: domain.AuthorAI,
			Blocks: []domain.ManuscriptBlock{{
				ID:   fmt.Sprintf("chapter-%d-block-1", input.ChapterNumber),
				Text: fmt.Sprintf("第%d章正文（按审阅意见重写：%s）", input.ChapterNumber, strings.Join(input.Findings, "；")),
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
			ID: chapter.ID + "-outcome", Kind: domain.CanonEvent, SubjectID: "hero",
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
	case domain.OperationReviseCanon:
		input, err := domain.TaskInputAs[domain.ReviseCanonInput](operation)
		if err != nil {
			return domain.OperationOutcome{}, err
		}
		// 待核验事实原样确认；未入账的章补记一条。
		for _, id := range input.FactIDs {
			stored, err := e.authorityStore.GetDocument(ctx, operation.Target, domain.DocumentRef{Kind: domain.DocumentCanon, ID: id}, operation.Snapshot.BaseRevision)
			if err != nil {
				return domain.OperationOutcome{}, err
			}
			var fact domain.CanonFact
			if err := json.Unmarshal(stored.Content, &fact); err != nil {
				return domain.OperationOutcome{}, err
			}
			fact.PreviousValue = fact.Value
			content, err := json.Marshal(fact)
			if err != nil {
				return domain.OperationOutcome{}, err
			}
			patches = append(patches, domain.Patch{Document: stored.Document, Operation: domain.PatchPut, Content: content})
		}
		if len(input.FactIDs) == 0 {
			content, err := json.Marshal(domain.CanonFact{
				ID: input.ChapterID + "-outcome", Kind: domain.CanonEvent, SubjectID: "hero",
				Predicate: "event.chapter_outcome", Value: json.RawMessage(`"补账"`), SourceChapterID: input.ChapterID,
			})
			if err != nil {
				return domain.OperationOutcome{}, err
			}
			patches = append(patches, domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: input.ChapterID + "-outcome"}, Operation: domain.PatchPut, Content: content})
		}
	default:
		return domain.OperationOutcome{}, fmt.Errorf("unexpected operation kind %s", operation.Kind)
	}
	return domain.OperationOutcome{Proposal: &domain.Proposal{
		ID: operation.ID + "-proposal", OperationID: operation.ID, Target: operation.Target,
		BaseRevision: operation.Snapshot.BaseRevision,
		Author:       domain.Author{Kind: domain.AuthorAI, ID: "scripted.writer@1"},
		Reason:       "quick write candidate", Patches: patches,
		ApprovalState: domain.ApprovalPending, CreatedAt: e.now.Add(time.Duration(e.calls) * time.Second),
	}}, nil
}

func TestQuickWriteUnrelatedDirectiveKeepsWindowVerdictValid(t *testing.T) {
	// D48：审阅证据按基线失效，不按整本 Revision。扩窗时用户对第 5 章提要求，
	// 1–3 章的阶段审阅证据仍然有效、不重审；对第 2 章提要求则必须重审。
	ctx := context.Background()
	run := func(projectID, scope string) (QuickWriteResult, *scriptedQuickExecutor) {
		t.Helper()
		executor := &scriptedQuickExecutor{now: serviceTime()}
		api := newQuickTestService(t, executor)
		command := QuickWriteCommand{
			ProjectID: projectID, UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
			Chapters: 5, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
		}
		executor.onExecute = func(operation domain.Operation) {
			if operation.ID != runQuickID("run:"+projectID+":1", "plan", "extend", "3") {
				return
			}
			if _, err := api.AddDirective(ctx, AddDirectiveCommand{
				ProjectID: projectID, ChangeID: "mid-run-directive", UserID: "user-1", DirectiveID: "late",
				Scope: scope, Text: "结尾要有告别场景", Reason: "扩窗时提出要求",
				CreatedAt: command.CreatedAt.Add(30 * time.Minute),
			}); err != nil {
				t.Fatalf("mid-run directive: %v", err)
			}
		}
		result, err := api.QuickWrite(ctx, command)
		if err != nil {
			t.Fatalf("quick write %s: %v", projectID, err)
		}
		return result, executor
	}
	reviewed := func(executor *scriptedQuickExecutor, runID string, revision domain.Revision) bool {
		t.Helper()
		_, err := executor.authorityStore.GetOperation(ctx, reviewOperationID(runID, revision))
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("read review operation: %v", err)
		}
		return err == nil
	}
	// 规划 1 + 三章 3 + 阶段审阅 1 + 扩窗（提案重定位，D51）1 + 两章 2 + 终审 1 = 9 次；
	// 要求提交后（Revision 6）没有新的阶段审阅。
	unrelated, executor := run("late-directive-book", "from_chapter:5")
	if unrelated.RunState != domain.RunCompleted || unrelated.Revision != 9 || executor.calls != 9 ||
		reviewed(executor, unrelated.RunID, 6) {
		t.Fatalf("unrelated directive result = %#v, executor calls = %d", unrelated, executor.calls)
	}
	// 覆盖第 2 章的要求改变了窗口裁定的作用域基线：扩窗失效、重审一次再扩，共 11 次。
	related, executor := run("early-directive-book", "chapter_range:2-2")
	if related.RunState != domain.RunCompleted || related.Revision != 9 || executor.calls != 11 ||
		!reviewed(executor, related.RunID, 6) {
		t.Fatalf("related directive result = %#v, executor calls = %d", related, executor.calls)
	}
}

// TestQuickWriteAcceptedFindingUnblocksAndWithdrawRestoresBlock 是 D43 的闭环：预算用尽
// 等待用户 → 用户接受阻塞发现 → 续跑不重写即完成；撤回后阻塞恢复，新一轮按意见重写。
func TestQuickWriteAcceptedFindingUnblocksAndWithdrawRestoresBlock(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: serviceTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 3,
	}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "accept-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil || result.RunState != domain.RunWaitingUser || executor.calls != 8 {
		t.Fatalf("result = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
	snapshot, err := api.WorkbenchSnapshot(ctx, command.ProjectID)
	if err != nil || len(snapshot.Findings) != 1 || snapshot.Findings[0].Severity != domain.FindingBlocking {
		t.Fatalf("findings before adjudication = %#v, %v", snapshot.Findings, err)
	}
	accept := AddAdjudicationCommand{
		ProjectID: command.ProjectID, ChangeID: "accept-1", UserID: "user-1",
		Finding: snapshot.Findings[0].ID, Reason: "仓促的告别也是一种风格", CreatedAt: serviceTime().Add(time.Minute),
	}
	accepted, err := api.AddAdjudication(ctx, accept)
	if err != nil || accepted.Adjudication.Finding != accept.Finding || accepted.Adjudication.Basis.Equal(domain.EvidenceBasis{}) {
		t.Fatalf("accepted = %#v, %v", accepted, err)
	}
	accept.CreatedAt = serviceTime().Add(2 * time.Minute)
	if again, err := api.AddAdjudication(ctx, accept); err != nil || again.Revision != accepted.Revision {
		t.Fatalf("retry must be idempotent: %#v, %v", again, err)
	}
	if snapshot, err = api.WorkbenchSnapshot(ctx, command.ProjectID); err != nil ||
		len(snapshot.Findings) != 0 || len(snapshot.Adjudications) != 1 {
		t.Fatalf("snapshot after adjudication = findings %#v, adjudications %#v, %v", snapshot.Findings, snapshot.Adjudications, err)
	}
	resume := command
	resume.CreatedAt = serviceTime().Add(3 * time.Minute)
	if result, err = api.QuickWrite(ctx, resume); err != nil || result.RunState != domain.RunCompleted || executor.calls != 8 {
		t.Fatalf("resume after adjudication = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
	if _, err := api.WithdrawAdjudication(ctx, WithdrawAdjudicationCommand{
		ProjectID: command.ProjectID, ChangeID: "withdraw-1", UserID: "user-1",
		AdjudicationID: "accept-1", Reason: "还是改一下", CreatedAt: serviceTime().Add(4 * time.Minute),
	}); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, err := api.WithdrawAdjudication(ctx, WithdrawAdjudicationCommand{
		ProjectID: command.ProjectID, ChangeID: "withdraw-2", UserID: "user-1",
		AdjudicationID: "accept-1", Reason: "再撤一次", CreatedAt: serviceTime().Add(5 * time.Minute),
	}); !errors.Is(err, store.ErrStateConflict) {
		t.Fatalf("second withdraw err = %v", err)
	}
	if snapshot, err = api.WorkbenchSnapshot(ctx, command.ProjectID); err != nil ||
		len(snapshot.Findings) != 1 || len(snapshot.Adjudications) != 0 {
		t.Fatalf("snapshot after withdrawal = findings %#v, adjudications %#v, %v", snapshot.Findings, snapshot.Adjudications, err)
	}
	// 新一轮续写：阻塞恢复，按意见重写 1 + 终审通过 1。
	resume.CreatedAt = serviceTime().Add(6 * time.Minute)
	if result, err = api.QuickWrite(ctx, resume); err != nil || result.RunState != domain.RunCompleted ||
		result.RunID != "run:accept-book:2" || executor.calls != 10 {
		t.Fatalf("rerun after withdrawal = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
}

// TestAdjudicationExpiresWhenChapterChanges：裁决绑定被接受的基线（D43/D48），
// 用户改了该章正文，裁决与裁定一起失效。
func TestAdjudicationExpiresWhenChapterChanges(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{
		now: serviceTime(), blockChapter: "chapter-chapter-plan-2", blockRemaining: 3,
	}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "expire-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	if result, err := api.QuickWrite(ctx, command); err != nil || result.RunState != domain.RunWaitingUser {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	snapshot, err := api.WorkbenchSnapshot(ctx, command.ProjectID)
	if err != nil || len(snapshot.Findings) != 1 {
		t.Fatalf("findings = %#v, %v", snapshot.Findings, err)
	}
	if _, err := api.AddAdjudication(ctx, AddAdjudicationCommand{
		ProjectID: command.ProjectID, ChangeID: "accept-1", UserID: "user-1",
		Finding: snapshot.Findings[0].ID, Reason: "接受", CreatedAt: serviceTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	projection, err := api.ExportProject(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for index := range projection.Manuscript {
		if projection.Manuscript[index].ID == "chapter-chapter-plan-2" {
			projection.Manuscript[index].Blocks[0].Text += "（用户手工润色）"
		}
	}
	proposal, err := api.ImportProject(ctx, "edit-chapter-2", "user-1", "手工润色", projection, serviceTime().Add(2*time.Minute))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := api.Approve(ctx, proposal.ID, "user-1", serviceTime().Add(3*time.Minute)); err != nil {
		t.Fatalf("approve import: %v", err)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if len(project.Adjudications) != 1 {
		t.Fatalf("adjudication records = %#v", project.Adjudications)
	}
	valid, err := api.validAdjudications(ctx, project)
	if err != nil || len(valid) != 0 {
		t.Fatalf("valid adjudications after chapter edit = %#v, %v", valid, err)
	}
	if snapshot, err = api.WorkbenchSnapshot(ctx, command.ProjectID); err != nil || len(snapshot.Adjudications) != 0 {
		t.Fatalf("workbench adjudications after edit = %#v, %v", snapshot.Adjudications, err)
	}
}

// TestQuickWriteDisjointDirectiveMidRunRelocatesInFlightChapter 是 S12 的重定位半边（D51）：
// 写第 2 章时用户给第 5 章提要求，第 2 章的提案重定位后照常提交，没有后继、不重跑模型；
// 要求按当前快照进入第 5 章的任务输入。
func TestQuickWriteDisjointDirectiveMidRunRelocatesInFlightChapter(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "disjoint-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 5, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	executor.onExecute = func(operation domain.Operation) {
		if operation.ID != runQuickID("run:disjoint-book:1", "chapter", "chapter-plan-2") {
			return
		}
		if _, err := api.AddDirective(ctx, AddDirectiveCommand{
			ProjectID: "disjoint-book", ChangeID: "late-farewell", UserID: "user-1", DirectiveID: "farewell",
			Scope: "from_chapter:5", Text: "结尾要有告别场景", Reason: "写作中途提出要求",
			CreatedAt: command.CreatedAt.Add(30 * time.Minute),
		}); err != nil {
			t.Fatalf("mid-run directive: %v", err)
		}
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	// 规划 1 + 三章 3 + 阶段审阅 1 + 扩窗 1 + 两章 2 + 终审 1 = 9 次，第 2 章没有后继。
	if result.RunState != domain.RunCompleted || result.Revision != 9 || executor.calls != 9 {
		t.Fatalf("result = %#v, executor calls = %d", result, executor.calls)
	}
	chapterTwo := runQuickID(result.RunID, "chapter", "chapter-plan-2")
	if _, err := executor.authorityStore.GetOperation(ctx, chapterTwo+":r2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("chapter 2 must not need a successor, got %v", err)
	}
	proposal, err := api.ProposalForOperation(ctx, chapterTwo)
	if err != nil || proposal.BaseRevision != 4 || proposal.ApprovalState != domain.ApprovalApproved {
		t.Fatalf("relocated chapter 2 proposal = %#v, %v", proposal, err)
	}
	operation, err := executor.authorityStore.GetOperation(ctx, runQuickID(result.RunID, "chapter", "chapter-plan-5"))
	if err != nil {
		t.Fatalf("read chapter 5 operation: %v", err)
	}
	input, err := domain.TaskInputAs[domain.WriteChapterInput](operation)
	if err != nil || len(input.Directives) != 1 || input.Directives[0].ID != "farewell" {
		t.Fatalf("chapter 5 input = %#v, %v", input, err)
	}
}

// TestQuickWriteDriftDuringReviewFollowsBasis：审阅期间的漂移按裁定基线判定（D48/D51）——
// 锁定设定不作废裁定，覆盖范围内章节的要求才使审阅失效并重审。
func TestQuickWriteDriftDuringReviewFollowsBasis(t *testing.T) {
	ctx := context.Background()
	run := func(projectID string, drift func(api *Service, at time.Time)) (QuickWriteResult, *scriptedQuickExecutor) {
		t.Helper()
		executor := &scriptedQuickExecutor{now: serviceTime()}
		api := newQuickTestService(t, executor)
		command := QuickWriteCommand{
			ProjectID: projectID, UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
			Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
		}
		executor.onExecute = func(operation domain.Operation) {
			if operation.ID == reviewOperationID("run:"+projectID+":1", 4) {
				drift(api, command.CreatedAt.Add(30*time.Minute))
			}
		}
		result, err := api.QuickWrite(ctx, command)
		if err != nil {
			t.Fatalf("quick write %s: %v", projectID, err)
		}
		return result, executor
	}
	// 规划 1 + 两章 2 + 终审 1 = 4 次：审阅期间锁定设定，裁定照常生效。
	locked, executor := run("locked-review-book", func(api *Service, at time.Time) {
		if _, err := api.SetOwnership(ctx, SetOwnershipCommand{
			ProjectID: "locked-review-book", ChangeID: "review-lock", UserID: "user-1",
			Target: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-1"}, Control: domain.ControlLocked,
			Reason: "审阅中途锁定", CreatedAt: at,
		}); err != nil {
			t.Fatalf("lock during review: %v", err)
		}
	})
	if locked.RunState != domain.RunCompleted || executor.calls != 4 {
		t.Fatalf("locked result = %#v, executor calls = %d", locked, executor.calls)
	}
	// 覆盖第 1 章起的要求改变了裁定的作用域基线：审阅失效、重审一次，共 5 次。
	related, executor := run("related-review-book", func(api *Service, at time.Time) {
		if _, err := api.AddDirective(ctx, AddDirectiveCommand{
			ProjectID: "related-review-book", ChangeID: "review-hook", UserID: "user-1", DirectiveID: "hook",
			Scope: "from_chapter:1", Text: "每章结尾留钩子", Reason: "审阅中途提出要求", CreatedAt: at,
		}); err != nil {
			t.Fatalf("directive during review: %v", err)
		}
	})
	if related.RunState != domain.RunCompleted || executor.calls != 5 {
		t.Fatalf("related result = %#v, executor calls = %d", related, executor.calls)
	}
	stale, err := executor.authorityStore.GetOperation(ctx, reviewOperationID(related.RunID, 4))
	if err != nil || stale.State != domain.OperationStale {
		t.Fatalf("first review = %#v, %v; want stale", stale, err)
	}
}

// TestQuickWriteUserEditedChapterIsVerifiedBeforeNextWrite 是 D41 第 3 条的闭环：用户改了
// 第 1 章正文后再续写，先核验第 1 章的来源事实，再重审、扩窗、写第 4 章；核验后缺口消失。
func TestQuickWriteUserEditedChapterIsVerifiedBeforeNextWrite(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "edited-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil || result.RunState != domain.RunCompleted || result.Revision != 5 || executor.calls != 5 {
		t.Fatalf("first run = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
	projection, err := api.ExportProject(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for index := range projection.Manuscript {
		if projection.Manuscript[index].ID == "chapter-chapter-plan-1" {
			projection.Manuscript[index].Blocks[0].Text += "（用户改写了开头）"
		}
	}
	proposal, err := api.ImportProject(ctx, "edit-chapter-1", "user-1", "手工润色", projection, serviceTime().Add(time.Hour))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := api.Approve(ctx, proposal.ID, "user-1", serviceTime().Add(time.Hour+time.Minute)); err != nil {
		t.Fatalf("approve import: %v", err)
	}
	snapshot, err := api.WorkbenchSnapshot(ctx, command.ProjectID)
	if err != nil || len(snapshot.PendingCanon) != 1 || snapshot.PendingCanon[0] != "chapter-chapter-plan-1-outcome" {
		t.Fatalf("pending canon after edit = %#v, %v", snapshot.PendingCanon, err)
	}
	// 续写到 4 章：核验 1 + 重审（旧终审因第 1 章变化失效）1 + 扩窗 1 + 第 4 章 1 + 终审 1 = 10 次。
	command.Chapters, command.CreatedAt = 4, serviceTime().Add(2*time.Hour)
	if result, err = api.QuickWrite(ctx, command); err != nil || result.RunState != domain.RunCompleted || executor.calls != 10 {
		t.Fatalf("second run = %#v, calls = %d, err = %v", result, executor.calls, err)
	}
	verify, err := executor.authorityStore.GetOperation(ctx, runQuickID(result.RunID, "canon", "chapter-chapter-plan-1", "r6"))
	if err != nil || verify.State != domain.OperationSucceeded {
		t.Fatalf("canon verification = %#v, %v", verify, err)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if gaps := canonGaps(project); len(gaps) != 0 {
		t.Fatalf("gaps after verification = %#v", gaps)
	}
}
