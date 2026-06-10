package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

// TestCommitChapter_ForeshadowFacts 验证 commit 返回未知伏笔 ID 与逾期伏笔事实。
func TestCommitChapter_ForeshadowFacts(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init("测试书", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.World.UpdateForeshadow(1, []domain.ForeshadowUpdate{
		{ID: "f-old", Action: "plant", Description: "旧伏笔", Deadline: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(3, "正文内容……"); err != nil {
		t.Fatal(err)
	}
	tool := NewCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    3,
		"summary":    "摘要",
		"characters": []string{"林尘"},
		"key_events": []string{"事件"},
		"foreshadow_updates": []map[string]any{
			{"id": "ghost", "action": "advance"},
		},
	})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out struct {
		ForeshadowUnknownIDs []string                 `json:"foreshadow_unknown_ids"`
		ForeshadowOverdue    []domain.ForeshadowEntry `json:"foreshadow_overdue"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ForeshadowUnknownIDs) != 1 || out.ForeshadowUnknownIDs[0] != "ghost" {
		t.Fatalf("unknown_ids = %v, want [ghost]", out.ForeshadowUnknownIDs)
	}
	if len(out.ForeshadowOverdue) != 1 || out.ForeshadowOverdue[0].ID != "f-old" {
		t.Fatalf("overdue = %+v, want [f-old]", out.ForeshadowOverdue)
	}
}
