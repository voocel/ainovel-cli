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

// SkillAddTool 把可复用经验沉淀进跨书 skill 库。仅 coordinator 持有，
// 在 book_complete 后的"完结学习"环节调用。
//
// 设计动机：让 LLM 主动把本书创作中形成的可复用经验（新题材套路、解决过的结构
// 难题、效果好的开篇模板等）落盘到 ~/.ainovel/skills/<category>/<name>.md，
// 后续项目可用 search_skills 检索到——形成跨书学习闭环。
//
// 限制：name 必须为 [a-z0-9-]+（避免文件系统/路径注入）；同名 skill 已存在时报错，
// 不会覆盖用户手改。
type SkillAddTool struct {
	store *skills.Store
}

func NewSkillAddTool(store *skills.Store) *SkillAddTool {
	return &SkillAddTool{store: store}
}

func (t *SkillAddTool) Name() string  { return "add_skill" }
func (t *SkillAddTool) Label() string { return "沉淀 skill 到跨书库" }

func (t *SkillAddTool) Description() string {
	return "把本书创作中形成的可复用经验沉淀为 skill 文件，写入 ~/.ainovel/skills/<category>/<name>.md，跨书共享。" +
		"仅在全书完结后（book_complete=true）调用一次，且只提炼真正可复用的经验——" +
		"新题材套路、结构解决方案、有效的开篇/伏笔模板等。不要把本书独有设定当通用套路。" +
		"name 用 kebab-case 英文（如 cyberpunk-noir-checklist），description 一句话写用途，" +
		"body 是 Markdown 正文，建议用 when/do 结构。" +
		"不强制每次都加；没有可提炼的就跳过本工具。"
}

// 写工具，禁止并发。
func (t *SkillAddTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SkillAddTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SkillAddTool) ActivityDescription(_ json.RawMessage) string {
	return "沉淀 skill 到跨书库"
}

func (t *SkillAddTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("name", schema.String("skill 唯一名，kebab-case 英文（[a-z0-9-]+），如 cyberpunk-noir-checklist")).Required(),
		schema.Property("description", schema.String("一句话用途说明，方便后续检索；中英文均可")).Required(),
		schema.Property("category", schema.String("分类，建议从 genres/structures/styles/tropes/processes 中选；缺省 misc")).Required(),
		schema.Property("body", schema.String("skill 正文，Markdown 格式；建议用 ## when / ## do / ## checklist 等小节")).Required(),
		schema.Property("tags", schema.Array("标签数组（精确匹配加分）", schema.String("标签，建议英文短词"))),
		schema.Property("triggers", schema.Array("触发词数组（用户输入命中时优先级提升）", schema.String("触发词，建议中英文都列"))),
		schema.Property("priority", schema.Int("优先级 0-100，默认 50；高频 skill 可调高")),
	)
}

func (t *SkillAddTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Category    string   `json:"category"`
		Body        string   `json:"body"`
		Tags        []string `json:"tags"`
		Triggers    []string `json:"triggers"`
		Priority    int      `json:"priority"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return nil, fmt.Errorf("name 不能为空: %w", errs.ErrToolArgs)
	}
	if strings.TrimSpace(a.Description) == "" {
		return nil, fmt.Errorf("description 不能为空: %w", errs.ErrToolArgs)
	}
	if strings.TrimSpace(a.Body) == "" {
		return nil, fmt.Errorf("body 不能为空: %w", errs.ErrToolArgs)
	}
	if t.store == nil {
		return nil, fmt.Errorf("本地 skill 库未启用")
	}
	if a.Priority == 0 {
		a.Priority = 50
	}

	meta := skills.SkillMeta{
		Name:        name,
		Description: strings.TrimSpace(a.Description),
		Category:    strings.TrimSpace(a.Category),
		Tags:        a.Tags,
		Triggers:    a.Triggers,
		Priority:    a.Priority,
	}
	if err := t.store.Add(meta, a.Body); err != nil {
		return nil, fmt.Errorf("add skill: %w", err)
	}

	// 读回完整路径返回给 LLM
	path := t.store.Root() + "/" + meta.Category + "/" + meta.Name + ".md"
	return json.Marshal(map[string]any{
		"saved":    true,
		"name":     meta.Name,
		"category": meta.Category,
		"path":     path,
		"hint":     "skill 已落盘到跨书库。后续项目调 search_skills 即可命中。",
	})
}
