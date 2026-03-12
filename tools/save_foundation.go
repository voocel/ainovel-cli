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
	return "保存小说基础设定。type=premise 时 content 为 Markdown；type=outline 时 content 为 JSON 数组；type=characters 时 content 为 JSON 数组；type=world_rules 时 content 为 JSON 数组。scale 可选，用于记录 short/mid/long 规划级别"
}
func (t *SaveFoundationTool) Label() string { return "保存设定" }

func (t *SaveFoundationTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("type", schema.Enum("设定类型", "premise", "outline", "layered_outline", "characters", "world_rules")).Required(),
		schema.Property("content", schema.String("内容。premise 为 Markdown 文本，outline/layered_outline/characters/world_rules 为 JSON 字符串")).Required(),
		schema.Property("scale", schema.Enum("规划级别", "short", "mid", "long")),
	)
}

func (t *SaveFoundationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Type    string `json:"type"`
		Content string `json:"content"`
		Scale   string `json:"scale"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Scale != "" {
		switch domain.PlanningTier(a.Scale) {
		case domain.PlanningTierShort, domain.PlanningTierMid, domain.PlanningTierLong:
		default:
			return nil, fmt.Errorf("invalid scale %q, expected short/mid/long", a.Scale)
		}
		if err := t.store.SetPlanningTier(domain.PlanningTier(a.Scale)); err != nil {
			return nil, fmt.Errorf("save planning tier: %w", err)
		}
	}

	switch a.Type {
	case "premise":
		if err := t.store.SavePremise(a.Content); err != nil {
			return nil, fmt.Errorf("save premise: %w", err)
		}
		_ = t.store.UpdatePhase(domain.PhasePremise)
		return json.Marshal(map[string]any{"saved": true, "type": "premise", "scale": a.Scale})

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
		if domain.PlanningTier(a.Scale) != domain.PlanningTierLong {
			_ = t.store.SetLayered(false)
			_ = t.store.UpdateVolumeArc(0, 0)
			_ = t.store.ClearLayeredOutline()
		}
		return json.Marshal(map[string]any{"saved": true, "type": "outline", "chapters": len(entries), "scale": a.Scale})

	case "layered_outline":
		var volumes []domain.VolumeOutline
		if err := json.Unmarshal([]byte(a.Content), &volumes); err != nil {
			return nil, fmt.Errorf("parse layered_outline JSON: %w", err)
		}
		if err := t.store.SaveLayeredOutline(volumes); err != nil {
			return nil, fmt.Errorf("save layered_outline: %w", err)
		}
		// 展开为扁平大纲，兼容现有 GetChapterOutline
		flat := domain.FlattenOutline(volumes)
		if err := t.store.SaveOutline(flat); err != nil {
			return nil, fmt.Errorf("save flattened outline: %w", err)
		}
		total := domain.TotalChapters(volumes)
		_ = t.store.UpdatePhase(domain.PhaseOutline)
		_ = t.store.SetTotalChapters(total)
		_ = t.store.SetLayered(true)
		if len(volumes) > 0 && len(volumes[0].Arcs) > 0 {
			_ = t.store.UpdateVolumeArc(volumes[0].Index, volumes[0].Arcs[0].Index)
		}
		return json.Marshal(map[string]any{
			"saved": true, "type": "layered_outline",
			"volumes": len(volumes), "chapters": total,
			"scale": a.Scale,
		})

	case "characters":
		var chars []domain.Character
		if err := json.Unmarshal([]byte(a.Content), &chars); err != nil {
			return nil, fmt.Errorf("parse characters JSON: %w", err)
		}
		if err := t.store.SaveCharacters(chars); err != nil {
			return nil, fmt.Errorf("save characters: %w", err)
		}
		return json.Marshal(map[string]any{"saved": true, "type": "characters", "count": len(chars), "scale": a.Scale})

	case "world_rules":
		var rules []domain.WorldRule
		if err := json.Unmarshal([]byte(a.Content), &rules); err != nil {
			return nil, fmt.Errorf("parse world_rules JSON: %w", err)
		}
		if err := t.store.SaveWorldRules(rules); err != nil {
			return nil, fmt.Errorf("save world_rules: %w", err)
		}
		return json.Marshal(map[string]any{"saved": true, "type": "world_rules", "count": len(rules), "scale": a.Scale})

	default:
		return nil, fmt.Errorf("unknown type %q, expected premise/outline/layered_outline/characters/world_rules", a.Type)
	}
}
