package store

import (
	"cmp"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// QueueReviewRecheck 在覆盖终稿前登记正文返工。标题变化不调用此方法；同一原评审
// 的整批修改合成一次复核，已有结果的评审不会因后来再次修改而自动重启循环。
func (s *WorldStore) QueueReviewRecheck(chapter int) error {
	reviews, err := s.LoadReviewsAffectingChapter(chapter)
	if err != nil {
		return err
	}
	for _, review := range reviews {
		if review.Recheck != nil && (review.Recheck.Result != nil || slices.Contains(review.Recheck.Chapters, chapter)) {
			continue
		}
		requested := false
		for _, issue := range review.Issues {
			if issue.RequiresChange && slices.Contains(issue.Chapters, chapter) {
				requested = true
				break
			}
		}
		if !requested {
			continue
		}
		if review.Recheck == nil {
			review.Recheck = &domain.ReviewRecheck{}
		}
		review.Recheck.Chapters = append(review.Recheck.Chapters, chapter)
		slices.Sort(review.Recheck.Chapters)
		if err := s.SaveReview(review); err != nil {
			return err
		}
	}
	return nil
}

// LoadPendingReviewRecheck 只读持久化评审元数据，不回读全书正文。
func (s *WorldStore) LoadPendingReviewRecheck() (*domain.ReviewEntry, error) {
	reviews, err := s.loadReviews()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(reviews, func(a, b domain.ReviewEntry) int {
		if a.Chapter != b.Chapter {
			return a.Chapter - b.Chapter
		}
		return cmp.Compare(a.Scope, b.Scope)
	})
	for i := range reviews {
		r := &reviews[i]
		if r.Recheck != nil && r.Recheck.Result == nil {
			return r, nil
		}
	}
	return nil, nil
}
