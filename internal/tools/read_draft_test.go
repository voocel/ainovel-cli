package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestReadChapterFinal(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Drafts.SaveFinalChapter(1, "第一章的终稿正文。"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"chapter": 1, "source": "final"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Chapter   int    `json:"chapter"`
		Content   string `json:"content"`
		WordCount int    `json:"word_count"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Content == "" {
		t.Fatal("expected non-empty content")
	}
	if payload.WordCount == 0 {
		t.Fatal("expected non-zero word count")
	}
}

func TestReadChapterDraft(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Drafts.SaveDraft(3, "第三章的草稿内容。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"chapter": 3, "source": "draft"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Content == "" {
		t.Fatal("expected draft content")
	}
}

func TestReadChapterDialogue(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Characters.Save([]domain.Character{
		{Name: "张三", Aliases: []string{"老张"}},
	}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := store.Drafts.SaveFinalChapter(1, "张三站起身来。\u201c我不同意这个方案，\u201d张三冷冷地说。"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"source": "final", "character": "张三"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Character string   `json:"character"`
		Samples   []string `json:"samples"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Character != "张三" {
		t.Fatalf("expected character 张三, got %s", payload.Character)
	}
}

func TestReadChapterRange(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if err := store.Drafts.SaveFinalChapter(i, "这是一段正文内容。"); err != nil {
			t.Fatalf("SaveFinalChapter(%d): %v", i, err)
		}
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"from": 1, "to": 3, "source": "final"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Chapters map[string]string `json:"chapters"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Chapters) != 3 {
		t.Fatalf("expected 3 chapters, got %d", len(payload.Chapters))
	}
}

func TestDraftChapterWrite(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewDraftChapterTool(store)
	args, _ := json.Marshal(map[string]any{
		"chapter": 1,
		"content": "这是整章的正文内容，一次写完。",
		"mode":    "write",
	})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Written   bool `json:"written"`
		WordCount int  `json:"word_count"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !payload.Written {
		t.Fatal("expected written=true")
	}
	if payload.WordCount == 0 {
		t.Fatal("expected non-zero word count")
	}

	// 验证能读回来
	content, err := store.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if content == "" {
		t.Fatal("expected non-empty draft")
	}
	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.InProgressChapter != 1 {
		t.Fatalf("expected in-progress chapter 1, got %d", progress.InProgressChapter)
	}
	if progress.Phase != domain.PhaseWriting {
		t.Fatalf("expected phase writing, got %s", progress.Phase)
	}
}

func TestDraftChapterAppend(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Drafts.SaveDraft(2, "前半部分。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewDraftChapterTool(store)
	args, _ := json.Marshal(map[string]any{
		"chapter": 2,
		"content": "后半部分。",
		"mode":    "append",
	})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Mode      string `json:"mode"`
		WordCount int    `json:"word_count"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Mode != "append" {
		t.Fatalf("expected mode=append, got %s", payload.Mode)
	}

	content, _ := store.Drafts.LoadDraft(2)
	if content == "" || content == "前半部分。" {
		t.Fatal("expected appended content")
	}
}

func TestReadChapterMissingReturnsJSON(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"chapter": 1, "source": "final"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("expected no error for missing chapter, got: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if parsed["exists"] != false {
		t.Fatal("expected exists=false")
	}
}

func TestPlanChapterMarksInProgress(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewPlanChapterTool(store)
	args, _ := json.Marshal(map[string]any{
		"chapter":  1,
		"title":    "起头",
		"goal":     "建立处境",
		"conflict": "债务逼近",
		"hook":     "发现线索",
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.Phase != domain.PhaseWriting {
		t.Fatalf("expected phase writing, got %s", progress.Phase)
	}
	if progress.InProgressChapter != 1 {
		t.Fatalf("expected in-progress chapter 1, got %d", progress.InProgressChapter)
	}
}
