package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestReviseOutlineSchemaIsStrict(t *testing.T) {
	tool := NewReviseOutlineTool(store.NewStore(t.TempDir()))
	if !tool.StrictSchema() {
		t.Fatal("revise_outline must use strict schema")
	}
	if err := llmcontract.ValidateStrictReady(tool.Schema()); err != nil {
		t.Fatalf("revise_outline schema is not strict-ready: %v", err)
	}
}

func TestReviseOutlineReplacesFlatTailIdempotently(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init("test", 4, domain.GenreNovel); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "已完成"},
		{Chapter: 2, Title: "旧二"},
		{Chapter: 3, Title: "旧三"},
		{Chapter: 4, Title: "旧四"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{
		"from_chapter": 2,
		"replacement": []map[string]any{
			{"title": "新二", "core_event": "转折", "hook": "追查", "scenes": []string{"现场"}},
			{"title": "新三", "core_event": "揭示", "hook": "危机", "scenes": []string{}},
		},
		"reason": "依据已完成正文调整后续",
	})
	tool := NewReviseOutlineTool(s)
	for i := 0; i < 2; i++ {
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatalf("Execute #%d: %v", i+1, err)
		}
	}

	outline, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatal(err)
	}
	if len(outline) != 3 || outline[0].Title != "已完成" || outline[1].Title != "新二" || outline[2].Title != "新三" {
		t.Fatalf("unexpected revised outline: %+v", outline)
	}
	for i, entry := range outline {
		if entry.Chapter != i+1 {
			t.Fatalf("outline chapter numbering broken: %+v", outline)
		}
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if progress.TotalChapters != 3 {
		t.Fatalf("TotalChapters = %d, want 3", progress.TotalChapters)
	}
	if cp := s.Checkpoints.LatestByStep(domain.GlobalScope(), "revise_outline"); cp == nil {
		t.Fatal("revise_outline checkpoint missing")
	}
}

func TestReviseOutlineProtectsCompletedChapter(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init("test", 2, domain.GenreNovel); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一"}, {Chapter: 2, Title: "二"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"from_chapter":1,"replacement":[{"title":"改写","core_event":"改写","hook":"继续","scenes":[]}],"reason":"测试"}`)
	if _, err := NewReviseOutlineTool(s).Execute(context.Background(), args); err == nil {
		t.Fatal("expected completed chapter revision to be rejected")
	}
	outline, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatal(err)
	}
	if len(outline) != 2 || outline[0].Title != "一" {
		t.Fatalf("rejected revision changed outline: %+v", outline)
	}
}

func TestReviseOutlinePreservesOtherLayeredArcs(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "卷一",
		Arcs: []domain.ArcOutline{
			{Index: 1, Title: "弧一", Chapters: []domain.OutlineEntry{
				{Chapter: 1, Title: "已完成"},
				{Chapter: 2, Title: "旧二"},
				{Chapter: 3, Title: "旧三"},
			}},
			{Index: 2, Title: "弧二", Chapters: []domain.OutlineEntry{
				{Chapter: 4, Title: "保留四"},
				{Chapter: 5, Title: "保留五"},
			}},
		},
	}}
	if err := s.Progress.Init("test", 5, domain.GenreNovel); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatal(err)
	}

	args := json.RawMessage(`{"from_chapter":2,"replacement":[{"title":"新二","core_event":"转折","hook":"继续","scenes":[]}],"reason":"压缩当前弧"}`)
	tool := NewReviseOutlineTool(s)
	for i := 0; i < 2; i++ {
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatalf("Execute #%d: %v", i+1, err)
		}
	}

	layered, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	if got := layered[0].Arcs[0].Chapters; len(got) != 2 || got[1].Title != "新二" {
		t.Fatalf("target arc revision = %+v", got)
	}
	if got := layered[0].Arcs[1].Chapters; len(got) != 2 || got[0].Title != "保留四" || got[1].Title != "保留五" {
		t.Fatalf("following arc changed: %+v", got)
	}
	outline, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatal(err)
	}
	if len(outline) != 4 || outline[2].Title != "保留四" || outline[2].Chapter != 3 {
		t.Fatalf("flat projection not regenerated: %+v", outline)
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if progress.TotalChapters != 4 {
		t.Fatalf("TotalChapters = %d, want 4", progress.TotalChapters)
	}
}
