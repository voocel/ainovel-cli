package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveVolumeSummaryTool 保存卷级摘要，Editor 在卷结束时调用。
type SaveVolumeSummaryTool struct {
	store *store.Store
}

func NewSaveVolumeSummaryTool(store *store.Store) *SaveVolumeSummaryTool {
	return &SaveVolumeSummaryTool{store: store}
}

func (t *SaveVolumeSummaryTool) Name() string { return "save_volume_summary" }
func (t *SaveVolumeSummaryTool) Description() string {
	return "保存卷级摘要（长篇模式，卷结束时调用）"
}
func (t *SaveVolumeSummaryTool) Label() string { return "保存卷摘要" }

func (t *SaveVolumeSummaryTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("volume", schema.Int("卷号")).Required(),
		schema.Property("title", schema.String("卷标题")).Required(),
		schema.Property("summary", schema.String("卷摘要（500字以内）")).Required(),
		schema.Property("key_events", schema.Array("卷内关键事件", schema.String(""))).Required(),
	)
}

func (t *SaveVolumeSummaryTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Volume    int      `json:"volume"`
		Title     string   `json:"title"`
		Summary   string   `json:"summary"`
		KeyEvents []string `json:"key_events"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Volume <= 0 {
		return nil, fmt.Errorf("volume must be > 0")
	}

	volSummary := domain.VolumeSummary{
		Volume:    a.Volume,
		Title:     a.Title,
		Summary:   a.Summary,
		KeyEvents: a.KeyEvents,
	}
	if err := t.store.Summaries.SaveVolumeSummary(volSummary); err != nil {
		return nil, fmt.Errorf("save volume summary: %w", err)
	}

	return json.Marshal(map[string]any{
		"saved": true, "type": "volume_summary", "volume": a.Volume,
	})
}
