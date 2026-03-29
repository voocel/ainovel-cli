package orchestrator

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestDetermineRecoveryIncludesPlanningTierGuidance(t *testing.T) {
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    3,
		CompletedChapters: []int{1, 2},
		TotalWordCount:    2400,
		TotalChapters:     12,
	}
	runMeta := &domain.RunMeta{
		PlanningTier: domain.PlanningTierLong,
	}

	recovery := determineRecovery(progress, runMeta)
	if !strings.Contains(recovery.PromptText, "architect_long") {
		t.Fatalf("expected architect_long guidance, got %q", recovery.PromptText)
	}
	if !strings.Contains(recovery.PromptText, "分层大纲") {
		t.Fatalf("expected layered-outline guidance, got %q", recovery.PromptText)
	}
}

func TestDetermineRecoveryPendingSteer(t *testing.T) {
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    4,
		CompletedChapters: []int{1, 2, 3},
		TotalWordCount:    3600,
		TotalChapters:     10,
	}
	runMeta := &domain.RunMeta{
		PendingSteer: "让女主提前登场",
	}

	recovery := determineRecovery(progress, runMeta)
	if !strings.Contains(recovery.Label, "Steer 恢复") {
		t.Fatalf("expected steer recovery label, got %q", recovery.Label)
	}
	if !strings.Contains(recovery.PromptText, "让女主提前登场") {
		t.Fatalf("expected pending steer prompt, got %q", recovery.PromptText)
	}
}

func TestDetermineRecoveryReviewing(t *testing.T) {
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowReviewing,
		CurrentChapter:    5,
		CompletedChapters: []int{1, 2, 3, 4},
		TotalWordCount:    5200,
		TotalChapters:     12,
	}

	recovery := determineRecovery(progress, nil)
	if !strings.Contains(recovery.Label, "审阅恢复") {
		t.Fatalf("expected reviewing recovery label, got %q", recovery.Label)
	}
	if !strings.Contains(recovery.PromptText, "重新调用 editor") {
		t.Fatalf("expected editor recovery prompt, got %q", recovery.PromptText)
	}
}

func TestDetermineRecoveryReconcilesPendingCommit(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		CurrentChapter:    3,
		CompletedChapters: []int{1, 2},
		TotalWordCount:    4200,
		TotalChapters:     8,
	}
	if err := s.Progress.Save(progress); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}
	if err := s.Signals.SavePendingCommit(domain.PendingCommit{
		Chapter: 2,
		Stage:   domain.CommitStageProgressMarked,
		Result: &domain.CommitResult{
			Chapter:        2,
			Committed:      true,
			WordCount:      2100,
			NextChapter:    3,
			ReviewRequired: true,
			ReviewReason:   "达到阶段性审阅点",
		},
	}); err != nil {
		t.Fatalf("SavePendingCommit: %v", err)
	}

	recovery := determineRecovery(progress, nil, s)
	if !strings.Contains(recovery.Label, "补齐第 2 章提交") {
		t.Fatalf("unexpected recovery label: %q", recovery.Label)
	}
	if !strings.Contains(recovery.PromptText, "调用 editor") {
		t.Fatalf("expected editor follow-up prompt, got %q", recovery.PromptText)
	}

	updated, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if updated.Flow != domain.FlowReviewing {
		t.Fatalf("expected flow reviewing after reconcile, got %s", updated.Flow)
	}
	pending, err := s.Signals.LoadPendingCommit()
	if err != nil {
		t.Fatalf("LoadPendingCommit: %v", err)
	}
	if pending != nil {
		t.Fatalf("expected pending commit cleared, got %+v", pending)
	}
}

func TestDetermineRecoveryKeepsManualPendingCommit(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    3,
		CompletedChapters: []int{1, 2},
		TotalWordCount:    4200,
		TotalChapters:     8,
	}
	if err := s.Progress.Save(progress); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}
	if err := s.Signals.SavePendingCommit(domain.PendingCommit{
		Chapter: 2,
		Stage:   domain.CommitStageStateApplied,
		Summary: "第2章摘要",
	}); err != nil {
		t.Fatalf("SavePendingCommit: %v", err)
	}

	recovery := determineRecovery(progress, nil, s)
	if !strings.Contains(recovery.Label, "提交中断") {
		t.Fatalf("unexpected recovery label: %q", recovery.Label)
	}
	if !strings.Contains(recovery.PromptText, "重新调用 commit_chapter") {
		t.Fatalf("expected manual recovery prompt, got %q", recovery.PromptText)
	}

	pending, err := s.Signals.LoadPendingCommit()
	if err != nil {
		t.Fatalf("LoadPendingCommit: %v", err)
	}
	if pending == nil || pending.Stage != domain.CommitStageStateApplied {
		t.Fatalf("expected pending commit preserved, got %+v", pending)
	}
}
