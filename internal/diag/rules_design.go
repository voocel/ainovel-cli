package diag

import (
	"fmt"
	"sort"
	"strings"
)

// ChapterPlanInjectionGap 检测问题章节是否普遍缺失 chapter_plan 注入。
func ChapterPlanInjectionGap(snap *Snapshot) []Finding {
	traces := problematicTraces(snap)
	if len(traces) < 3 {
		return nil
	}

	var missing []int
	for _, trace := range traces {
		if trace.Context == nil || !trace.Context.HasChapterPlan {
			missing = append(missing, trace.Chapter)
		}
	}
	if len(missing) < ThresholdDesignMinSamples {
		return nil
	}
	rate := float64(len(missing)) / float64(len(traces))
	if rate < ThresholdDesignWeakRate {
		return nil
	}

	return []Finding{{
		Rule:       "ChapterPlanInjectionGap",
		Category:   CatContext,
		Severity:   SevWarning,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "context.chapter_plan",
		Title:      fmt.Sprintf("问题章节常缺 chapter_plan 注入 (%.0f%%)", rate*100),
		Evidence:   fmt.Sprintf("存在问题的章节 %d 个，其中 %d 个缺少 chapter_plan: [%s]", len(traces), len(missing), intsToStr(missing)),
		Suggestion: "优先检查 novel_context 的 chapter_plan 加载与裁剪链路，再决定是否修改 Writer prompt。",
	}}
}

// ContinuitySupportWeak 检测 continuity 问题是否常伴随时间线/状态变化支持缺失。
func ContinuitySupportWeak(snap *Snapshot) []Finding {
	traces := tracesWithContinuityIssue(snap)
	if len(traces) < ThresholdDesignMinSamples {
		return nil
	}

	var missing []string
	for _, trace := range traces {
		if trace.Context == nil || (trace.Context.TimelineCount == 0 && trace.Context.StateChangeCount == 0) {
			missing = append(missing, fmt.Sprintf("ch%d", trace.Chapter))
		}
	}
	if len(missing) < ThresholdDesignMinSamples {
		return nil
	}
	rate := float64(len(missing)) / float64(len(traces))
	if rate < ThresholdDesignWeakRate {
		return nil
	}

	return []Finding{{
		Rule:       "ContinuitySupportWeak",
		Category:   CatContext,
		Severity:   SevWarning,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "context.timeline",
		Title:      fmt.Sprintf("continuity 低分常伴随上下文支持缺失 (%.0f%%)", rate*100),
		Evidence:   fmt.Sprintf("出现 continuity 问题的章节 %d 个，其中 %d 个缺少 timeline/state_changes: [%s]", len(traces), len(missing), strings.Join(missing, ", ")),
		Suggestion: "检查 novel_context 是否稳定注入 timeline 与 recent_state_changes，避免连续性校验只靠模型短期记忆。",
	}}
}

// RewriteAfterCompaction 检测压缩后的章节是否更容易被要求重写。
func RewriteAfterCompaction(snap *Snapshot) []Finding {
	var total int
	var rewrites []string
	for _, trace := range reviewedTraces(snap) {
		if !trace.ContextCompacted {
			continue
		}
		total++
		if trace.reviewVerdict() == "rewrite" {
			rewrites = append(rewrites, fmt.Sprintf("ch%d(%s)", trace.Chapter, trace.ContextCompactionKind))
		}
	}
	if total < ThresholdDesignMinSamples || len(rewrites) < ThresholdDesignMinSamples {
		return nil
	}
	rate := float64(len(rewrites)) / float64(total)
	if rate < ThresholdRewriteRate {
		return nil
	}

	return []Finding{{
		Rule:       "RewriteAfterCompaction",
		Category:   CatContext,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "context.window",
		Title:      fmt.Sprintf("压缩后的章节更易触发 rewrite (%.0f%%)", rate*100),
		Evidence:   fmt.Sprintf("压缩后有评审的章节 %d 个，其中 %d 个被判 rewrite: [%s]", total, len(rewrites), strings.Join(rewrites, ", ")),
		Suggestion: "优先检查上下文压缩策略和压缩后保留的信息结构，而不是先调高 Writer 或 Editor 的阈值。",
	}}
}

// ContractExecutionWeak 判断 contract 问题更像上下文缺失还是 Writer 执行不足。
func ContractExecutionWeak(snap *Snapshot) []Finding {
	traces := tracesWithContractIssue(snap)
	if len(traces) < ThresholdDesignMinSamples {
		return nil
	}

	var missingContract []string
	var contractPresent []string
	for _, trace := range traces {
		if trace.Context != nil && trace.Context.HasChapterContract {
			contractPresent = append(contractPresent, fmt.Sprintf("ch%d", trace.Chapter))
			continue
		}
		missingContract = append(missingContract, fmt.Sprintf("ch%d", trace.Chapter))
	}

	if len(missingContract) >= ThresholdDesignMinSamples {
		rate := float64(len(missingContract)) / float64(len(traces))
		if rate >= ThresholdDesignWeakRate {
			return []Finding{{
				Rule:       "ContractExecutionWeak",
				Category:   CatContext,
				Severity:   SevWarning,
				Confidence: ConfHigh,
				AutoLevel:  AutoNone,
				Target:     "context.chapter_contract",
				Title:      fmt.Sprintf("contract 失配更像上下文注入缺口 (%.0f%%)", rate*100),
				Evidence:   fmt.Sprintf("contract partial/missed 的章节 %d 个，其中 %d 个未注入 chapter_contract: [%s]", len(traces), len(missingContract), strings.Join(missingContract, ", ")),
				Suggestion: "优先检查 chapter_contract 是否稳定进入 novel_context，而不是立即修改 Writer prompt。",
			}}
		}
	}

	if len(contractPresent) < ThresholdDesignMinSamples {
		return nil
	}
	rate := float64(len(contractPresent)) / float64(len(traces))
	if rate < ThresholdDesignWeakRate {
		return nil
	}
	return []Finding{{
		Rule:       "ContractExecutionWeak",
		Category:   CatQuality,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "prompt.writer",
		Title:      fmt.Sprintf("contract 失配更像执行能力不足 (%.0f%%)", rate*100),
		Evidence:   fmt.Sprintf("contract partial/missed 的章节 %d 个，其中 %d 个已注入 chapter_contract: [%s]", len(traces), len(contractPresent), strings.Join(contractPresent, ", ")),
		Suggestion: "chapter_contract 已经给到 Writer，但执行仍不稳定。优先检查 writer prompt 对 required_beats / payoff / hook_goal 的落实指令。",
	}}
}

func reviewedTraces(snap *Snapshot) []*chapterTrace {
	if snap == nil || len(snap.ChapterTraces) == 0 {
		return nil
	}
	chapters := make([]int, 0, len(snap.ChapterTraces))
	for chapter, trace := range snap.ChapterTraces {
		if trace == nil || trace.reviewVerdict() == "" {
			continue
		}
		chapters = append(chapters, chapter)
	}
	sort.Ints(chapters)

	out := make([]*chapterTrace, 0, len(chapters))
	for _, chapter := range chapters {
		out = append(out, snap.ChapterTraces[chapter])
	}
	return out
}

func problematicTraces(snap *Snapshot) []*chapterTrace {
	var out []*chapterTrace
	for _, trace := range reviewedTraces(snap) {
		verdict := trace.reviewVerdict()
		if verdict == "rewrite" || verdict == "polish" {
			out = append(out, trace)
		}
	}
	return out
}

func tracesWithContinuityIssue(snap *Snapshot) []*chapterTrace {
	var out []*chapterTrace
	for _, trace := range reviewedTraces(snap) {
		if trace.hasLowDimension("continuity") || trace.hasIssueType("continuity") {
			out = append(out, trace)
		}
	}
	return out
}

func tracesWithContractIssue(snap *Snapshot) []*chapterTrace {
	var out []*chapterTrace
	for _, trace := range reviewedTraces(snap) {
		status := trace.contractStatus()
		if status == "partial" || status == "missed" {
			out = append(out, trace)
		}
	}
	return out
}

func (t *chapterTrace) reviewVerdict() string {
	if t == nil {
		return ""
	}
	if t.ReviewOutcome != nil && t.ReviewOutcome.Verdict != "" {
		return t.ReviewOutcome.Verdict
	}
	if t.Review != nil {
		return t.Review.Verdict
	}
	return ""
}

func (t *chapterTrace) contractStatus() string {
	if t == nil {
		return ""
	}
	if t.ReviewOutcome != nil && t.ReviewOutcome.ContractStatus != "" {
		return t.ReviewOutcome.ContractStatus
	}
	if t.Review != nil {
		return t.Review.ContractStatus
	}
	return ""
}

func (t *chapterTrace) hasLowDimension(name string) bool {
	if t == nil {
		return false
	}
	if t.ReviewOutcome != nil {
		for _, dim := range t.ReviewOutcome.LowDimensions {
			if dim == name {
				return true
			}
		}
	}
	if t.Review != nil {
		if dim := t.Review.Dimension(name); dim != nil && (dim.Verdict == "warning" || dim.Verdict == "fail") {
			return true
		}
	}
	return false
}

func (t *chapterTrace) hasIssueType(name string) bool {
	if t == nil {
		return false
	}
	if t.ReviewOutcome != nil {
		for _, issueType := range t.ReviewOutcome.CriticalIssueTypes {
			if issueType == name {
				return true
			}
		}
	}
	if t.Review != nil {
		for _, issue := range t.Review.Issues {
			if issue.Type == name && (issue.Severity == "critical" || issue.Severity == "error") {
				return true
			}
		}
	}
	return false
}
