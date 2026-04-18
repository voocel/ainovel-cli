package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	return "保存审阅结果并更新流程状态。verdict 为 accept/polish/rewrite 之一。" +
		"工具内部执行评分卡门禁（可能升级 verdict），直接更新 Progress 的 flow 和 pending_rewrites。" +
		"返回 system_hints 指示下一步（重写/打磨/继续写作）"
}
func (t *SaveReviewTool) Label() string { return "保存审阅" }

func (t *SaveReviewTool) Schema() map[string]any {
	issueSchema := schema.Object(
		schema.Property("type", schema.Enum("问题维度", "consistency", "character", "pacing", "continuity", "foreshadow", "hook", "aesthetic")).Required(),
		schema.Property("severity", schema.Enum("严重程度", "critical", "error", "warning")).Required(),
		schema.Property("description", schema.String("问题描述")).Required(),
		schema.Property("evidence", schema.String("证据：原文片段、具体情节或状态数据")).Required(),
		schema.Property("suggestion", schema.String("修改建议")),
	)
	dimensionSchema := schema.Object(
		schema.Property("dimension", schema.Enum("维度", "consistency", "character", "pacing", "continuity", "foreshadow", "hook", "aesthetic")).Required(),
		schema.Property("score", schema.Int("评分（0-100）")).Required(),
		schema.Property("verdict", schema.Enum("维度结论", "pass", "warning", "fail")).Required(),
		schema.Property("comment", schema.String("该维度的简要结论")),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("审阅的章节号（全局审阅填最新章节号）")).Required(),
		schema.Property("scope", schema.Enum("审阅范围", "chapter", "global", "arc")).Required(),
		schema.Property("dimensions", schema.Array("分维度评分（七个维度各一条）", dimensionSchema)).Required(),
		schema.Property("issues", schema.Array("发现的问题", issueSchema)).Required(),
		schema.Property("contract_status", schema.Enum("章节契约完成度", "met", "partial", "missed")),
		schema.Property("contract_misses", schema.Array("未完成或违背的 contract 条目", schema.String(""))),
		schema.Property("contract_notes", schema.String("对 contract 履行情况的简要说明")),
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
	if err := validateReviewEntry(r); err != nil {
		return nil, err
	}

	if err := t.store.World.SaveReview(r); err != nil {
		return nil, fmt.Errorf("save review: %w", err)
	}

	// 评分卡门禁 — 内联原 policy/review.go 的升级逻辑
	finalVerdict := r.Verdict
	var escalationReason string

	if r.Verdict == "accept" {
		// 合同状态检查
		if r.ContractStatus == "missed" {
			finalVerdict = "rewrite"
			escalationReason = "合同履约状态为 missed，升级为重写"
		} else if r.ContractStatus == "partial" {
			finalVerdict = "polish"
			escalationReason = "合同履约状态为 partial，升级为打磨"
		}
		// 评分卡门禁
		if finalVerdict == "accept" {
			if gate := evaluateScorecardGate(r.Dimensions); gate != "" {
				if strings.Contains(gate, "rewrite") {
					finalVerdict = "rewrite"
				} else {
					finalVerdict = "polish"
				}
				escalationReason = gate
			}
		}
	}

	// 根据最终 verdict 更新 Progress
	var hints []string
	progress, _ := t.store.Progress.Load()
	if finalVerdict == "rewrite" || finalVerdict == "polish" {
		affected := r.AffectedChapters
		if len(affected) == 0 && r.Chapter > 0 {
			affected = []int{r.Chapter}
		}
		flow := domain.FlowRewriting
		verb := "重写"
		key := "rewrite_required"
		if finalVerdict == "polish" {
			flow = domain.FlowPolishing
			verb = "打磨"
			key = "polish_required"
		}
		_ = t.store.Progress.SetPendingRewrites(affected, r.Summary)
		_ = t.store.Progress.SetFlow(flow)

		hint := fmt.Sprintf("[系统] %s: 审阅结论为 %s，受影响章节 %v。", key, finalVerdict, affected)
		if escalationReason != "" {
			hint += fmt.Sprintf(" （升级原因：%s）", escalationReason)
		}
		hint += fmt.Sprintf(" 请立即逐章调 writer 执行%s；队列清空前，即使此前收到 expand_arc_required 或其他提示，也不要调 architect 展开新弧、不要调 writer 写新章节。", verb)
		hints = append(hints, hint)
	} else {
		_ = t.store.Progress.SetFlow(domain.FlowWriting)
		nextCh := 0
		if progress != nil {
			nextCh = progress.NextChapter()
		}
		hints = append(hints, fmt.Sprintf(
			"[系统] review_accepted: 审阅通过，继续写第 %d 章。", nextCh))
	}

	// 追加 checkpoint
	scope := domain.ChapterScope(r.Chapter)
	if r.Scope == "arc" {
		vol, arc := 0, 0
		if progress != nil {
			vol, arc = progress.CurrentVolume, progress.CurrentArc
		}
		scope = domain.ArcScope(vol, arc)
	}
	_, _ = t.store.Checkpoints.Append(scope, "review", "", "")

	return json.Marshal(map[string]any{
		"saved":        true,
		"chapter":      r.Chapter,
		"scope":        r.Scope,
		"verdict":      r.Verdict,
		"final_verdict": finalVerdict,
		"escalation":   escalationReason,
		"issues":       len(r.Issues),
		"system_hints": hints,
	})
}

var expectedReviewDimensions = map[string]struct{}{
	"consistency": {},
	"character":   {},
	"pacing":      {},
	"continuity":  {},
	"foreshadow":  {},
	"hook":        {},
	"aesthetic":   {},
}

func validateReviewEntry(r domain.ReviewEntry) error {
	if strings.TrimSpace(r.Scope) == "" {
		return fmt.Errorf("scope is required")
	}
	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	for _, issue := range r.Issues {
		if strings.TrimSpace(issue.Description) == "" {
			return fmt.Errorf("issue description is required")
		}
		if strings.TrimSpace(issue.Evidence) == "" {
			return fmt.Errorf("issue evidence is required")
		}
	}
	if err := validateDimensions(r.Dimensions); err != nil {
		return err
	}
	if (r.Verdict == "rewrite" || r.Verdict == "polish") && len(r.AffectedChapters) == 0 {
		return fmt.Errorf("affected_chapters is required when verdict=%s", r.Verdict)
	}
	return nil
}

func validateDimensions(dimensions []domain.DimensionScore) error {
	if len(dimensions) != len(expectedReviewDimensions) {
		return fmt.Errorf("dimensions must contain exactly %d entries", len(expectedReviewDimensions))
	}

	seen := make(map[string]struct{}, len(dimensions))
	for _, dim := range dimensions {
		if _, ok := expectedReviewDimensions[dim.Dimension]; !ok {
			return fmt.Errorf("unknown dimension: %s", dim.Dimension)
		}
		if _, ok := seen[dim.Dimension]; ok {
			return fmt.Errorf("duplicate dimension: %s", dim.Dimension)
		}
		seen[dim.Dimension] = struct{}{}
		if dim.Score < 0 || dim.Score > 100 {
			return fmt.Errorf("invalid score for %s: %d", dim.Dimension, dim.Score)
		}
		expectedVerdict := expectedDimensionVerdict(dim.Score)
		if dim.Verdict != expectedVerdict {
			return fmt.Errorf("dimension %s has inconsistent score/verdict: score=%d verdict=%s", dim.Dimension, dim.Score, dim.Verdict)
		}
		if dim.Dimension == "aesthetic" && strings.TrimSpace(dim.Comment) == "" {
			return fmt.Errorf("aesthetic comment is required")
		}
	}
	return nil
}

func expectedDimensionVerdict(score int) string {
	switch {
	case score >= 80:
		return "pass"
	case score >= 60:
		return "warning"
	default:
		return "fail"
	}
}

// criticalDimensions 定义会触发 verdict 升级的关键维度。
var criticalDimensions = map[string]struct{}{
	"consistency": {},
	"character":   {},
	"continuity":  {},
}

// evaluateScorecardGate 检查评分卡是否需要升级 verdict。
// 返回空字符串表示不升级。
func evaluateScorecardGate(dimensions []domain.DimensionScore) string {
	var criticalFails []string
	var polishIssues []string

	for _, dim := range dimensions {
		_, isCritical := criticalDimensions[dim.Dimension]
		if isCritical && (dim.Verdict == "fail" || dim.Score < 60) {
			criticalFails = append(criticalFails, fmt.Sprintf("%s(%d)", dim.Dimension, dim.Score))
		} else if dim.Verdict == "warning" || (isCritical && dim.Score < 80) {
			polishIssues = append(polishIssues, fmt.Sprintf("%s(%d)", dim.Dimension, dim.Score))
		}
	}

	if len(criticalFails) > 0 {
		return fmt.Sprintf("rewrite: 关键维度不合格 %v", criticalFails)
	}
	if len(polishIssues) > 0 {
		return fmt.Sprintf("polish: 部分维度需打磨 %v", polishIssues)
	}
	return ""
}
