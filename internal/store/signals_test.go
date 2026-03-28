package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestPendingCommitLifecycle(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pending := domain.PendingCommit{
		Chapter:   3,
		Stage:     domain.CommitStageProgressMarked,
		Summary:   "第3章摘要",
		StartedAt: "2026-03-27T10:00:00Z",
		UpdatedAt: "2026-03-27T10:01:00Z",
		Result: &domain.CommitResult{
			Chapter:     3,
			Committed:   true,
			WordCount:   2400,
			NextChapter: 4,
		},
	}
	if err := s.SavePendingCommit(pending); err != nil {
		t.Fatalf("SavePendingCommit: %v", err)
	}

	got, err := s.LoadPendingCommit()
	if err != nil {
		t.Fatalf("LoadPendingCommit: %v", err)
	}
	if got == nil {
		t.Fatal("expected pending commit, got nil")
	}
	if got.Chapter != 3 || got.Stage != domain.CommitStageProgressMarked {
		t.Fatalf("unexpected pending commit: %+v", got)
	}
	if got.Result == nil || got.Result.NextChapter != 4 {
		t.Fatalf("unexpected pending result: %+v", got.Result)
	}

	if err := s.ClearPendingCommit(); err != nil {
		t.Fatalf("ClearPendingCommit: %v", err)
	}
	got, err = s.LoadPendingCommit()
	if err != nil {
		t.Fatalf("LoadPendingCommit after clear: %v", err)
	}
	if got != nil {
		t.Fatalf("expected pending commit cleared, got %+v", got)
	}
}
