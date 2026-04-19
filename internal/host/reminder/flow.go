package reminder

import (
	"context"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// flowGen 根据当前 Progress.Flow 告诉 Coordinator 下一步应该调哪个子代理。
// 这条 reminder 替代了原 `[系统] continue:` / `[系统] review_required:` 字符串。
type flowGen struct{}

func (flowGen) Source() string { return "flow" }

func (flowGen) Generate(_ context.Context, state State) string {
	p := state.Progress
	if p == nil {
		return ""
	}
	switch p.Phase {
	case domain.PhaseInit, domain.PhasePremise, domain.PhaseOutline:
		// 设定阶段由 architect 推进；具体缺哪项由 save_foundation 的 remaining 字段告知
		return "当前处于设定阶段。请调 novel_context 查看 foundation_status.missing，" +
			"再按缺项派发 architect_short / architect_mid / architect_long（每次只补一项）。"
	case domain.PhaseWriting:
		return writingFlowReminder(state)
	case domain.PhaseComplete:
		return ""
	}
	return ""
}

func writingFlowReminder(state State) string {
	p := state.Progress
	switch p.Flow {
	case domain.FlowRewriting, domain.FlowPolishing:
		// 由 queueGuardGen 专门处理
		return ""
	case domain.FlowReviewing:
		return "当前 flow=reviewing。刚刚完成弧结束或审阅触发，请调 editor 完成本次评审，" +
			"读取返回里的 final_verdict：accept → 生成弧摘要并继续写作；polish/rewrite → 按 affected_chapters 逐章调 writer。"
	default:
		// FlowWriting 或空：弧尾未交接时刹车，让 LLM 走 coordinator.md 的弧结束流程
		if state.ArcHandoffPending {
			return "弧已全部写完但 arc_summary 尚未归档，禁止直接派 writer 写新章。" +
				"请按 coordinator.md 『长篇模式 / 弧结束』：先调 editor 生成弧摘要，" +
				"再按返回里的 needs_expansion / needs_new_volume 调 architect_long 展开/追加，最后才继续写作。"
		}
		next := p.NextChapter()
		if p.Layered {
			return fmt.Sprintf(
				"当前 flow=writing，next_chapter=%d。请直接调 subagent(writer, \"写第 %d 章\")，不要先调 novel_context。",
				next, next)
		}
		return fmt.Sprintf(
			"当前 flow=writing，已完成 %d / %d 章，next_chapter=%d。"+
				"请直接调 subagent(writer, \"写第 %d 章\")，不要先调 novel_context。",
			len(p.CompletedChapters), p.TotalChapters, next, next)
	}
}
