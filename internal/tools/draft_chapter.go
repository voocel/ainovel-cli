package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/store"
)

// DraftChapterTool 写入整章草稿，替代旧的 write_scene + polish_chapter 流水线。
// Agent 自主决定一次写完还是分批续写。
type DraftChapterTool struct {
	store *store.Store
}

func NewDraftChapterTool(store *store.Store) *DraftChapterTool {
	return &DraftChapterTool{store: store}
}

func (t *DraftChapterTool) Name() string { return "draft_chapter" }
func (t *DraftChapterTool) Description() string {
	return "写入章节正文。mode=write 覆盖写入整章，mode=append 追加到现有草稿（续写/修改）"
}
func (t *DraftChapterTool) Label() string { return "写入章节" }

func (t *DraftChapterTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("content", schema.String("章节正文")).Required(),
		schema.Property("mode", schema.Enum("写入模式", "write", "append")),
	)
}

func (t *DraftChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int    `json:"chapter"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	if a.Content == "" {
		return nil, fmt.Errorf("content must not be empty")
	}

	switch a.Mode {
	case "append":
		if err := t.store.AppendDraft(a.Chapter, a.Content); err != nil {
			return nil, fmt.Errorf("append draft: %w", err)
		}
		// 读取合并后的完整内容计算字数
		full, err := t.store.LoadDraft(a.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load draft after append: %w", err)
		}
		return json.Marshal(map[string]any{
			"written":    true,
			"chapter":    a.Chapter,
			"mode":       "append",
			"word_count": utf8.RuneCountInString(full),
			"next_step":  "自审后调用 commit_chapter 提交",
		})
	default: // write
		if err := t.store.SaveDraft(a.Chapter, a.Content); err != nil {
			return nil, fmt.Errorf("save draft: %w", err)
		}
		return json.Marshal(map[string]any{
			"written":    true,
			"chapter":    a.Chapter,
			"mode":       "write",
			"word_count": utf8.RuneCountInString(a.Content),
			"next_step":  "自审后调用 commit_chapter 提交",
		})
	}
}
