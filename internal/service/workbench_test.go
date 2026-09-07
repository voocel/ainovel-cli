package service

import (
	"context"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// TestWorkbenchSnapshotMarksStaleCandidateAfterRevisionDrift 守卫"过期候选不得
// 伪装成当前稿件"（页面设计 §4）：等待裁决期间用户变更推进了 Revision，
// 决定卡标记 Stale、不再解出章节候选，大纲不得出现待确认态。
func TestWorkbenchSnapshotMarksStaleCandidateAfterRevisionDrift(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "drift-book", UserID: "user-1", Premise: "漂移测试",
		Chapters: 1, Approval: domain.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write to plan wait: %v", err)
	}
	if _, err := api.Approve(ctx, runQuickID(result.RunID, "plan")+"-proposal", "user-1", serviceTime().Add(time.Hour)); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	command.CreatedAt = serviceTime().Add(time.Hour + time.Minute)
	if result, err = api.QuickWrite(ctx, command); err != nil || result.RunState != domain.RunWaitingUser {
		t.Fatalf("resume to chapter wait: %#v, %v", result, err)
	}
	// 等待期间用户锁定设定：Revision 前进，候选基线随之过期。
	if _, err := api.SetOwnership(ctx, SetOwnershipCommand{
		ProjectID: "drift-book", ChangeID: "drift-lock", UserID: "user-1",
		Target:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-1"},
		Control: domain.ControlGuided, Guidance: []string{"保持启程弧基调"},
		Reason: "写作中途锁定", CreatedAt: serviceTime().Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("drift via ownership change: %v", err)
	}
	snapshot, err := api.WorkbenchSnapshot(ctx, "drift-book")
	if err != nil {
		t.Fatalf("snapshot after drift: %v", err)
	}
	if snapshot.Decision == nil || !snapshot.Decision.HasProposal || !snapshot.Decision.Stale {
		t.Fatalf("decision after drift = %#v", snapshot.Decision)
	}
	if len(snapshot.Candidates) != 0 {
		t.Fatalf("stale candidates leaked: %#v", snapshot.Candidates)
	}
	for _, entry := range snapshot.Outline {
		if entry.State == ChapterPending {
			t.Fatalf("stale candidate must not mark outline pending: %#v", entry)
		}
	}
}

// TestWorkbenchSnapshotAcrossLifecycle 覆盖文档 §4 数据契约的三个关键状态：
// 未开始、章节候选待确认（含血统）、全书完成（大纲状态与权威一致）。
func TestWorkbenchSnapshotAcrossLifecycle(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)

	// 未开始：只有 Project、没有 Run。
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "fresh-book", ChangeID: "create-fresh", UserID: "user-1",
		Reason: "工作台快照测试", Draft: testProjectDraft(), CreatedAt: serviceTime(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	snapshot, err := api.WorkbenchSnapshot(ctx, "fresh-book")
	if err != nil {
		t.Fatalf("snapshot before run: %v", err)
	}
	if snapshot.Run != nil || snapshot.Decision != nil || snapshot.NextStep == "" {
		t.Fatalf("fresh snapshot = %#v", snapshot)
	}

	// manual 跑到蓝图等待 → 批准 → 续跑到第一章候选待确认。
	command := QuickWriteCommand{
		ProjectID: "bench-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, Approval: domain.ApprovalManual,
		WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	result, err := api.QuickWrite(ctx, command)
	if err != nil {
		t.Fatalf("quick write: %v", err)
	}
	if _, err := api.Approve(ctx, runQuickID(result.RunID, "plan")+"-proposal", "user-1", serviceTime().Add(time.Hour)); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	command.CreatedAt = serviceTime().Add(time.Hour + time.Minute)
	if result, err = api.QuickWrite(ctx, command); err != nil {
		t.Fatalf("resume to chapter: %v", err)
	}
	if result.RunState != domain.RunWaitingUser {
		t.Fatalf("run state = %v, want waiting_user", result.RunState)
	}

	snapshot, err = api.WorkbenchSnapshot(ctx, "bench-book")
	if err != nil {
		t.Fatalf("snapshot while pending: %v", err)
	}
	if snapshot.Decision == nil || !snapshot.Decision.HasProposal {
		t.Fatalf("pending decision = %#v", snapshot.Decision)
	}
	if len(snapshot.Candidates) != 1 || snapshot.Candidates[0].OperationID == "" ||
		snapshot.Candidates[0].BaseRevision != snapshot.Revision {
		t.Fatalf("candidates = %#v (revision %d)", snapshot.Candidates, snapshot.Revision)
	}
	pendingSeen, chapterNodes := false, 0
	for _, entry := range snapshot.Outline {
		if entry.Node.Kind != domain.PlanChapter {
			continue
		}
		chapterNodes++
		if entry.State == ChapterPending {
			pendingSeen = true
		}
	}
	if !pendingSeen || chapterNodes != 2 {
		t.Fatalf("outline = %#v", snapshot.Outline)
	}

	// 批准并跑完全书：大纲全部已确认，下一步引导指向提高目标。
	for _, suffix := range [][]string{{"chapter", "chapter-plan-1"}, {"chapter", "chapter-plan-2"}} {
		command.CreatedAt = command.CreatedAt.Add(time.Hour)
		if _, err := api.Approve(ctx, runQuickID(result.RunID, suffix...)+"-proposal", "user-1", command.CreatedAt); err != nil {
			t.Fatalf("approve %v: %v", suffix, err)
		}
		command.CreatedAt = command.CreatedAt.Add(time.Minute)
		if result, err = api.QuickWrite(ctx, command); err != nil {
			t.Fatalf("resume %v: %v", suffix, err)
		}
	}
	if result.RunState != domain.RunCompleted {
		t.Fatalf("final run state = %v", result.RunState)
	}
	snapshot, err = api.WorkbenchSnapshot(ctx, "bench-book")
	if err != nil {
		t.Fatalf("snapshot after completion: %v", err)
	}
	confirmedCount := 0
	for _, entry := range snapshot.Outline {
		if entry.State == ChapterConfirmed {
			confirmedCount++
		}
	}
	if confirmedCount != 2 || snapshot.Decision != nil || snapshot.CurrentPhase != "" {
		t.Fatalf("completed snapshot: confirmed=%d decision=%v phase=%q", confirmedCount, snapshot.Decision, snapshot.CurrentPhase)
	}
	if snapshot.Run == nil || snapshot.Run.State != domain.RunCompleted || snapshot.NextStep == "" {
		t.Fatalf("completed run projection = %#v next=%q", snapshot.Run, snapshot.NextStep)
	}
}
