package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveArcSummaryTool 保存弧级摘要、角色快照和写作规则，Editor 在弧结束时调用。
type SaveArcSummaryTool struct {
	store *store.Store
}

func NewSaveArcSummaryTool(store *store.Store) *SaveArcSummaryTool {
	return &SaveArcSummaryTool{store: store}
}

func (t *SaveArcSummaryTool) Name() string { return "save_arc_summary" }
func (t *SaveArcSummaryTool) Description() string {
	return "保存弧级摘要、角色状态快照和写作规则（长篇模式，弧结束时调用）"
}
func (t *SaveArcSummaryTool) Label() string { return "保存弧摘要" }

// 写工具，禁止并发。
func (t *SaveArcSummaryTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveArcSummaryTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveArcSummaryTool) Schema() map[string]any {
	snapshotSchema := schema.Object(
		schema.Property("name", schema.String("角色名")).Required(),
		schema.Property("status", schema.String("当前状态（存活/受伤/失踪等）")).Required(),
		schema.Property("power", schema.String("能力变化")),
		schema.Property("motivation", schema.String("当前动机")).Required(),
		schema.Property("relations", schema.String("关键关系变化")),
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
		schema.Property("title", schema.String("弧标题")).Required(),
		schema.Property("summary", schema.String("弧摘要（500字以内）")).Required(),
		schema.Property("key_events", schema.Array("弧内关键事件", schema.String(""))).Required(),
		schema.Property("character_snapshots", schema.Array("角色状态快照", snapshotSchema)).Required(),
		schema.Property("style_rules", styleRulesSchema).Required(),
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
	if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Summary) == "" {
		return nil, fmt.Errorf("title and summary are required: %w", errs.ErrToolArgs)
	}
	if err := validateArcSummaryStyleRules(a.StyleRules); err != nil {
		return nil, err
	}
	for i := range a.CharacterSnapshots {
		a.CharacterSnapshots[i].Volume = a.Volume
		a.CharacterSnapshots[i].Arc = a.Arc
	}
	arcSummary := domain.ArcSummary{
		Volume: a.Volume, Arc: a.Arc, Title: a.Title, Summary: a.Summary, KeyEvents: a.KeyEvents,
	}
	rules := domain.WritingStyleRules{
		Volume:    a.Volume,
		Arc:       a.Arc,
		Prose:     a.StyleRules.Prose,
		Dialogue:  a.StyleRules.Dialogue,
		Taboos:    a.StyleRules.Taboos,
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	replay, err := t.arcSummaryReplay(arcSummary, a.CharacterSnapshots, &rules)
	if err != nil {
		return nil, err
	}
	if !replay {
		if err := requireAggregateTarget(t.store, flow.AggregateArcSummary, a.Volume, a.Arc, 0); err != nil {
			return nil, err
		}
		if len(a.CharacterSnapshots) > 0 {
			if err := t.store.Characters.SaveSnapshots(a.Volume, a.Arc, a.CharacterSnapshots); err != nil {
				return nil, fmt.Errorf("save character snapshots: %w: %w", errs.ErrStoreWrite, err)
			}
		}
	}
	// 重放沿用已保存规则的时间戳；旧数据也能补齐本弧规则工件，完成 checkpoint。
	if err := t.store.World.SaveArcStyleRules(rules); err != nil {
		return nil, fmt.Errorf("save style rules: %w: %w", errs.ErrStoreWrite, err)
	}

	if !replay {
		// 弧摘要是 Router 的完成标记，作为最后一个语义工件写入。此前任一步
		// 失败时摘要保持缺失，恢复后 Router 仍会重派本任务。
		if err := t.store.Summaries.SaveArcSummary(arcSummary); err != nil {
			return nil, fmt.Errorf("save arc summary: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	artifacts := []string{fmt.Sprintf("summaries/arc-v%02da%02d.json", a.Volume, a.Arc)}
	if len(a.CharacterSnapshots) > 0 {
		artifacts = append(artifacts, fmt.Sprintf("meta/snapshots/v%02da%02d.json", a.Volume, a.Arc))
	}
	artifacts = append(artifacts, store.ArcStyleRulesPath(a.Volume, a.Arc))

	if _, err := t.store.Checkpoints.AppendArtifacts(
		domain.ArcScope(a.Volume, a.Arc), "arc_summary", artifacts...,
	); err != nil {
		return nil, fmt.Errorf("checkpoint arc summary: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(map[string]any{
		"saved": true, "type": "arc_summary",
		"volume": a.Volume, "arc": a.Arc,
		"snapshots":         len(a.CharacterSnapshots),
		"style_rules_saved": true,
	})
}

// arcSummaryReplay 只放行内容完全相同的幂等收尾，用于语义工件已落盘但
// checkpoint 追加失败的重试。任何差异都显式冲突，不能借重试覆盖历史聚合事实。
func (t *SaveArcSummaryTool) arcSummaryReplay(
	summary domain.ArcSummary,
	snapshots []domain.CharacterSnapshot,
	rules *domain.WritingStyleRules,
) (bool, error) {
	existing, err := t.store.Summaries.LoadArcSummary(summary.Volume, summary.Arc)
	if err != nil {
		return false, fmt.Errorf("load arc summary: %w: %w", errs.ErrStoreRead, err)
	}
	if existing == nil {
		return false, nil
	}
	storedSnapshots, err := t.store.Characters.LoadSnapshots(summary.Volume, summary.Arc)
	if err != nil {
		return false, fmt.Errorf("load character snapshots: %w: %w", errs.ErrStoreRead, err)
	}
	storedRules, err := t.store.World.LoadArcStyleRules(summary.Volume, summary.Arc)
	if err != nil {
		return false, fmt.Errorf("load style rules: %w: %w", errs.ErrStoreRead, err)
	}
	if storedRules != nil {
		rules.UpdatedAt = storedRules.UpdatedAt
	}
	if !reflect.DeepEqual(*existing, summary) ||
		!slices.Equal(storedSnapshots, snapshots) ||
		storedRules == nil || !reflect.DeepEqual(*storedRules, *rules) {
		return false, fmt.Errorf("第 %d 卷第 %d 弧摘要已存在但关联工件不同，拒绝覆盖: %w", summary.Volume, summary.Arc, errs.ErrToolConflict)
	}
	return true, nil
}

type arcSummaryStyleRules struct {
	Prose    []string                `json:"prose"`
	Dialogue []domain.CharacterVoice `json:"dialogue"`
	Taboos   []string                `json:"taboos"`
}

func validateArcSummaryStyleRules(rules *arcSummaryStyleRules) error {
	if rules == nil {
		return fmt.Errorf("style_rules is required: %w", errs.ErrToolArgs)
	}
	if len(rules.Prose) == 0 {
		return fmt.Errorf("style_rules.prose is required: %w", errs.ErrToolArgs)
	}
	if len(rules.Dialogue) == 0 {
		return fmt.Errorf("style_rules.dialogue is required; expected array of objects {name, rules}: %w", errs.ErrToolArgs)
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
