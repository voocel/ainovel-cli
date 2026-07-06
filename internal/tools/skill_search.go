package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/skills"
)

// SkillSearchTool 让 architect/coordinator 在本地跨书 skill 库（~/.ainovel/skills/）
// 中按关键词检索可复用经验（题材套路、结构模板、风格预设、流派常识等）。
//
// 与 web_search 的关系：本工具是"本地优先"层——零成本、零延迟、可复用。
// 命中后再用 skill_read 读全文；未命中（count=0）再走 web_search 联网搜索。
type SkillSearchTool struct {
	store *skills.Store
}

func NewSkillSearchTool(store *skills.Store) *SkillSearchTool {
	return &SkillSearchTool{store: store}
}

func (t *SkillSearchTool) Name() string  { return "search_skills" }
func (t *SkillSearchTool) Label() string { return "检索本地 skill 库" }

func (t *SkillSearchTool) Description() string {
	return "在本地跨书 skill 库（~/.ainovel/skills/）中按关键词检索可复用经验——" +
		"题材套路、结构模板、风格预设、流派常识、创作流程清单等。" +
		"返回 top-N 元数据（name/description/category/tags/priority），命中后再调 read_skill 读全文。" +
		"零联网成本、零延迟；未命中（count=0）时再走 web_search。" +
		"典型场景：用户提到某流派（赛博朋克、武侠、克苏鲁...）或结构（三幕剧、双线叙事...）时先调本工具查有没有相关 skill。"
}

// 纯读，并发安全。
func (t *SkillSearchTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *SkillSearchTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *SkillSearchTool) ActivityDescription(_ json.RawMessage) string {
	return "检索本地 skill 库"
}

func (t *SkillSearchTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("query", schema.String("检索关键词，流派名/结构名/风格特征等（中英文均可）")).Required(),
		schema.Property("category", schema.String("可选：限定分类（如 genres/structures/styles/tropes/processes）")),
		schema.Property("limit", schema.Int("返回上限，默认 5，最大 20")),
	)
}

func (t *SkillSearchTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Query    string `json:"query"`
		Category string `json:"category"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	q := strings.TrimSpace(a.Query)
	if q == "" {
		return nil, fmt.Errorf("query 不能为空: %w", errs.ErrToolArgs)
	}
	if t.store == nil {
		// store 未配置（如 ~/.ainovel 路径解析失败）→ 返回空结果而非错误，
		// 让 LLM 知道本工具不可用、走 web_search fallback。
		return json.Marshal(map[string]any{
			"query":   q,
			"results": []any{},
			"count":   0,
			"hint":    "本地 skill 库未启用（路径解析失败或禁用）。请走 web_search 联网搜索。",
		})
	}
	if a.Limit <= 0 {
		a.Limit = 5
	}
	if a.Limit > 20 {
		a.Limit = 20
	}

	metas := t.store.Search(q, a.Limit)
	results := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		if a.Category != "" && m.Category != a.Category {
			continue
		}
		results = append(results, map[string]any{
			"name":        m.Name,
			"description": m.Description,
			"category":    m.Category,
			"tags":        m.Tags,
			"triggers":    m.Triggers,
			"priority":    m.Priority,
		})
	}

	out := map[string]any{
		"query":   q,
		"results": results,
		"count":   len(results),
	}
	if len(results) == 0 {
		out["hint"] = "本地 skill 库未命中。可改走 web_search 联网搜索，或基于已有知识工作。"
	}
	return json.Marshal(out)
}
