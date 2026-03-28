package orchestrator

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestReminderEngineReadOnlySpiral(t *testing.T) {
	engine := newReminderEngine(nil)
	for range readOnlyReminderThreshold {
		engine.observeToolProgress(toolProgress{Tool: "novel_context"})
	}
	actions := engine.drain()
	if len(actions) == 0 {
		t.Fatal("expected reminder actions, got none")
	}

	var hasNotice, hasFollowUp bool
	for _, action := range actions {
		switch action.Kind {
		case actionEmitNotice:
			hasNotice = action.Summary == "连续只读探索过多，提醒开始落稿"
		case actionFollowUp:
			hasFollowUp = action.Message != ""
		}
	}
	if !hasNotice || !hasFollowUp {
		t.Fatalf("expected notice and follow_up, got %+v", actions)
	}
	if engine.consecutiveReadOnly != 0 {
		t.Fatalf("expected streak reset, got %d", engine.consecutiveReadOnly)
	}
}

func TestReminderEngineProductiveToolResetsReadOnlyStreak(t *testing.T) {
	engine := newReminderEngine(nil)
	engine.observeToolProgress(toolProgress{Tool: "novel_context"})
	engine.observeToolProgress(toolProgress{Tool: "read_chapter"})
	engine.observeToolProgress(toolProgress{Tool: "draft_chapter"})

	if engine.consecutiveReadOnly != 0 {
		t.Fatalf("expected productive tool reset streak, got %d", engine.consecutiveReadOnly)
	}
}

func TestReminderEngineToolFailureCreatesReminder(t *testing.T) {
	engine := newReminderEngine(nil)
	engine.observeToolProgress(toolProgress{
		Tool:    "commit_chapter",
		Error:   true,
		Message: "save summary: disk full",
	})
	actions := engine.drain()
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	if actions[0].Kind != actionEmitNotice || actions[1].Kind != actionFollowUp {
		t.Fatalf("unexpected actions: %+v", actions)
	}
}

func TestReminderEngineFoundationIncompleteReminder(t *testing.T) {
	engine := newReminderEngine(nil)
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.SaveProgress(&domain.Progress{Phase: domain.PhasePremise}); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}
	if err := s.InitRunMeta("default", "openrouter", "gpt-5.4"); err != nil {
		t.Fatalf("InitRunMeta: %v", err)
	}
	if err := s.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("SetPlanningTier: %v", err)
	}

	engine.observeSubAgentDone(s, false)
	actions := engine.drain()
	if len(actions) < 2 {
		t.Fatalf("expected reminder actions, got %+v", actions)
	}
	if actions[0].Kind != actionEmitNotice || actions[1].Kind != actionFollowUp {
		t.Fatalf("unexpected actions: %+v", actions)
	}
	if actions[1].Message == "" || !contains(actions[1].Message, "architect_long") {
		t.Fatalf("expected planning guidance in follow_up, got %q", actions[1].Message)
	}
}

func TestReminderEngineUncommittedDraftReminder(t *testing.T) {
	engine := newReminderEngine(nil)
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.SaveProgress(&domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    2,
		CompletedChapters: []int{1},
		TotalChapters:     6,
	}); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}
	if err := s.SaveDraft(2, "第2章草稿"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	engine.observeSubAgentDone(s, false)
	actions := engine.drain()
	if len(actions) != 2 {
		t.Fatalf("expected 2 reminder actions, got %+v", actions)
	}
	if actions[0].Kind != actionEmitNotice || actions[1].Kind != actionFollowUp {
		t.Fatalf("unexpected actions: %+v", actions)
	}
	if actions[1].Message == "" || !contains(actions[1].Message, "commit_chapter") {
		t.Fatalf("expected commit reminder, got %q", actions[1].Message)
	}
}

func TestReminderEngineCommittedSubAgentSkipsUncommittedDraftReminder(t *testing.T) {
	engine := newReminderEngine(nil)
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.SaveProgress(&domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    2,
		CompletedChapters: []int{1},
		TotalChapters:     6,
	}); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}
	if err := s.SaveDraft(2, "第2章草稿"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	engine.observeSubAgentDone(s, true)
	actions := engine.drain()
	if len(actions) != 0 {
		t.Fatalf("expected no reminder after commit, got %+v", actions)
	}
}

func TestReminderEngineQueueDeduplicatesSameReminder(t *testing.T) {
	engine := newReminderEngine(nil)
	engine.observeToolFailure("commit_chapter", "save summary: disk full")
	engine.observeToolFailure("commit_chapter", "save summary: disk full")

	actions := engine.drain()
	if len(actions) != 2 {
		t.Fatalf("expected deduplicated 2 actions, got %+v", actions)
	}
	if drainedAgain := engine.drain(); len(drainedAgain) != 0 {
		t.Fatalf("expected queue empty after drain, got %+v", drainedAgain)
	}
}

func TestReminderEngineQueueDeduplicatesByStableKeyInsteadOfMessage(t *testing.T) {
	engine := newReminderEngine(nil)
	engine.enqueue(
		withDedupKey(followUp("[系统] 第一版文案"), "reminder.test.followup"),
		withDedupKey(followUp("[系统] 第二版文案"), "reminder.test.followup"),
	)

	actions := engine.drain()
	if len(actions) != 1 {
		t.Fatalf("expected deduplicated single action, got %+v", actions)
	}
	if actions[0].DedupKey != "reminder.test.followup" {
		t.Fatalf("unexpected dedup key: %+v", actions[0])
	}
}

func TestReminderEngineUsesMemoryPolicyThreshold(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.SaveProgress(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		TotalChapters:     80,
		CompletedChapters: []int{1, 2, 3, 4, 5, 6},
	}); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}
	engine := newReminderEngine(s)
	for range 4 {
		engine.observeToolProgress(toolProgress{Tool: "novel_context"})
	}
	actions := engine.drain()
	if len(actions) == 0 {
		t.Fatal("expected reminder actions under memory policy threshold")
	}
}

func contains(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && strings.Contains(s, sub)
}
