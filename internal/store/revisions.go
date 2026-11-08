package store

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const pendingRevisionPath = "meta/pending_revision.json"

type RevisionStore struct{ io *IO }

func NewRevisionStore(io *IO) *RevisionStore { return &RevisionStore{io: io} }

func (s *RevisionStore) LoadPending() (*domain.PendingRevision, error) {
	var pending domain.PendingRevision
	if err := s.io.ReadJSON(pendingRevisionPath, &pending); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &pending, nil
}

func (s *RevisionStore) SavePending(pending domain.PendingRevision) error {
	return s.io.WriteJSON(pendingRevisionPath, pending)
}

func (s *RevisionStore) ClearPending() error {
	return s.io.RemoveFile(pendingRevisionPath)
}

// InvalidateChapterAggregates 删除输入范围包含修订章节的模型派生工件。
// 章节级投影由 revision.Projector 单独重建。
func (s *Store) InvalidateChapterAggregates(fromChapter int) error {
	if fromChapter <= 0 {
		return fmt.Errorf("from chapter must be > 0")
	}
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return fmt.Errorf("读取分层大纲: %w", err)
	}
	chapter := 1
	arcEnds := make(map[[2]int]int)
	for _, volume := range volumes {
		volumeEnd := chapter - 1
		for _, arc := range volume.Arcs {
			if len(arc.Chapters) == 0 {
				continue
			}
			end := chapter + len(arc.Chapters) - 1
			arcEnds[[2]int{volume.Index, arc.Index}] = end
			if end >= fromChapter {
				if err := s.Summaries.io.RemoveFile(fmt.Sprintf("summaries/arc-v%02da%02d.json", volume.Index, arc.Index)); err != nil {
					return err
				}
				if err := s.Characters.io.RemoveFile(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume.Index, arc.Index)); err != nil {
					return err
				}
				if err := s.World.io.RemoveFile(ArcStyleRulesPath(volume.Index, arc.Index)); err != nil {
					return err
				}
			}
			chapter = end + 1
			volumeEnd = end
		}
		if volumeEnd >= fromChapter {
			if err := s.Summaries.io.RemoveFile(fmt.Sprintf("summaries/vol-v%02d.json", volume.Index)); err != nil {
				return err
			}
		}
	}
	if style, err := s.World.LoadStyleRules(); err != nil {
		return fmt.Errorf("读取写作规则: %w", err)
	} else if style != nil {
		end, ok := arcEnds[[2]int{style.Volume, style.Arc}]
		if !ok {
			return fmt.Errorf("写作规则引用未知弧 V%dA%d", style.Volume, style.Arc)
		}
		if end >= fromChapter {
			if err := s.World.io.RemoveFile("meta/style_rules.json"); err != nil {
				return err
			}
		}
	}
	return s.invalidateReviewsFrom(fromChapter)
}

// InvalidateRewriteAggregates 只失效返工所在弧及所属卷的派生工件。
// 在覆盖终稿前调用，使 PendingCommit 的重放可以完成中断的失效操作；
// 原评审保留为历史结论，记录改后版本，不因删除评审重新启动返工循环。
func (s *Store) InvalidateRewriteAggregates(version domain.ChapterVersion) error {
	boundary, err := s.Outline.CheckArcBoundary(version.Chapter)
	if err != nil {
		return err
	}
	reviews, err := s.World.LoadReviews()
	if err != nil {
		return err
	}
	for _, review := range reviews {
		covered := false
		switch review.Scope {
		case "chapter":
			covered = review.Chapter == version.Chapter
		case "global":
			covered = version.Chapter <= review.Chapter
		case "arc":
			covered = boundary != nil && review.Chapter == boundary.EndChapter
		}
		if !covered {
			continue
		}
		index := slices.IndexFunc(review.RevisedChapters, func(v domain.ChapterVersion) bool { return v.Chapter == version.Chapter })
		if index >= 0 {
			review.RevisedChapters[index] = version
		} else {
			review.RevisedChapters = append(review.RevisedChapters, version)
			slices.SortFunc(review.RevisedChapters, func(a, b domain.ChapterVersion) int { return a.Chapter - b.Chapter })
		}
		if err := s.World.SaveReview(review); err != nil {
			return err
		}
	}
	if boundary == nil {
		return nil // 非分层书没有弧/卷派生工件。
	}
	for _, path := range []string{
		fmt.Sprintf("summaries/arc-v%02da%02d.json", boundary.Volume, boundary.Arc),
		fmt.Sprintf("summaries/vol-v%02d.json", boundary.Volume),
		fmt.Sprintf("meta/snapshots/v%02da%02d.json", boundary.Volume, boundary.Arc),
		ArcStyleRulesPath(boundary.Volume, boundary.Arc),
	} {
		if err := s.World.io.RemoveFile(path); err != nil {
			return err
		}
	}
	rules, err := s.World.LoadStyleRules()
	if err != nil {
		return err
	}
	if rules != nil && rules.Volume == boundary.Volume && rules.Arc == boundary.Arc {
		return s.World.io.RemoveFile("meta/style_rules.json")
	}
	return nil
}

func (s *Store) invalidateReviewsFrom(fromChapter int) error {
	entries, err := os.ReadDir(s.World.io.path("reviews"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rel := "reviews/" + entry.Name()
		data, err := s.World.io.ReadFile(rel)
		if err != nil {
			return err
		}
		var review domain.ReviewEntry
		if err := json.Unmarshal(data, &review); err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		if review.Chapter >= fromChapter {
			if err := s.World.io.RemoveFile(rel); err != nil {
				return err
			}
		}
	}
	return nil
}
