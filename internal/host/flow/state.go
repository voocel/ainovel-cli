package flow

import (
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// LoadState 从 Store Đọc Route 所需的Tất cả事实。
// 这是路由的"IO 边界"：所有Đọc集中在这里，Route 保持纯。
// ĐọcThất bại按保守Mặc định填充（has*=false, boundary=nil），让 Router 倾向重派而非Bỏ qua。
func LoadState(store *storepkg.Store) State {
	s := State{
		FoundationMissing: store.FoundationMissing(),
	}
	progress, err := store.Progress.Load()
	if err != nil || progress == nil {
		return s
	}
	s.Progress = progress

	if n := len(progress.CompletedChapters); n > 0 {
		s.LastCompleted = progress.CompletedChapters[n-1]
	}

	// 弧边界仅在分层模式且有Đã hoàn thànhChương时才计算
	if progress.Layered && s.LastCompleted > 0 {
		if boundary, berr := store.Outline.CheckArcBoundary(s.LastCompleted); berr == nil && boundary != nil {
			s.ArcBoundary = boundary
			if boundary.IsArcEnd {
				s.HasArcReview = store.World.HasArcReview(s.LastCompleted)
				s.HasArcSummary = store.Summaries.HasArcSummary(boundary.Volume, boundary.Arc)
				if boundary.IsVolumeEnd {
					s.HasVolumeSummary = store.Summaries.HasVolumeSummary(boundary.Volume)
				}
			}
		}
	}

	return s
}
