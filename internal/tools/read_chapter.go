package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ReadChapterTool 读取章节原文，让 Agent 能回读自己和前文的文字。
type ReadChapterTool struct {
	store *store.Store
}

func NewReadChapterTool(store *store.Store) *ReadChapterTool {
	return &ReadChapterTool{store: store}
}

func (t *ReadChapterTool) Name() string { return "read_chapter" }
func (t *ReadChapterTool) Description() string {
	return "读取章节原文。可读终稿、草稿，或提取角色对话片段"
}
func (t *ReadChapterTool) Label() string { return "读取章节" }

func (t *ReadChapterTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号（读单章时必填）")),
		schema.Property("from", schema.Int("起始章节号（读范围时使用）")),
		schema.Property("to", schema.Int("结束章节号（读范围时使用）")),
		schema.Property("source", schema.Enum("来源", "final", "draft")).Required(),
		schema.Property("character", schema.String("角色名（提取对话片段时使用）")),
		schema.Property("max_runes", schema.Int("每章最大字符数（范围读取时截取，默认 2000）")),
	)
}

func (t *ReadChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter   int    `json:"chapter"`
		From      int    `json:"from"`
		To        int    `json:"to"`
		Source    string `json:"source"`
		Character string `json:"character"`
		MaxRunes  int    `json:"max_runes"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	// 模式 1：提取角色对话
	if a.Character != "" {
		chars, _ := t.store.LoadCharacters()
		var aliases []string
		for _, c := range chars {
			if c.Name == a.Character {
				aliases = c.Aliases
				break
			}
		}
		samples := t.store.ExtractDialogue(a.Character, aliases, 8)
		return json.Marshal(map[string]any{
			"character": a.Character,
			"samples":   samples,
		})
	}

	// 模式 2：范围读取
	if a.From > 0 && a.To > 0 {
		maxRunes := a.MaxRunes
		if maxRunes <= 0 {
			maxRunes = 2000
		}
		texts, err := t.store.LoadChapterRange(a.From, a.To, maxRunes)
		if err != nil {
			return nil, fmt.Errorf("load chapter range: %w", err)
		}
		return json.Marshal(map[string]any{
			"chapters": texts,
			"from":     a.From,
			"to":       a.To,
		})
	}

	// 模式 3：单章读取
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter is required")
	}

	var content string
	var err error
	switch a.Source {
	case "draft":
		content, err = t.store.LoadDraft(a.Chapter)
	default: // final
		content, err = t.store.LoadChapterText(a.Chapter)
		if err == nil && content == "" {
			// 回退到草稿
			content, err = t.store.LoadDraft(a.Chapter)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read chapter %d: %w", a.Chapter, err)
	}
	if content == "" {
		return json.Marshal(map[string]any{
			"chapter": a.Chapter,
			"content": "",
			"note":    "章节不存在",
		})
	}

	return json.Marshal(map[string]any{
		"chapter":    a.Chapter,
		"content":    content,
		"word_count": len([]rune(content)),
	})
}
