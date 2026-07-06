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

// SkillReadTool 加载某 skill 的完整 Markdown 内容（含 frontmatter）。
// 配合 search_skills：先检索拿到 name，再读全文获取详细套路/清单。
type SkillReadTool struct {
	store *skills.Store
}

func NewSkillReadTool(store *skills.Store) *SkillReadTool {
	return &SkillReadTool{store: store}
}

func (t *SkillReadTool) Name() string  { return "read_skill" }
func (t *SkillReadTool) Label() string { return "读取 skill 全文" }

func (t *SkillReadTool) Description() string {
	return "读取本地 skill 库中某 skill 的完整 Markdown 内容（含 frontmatter 元数据）。" +
		"先用 search_skills 检索拿到 name，再调本工具读全文。" +
		"返回 {name, content, length}；name 不存在时返回错误。"
}

func (t *SkillReadTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *SkillReadTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *SkillReadTool) ActivityDescription(_ json.RawMessage) string {
	return "读取 skill 全文"
}

func (t *SkillReadTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("name", schema.String("skill 名称（来自 search_skills 返回的 name 字段）")).Required(),
	)
}

func (t *SkillReadTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return nil, fmt.Errorf("name 不能为空: %w", errs.ErrToolArgs)
	}
	if t.store == nil {
		return nil, fmt.Errorf("本地 skill 库未启用")
	}
	content, err := t.store.Read(name)
	if err != nil {
		return nil, fmt.Errorf("read skill %s: %w", name, err)
	}
	return json.Marshal(map[string]any{
		"name":    name,
		"content": content,
		"length":  len([]rune(content)),
	})
}
