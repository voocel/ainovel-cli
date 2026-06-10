package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

// TestCheckConsistency_EntityStates 验证返回实体最新状态与死亡名单。
func TestCheckConsistency_EntityStates(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Drafts.SaveDraft(5, "正文"); err != nil {
		t.Fatal(err)
	}
	if err := s.World.AppendStateChanges([]domain.StateChange{
		{Chapter: 3, Entity: "甲", Field: "status", NewValue: "死亡"},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := NewCheckConsistencyTool(s).Execute(context.Background(),
		json.RawMessage(`{"chapter":5}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		EntityStates map[string]map[string]domain.StateChange `json:"entity_states"`
		DeadEntities map[string]int                           `json:"dead_entities"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.EntityStates["甲"]["status"].NewValue != "死亡" {
		t.Fatalf("entity_states = %+v", out.EntityStates)
	}
	if out.DeadEntities["甲"] != 3 {
		t.Fatalf("dead_entities = %+v, want 甲:3", out.DeadEntities)
	}
}
