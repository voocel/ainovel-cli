package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveFoundationTool 保存基础设定（premise/outline/characters），Architect 专用。
type SaveFoundationTool struct {
	store *store.Store
}

func NewSaveFoundationTool(store *store.Store) *SaveFoundationTool {
	return &SaveFoundationTool{store: store}
}

func (t *SaveFoundationTool) Name() string { return "save_foundation" }
func (t *SaveFoundationTool) Description() string {
	return "保存小说基础设定。参数固定为 {type, content, scale?}。type 可选 premise / outline / layered_outline / characters / world_rules。premise 时 content 必须是 Markdown 字符串；outline、layered_outline、characters、world_rules 时 content 优先直接传 JSON 数组或对象，不要再手动包一层转义字符串；工具也兼容传入 JSON 字符串。scale 可选，仅允许 short / mid / long。"
}
func (t *SaveFoundationTool) Label() string { return "保存设定" }

func (t *SaveFoundationTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("type", schema.Enum("设定类型", "premise", "outline", "layered_outline", "characters", "world_rules")).Required(),
		schema.Property("content", map[string]any{
			"description": "内容。premise 传 Markdown 字符串；outline/layered_outline/characters/world_rules 直接传 JSON 数组或对象即可，也兼容传 JSON 字符串。不要把数组再次手动转义成难读的字符串。",
		}).Required(),
		schema.Property("scale", schema.Enum("规划级别", "short", "mid", "long")),
	)
}

func (t *SaveFoundationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Scale   string          `json:"scale"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	content, err := normalizeFoundationContent(a.Content)
	if err != nil {
		return nil, err
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

	result := map[string]any{"saved": true, "type": a.Type, "scale": a.Scale}

	switch a.Type {
	case "premise":
		if err := t.store.SavePremise(content); err != nil {
			return nil, fmt.Errorf("save premise: %w", err)
		}
		_ = t.store.UpdatePhase(domain.PhasePremise)

	case "outline":
		var entries []domain.OutlineEntry
		if err := json.Unmarshal([]byte(content), &entries); err != nil {
			return nil, fmt.Errorf("parse outline JSON: %w", err)
		}
		if err := t.store.SaveOutline(entries); err != nil {
			return nil, fmt.Errorf("save outline: %w", err)
		}
		_ = t.store.UpdatePhase(domain.PhaseOutline)
		_ = t.store.SetTotalChapters(len(entries))
		if domain.PlanningTier(a.Scale) != domain.PlanningTierLong {
			_ = t.store.SetLayered(false)
			_ = t.store.UpdateVolumeArc(0, 0)
			_ = t.store.ClearLayeredOutline()
		}
		result["chapters"] = len(entries)

	case "layered_outline":
		var volumes []domain.VolumeOutline
		if err := json.Unmarshal([]byte(content), &volumes); err != nil {
			return nil, fmt.Errorf("parse layered_outline JSON: %w", err)
		}
		if err := t.store.SaveLayeredOutline(volumes); err != nil {
			return nil, fmt.Errorf("save layered_outline: %w", err)
		}
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
		result["volumes"] = len(volumes)
		result["chapters"] = total

	case "characters":
		var chars []domain.Character
		if err := json.Unmarshal([]byte(content), &chars); err != nil {
			return nil, fmt.Errorf("parse characters JSON: %w", err)
		}
		if err := t.store.SaveCharacters(chars); err != nil {
			return nil, fmt.Errorf("save characters: %w", err)
		}
		result["count"] = len(chars)

	case "world_rules":
		var rules []domain.WorldRule
		if err := json.Unmarshal([]byte(content), &rules); err != nil {
			return nil, fmt.Errorf("parse world_rules JSON: %w", err)
		}
		if err := t.store.SaveWorldRules(rules); err != nil {
			return nil, fmt.Errorf("save world_rules: %w", err)
		}
		result["count"] = len(rules)

	default:
		return nil, fmt.Errorf("unknown type %q, expected premise/outline/layered_outline/characters/world_rules", a.Type)
	}

	// 返回剩余未完成项，引导 Architect 继续
	result["remaining"] = t.remaining()
	return json.Marshal(result)
}

func normalizeFoundationContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("content is required")
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	if !json.Valid(raw) {
		return "", fmt.Errorf("invalid content: expected Markdown string or valid JSON value")
	}
	return string(raw), nil
}

// remaining 检查基础设定中还缺少哪些必要项。
func (t *SaveFoundationTool) remaining() []string {
	var missing []string
	if p, _ := t.store.LoadPremise(); p == "" {
		missing = append(missing, "premise")
	}
	if o, _ := t.store.LoadOutline(); len(o) == 0 {
		missing = append(missing, "outline")
	}
	if c, _ := t.store.LoadCharacters(); len(c) == 0 {
		missing = append(missing, "characters")
	}
	if r, _ := t.store.LoadWorldRules(); len(r) == 0 {
		missing = append(missing, "world_rules")
	}
	return missing
}
