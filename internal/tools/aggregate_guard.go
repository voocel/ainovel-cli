package tools

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

// requireAggregateTarget 将 Editor 的新聚合写入绑定到 Router 当前唯一待补的工件。
// 目标完全由已落盘事实推导，不依赖任务文案，也不信任模型自填的章节/卷弧号；
// 已落盘同内容的幂等收尾由各工具在调用本函数前识别。
func requireAggregateTarget(st *store.Store, kind flow.AggregateKind, volume, arc, endChapter int) error {
	state, err := flow.LoadState(st)
	if err != nil {
		return fmt.Errorf("load aggregate state: %w: %w", errs.ErrStoreRead, err)
	}
	if state.ReviewRecheck != nil {
		return fmt.Errorf("须先完成当前正文返工复核: %w", errs.ErrToolPrecondition)
	}
	due := state.AggregateRefresh
	if due == nil {
		return fmt.Errorf("当前没有待处理的 %s 工件: %w", kind, errs.ErrToolPrecondition)
	}
	targetMismatch := due.Kind != kind
	switch kind {
	case flow.AggregateArcReview, flow.AggregateArcSummary:
		targetMismatch = targetMismatch || due.Volume != volume || due.Arc != arc
	case flow.AggregateVolumeSummary:
		targetMismatch = targetMismatch || due.Volume != volume
	case flow.AggregateGlobalReview:
		// 全局评审没有卷弧坐标，只由 kind 和截止章节定位。
	}
	endMismatch := endChapter > 0 && due.EndChapter != endChapter
	if targetMismatch || endMismatch {
		return fmt.Errorf(
			"聚合写入目标不匹配：当前应处理 kind=%s volume=%d arc=%d end_chapter=%d，收到 kind=%s volume=%d arc=%d end_chapter=%d: %w",
			due.Kind, due.Volume, due.Arc, due.EndChapter,
			kind, volume, arc, endChapter, errs.ErrToolConflict,
		)
	}
	return nil
}
