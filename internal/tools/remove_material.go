package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// RemoveMaterialTool 按 ID 删除单条素材。
//
// 用途：用户/LLM 发现某条素材不准确或重复时清理。批量删除请逐条调用。
// 不提供 delete_all：避免误操作清库（真要重置可直接删 meta/materials.json）。
type RemoveMaterialTool struct {
	store *store.Store
}

func NewRemoveMaterialTool(s *store.Store) *RemoveMaterialTool {
	return &RemoveMaterialTool{store: s}
}

func (t *RemoveMaterialTool) Name() string  { return "remove_material" }
func (t *RemoveMaterialTool) Label() string { return "删除素材" }

func (t *RemoveMaterialTool) Description() string {
	return "按 ID 删除单条素材（mat-001/...）。不存在的 ID 返回 error。批量删除请逐条调用。"
}

func (t *RemoveMaterialTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *RemoveMaterialTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *RemoveMaterialTool) ActivityDescription(_ json.RawMessage) string {
	return "删除素材"
}

func (t *RemoveMaterialTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("id", schema.String("要删除的素材 ID（mat-001/...）")).Required(),
	)
}

func (t *RemoveMaterialTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.ID == "" {
		return nil, fmt.Errorf("id is required: %w", errs.ErrToolArgs)
	}
	if err := t.store.Materials.Remove(a.ID); err != nil {
		return nil, fmt.Errorf("remove material: %w", err)
	}
	return json.Marshal(map[string]any{"removed": a.ID})
}
