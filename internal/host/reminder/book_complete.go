package reminder

import (
	"context"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// bookCompleteGen 仅在 Phase=Complete 时触发，指示 Coordinator 输出全书总结后正常停机。
// 与 StopGuard 配合：StopGuard 仅在此 Phase 放行 end_turn。
type bookCompleteGen struct{}

func (bookCompleteGen) Source() string { return "book_complete" }

func (bookCompleteGen) Generate(_ context.Context, state State) string {
	p := state.Progress
	if p == nil || p.Phase != domain.PhaseComplete {
		return ""
	}
	return fmt.Sprintf(
		"全书已完成（%d 章，%d 字）。请输出全书总结（总章数 / 总字数 / 各章概要 / 主要角色弧线 / 伏笔回收情况），"+
			"然后正常结束本次对话。不要再调任何子代理或工具。",
		len(p.CompletedChapters), p.TotalWordCount)
}
