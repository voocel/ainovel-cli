package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// TestWorkbenchStaleCandidateFollowsRelocationRule 守卫"过期候选不得伪装成当前稿件"
// （页面设计 §4）与 D51：等待裁决期间的相干要求使候选过期（标 Stale、不解出候选、
// 大纲不得待确认）；只锁定设定则候选重定位到当前 Revision，照常裁决通过。
func TestWorkbenchStaleCandidateFollowsRelocationRule(t *testing.T) {
	ctx := context.Background()
	prepare := func(projectID string) (*Service, string) {
		t.Helper()
		executor := &scriptedQuickExecutor{now: serviceTime()}
		api := newQuickTestService(t, executor)
		command := QuickWriteCommand{
			ProjectID: projectID, UserID: "user-1", Premise: "漂移测试",
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
		return api, runQuickID(result.RunID, "chapter", "chapter-plan-1") + "-proposal"
	}

	t.Run("related directive expires the candidate", func(t *testing.T) {
		api, proposalID := prepare("drift-book")
		if _, err := api.AddDirective(ctx, AddDirectiveCommand{
			ProjectID: "drift-book", ChangeID: "drift-hook", UserID: "user-1", DirectiveID: "hook",
			Scope: "chapter_range:1-1", Text: "结尾留钩子", Reason: "等待裁决时提出要求",
			CreatedAt: serviceTime().Add(2 * time.Hour),
		}); err != nil {
			t.Fatalf("drift via directive: %v", err)
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
		if _, err := api.Approve(ctx, proposalID, "user-1", serviceTime().Add(3*time.Hour)); !errors.Is(err, store.ErrRevisionConflict) {
			t.Fatalf("approving a stale candidate err = %v, want ErrRevisionConflict", err)
		}
	})

	t.Run("ownership change relocates the candidate", func(t *testing.T) {
		api, proposalID := prepare("relocate-book")
		if _, err := api.SetOwnership(ctx, SetOwnershipCommand{
			ProjectID: "relocate-book", ChangeID: "drift-lock", UserID: "user-1",
			Target:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-1"},
			Control: domain.ControlGuided, Guidance: []string{"保持启程弧基调"},
			Reason: "写作中途锁定", CreatedAt: serviceTime().Add(2 * time.Hour),
		}); err != nil {
			t.Fatalf("drift via ownership change: %v", err)
		}
		snapshot, err := api.WorkbenchSnapshot(ctx, "relocate-book")
		if err != nil {
			t.Fatalf("snapshot after drift: %v", err)
		}
		if snapshot.Decision == nil || snapshot.Decision.Stale || len(snapshot.Candidates) != 1 ||
			snapshot.Candidates[0].BaseRevision != snapshot.Revision {
			t.Fatalf("relocated decision = %#v, candidates = %#v", snapshot.Decision, snapshot.Candidates)
		}
		committed, err := api.Approve(ctx, proposalID, "user-1", serviceTime().Add(3*time.Hour))
		if err != nil || committed.BaseRevision != snapshot.Revision || committed.NewRevision != snapshot.Revision+1 {
			t.Fatalf("approve relocated candidate = %#v, %v", committed, err)
		}
	})
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
