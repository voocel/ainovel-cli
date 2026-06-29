package diag

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// InvalidPendingRewrites 检测返工队列里混入未Hoàn thànhChương。
func InvalidPendingRewrites(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Progress.PendingRewrites) == 0 {
		return nil
	}
	p := snap.Progress
	completed := append([]int(nil), p.CompletedChapters...)
	slices.Sort(completed)

	var invalid []int
	for _, ch := range p.PendingRewrites {
		if ch <= 0 || !slices.Contains(completed, ch) {
			invalid = append(invalid, ch)
		}
	}
	if len(invalid) == 0 {
		return nil
	}
	slices.Sort(invalid)
	return []Finding{{
		Rule:       "InvalidPendingRewrites",
		Category:   CatFlow,
		Severity:   SevCritical,
		Confidence: ConfHigh,
		AutoLevel:  AutoSuggest,
		Target:     "meta/progress.json",
		Title:      fmt.Sprintf("返工队列包含未Hoàn thànhChương: [%s]", intsToStr(invalid)),
		Evidence:   fmt.Sprintf("pending_rewrites=[%s], completed_chapters=[%s], flow=%s", intsToStr(p.PendingRewrites), intsToStr(completed), p.Flow),
		Suggestion: "这是Trạng thái不变量损坏。Vui lòng停止运行后Sửa meta/progress.json，移除 pending_rewrites 中未Hoàn thànhChương；若队列为Rỗng，将 flow 改为 writing 并清Rỗng rewrite_reason。",
	}}
}

// RewritePendingPressure 检测存在待改写Chương（Hiện tại仅检测Trạng thái存在，不判定停滞）。
func RewritePendingPressure(snap *Snapshot) []Finding {
	if snap.Progress == nil {
		return nil
	}
	p := snap.Progress
	if len(p.PendingRewrites) == 0 {
		return nil
	}
	if p.Flow != domain.FlowRewriting && p.Flow != domain.FlowPolishing {
		return nil
	}
	chapters := intsToStr(p.PendingRewrites)
	return []Finding{{
		Rule:       "RewritePendingPressure",
		Category:   CatFlow,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "runtime.flow",
		Title:      fmt.Sprintf("待改写Chương: [%s]", chapters),
		Evidence:   fmt.Sprintf("flow=%s, pending_rewrites=[%s]", p.Flow, chapters),
		Suggestion: "Kiểm tra Editor 评审标准Có czy không过严，或 Writer 改写 prompt Có czy không有效。" +
			"如需人工打断，Vui lòng在Nhập框Nộp干预指令。",
	}}
}

// OrphanedSteer 检测未消费的用户转向指令。
func OrphanedSteer(snap *Snapshot) []Finding {
	if snap.RunMeta == nil || snap.RunMeta.PendingSteer == "" {
		return nil
	}
	if snap.Progress != nil && snap.Progress.Flow == domain.FlowSteering {
		return nil // Đang处理中，不算孤立
	}
	return []Finding{{
		Rule:       "OrphanedSteer",
		Category:   CatFlow,
		Severity:   SevWarning,
		Confidence: ConfHigh,
		AutoLevel:  AutoSafe,
		Target:     "runtime.recovery",
		Title:      "存在未消费的转向指令",
		Evidence:   fmt.Sprintf("pending_steer=%q, flow=%s", truncStr(snap.RunMeta.PendingSteer, 60), flowStr(snap.Progress)),
		Suggestion: "该 steer 被持久化但未被 Coordinator 消费。Kiểm tra中断Phục hồi逻辑，或通过重MớiNộp覆盖。",
	}}
}

// PhaseFlowMismatch 检测阶段与流程Trạng thái不匹配。
func PhaseFlowMismatch(snap *Snapshot) []Finding {
	if snap.Progress == nil {
		return nil
	}
	p := snap.Progress
	if p.Phase == domain.PhaseWriting || p.Phase == "" {
		return nil
	}
	if p.Flow == "" || p.Flow == domain.FlowWriting {
		return nil
	}
	return []Finding{{
		Rule:       "PhaseFlowMismatch",
		Category:   CatFlow,
		Severity:   SevCritical,
		Confidence: ConfHigh,
		AutoLevel:  AutoSafe,
		Target:     "runtime.flow",
		Title:      fmt.Sprintf("阶段/流程Trạng thái不匹配: phase=%s, flow=%s", p.Phase, p.Flow),
		Evidence:   fmt.Sprintf("phase=%s 不应出现非初始 flow=%s", p.Phase, p.Flow),
		Suggestion: "Trạng thái机可能损坏，需手动Kiểm tra meta/progress.json 的 phase 和 flow 字段。",
	}}
}

// ChapterGaps 检测Đã hoàn thànhChương列表中的跳号。
func ChapterGaps(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Progress.CompletedChapters) < 2 {
		return nil
	}
	sorted := append([]int(nil), snap.Progress.CompletedChapters...)
	sort.Ints(sorted)

	var gaps []int
	for i := 1; i < len(sorted); i++ {
		for ch := sorted[i-1] + 1; ch < sorted[i]; ch++ {
			gaps = append(gaps, ch)
		}
	}
	if len(gaps) == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "ChapterGaps",
		Category:   CatFlow,
		Severity:   SevWarning,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "runtime.flow",
		Title:      fmt.Sprintf("Chương跳号: Thiếu [%s]", intsToStr(gaps)),
		Evidence:   fmt.Sprintf("completed=[%s]", intsToStr(sorted)),
		Suggestion: "commit_chapter 可能中途中断。Kiểm tra meta/pending_commit.json Có czy không存在未Hoàn thànhNộp。",
	}}
}

func flowStr(p *domain.Progress) string {
	if p == nil {
		return "<nil>"
	}
	return string(p.Flow)
}

func truncStr(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-3]) + "..."
}

func intsToStr(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, ", ")
}
