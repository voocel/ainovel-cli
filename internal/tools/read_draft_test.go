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
	if err := store.SaveFinalChapter(1, "第一章的终稿正文。"); err != nil {
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
	if err := store.SaveDraft(3, "第三章的草稿内容。"); err != nil {
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
	if err := store.SaveCharacters([]domain.Character{
		{Name: "张三", Aliases: []string{"老张"}},
	}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := store.SaveFinalChapter(1, "张三站起身来。\u201c我不同意这个方案，\u201d张三冷冷地说。"); err != nil {
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
		if err := store.SaveFinalChapter(i, "这是一段正文内容。"); err != nil {
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
	content, err := store.LoadDraft(1)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if content == "" {
		t.Fatal("expected non-empty draft")
	}
}

func TestDraftChapterAppend(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.SaveDraft(2, "前半部分。"); err != nil {
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

	content, _ := store.LoadDraft(2)
	if content == "" || content == "前半部分。" {
		t.Fatal("expected appended content")
	}
}
