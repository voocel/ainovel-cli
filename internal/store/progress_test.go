package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSetFlow(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.InitProgress("test", 10)

	if err := store.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	p, _ := store.LoadProgress()
	if p.Flow != domain.FlowRewriting {
		t.Errorf("expected FlowRewriting, got %s", p.Flow)
	}
}

func TestSetPendingRewrites(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.InitProgress("test", 10)

	chapters := []int{3, 5, 7}
	if err := store.SetPendingRewrites(chapters, "角色动机不连贯"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}

	p, _ := store.LoadProgress()
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
	_ = store.InitProgress("test", 10)
	_ = store.SetPendingRewrites([]int{3, 5, 7}, "测试重写")
	_ = store.SetFlow(domain.FlowRewriting)

	// 完成第 5 章
	if err := store.CompleteRewrite(5); err != nil {
		t.Fatalf("CompleteRewrite(5): %v", err)
	}
	p, _ := store.LoadProgress()
	if len(p.PendingRewrites) != 2 {
		t.Fatalf("expected 2 pending after removing 5, got %d", len(p.PendingRewrites))
	}
	if p.Flow != domain.FlowRewriting {
		t.Errorf("flow should still be rewriting, got %s", p.Flow)
	}

	// 完成第 3 章
	_ = store.CompleteRewrite(3)
	p, _ = store.LoadProgress()
	if len(p.PendingRewrites) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(p.PendingRewrites))
	}

	// 完成最后一章 → 自动重置 Flow
	_ = store.CompleteRewrite(7)
	p, _ = store.LoadProgress()
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
	_ = store.InitProgress("test", 10)
	_ = store.SetPendingRewrites([]int{3, 5}, "测试")

	// 完成不在队列中的章节不应报错
	if err := store.CompleteRewrite(99); err != nil {
		t.Fatalf("CompleteRewrite(99): %v", err)
	}
	p, _ := store.LoadProgress()
	if len(p.PendingRewrites) != 2 {
		t.Errorf("queue should be unchanged, got %d", len(p.PendingRewrites))
	}
}

func TestClearPendingRewrites(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_ = store.InitProgress("test", 10)
	_ = store.SetPendingRewrites([]int{1, 2, 3}, "测试")
	_ = store.SetFlow(domain.FlowRewriting)

	if err := store.ClearPendingRewrites(); err != nil {
		t.Fatalf("ClearPendingRewrites: %v", err)
	}
	p, _ := store.LoadProgress()
	if len(p.PendingRewrites) != 0 {
		t.Errorf("expected empty, got %d", len(p.PendingRewrites))
	}
	if p.Flow != domain.FlowWriting {
		t.Errorf("flow should be writing, got %s", p.Flow)
	}
}
