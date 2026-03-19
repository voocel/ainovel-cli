package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveArcSummaryTool 保存弧级摘要和角色快照，Editor 在弧结束时调用。
type SaveArcSummaryTool struct {
	store *store.Store
}

func NewSaveArcSummaryTool(store *store.Store) *SaveArcSummaryTool {
	return &SaveArcSummaryTool{store: store}
}

func (t *SaveArcSummaryTool) Name() string { return "save_arc_summary" }
func (t *SaveArcSummaryTool) Description() string {
	return "保存弧级摘要和角色状态快照（长篇模式，弧结束时调用）"
}
func (t *SaveArcSummaryTool) Label() string { return "保存弧摘要" }

func (t *SaveArcSummaryTool) Schema() map[string]any {
	snapshotSchema := schema.Object(
		schema.Property("name", schema.String("角色名")).Required(),
		schema.Property("status", schema.String("当前状态（存活/受伤/失踪等）")).Required(),
		schema.Property("power", schema.String("能力变化")),
		schema.Property("motivation", schema.String("当前动机")).Required(),
		schema.Property("relations", schema.String("关键关系变化")),
	)
	return schema.Object(
		schema.Property("volume", schema.Int("卷号")).Required(),
		schema.Property("arc", schema.Int("弧号")).Required(),
		schema.Property("title", schema.String("弧标题")).Required(),
		schema.Property("summary", schema.String("弧摘要（500字以内）")).Required(),
		schema.Property("key_events", schema.Array("弧内关键事件", schema.String(""))).Required(),
		schema.Property("character_snapshots", schema.Array("角色状态快照", snapshotSchema)).Required(),
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
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Volume <= 0 || a.Arc <= 0 {
		return nil, fmt.Errorf("volume and arc must be > 0")
	}

	arcSummary := domain.ArcSummary{
		Volume:    a.Volume,
		Arc:       a.Arc,
		Title:     a.Title,
		Summary:   a.Summary,
		KeyEvents: a.KeyEvents,
	}
	if err := t.store.SaveArcSummary(arcSummary); err != nil {
		return nil, fmt.Errorf("save arc summary: %w", err)
	}

	if len(a.CharacterSnapshots) > 0 {
		for i := range a.CharacterSnapshots {
			a.CharacterSnapshots[i].Volume = a.Volume
			a.CharacterSnapshots[i].Arc = a.Arc
		}
		if err := t.store.SaveCharacterSnapshots(a.Volume, a.Arc, a.CharacterSnapshots); err != nil {
			return nil, fmt.Errorf("save character snapshots: %w", err)
		}
	}

	return json.Marshal(map[string]any{
		"saved": true, "type": "arc_summary",
		"volume": a.Volume, "arc": a.Arc,
		"snapshots": len(a.CharacterSnapshots),
	})
}
