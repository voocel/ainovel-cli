package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/domain"
	"github.com/voocel/ainovel-cli/state"
)

// SaveFoundationTool 保存基础设定（premise/outline/characters），Architect 专用。
type SaveFoundationTool struct {
	store *state.Store
}

func NewSaveFoundationTool(store *state.Store) *SaveFoundationTool {
	return &SaveFoundationTool{store: store}
}

func (t *SaveFoundationTool) Name() string { return "save_foundation" }
func (t *SaveFoundationTool) Description() string {
	return "保存小说基础设定。type=premise 时 content 为 Markdown；type=outline 时 content 为 JSON 数组；type=characters 时 content 为 JSON 数组；type=world_rules 时 content 为 JSON 数组"
}
func (t *SaveFoundationTool) Label() string { return "保存设定" }

func (t *SaveFoundationTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("type", schema.Enum("设定类型", "premise", "outline", "characters", "world_rules")).Required(),
		schema.Property("content", schema.String("内容。premise 为 Markdown 文本，outline 和 characters 为 JSON 字符串")).Required(),
	)
}

func (t *SaveFoundationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	switch a.Type {
	case "premise":
		if err := t.store.SavePremise(a.Content); err != nil {
			return nil, fmt.Errorf("save premise: %w", err)
		}
		_ = t.store.UpdatePhase(domain.PhasePremise)
		return json.Marshal(map[string]any{"saved": true, "type": "premise"})

	case "outline":
		var entries []domain.OutlineEntry
		if err := json.Unmarshal([]byte(a.Content), &entries); err != nil {
			return nil, fmt.Errorf("parse outline JSON: %w", err)
		}
		if err := t.store.SaveOutline(entries); err != nil {
			return nil, fmt.Errorf("save outline: %w", err)
		}
		_ = t.store.UpdatePhase(domain.PhaseOutline)
		// 根据大纲长度自动设定总章节数
		_ = t.store.SetTotalChapters(len(entries))
		return json.Marshal(map[string]any{"saved": true, "type": "outline", "chapters": len(entries)})

	case "characters":
		var chars []domain.Character
		if err := json.Unmarshal([]byte(a.Content), &chars); err != nil {
			return nil, fmt.Errorf("parse characters JSON: %w", err)
		}
		if err := t.store.SaveCharacters(chars); err != nil {
			return nil, fmt.Errorf("save characters: %w", err)
		}
		return json.Marshal(map[string]any{"saved": true, "type": "characters", "count": len(chars)})

	case "world_rules":
		var rules []domain.WorldRule
		if err := json.Unmarshal([]byte(a.Content), &rules); err != nil {
			return nil, fmt.Errorf("parse world_rules JSON: %w", err)
		}
		if err := t.store.SaveWorldRules(rules); err != nil {
			return nil, fmt.Errorf("save world_rules: %w", err)
		}
		return json.Marshal(map[string]any{"saved": true, "type": "world_rules", "count": len(rules)})

	default:
		return nil, fmt.Errorf("unknown type %q, expected premise/outline/characters/world_rules", a.Type)
	}
}
