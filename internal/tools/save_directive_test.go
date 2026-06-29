package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func execDirective(t *testing.T, tool *SaveDirectiveTool, args map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(args)
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute %v: %v", args, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	return payload
}

func TestSaveDirectiveAddAndRemove(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	tool := NewSaveDirectiveTool(s)

	// add：Kết quả含带序号的全量列表
	payload := execDirective(t, tool, map[string]any{"action": "add", "text": "对话占比提高"})
	execDirective(t, tool, map[string]any{"action": "add", "text": "Tiêu đề只用中文"})

	directives, ok := payload["directives"].([]any)
	if !ok || len(directives) != 1 {
		t.Fatalf("unexpected directives after first add: %v", payload["directives"])
	}
	first, _ := directives[0].(map[string]any)
	if first["text"] != "对话占比提高" || first["index"] != float64(1) {
		t.Errorf("unexpected first entry: %v", first)
	}
	// Tiến độChụp由工具从 Progress Đọc，不依赖 LLM 传参：
	// Progress.Init("test", 10) 后 NextChapter=1、TotalChapters=10
	if first["at_chapter"] != float64(1) || first["at_total_chapters"] != float64(10) {
		t.Errorf("entry should carry progress snapshot, got %v", first)
	}

	// remove：按序号删除
	payload = execDirective(t, tool, map[string]any{"action": "remove", "index": 1})
	directives, _ = payload["directives"].([]any)
	if len(directives) != 1 {
		t.Fatalf("expected 1 entry after remove, got %d", len(directives))
	}
	remaining, _ := directives[0].(map[string]any)
	if remaining["text"] != "Tiêu đề只用中文" || remaining["index"] != float64(1) {
		t.Errorf("remaining entry should be renumbered: %v", remaining)
	}
}

func TestSaveDirectiveRejectsBadArgs(t *testing.T) {
	s := store.NewStore(t.TempDir())
	tool := NewSaveDirectiveTool(s)

	cases := []map[string]any{
		{"action": "add"},                // 缺 text
		{"action": "add", "text": "  "},  // Rỗng白 text
		{"action": "remove"},             // 缺 index
		{"action": "remove", "index": 9}, // 越界
		{"action": "merge", "text": "x"}, // Không rõ action
	}
	for _, args := range cases {
		raw, _ := json.Marshal(args)
		if _, err := tool.Execute(context.Background(), raw); err == nil {
			t.Errorf("expected error for args %v", args)
		}
	}
}
