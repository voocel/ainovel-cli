package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CheckConsistencyTool 返回章节内容和全部状态数据，供 Agent 自行对照判断。
// 纯 IO 工具：只负责加载数据，不注入指令。
type CheckConsistencyTool struct {
	store *store.Store
}

func NewCheckConsistencyTool(store *store.Store) *CheckConsistencyTool {
	return &CheckConsistencyTool{store: store}
}

func (t *CheckConsistencyTool) Name() string { return "check_consistency" }
func (t *CheckConsistencyTool) Description() string {
	return "加载已写草稿和对照数据（世界规则、伏笔、关系、别名、最近摘要），供你检查一致性。必须在 draft_chapter 之后调用"
}
func (t *CheckConsistencyTool) Label() string { return "一致性检查" }

func (t *CheckConsistencyTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("要检查的章节号")).Required(),
	)
}

func (t *CheckConsistencyTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int `json:"chapter"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}

	result := map[string]any{"chapter": a.Chapter}

	// 章节内容
	content, wordCount, err := t.store.Drafts.LoadChapterContent(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load chapter content: %w", err)
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d", a.Chapter)
	}
	result["content"] = content
	result["word_count"] = wordCount

	// 对照数据：保留全局性的一致性检查数据，避免重复加载 novel_context 已有的窗口数据
	if rules, _ := t.store.World.LoadWorldRules(); len(rules) > 0 {
		result["world_rules"] = rules
	}
	if foreshadow, _ := t.store.World.LoadActiveForeshadow(); len(foreshadow) > 0 {
		result["foreshadow_ledger"] = foreshadow
	}
	if relationships, _ := t.store.World.LoadRelationships(); len(relationships) > 0 {
		result["relationships"] = relationships
	}
	if chars, _ := t.store.Characters.Load(); len(chars) > 0 {
		aliasMap := make(map[string]string)
		for _, c := range chars {
			for _, alias := range c.Aliases {
				aliasMap[alias] = c.Name
			}
		}
		if len(aliasMap) > 0 {
			result["alias_map"] = aliasMap
		}
	}
	if summaries, _ := t.store.Summaries.LoadRecentSummaries(a.Chapter, 2); len(summaries) > 0 {
		result["recent_summaries"] = summaries
	}

	_, _ = t.store.Checkpoints.Append(
		domain.ChapterScope(a.Chapter), "consistency_check", "", "",
	)

	return json.Marshal(result)
}
