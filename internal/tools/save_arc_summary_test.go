package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSaveArcSummaryPersistsStyleRulesDialogueObjects(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveArcSummaryTool(s)
	args, err := json.Marshal(map[string]any{
		"volume":     1,
		"arc":        2,
		"title":      "入山",
		"summary":    "主角Hoàn thành入山试炼，Xác nhận后续追索方向。",
		"key_events": []string{"通过试炼", "发现Cũ案线索"},
		"character_snapshots": []map[string]any{
			{"name": "沈渊", "status": "存活", "motivation": "追查Cũ案"},
		},
		"style_rules": map[string]any{
			"prose": []string{"环境描写优先触觉和嗅觉", "动作戏用短句推进", "心理描写不解释结论"},
			"dialogue": []map[string]any{
				{"name": "沈渊", "rules": []string{"对话极简", "少用疑问句"}},
			},
			"taboos": []string{"避免章末长独白"},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	rules, err := s.World.LoadStyleRules()
	if err != nil {
		t.Fatalf("LoadStyleRules: %v", err)
	}
	if rules == nil || len(rules.Dialogue) != 1 {
		t.Fatalf("expected one dialogue rule, got %+v", rules)
	}
	if rules.Dialogue[0].Name != "沈渊" || len(rules.Dialogue[0].Rules) != 2 {
		t.Fatalf("unexpected dialogue rule: %+v", rules.Dialogue[0])
	}
}

func TestSaveArcSummaryRejectsDialogueStringArray(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveArcSummaryTool(s)
	args, err := json.Marshal(map[string]any{
		"volume":              1,
		"arc":                 2,
		"title":               "入山",
		"summary":             "主角Hoàn thành入山试炼，Xác nhận后续追索方向。",
		"key_events":          []string{"通过试炼"},
		"character_snapshots": []map[string]any{},
		"style_rules": map[string]any{
			"prose":    []string{"环境描写优先触觉和嗅觉"},
			"dialogue": []string{"沈渊对话极简"},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "style_rules.dialogue") {
		t.Fatalf("expected style_rules.dialogue validation error, got %v", err)
	}
}
