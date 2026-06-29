package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveArcSummaryTool Lưu弧级Tóm tắt和角色Chụp，Editor 在弧Kết thúc时调用。
type SaveArcSummaryTool struct {
	store *store.Store
}

func NewSaveArcSummaryTool(store *store.Store) *SaveArcSummaryTool {
	return &SaveArcSummaryTool{store: store}
}

func (t *SaveArcSummaryTool) Name() string { return "save_arc_summary" }
func (t *SaveArcSummaryTool) Description() string {
	return "Lưu弧级Tóm tắt和角色Trạng tháiChụp（长篇模式，弧Kết thúc时调用）"
}
func (t *SaveArcSummaryTool) Label() string { return "Lưu弧Tóm tắt" }

// 写工具，禁止并发。
func (t *SaveArcSummaryTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveArcSummaryTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveArcSummaryTool) Schema() map[string]any {
	snapshotSchema := schema.Object(
		schema.Property("name", schema.String("角色名")).Required(),
		schema.Property("status", schema.String("Hiện tạiTrạng thái（存活/受伤/失踪等）")).Required(),
		schema.Property("power", schema.String("能力变化")),
		schema.Property("motivation", schema.String("Hiện tại动机")).Required(),
		schema.Property("relations", schema.String("关键Thay đổi quan hệ")),
	)
	voiceSchema := schema.Object(
		schema.Property("name", schema.String("角色名")).Required(),
		schema.Property("rules", schema.Array("2-3 条语言特征规则（每条 ≤30 字）", schema.String(""))).Required(),
	)
	styleRulesSchema := schema.Object(
		schema.Property("prose", schema.Array("3-5 条叙述风格规则（每条 ≤50 字，要具体可执行）", schema.String(""))).Required(),
		schema.Property("dialogue", schema.Array("核心角色的对话特征规则", voiceSchema)).Required(),
		schema.Property("taboos", schema.Array("本小说需避免的写法", schema.String(""))),
	)
	return schema.Object(
		schema.Property("volume", schema.Int("卷号")).Required(),
		schema.Property("arc", schema.Int("弧号")).Required(),
		schema.Property("title", schema.String("弧Tiêu đề")).Required(),
		schema.Property("summary", schema.String("弧Tóm tắt（500字以内）")).Required(),
		schema.Property("key_events", schema.Array("弧内Sự kiện trọng tâm", schema.String(""))).Required(),
		schema.Property("character_snapshots", schema.Array("角色Trạng tháiChụp", snapshotSchema)).Required(),
		schema.Property("style_rules", styleRulesSchema),
	)
}

func (t *SaveArcSummaryTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Volume             int                        `json:"volume"`
		Arc                int                        `json:"arc"`
		Title              string                     `json:"title"`
		Summary            string                     `json:"summary"`
		KeyEvents          []string                   `json:"key_events"`
		CharacterSnapshots []domain.CharacterSnapshot `json:"character_snapshots"`
		StyleRules         *arcSummaryStyleRules      `json:"style_rules"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		if strings.Contains(err.Error(), "style_rules.dialogue") {
			return nil, fmt.Errorf("invalid args: style_rules.dialogue must be an array of objects {name, rules}, not strings: %w: %w", errs.ErrToolArgs, err)
		}
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Volume <= 0 || a.Arc <= 0 {
		return nil, fmt.Errorf("volume and arc must be > 0: %w", errs.ErrToolArgs)
	}
	if err := validateArcSummaryStyleRules(a.StyleRules); err != nil {
		return nil, err
	}

	arcSummary := domain.ArcSummary{
		Volume:    a.Volume,
		Arc:       a.Arc,
		Title:     a.Title,
		Summary:   a.Summary,
		KeyEvents: a.KeyEvents,
	}
	if err := t.store.Summaries.SaveArcSummary(arcSummary); err != nil {
		return nil, fmt.Errorf("save arc summary: %w: %w", errs.ErrStoreWrite, err)
	}

	if len(a.CharacterSnapshots) > 0 {
		for i := range a.CharacterSnapshots {
			a.CharacterSnapshots[i].Volume = a.Volume
			a.CharacterSnapshots[i].Arc = a.Arc
		}
		if err := t.store.Characters.SaveSnapshots(a.Volume, a.Arc, a.CharacterSnapshots); err != nil {
			return nil, fmt.Errorf("save character snapshots: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	styleRulesSaved := false
	if a.StyleRules != nil && len(a.StyleRules.Prose) > 0 {
		rules := domain.WritingStyleRules{
			Volume:    a.Volume,
			Arc:       a.Arc,
			Prose:     a.StyleRules.Prose,
			Dialogue:  a.StyleRules.Dialogue,
			Taboos:    a.StyleRules.Taboos,
			UpdatedAt: time.Now().Format(time.RFC3339),
		}
		if err := t.store.World.SaveStyleRules(rules); err != nil {
			return nil, fmt.Errorf("save style rules: %w: %w", errs.ErrStoreWrite, err)
		}
		styleRulesSaved = true
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ArcScope(a.Volume, a.Arc), "arc_summary",
		fmt.Sprintf("summaries/arc-v%02da%02d.json", a.Volume, a.Arc),
	); err != nil {
		return nil, fmt.Errorf("checkpoint arc summary: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(map[string]any{
		"saved": true, "type": "arc_summary",
		"volume": a.Volume, "arc": a.Arc,
		"snapshots":         len(a.CharacterSnapshots),
		"style_rules_saved": styleRulesSaved,
	})
}

type arcSummaryStyleRules struct {
	Prose    []string                `json:"prose"`
	Dialogue []domain.CharacterVoice `json:"dialogue"`
	Taboos   []string                `json:"taboos"`
}

func validateArcSummaryStyleRules(rules *arcSummaryStyleRules) error {
	if rules == nil {
		return nil
	}
	if len(rules.Prose) == 0 {
		return fmt.Errorf("style_rules.prose is required when style_rules is provided: %w", errs.ErrToolArgs)
	}
	if len(rules.Dialogue) == 0 {
		return fmt.Errorf("style_rules.dialogue is required when style_rules is provided; expected array of objects {name, rules}: %w", errs.ErrToolArgs)
	}
	for i, voice := range rules.Dialogue {
		if strings.TrimSpace(voice.Name) == "" {
			return fmt.Errorf("style_rules.dialogue[%d].name is required: %w", i, errs.ErrToolArgs)
		}
		if len(voice.Rules) == 0 {
			return fmt.Errorf("style_rules.dialogue[%d].rules is required: %w", i, errs.ErrToolArgs)
		}
		for j, rule := range voice.Rules {
			if strings.TrimSpace(rule) == "" {
				return fmt.Errorf("style_rules.dialogue[%d].rules[%d] is empty: %w", i, j, errs.ErrToolArgs)
			}
		}
	}
	return nil
}
