package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveMaterialsTool 把 architect 收集到的素材批量持久化到 meta/materials.json。
//
// 适用阶段：architect 在规划前 web_search / 调本地 skill / 自身知识搜集完素材后调用，
// 后续 novel_context.reference_pack.materials 会注入这些素材供所有子代理消费。
//
// 设计：批量接口而非单条 add——architect 一次产出的素材通常是 5-15 条命名/术语/设定的列表，
// 单条 add 工具会逼 LLM 多轮 tool_call，浪费 token。批量提交+错误整体回滚避免半落盘。
type SaveMaterialsTool struct {
	store *store.Store
}

func NewSaveMaterialsTool(s *store.Store) *SaveMaterialsTool {
	return &SaveMaterialsTool{store: s}
}

func (t *SaveMaterialsTool) Name() string  { return "save_materials" }
func (t *SaveMaterialsTool) Label() string { return "保存素材" }

func (t *SaveMaterialsTool) Description() string {
	return "批量保存项目级素材到 meta/materials.json。architect 在规划前搜集到的命名表、术语、" +
		"视觉锚点、设定资料、参考资料等都通过此工具持久化；后续 novel_context.reference_pack.materials " +
		"会注入这些素材供 writer/editor 消费。category 可选 naming/terminology/visual/setting/reference，" +
		"自定义值也接受。items 必须含 title 与 content，空值的条目会被跳过（不报错）；至少有一条有效条目才会落盘。"
}

// 写工具，禁止并发——保护 meta/materials.json 不被同时改写。
func (t *SaveMaterialsTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveMaterialsTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveMaterialsTool) ActivityDescription(_ json.RawMessage) string {
	return "保存素材"
}

func (t *SaveMaterialsTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("items", schema.Array(
			"素材列表（每条含 category/title/content/source）",
			schema.Object(
				schema.Property("category", schema.String("分类：naming/terminology/visual/setting/reference，可自定义")).Required(),
				schema.Property("title", schema.String("素材标题（一句话概括，便于检索展示")).Required(),
				schema.Property("content", schema.String("素材正文（Markdown），原样持久化")).Required(),
				schema.Property("source", schema.String("来源标记，便于追溯。建议格式：web_search:<query> / skill:<name> / builtin")),
			),
		)).Required(),
	)
}

func (t *SaveMaterialsTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Items []struct {
			Category string `json:"category"`
			Title    string `json:"title"`
			Content  string `json:"content"`
			Source   string `json:"source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}

	// 过滤掉 title/content 空的条目——LLM 偶尔会输出占位 {"title":""}，本小姐宁可跳过也不让报错污染整批。
	items := make([]domain.MaterialItem, 0, len(a.Items))
	skipped := 0
	for _, in := range a.Items {
		if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Content) == "" {
			skipped++
			continue
		}
		items = append(items, domain.MaterialItem{
			Category: in.Category,
			Title:    in.Title,
			Content:  in.Content,
			Source:   in.Source,
		})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no valid items (all %d had empty title or content): %w", len(a.Items), errs.ErrToolArgs)
	}

	saved, err := t.store.Materials.AddBatch(items)
	if err != nil {
		return nil, fmt.Errorf("save materials: %w", err)
	}

	// 返回每条的 ID/Category/Title，便于 LLM 在后续 remove_material 时引用。
	summaries := make([]map[string]string, 0, len(saved))
	for _, it := range saved {
		summaries = append(summaries, map[string]string{
			"id":       it.ID,
			"category": it.Category,
			"title":    it.Title,
		})
	}
	return json.Marshal(map[string]any{
		"saved":   len(saved),
		"skipped": skipped,
		"items":   summaries,
	})
}
