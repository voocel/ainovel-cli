package flow

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// LoadState 从 Store 读取 Route 所需的全部事实。
// 这是路由的"IO 边界"：所有读取集中在这里，Route 保持纯。
// 任何读取失败都返回错误；损坏的工件与“尚未生成”是两种不同事实，Router 不得
// 在不完整快照上继续派单。
func LoadState(store *storepkg.Store) (State, error) {
	var s State
	missing, err := store.FoundationMissing()
	if err != nil {
		return s, fmt.Errorf("load foundation state: %w", err)
	}
	s.FoundationMissing = missing
	// 规划级别:save_foundation 落 scale 时写入 RunMeta,补齐分支据此推导规划师。
	// 读失败按未知处理(tier 空 → 补齐交 LLM 裁定),与其余事实的保守默认一致。
	meta, err := store.RunMeta.Load()
	if err != nil {
		return s, fmt.Errorf("load run meta: %w", err)
	}
	if meta != nil {
		s.PlanningTier = meta.PlanningTier
	}
	progress, err := store.Progress.Load()
	if err != nil {
		return s, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil {
		return s, nil
	}
	s.Progress = progress

	if n := len(progress.CompletedChapters); n > 0 {
		s.LastCompleted = progress.CompletedChapters[n-1]
	}

	// 弧边界仅在分层模式且有已完成章节时才计算
	if progress.Layered && s.LastCompleted > 0 {
		boundary, err := store.Outline.CheckArcBoundary(s.LastCompleted)
		if err != nil {
			return s, fmt.Errorf("check arc boundary: %w", err)
		}
		if boundary != nil {
			s.ArcBoundary = boundary
			if boundary.IsArcEnd {
				s.HasArcReview, err = store.World.HasArcReview(s.LastCompleted)
				if err != nil {
					return s, fmt.Errorf("load arc review: %w", err)
				}
				s.HasArcSummary, err = store.Summaries.HasArcSummary(boundary.Volume, boundary.Arc)
				if err != nil {
					return s, fmt.Errorf("load arc summary: %w", err)
				}
				if boundary.IsVolumeEnd {
					s.HasVolumeSummary, err = store.Summaries.HasVolumeSummary(boundary.Volume)
					if err != nil {
						return s, fmt.Errorf("load volume summary: %w", err)
					}
				}
			}
		}
	}

	// 非分层全局审阅事实:仅在触发点读盘(其余组合 Route 不消费该字段)。
	if !progress.Layered && s.LastCompleted > 0 {
		if due, _ := domain.ShouldReview(len(progress.CompletedChapters)); due {
			s.HasGlobalReview, err = store.World.HasGlobalReview(s.LastCompleted)
			if err != nil {
				return s, fmt.Errorf("load global review: %w", err)
			}
		}
	}

	return s, nil
}
