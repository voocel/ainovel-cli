package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSetFlow(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)

	if err := store.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	p, _ := store.Progress.Load()
	if p.Flow != domain.FlowRewriting {
		t.Errorf("expected FlowRewriting, got %s", p.Flow)
	}
}

func TestSetNovelName(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)

	if err := store.Progress.SetNovelName("长夜燃灯"); err != nil {
		t.Fatalf("SetNovelName: %v", err)
	}

	p, _ := store.Progress.Load()
	if p.NovelName != "长夜燃灯" {
		t.Fatalf("expected novel name updated, got %q", p.NovelName)
	}
}

func TestSetFlowRejectsInvalidTransition(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)

	if err := store.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow rewriting: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowReviewing); err == nil {
		t.Fatal("expected invalid flow transition to be rejected")
	}
}

func TestUpdatePhaseRejectsRegression(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)

	if err := store.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatalf("UpdatePhase outline: %v", err)
	}
	if err := store.Progress.UpdatePhase(domain.PhasePremise); err == nil {
		t.Fatal("expected phase regression to be rejected")
	}
}

func TestStartChapter(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)

	if err := store.Progress.StartChapter(1); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}

	p, _ := store.Progress.Load()
	if p.Phase != domain.PhaseWriting {
		t.Fatalf("expected phase writing, got %s", p.Phase)
	}
	if p.Flow != domain.FlowWriting {
		t.Fatalf("expected flow writing, got %s", p.Flow)
	}
	if p.CurrentChapter != 1 {
		t.Fatalf("expected current chapter 1, got %d", p.CurrentChapter)
	}
	if p.InProgressChapter != 1 {
		t.Fatalf("expected in-progress chapter 1, got %d", p.InProgressChapter)
	}
}

func TestIsChapterCompleted(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)

	if store.Progress.IsChapterCompleted(1) {
		t.Fatal("chapter 1 should not be completed initially")
	}

	_ = store.Progress.StartChapter(1)
	_ = store.Progress.MarkChapterComplete(1, 5000, "", "")

	if !store.Progress.IsChapterCompleted(1) {
		t.Fatal("chapter 1 should be completed after MarkChapterComplete")
	}
	if store.Progress.IsChapterCompleted(2) {
		t.Fatal("chapter 2 should not be completed")
	}
}

func TestSetPendingRewrites(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)

	chapters := []int{3, 5, 7}
	if err := store.Progress.SetPendingRewrites(chapters, "角色动机不连贯"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}

	p, _ := store.Progress.Load()
	if len(p.PendingRewrites) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(p.PendingRewrites))
	}
	if p.RewriteReason != "角色动机不连贯" {
		t.Errorf("reason mismatch: %s", p.RewriteReason)
	}
}

func TestCompleteRewrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)
	_ = store.Progress.SetPendingRewrites([]int{3, 5, 7}, "测试重写")
	_ = store.Progress.SetFlow(domain.FlowRewriting)

	// 完成第 5 章
	if err := store.Progress.CompleteRewrite(5); err != nil {
		t.Fatalf("CompleteRewrite(5): %v", err)
	}
	p, _ := store.Progress.Load()
	if len(p.PendingRewrites) != 2 {
		t.Fatalf("expected 2 pending after removing 5, got %d", len(p.PendingRewrites))
	}
	if p.Flow != domain.FlowRewriting {
		t.Errorf("flow should still be rewriting, got %s", p.Flow)
	}

	// 完成第 3 章
	_ = store.Progress.CompleteRewrite(3)
	p, _ = store.Progress.Load()
	if len(p.PendingRewrites) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(p.PendingRewrites))
	}

	// 完成最后一章 → 自动重置 Flow
	_ = store.Progress.CompleteRewrite(7)
	p, _ = store.Progress.Load()
	if len(p.PendingRewrites) != 0 {
		t.Fatalf("expected 0 pending, got %d", len(p.PendingRewrites))
	}
	if p.Flow != domain.FlowWriting {
		t.Errorf("flow should reset to writing, got %s", p.Flow)
	}
	if p.RewriteReason != "" {
		t.Errorf("reason should be cleared, got %s", p.RewriteReason)
	}
}

func TestCompleteRewrite_NotInQueue(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)
	_ = store.Progress.SetPendingRewrites([]int{3, 5}, "测试")

	// 完成不在队列中的章节不应报错
	if err := store.Progress.CompleteRewrite(99); err != nil {
		t.Fatalf("CompleteRewrite(99): %v", err)
	}
	p, _ := store.Progress.Load()
	if len(p.PendingRewrites) != 2 {
		t.Errorf("queue should be unchanged, got %d", len(p.PendingRewrites))
	}
}

func TestClearPendingRewrites(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.Progress.Init("test", 10)
	_ = store.Progress.SetPendingRewrites([]int{1, 2, 3}, "测试")
	_ = store.Progress.SetFlow(domain.FlowRewriting)

	if err := store.Progress.ClearPendingRewrites(); err != nil {
		t.Fatalf("ClearPendingRewrites: %v", err)
	}
	p, _ := store.Progress.Load()
	if len(p.PendingRewrites) != 0 {
		t.Errorf("expected empty, got %d", len(p.PendingRewrites))
	}
	if p.Flow != domain.FlowWriting {
		t.Errorf("flow should be writing, got %s", p.Flow)
	}
}
