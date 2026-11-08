package tools

import (
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ReopenBook 把已完结的书重新打开进入返工态，由 Engine 在干预动作边界调用。
// 完本后 completePhaseGate 硬拦一切 subagent 派发，用户无法返工已写章节。
// 本函数不经 subagent，complete 期可调：原子地把 phase 切回 writing、目标章入
// PendingRewrites、flow=rewriting，随后 Flow Router 照既有返工队列派 writer 逐章重写，
// 队列排空并完成单次复核、受影响聚合重建后自动重新收尾完结。
func ReopenBook(s *store.Store, chapters []int, reason string) error {
	if len(chapters) == 0 {
		return fmt.Errorf("chapters 不能为空，需指明要返工的章节: %w", errs.ErrToolArgs)
	}

	progress, err := s.Progress.Load()
	if err != nil {
		return fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil {
		return fmt.Errorf("progress 未初始化: %w", errs.ErrToolPrecondition)
	}
	// 只能返工已写章；不在已完成集合的章号属续写/越界，明确拒绝引导用户走篇幅调整。
	var invalid []int
	for _, ch := range chapters {
		if !slices.Contains(progress.CompletedChapters, ch) {
			invalid = append(invalid, ch)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("第 %v 章尚未写完，reopen 只能返工已完成章节（新增/扩展剧情请走篇幅调整）: %w", invalid, errs.ErrToolPrecondition)
	}

	// phase 前置校验在 store.Reopen 内兜底（仅 complete 可调）。
	if err := s.Progress.Reopen(chapters, reason); err != nil {
		return fmt.Errorf("reopen: %w: %w", errs.ErrStoreWrite, err)
	}

	// checkpoint：与 complete_book 对称（GlobalScope + meta/progress.json）。
	if _, err := s.Checkpoints.AppendArtifact(domain.GlobalScope(), "reopen", "meta/progress.json"); err != nil {
		return fmt.Errorf("checkpoint reopen: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}
