package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
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
		"Editor 依据完整上下文作出 verdict，工具只校验事实并原子更新 Progress。" +
		"返回结构化事实：verdict / affected_chapters / next_flow / next_chapter"
}
func (t *SaveReviewTool) Label() string { return "保存审阅" }

// 写工具（同时更新 reviews/ 与 Progress 的 PendingRewrites/Flow），禁止并发。
func (t *SaveReviewTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveReviewTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *SaveReviewTool) StrictSchema() bool                     { return true }

func (t *SaveReviewTool) Schema() map[string]any {
	issueSchema := schema.Object(
		schema.Property("type", schema.String("问题维度；可使用评审提示中的基础维度，也可写更准确的具体维度")).Required(),
		schema.Property("severity", schema.Enum("严重程度", "critical", "error", "warning")).Required(),
		schema.Property("description", schema.String("问题描述")).Required(),
		schema.Property("evidence", schema.String("证据：原文片段、具体情节或状态数据")).Required(),
		schema.Property("suggestion", llmcontract.Nullable(schema.String("修改建议；无需建议时为 null"))).Required(),
		schema.Property("chapters", schema.Array("该问题证据实际所在的章节；弧评审必须落在任务给定区间", schema.Int("章节号"))).Required(),
		schema.Property("requires_change", schema.Bool("该问题是否应立即触发所列章节返工，由 Editor 结合整体阅读体验判断")).Required(),
	)
	dimensionSchema := schema.Object(
		schema.Property("dimension", schema.String("评价维度；由当前评审任务和 rubric 决定")).Required(),
		schema.Property("score", schema.Int("评分（0-100）")).Required(),
		schema.Property("comment", schema.String("该维度的简要结论和证据；每个维度必填")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("审阅的章节号（全局审阅填最新章节号）")).Required(),
		schema.Property("scope", schema.Enum("审阅范围", "chapter", "global", "arc")).Required(),
		schema.Property("dimensions", schema.Array("分维度评分；基础 rubric 由 Editor 提示提供，可按任务补充更具体维度", dimensionSchema)).Required(),
		schema.Property("issues", schema.Array("发现的问题", issueSchema)).Required(),
		schema.Property("contract_status", llmcontract.Nullable(schema.Enum("章节契约完成度；不适用时为 null", "met", "partial", "missed"))).Required(),
		schema.Property("contract_misses", schema.Array("未完成或违背的 contract 条目；无则为空数组", schema.String(""))).Required(),
		schema.Property("contract_notes", llmcontract.Nullable(schema.String("对 contract 履行情况的简要说明；无则为 null"))).Required(),
		schema.Property("verdict", schema.Enum("审阅结论", "accept", "polish", "rewrite")).Required(),
		schema.Property("summary", schema.String("审阅总结")).Required(),
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
	boundary, err := t.normalizeReviewEntry(&r)
	if err != nil {
		return nil, err
	}
	reviewOutcome, err := reviewFlow(r.Verdict)
	if err != nil {
		return nil, err
	}

	affected := r.AffectedChapters

	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil || !slices.Contains(progress.CompletedChapters, r.Chapter) {
		return nil, fmt.Errorf("review chapter %d must be completed", r.Chapter)
	}
	scope := domain.ChapterScope(r.Chapter)
	artifact := fmt.Sprintf("reviews/%02d.json", r.Chapter)
	var existing *domain.ReviewEntry
	switch r.Scope {
	case "chapter":
		// 只识别已有返工评审的重放；写入目标尚不是文件时，仍须先持久化
		// 新的返工意图，再由 SaveReview 显式报告工件写入失败。
		reviews, err := t.store.World.LoadReviewsAffectingChapter(r.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load chapter review: %w", err)
		}
		for i := range reviews {
			if reviews[i].Chapter == r.Chapter && reviews[i].Scope == "chapter" {
				existing = &reviews[i]
				break
			}
		}
	case "arc":
		scope = domain.ArcScope(boundary.Volume, boundary.Arc)
		existing, err = t.store.World.LoadReview(r.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load arc review: %w", err)
		}
		if existing != nil && existing.Scope != "arc" {
			existing = nil
		}
	case "global":
		artifact = fmt.Sprintf("reviews/%02d-global.json", r.Chapter)
		existing, err = t.store.World.LoadGlobalReview(r.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load global review: %w", err)
		}
	}
	if existing != nil {
		original := *existing
		original.Recheck = nil // 系统登记的返工复核不改变原评审的幂等身份。
		if reflect.DeepEqual(original, r) {
			return t.finishReview(r, progress, scope, artifact)
		}
		if r.Scope != "chapter" {
			return nil, fmt.Errorf("第 %d 章聚合评审已存在且内容不同，拒绝覆盖: %w", r.Chapter, errs.ErrToolConflict)
		}
	}
	check, err := t.store.World.LoadPendingReviewRecheck()
	if err != nil {
		return nil, fmt.Errorf("load review recheck: %w", err)
	}
	if check != nil && len(progress.PendingRewrites) == 0 {
		return nil, fmt.Errorf("须先用 save_review_check 完成当前定向复核，不得发起下一轮返工: %w", errs.ErrToolPrecondition)
	}
	switch r.Scope {
	case "arc":
		if err := requireAggregateTarget(t.store, flow.AggregateArcReview, boundary.Volume, boundary.Arc, r.Chapter); err != nil {
			return nil, err
		}
	case "global":
		if err := requireAggregateTarget(t.store, flow.AggregateGlobalReview, 0, 0, r.Chapter); err != nil {
			return nil, err
		}
	}

	// 先原子应用控制状态，再保存审阅工件。若第二步失败，返工意图仍然存在；
	// Writer 排空队列后，路由会因审阅工件缺失而重新派发 Editor，不会跳过审阅。
	latest, err := t.store.Progress.ApplyReviewOutcome(reviewOutcome, affected, r.Summary)
	if err != nil {
		return nil, fmt.Errorf("apply review outcome: %w", err)
	}
	if err := t.store.World.SaveReview(r); err != nil {
		return nil, fmt.Errorf("save review: %w", err)
	}

	return t.finishReview(r, latest, scope, artifact)
}

func (t *SaveReviewTool) finishReview(
	r domain.ReviewEntry,
	progress *domain.Progress,
	scope domain.Scope,
	artifact string,
) (json.RawMessage, error) {
	if _, err := t.store.Checkpoints.AppendArtifact(scope, "review", artifact); err != nil {
		return nil, fmt.Errorf("checkpoint review: %w", err)
	}

	// 使用原子更新返回的 Progress 快照作为事实，避免二次读取产生新的失败窗口。
	nextFlow := string(domain.FlowWriting)
	nextChapter := 0
	if progress != nil {
		nextFlow = string(progress.Flow)
		nextChapter = progress.NextChapter()
	}

	return json.Marshal(map[string]any{
		"saved":             true,
		"chapter":           r.Chapter,
		"scope":             r.Scope,
		"verdict":           r.Verdict,
		"affected_chapters": r.AffectedChapters,
		"issues":            len(r.Issues),
		"next_flow":         nextFlow,
		"next_chapter":      nextChapter,
	})
}

func (t *SaveReviewTool) normalizeReviewEntry(r *domain.ReviewEntry) (*store.ArcBoundary, error) {
	if r.Recheck != nil {
		return nil, fmt.Errorf("recheck is maintained by the system; use save_review_check for its result")
	}
	switch r.Scope {
	case "chapter", "global", "arc":
	default:
		return nil, fmt.Errorf("invalid review scope: %q", r.Scope)
	}
	if len(r.AffectedChapters) > 0 {
		return nil, fmt.Errorf("affected_chapters is derived from issues[].chapters; do not submit it")
	}
	if strings.TrimSpace(r.Summary) == "" {
		return nil, fmt.Errorf("summary is required")
	}
	if r.ContractStatus != "" && r.ContractStatus != "met" && r.ContractStatus != "partial" && r.ContractStatus != "missed" {
		return nil, fmt.Errorf("invalid contract_status: %q", r.ContractStatus)
	}
	for _, miss := range r.ContractMisses {
		if strings.TrimSpace(miss) == "" {
			return nil, fmt.Errorf("contract_misses cannot contain empty entries")
		}
	}
	var boundary *store.ArcBoundary
	if r.Scope == "arc" {
		var err error
		boundary, err = t.store.Outline.CheckArcBoundary(r.Chapter)
		if err != nil {
			return nil, fmt.Errorf("check arc scope: %w", err)
		}
		if boundary == nil || !boundary.IsArcEnd || boundary.EndChapter != r.Chapter {
			return nil, fmt.Errorf("arc review chapter must be an arc endpoint")
		}
	}

	affectedSet := make(map[int]struct{})
	for i := range r.Issues {
		issue := &r.Issues[i]
		if strings.TrimSpace(issue.Description) == "" {
			return nil, fmt.Errorf("issue description is required")
		}
		if strings.TrimSpace(issue.Evidence) == "" {
			return nil, fmt.Errorf("issue evidence is required")
		}
		switch issue.Severity {
		case "critical", "error", "warning":
		default:
			return nil, fmt.Errorf("invalid issue severity: %q", issue.Severity)
		}
		if len(issue.Chapters) == 0 && r.Scope == "chapter" {
			issue.Chapters = []int{r.Chapter}
		}
		if len(issue.Chapters) == 0 {
			return nil, fmt.Errorf("issue chapters are required when scope=%s", r.Scope)
		}
		issue.Chapters = uniqueSortedChapters(issue.Chapters)
		for _, chapter := range issue.Chapters {
			switch r.Scope {
			case "chapter":
				if chapter != r.Chapter {
					return nil, fmt.Errorf("chapter review issue must reference chapter %d, got %d", r.Chapter, chapter)
				}
			case "global":
				if chapter <= 0 || chapter > r.Chapter {
					return nil, fmt.Errorf("global review issue chapter %d outside 1-%d", chapter, r.Chapter)
				}
			case "arc":
				if chapter < boundary.StartChapter || chapter > boundary.EndChapter {
					return nil, fmt.Errorf("arc review issue chapter %d outside %d-%d", chapter, boundary.StartChapter, boundary.EndChapter)
				}
			}
			if issue.RequiresChange {
				affectedSet[chapter] = struct{}{}
			}
		}
	}
	if err := validateDimensions(r.Dimensions); err != nil {
		return nil, err
	}
	derived := make([]int, 0, len(affectedSet))
	for chapter := range affectedSet {
		derived = append(derived, chapter)
	}
	slices.Sort(derived)
	if r.Verdict == "accept" && len(derived) > 0 {
		return nil, fmt.Errorf("accept review cannot contain issues with requires_change=true")
	}
	if (r.Verdict == "rewrite" || r.Verdict == "polish") && len(derived) == 0 {
		return nil, fmt.Errorf("verdict=%s requires at least one issue with requires_change=true", r.Verdict)
	}
	r.AffectedChapters = derived
	return boundary, nil
}

func uniqueSortedChapters(chapters []int) []int {
	seen := make(map[int]struct{}, len(chapters))
	for _, chapter := range chapters {
		seen[chapter] = struct{}{}
	}
	result := make([]int, 0, len(seen))
	for chapter := range seen {
		result = append(result, chapter)
	}
	slices.Sort(result)
	return result
}

// reviewFlow 是文学裁定与持久化协议之间唯一的映射点。verdict 由 Editor 决定；
// 这里只接受 Router 能恢复的三种控制结果。
func reviewFlow(verdict string) (domain.FlowState, error) {
	switch verdict {
	case "accept":
		return domain.FlowWriting, nil
	case "polish":
		return domain.FlowPolishing, nil
	case "rewrite":
		return domain.FlowRewriting, nil
	default:
		return "", fmt.Errorf("invalid review verdict: %q", verdict)
	}
}

func validateDimensions(dimensions []domain.DimensionScore) error {
	if len(dimensions) == 0 {
		return fmt.Errorf("dimensions must contain at least one evidence-based assessment")
	}

	seen := make(map[string]struct{}, len(dimensions))
	for _, dim := range dimensions {
		name := strings.TrimSpace(dim.Dimension)
		if name == "" {
			return fmt.Errorf("dimension name is required")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate dimension: %s", name)
		}
		seen[name] = struct{}{}
		if dim.Score < 0 || dim.Score > 100 {
			return fmt.Errorf("invalid score for %s: %d", dim.Dimension, dim.Score)
		}
		if strings.TrimSpace(dim.Comment) == "" {
			return fmt.Errorf("dimension comment is required: %s", dim.Dimension)
		}
	}
	return nil
}
