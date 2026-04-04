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
	return "保存小说基础设定。参数固定为 {type, content, scale?, volume?, arc?}。type 可选 premise / outline / layered_outline / characters / world_rules / expand_arc / append_volume / update_compass。premise 时 content 必须是 Markdown 字符串；其他类型 content 优先直接传 JSON 数组或对象。expand_arc 展开骨架弧的详细章节（需 volume + arc）；append_volume 追加新卷（content 为完整 VolumeOutline JSON，含弧结构）；update_compass 更新终局方向（content 为 StoryCompass JSON）。scale 可选，仅允许 short / mid / long。"
}
func (t *SaveFoundationTool) Label() string { return "保存设定" }

func (t *SaveFoundationTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("type", schema.Enum("设定类型", "premise", "outline", "layered_outline", "characters", "world_rules", "expand_arc", "append_volume", "update_compass")).Required(),
		schema.Property("content", map[string]any{
			"description": "内容。premise 传 Markdown 字符串；其他类型直接传 JSON 数组或对象即可，也兼容传 JSON 字符串。expand_arc 时传章节数组。",
		}).Required(),
		schema.Property("scale", schema.Enum("规划级别", "short", "mid", "long")),
		schema.Property("volume", schema.Int("目标卷序号（仅 expand_arc 时必传）")),
		schema.Property("arc", schema.Int("目标弧序号（仅 expand_arc 时必传）")),
	)
}

func (t *SaveFoundationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Scale   string          `json:"scale"`
		Volume  int             `json:"volume"`
		Arc     int             `json:"arc"`
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
		if err := t.store.RunMeta.SetPlanningTier(domain.PlanningTier(a.Scale)); err != nil {
			return nil, fmt.Errorf("save planning tier: %w", err)
		}
	}

	result := map[string]any{"saved": true, "type": a.Type, "scale": a.Scale}

	// 写作阶段禁止全量覆盖大纲，只允许增量操作（expand_arc / append_volume）
	if (a.Type == "outline" || a.Type == "layered_outline") && t.isWriting() {
		return nil, fmt.Errorf(
			"写作阶段禁止使用 %s 全量覆盖大纲。请使用 expand_arc 展开骨架弧，或 append_volume 追加新卷", a.Type)
	}

	switch a.Type {
	case "premise":
		name := domain.ExtractNovelNameFromPremise(content)
		if err := t.store.Outline.SavePremise(content); err != nil {
			return nil, fmt.Errorf("save premise: %w", err)
		}
		if name != "" {
			_ = t.store.Progress.SetNovelName(name)
			result["novel_name"] = name
		}
		_ = t.store.Progress.UpdatePhase(domain.PhasePremise)

	case "outline":
		var entries []domain.OutlineEntry
		if err := json.Unmarshal([]byte(content), &entries); err != nil {
			return nil, fmt.Errorf("parse outline JSON: %w", err)
		}
		if err := t.store.Outline.SaveOutline(entries); err != nil {
			return nil, fmt.Errorf("save outline: %w", err)
		}
		_ = t.store.Progress.UpdatePhase(domain.PhaseOutline)
		_ = t.store.Progress.SetTotalChapters(len(entries))
		if domain.PlanningTier(a.Scale) != domain.PlanningTierLong {
			_ = t.store.Progress.SetLayered(false)
			_ = t.store.Progress.UpdateVolumeArc(0, 0)
			_ = t.store.Outline.ClearLayeredOutline()
		}
		result["chapters"] = len(entries)

	case "layered_outline":
		var volumes []domain.VolumeOutline
		if err := json.Unmarshal([]byte(content), &volumes); err != nil {
			return nil, fmt.Errorf("parse layered_outline JSON: %w", err)
		}
		if err := t.store.Outline.SaveLayeredOutline(volumes); err != nil {
			return nil, fmt.Errorf("save layered_outline: %w", err)
		}
		flat := domain.FlattenOutline(volumes)
		if err := t.store.Outline.SaveOutline(flat); err != nil {
			return nil, fmt.Errorf("save flattened outline: %w", err)
		}
		total := domain.TotalChapters(volumes)
		_ = t.store.Progress.UpdatePhase(domain.PhaseOutline)
		_ = t.store.Progress.SetTotalChapters(total)
		_ = t.store.Progress.SetLayered(true)
		if len(volumes) > 0 && len(volumes[0].Arcs) > 0 {
			_ = t.store.Progress.UpdateVolumeArc(volumes[0].Index, volumes[0].Arcs[0].Index)
		}
		result["volumes"] = len(volumes)
		result["chapters"] = total

	case "characters":
		var chars []domain.Character
		if err := json.Unmarshal([]byte(content), &chars); err != nil {
			return nil, fmt.Errorf("parse characters JSON: %w", err)
		}
		if err := t.store.Characters.Save(chars); err != nil {
			return nil, fmt.Errorf("save characters: %w", err)
		}
		result["count"] = len(chars)

	case "world_rules":
		var rules []domain.WorldRule
		if err := json.Unmarshal([]byte(content), &rules); err != nil {
			return nil, fmt.Errorf("parse world_rules JSON: %w", err)
		}
		if err := t.store.World.SaveWorldRules(rules); err != nil {
			return nil, fmt.Errorf("save world_rules: %w", err)
		}
		result["count"] = len(rules)

	case "expand_arc":
		if a.Volume <= 0 || a.Arc <= 0 {
			return nil, fmt.Errorf("expand_arc requires volume and arc parameters")
		}
		var chapters []domain.OutlineEntry
		if err := json.Unmarshal([]byte(content), &chapters); err != nil {
			return nil, fmt.Errorf("parse expand_arc chapters JSON: %w", err)
		}
		if err := t.store.ExpandArc(a.Volume, a.Arc, chapters); err != nil {
			return nil, fmt.Errorf("expand arc: %w", err)
		}
		result["volume"] = a.Volume
		result["arc"] = a.Arc
		result["chapters"] = len(chapters)

	case "append_volume":
		var vol domain.VolumeOutline
		if err := json.Unmarshal([]byte(content), &vol); err != nil {
			return nil, fmt.Errorf("parse append_volume JSON: %w", err)
		}
		if err := t.store.AppendVolume(vol); err != nil {
			return nil, fmt.Errorf("append volume: %w", err)
		}
		result["volume"] = vol.Index
		result["arcs"] = len(vol.Arcs)
		chCount := 0
		for _, arc := range vol.Arcs {
			chCount += len(arc.Chapters)
		}
		if chCount > 0 {
			result["chapters"] = chCount
		}

	case "update_compass":
		var compass domain.StoryCompass
		if err := json.Unmarshal([]byte(content), &compass); err != nil {
			return nil, fmt.Errorf("parse compass JSON: %w", err)
		}
		if err := t.store.Outline.SaveCompass(compass); err != nil {
			return nil, fmt.Errorf("save compass: %w", err)
		}
		result["ending_direction"] = compass.EndingDirection

	default:
		return nil, fmt.Errorf("unknown type %q, expected premise/outline/layered_outline/characters/world_rules/expand_arc/append_volume/update_compass", a.Type)
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

func (t *SaveFoundationTool) isWriting() bool {
	p, _ := t.store.Progress.Load()
	return p != nil && p.Phase == domain.PhaseWriting
}

// remaining 检查基础设定中还缺少哪些必要项。
func (t *SaveFoundationTool) remaining() []string {
	var missing []string
	if p, _ := t.store.Outline.LoadPremise(); p == "" {
		missing = append(missing, "premise")
	}
	if o, _ := t.store.Outline.LoadOutline(); len(o) == 0 {
		missing = append(missing, "outline")
	}
	// 长篇模式下 compass 也是必须项
	if layered, _ := t.store.Outline.LoadLayeredOutline(); len(layered) > 0 {
		if c, _ := t.store.Outline.LoadCompass(); c == nil {
			missing = append(missing, "compass")
		}
	}
	if c, _ := t.store.Characters.Load(); len(c) == 0 {
		missing = append(missing, "characters")
	}
	if r, _ := t.store.World.LoadWorldRules(); len(r) == 0 {
		missing = append(missing, "world_rules")
	}
	return missing
}
