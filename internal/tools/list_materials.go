package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ListMaterialsTool 列出本项目素材库（meta/materials.json）。
//
// 默认仅返回概览（id/title/category/source/added_at），不返回 content——
// 让 LLM 在拿全表后选择性读全文（避免把整库灌进上下文）。
//
// 适用场景：
//   - architect 规划前快速盘点已有素材避免重复搜集
//   - coordinator 完结时核对素材沉淀情况
//   - writer/editor 在写作过程中按需检索（一般走 novel_context.materials 即可，不必直接调本工具）
type ListMaterialsTool struct {
	store *store.Store
}

func NewListMaterialsTool(s *store.Store) *ListMaterialsTool {
	return &ListMaterialsTool{store: s}
}

func (t *ListMaterialsTool) Name() string  { return "list_materials" }
func (t *ListMaterialsTool) Label() string { return "列出素材" }

func (t *ListMaterialsTool) Description() string {
	return "列出项目素材库（meta/materials.json）。默认仅返回概览（不含 content），" +
		"include_content=true 时返回完整内容。可按 category 过滤。空库返回 {items:[], count:0}。"
}

func (t *ListMaterialsTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *ListMaterialsTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *ListMaterialsTool) ActivityDescription(_ json.RawMessage) string {
	return "列出素材"
}

func (t *ListMaterialsTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("category", schema.String("可选：仅返回该分类（naming/terminology/visual/setting/reference 或自定义）")),
		schema.Property("include_content", schema.Bool("是否返回 content 字段。默认 false（仅概览），true 返回完整内容")),
	)
}

func (t *ListMaterialsTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Category        string `json:"category"`
		IncludeContent  bool   `json:"include_content"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
		}
	}
	cat := strings.TrimSpace(a.Category)

	lib, err := t.store.Materials.Load()
	if err != nil {
		return nil, fmt.Errorf("load materials: %w", err)
	}

	type summary struct {
		ID       string `json:"id"`
		Category string `json:"category"`
		Title    string `json:"title"`
		Source   string `json:"source,omitempty"`
		AddedAt  string `json:"added_at,omitempty"`
		Content  string `json:"content,omitempty"`
	}

	out := make([]summary, 0, len(lib.Items))
	for _, it := range lib.Items {
		if cat != "" && !strings.EqualFold(it.Category, cat) {
			continue
		}
		row := summary{
			ID:       it.ID,
			Category: it.Category,
			Title:    it.Title,
			Source:   it.Source,
		}
		if !it.AddedAt.IsZero() {
			row.AddedAt = it.AddedAt.Format("2006-01-02 15:04")
		}
		if a.IncludeContent {
			row.Content = it.Content
		}
		out = append(out, row)
	}

	return json.Marshal(map[string]any{
		"items":            out,
		"count":            len(out),
		"category_filter":  cat,
		"include_content":  a.IncludeContent,
	})
}
