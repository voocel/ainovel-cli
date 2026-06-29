package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// completedBook 构造一本已完结的 N 章小说（phase=complete，CompletedChapters=1..n）。
func completedBook(t *testing.T, n int) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", n); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	for ch := 1; ch <= n; ch++ {
		if err := s.Progress.MarkChapterComplete(ch, 100, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
	}
	if err := s.Progress.MarkComplete(); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	return s
}

func TestReopenBookReopensCompletedBook(t *testing.T) {
	s := completedBook(t, 3)
	tool := NewReopenBookTool(s)

	args, _ := json.Marshal(map[string]any{"chapters": []int{3, 1}, "reason": "清理特殊字符"})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["reopened"] != true || payload["phase"] != string(domain.PhaseWriting) {
		t.Fatalf("unexpected payload: %v", payload)
	}

	p, _ := s.Progress.Load()
	if p.Phase != domain.PhaseWriting {
		t.Errorf("phase = %s, want writing", p.Phase)
	}
	if p.Flow != domain.FlowRewriting {
		t.Errorf("flow = %s, want rewriting", p.Flow)
	}
	if len(p.PendingRewrites) != 2 || p.PendingRewrites[0] != 3 || p.PendingRewrites[1] != 1 {
		t.Errorf("PendingRewrites = %v, want [3 1] (原样入队)", p.PendingRewrites)
	}

	if cp := s.Checkpoints.LatestByStep(domain.GlobalScope(), "reopen"); cp == nil {
		t.Error("expected a 'reopen' checkpoint")
	}
}

func TestReopenBookRejectsNonCompleteBook(t *testing.T) {
	// Viết中（未完结）的书不能 reopen
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 5); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(1, 100, "", ""); err != nil { // phase→writing
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	tool := NewReopenBookTool(s)
	args, _ := json.Marshal(map[string]any{"chapters": []int{1}})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected reopen to be rejected when phase != complete")
	}
}

func TestReopenBookRejectsUnwrittenChapters(t *testing.T) {
	s := completedBook(t, 3)
	tool := NewReopenBookTool(s)

	// 第 5 章不存在 → 拒绝（属续写/越界，应走篇幅调整）
	args, _ := json.Marshal(map[string]any{"chapters": []int{2, 5}})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected reopen to be rejected for unwritten chapter")
	}
	// Rỗng chapters → 拒绝
	args, _ = json.Marshal(map[string]any{"chapters": []int{}})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected reopen to be rejected for empty chapters")
	}
}
