package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

// TestCommitChapter_DeadCharacterViolation 验证已死角色出场返回 character_violations。
func TestCommitChapter_DeadCharacterViolation(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Progress.Init("测试书", 10); err != nil {
		t.Fatal(err)
	}
	if err := s.World.AppendStateChanges([]domain.StateChange{
		{Chapter: 1, Entity: "王老五", Field: "status", NewValue: "死亡"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(2, "正文"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"summary":    "摘要",
		"characters": []string{"王老五"},
		"key_events": []string{"事件"},
	})
	raw, err := NewCommitChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		CharacterViolations []string `json:"character_violations"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.CharacterViolations) != 1 {
		t.Fatalf("character_violations = %v, want 1 条", out.CharacterViolations)
	}
}
