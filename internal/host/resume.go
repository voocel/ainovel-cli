package host

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// resumeLabel 基于事实生成 Resume 的 UI 标签。
// label 为空表示无可恢复状态（应走新建）。恢复本身不需要任何 prompt——
// Engine 只恢复事实：从 store 重算路由续跑（docs/engine-rfc.md §6）。
func resumeLabel(store *storepkg.Store) (string, error) {
	progress, err := store.Progress.Load()
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if progress == nil || progress.Phase == domain.PhaseComplete {
		return "", nil
	}
	return describeResume(store, progress)
}

// describeResume 生成人类可读的恢复标签；不影响 Engine 路由。
// 所有执行路由由 Flow Router 按事实推导；这里仅面向 UI 的 "恢复：xxx"。
func describeResume(store *storepkg.Store, progress *domain.Progress) (string, error) {
	switch progress.Phase {
	case domain.PhasePremise, domain.PhaseOutline:
		return fmt.Sprintf("恢复：规划阶段（%s）", progress.Phase), nil
	case domain.PhaseWriting:
		// 优先级与 Router 的决策优先级对齐，让 label 与即将派发的指令一致。
		pending, err := store.Signals.LoadPendingCommit()
		if err != nil {
			return "", fmt.Errorf("读取待恢复提交: %w", err)
		}
		if pending != nil {
			return fmt.Sprintf("恢复：第 %d 章提交中断", pending.Chapter), nil
		}
		if len(progress.PendingRewrites) > 0 {
			verb := "重写"
			if progress.Flow == domain.FlowPolishing {
				verb = "打磨"
			}
			return fmt.Sprintf("%s恢复：%d 章待处理", verb, len(progress.PendingRewrites)), nil
		}
		if progress.Flow == domain.FlowReviewing {
			return "恢复：审阅中断", nil
		}
		if progress.InProgressChapter > 0 {
			return fmt.Sprintf("恢复：第 %d 章进行中", progress.InProgressChapter), nil
		}
		label, err := describeArcEndLabel(store, progress)
		if err != nil {
			return "", err
		}
		if label != "" {
			return label, nil
		}
		return fmt.Sprintf("恢复：从第 %d 章继续", progress.NextChapter()), nil
	}
	return "恢复", nil
}

// describeArcEndLabel 为弧末/卷末的多种中间状态生成贴合 UI 的标签。
// 与 flow.Route 的弧末分支保持同序，保证 label 与 Router 首条指令对齐。
func describeArcEndLabel(store *storepkg.Store, progress *domain.Progress) (string, error) {
	if !progress.Layered || len(progress.CompletedChapters) == 0 {
		return "", nil
	}
	lastCh := progress.CompletedChapters[len(progress.CompletedChapters)-1]
	boundary, err := store.Outline.CheckArcBoundary(lastCh)
	if err != nil {
		return "", fmt.Errorf("检查弧边界: %w", err)
	}
	if boundary == nil || !boundary.IsArcEnd {
		return "", nil
	}
	vol, arc := boundary.Volume, boundary.Arc
	hasArcReview, err := store.World.HasArcReview(lastCh)
	if err != nil {
		return "", fmt.Errorf("读取弧评审: %w", err)
	}
	hasArcSummary, err := store.Summaries.HasArcSummary(vol, arc)
	if err != nil {
		return "", fmt.Errorf("读取弧摘要: %w", err)
	}
	hasVolumeSummary := false
	if boundary.IsVolumeEnd {
		hasVolumeSummary, err = store.Summaries.HasVolumeSummary(vol)
		if err != nil {
			return "", fmt.Errorf("读取卷摘要: %w", err)
		}
	}
	switch {
	case !hasArcReview:
		return fmt.Sprintf("恢复：弧末评审待处理（V%d A%d）", vol, arc), nil
	case !hasArcSummary:
		return fmt.Sprintf("恢复：弧摘要待生成（V%d A%d）", vol, arc), nil
	case boundary.IsVolumeEnd && !hasVolumeSummary:
		return fmt.Sprintf("恢复：卷摘要待生成（V%d）", vol), nil
	case boundary.NeedsExpansion && boundary.NextArc > 0:
		return fmt.Sprintf("恢复：待展开下一弧（V%d A%d）", boundary.NextVolume, boundary.NextArc), nil
	case boundary.NeedsNewVolume:
		return fmt.Sprintf("恢复：待决策下一卷（V%d 末）", vol), nil
	}
	return "", nil
}
