package reminder

import (
	"context"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// neverStopGen 提醒 Coordinator 在 Phase != Complete 时不得主动 end_turn。
// 配合 agentcore 的 StopGuard 双重保险：Reminder 告诉 LLM"不要这么做"，
// StopGuard 拦截 LLM"还是这么做了"的情况。
type neverStopGen struct{}

func (neverStopGen) Source() string { return "never_stop" }

func (neverStopGen) Generate(_ context.Context, state State) string {
	if state.Progress == nil {
		// 还没规划开始，无从约束
		return ""
	}
	// Phase=Complete 由 bookCompleteGen 负责输出总结提示，这里无需重复
	if state.Progress.Phase == domain.PhaseComplete {
		return ""
	}
	return "禁止在本轮结束对话。未完成整本小说前，你必须继续调度 architect / writer / editor 子代理。" +
		"如果没有明确下一步，先调 novel_context 读取进度再决策。"
}
