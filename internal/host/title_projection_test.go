package host

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestFillDetailsUsesCommittedTitleOnlyForCompletedChapters(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init("test", 2, domain.GenreNovel); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "计划一"},
		{Chapter: 2, Title: "计划二"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter: 1, Title: "终稿一", Summary: "摘要",
	}); err != nil {
		t.Fatal(err)
	}

	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot UISnapshot
	(&Host{store: s}).fillDetails(&snapshot, progress)

	if len(snapshot.Outline) != 2 {
		t.Fatalf("outline snapshot = %+v", snapshot.Outline)
	}
	if snapshot.Outline[0].Title != "终稿一" {
		t.Fatalf("completed title = %q, want committed title", snapshot.Outline[0].Title)
	}
	if snapshot.Outline[1].Title != "计划二" {
		t.Fatalf("future title = %q, want planned title", snapshot.Outline[1].Title)
	}
}
