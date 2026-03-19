package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveReviewTool 保存 Editor 的审阅结果。
type SaveReviewTool struct {
	store *store.Store
}

func NewSaveReviewTool(store *store.Store) *SaveReviewTool {
	return &SaveReviewTool{store: store}
}

func (t *SaveReviewTool) Name() string { return "save_review" }
func (t *SaveReviewTool) Description() string {
	return "保存审阅结果。verdict 必须是 accept/polish/rewrite 之一"
}
func (t *SaveReviewTool) Label() string { return "保存审阅" }

func (t *SaveReviewTool) Schema() map[string]any {
	issueSchema := schema.Object(
		schema.Property("type", schema.Enum("问题维度", "consistency", "character", "pacing", "continuity", "foreshadow", "hook")).Required(),
		schema.Property("severity", schema.Enum("严重程度", "critical", "error", "warning")).Required(),
		schema.Property("description", schema.String("问题描述")).Required(),
		schema.Property("suggestion", schema.String("修改建议")),
	)
	dimensionSchema := schema.Object(
		schema.Property("dimension", schema.Enum("维度", "consistency", "character", "pacing", "continuity", "foreshadow", "hook")).Required(),
		schema.Property("score", schema.Int("评分（0-100）")).Required(),
		schema.Property("verdict", schema.Enum("维度结论", "pass", "warning", "fail")).Required(),
		schema.Property("comment", schema.String("该维度的简要结论")),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("审阅的章节号（全局审阅填最新章节号）")).Required(),
		schema.Property("scope", schema.Enum("审阅范围", "chapter", "global", "arc")).Required(),
		schema.Property("dimensions", schema.Array("分维度评分（六个维度各一条）", dimensionSchema)).Required(),
		schema.Property("issues", schema.Array("发现的问题", issueSchema)).Required(),
		schema.Property("verdict", schema.Enum("审阅结论", "accept", "polish", "rewrite")).Required(),
		schema.Property("summary", schema.String("审阅总结")).Required(),
		schema.Property("affected_chapters", schema.Array("需要重写或打磨的章节号列表（verdict 为 polish/rewrite 时必填）", schema.Int(""))),
	)
}

func (t *SaveReviewTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var r domain.ReviewEntry
	if err := json.Unmarshal(args, &r); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if r.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}

	if err := t.store.SaveReview(r); err != nil {
		return nil, fmt.Errorf("save review: %w", err)
	}

	// 写入信号文件供宿主读取
	if err := t.store.SaveLastReview(r); err != nil {
		return nil, fmt.Errorf("save review signal: %w", err)
	}

	return json.Marshal(map[string]any{
		"saved":   true,
		"chapter": r.Chapter,
		"scope":   r.Scope,
		"verdict": r.Verdict,
		"issues":  len(r.Issues),
	})
}
