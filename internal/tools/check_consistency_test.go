package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCheckConsistencyReturnsPartialFactsWithWarnings(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveDraft(1, "可供检查的章节草稿"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "world_rules.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("辅助事实损坏不应中止一致性检查: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "partial" || len(got["_warnings"].([]any)) == 0 || got["content"] == "" {
		t.Fatalf("应返回正文、partial 和数据告警: %+v", got)
	}
}
