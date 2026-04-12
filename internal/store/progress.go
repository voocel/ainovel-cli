package store

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/apperr"
	"github.com/voocel/ainovel-cli/internal/domain"
)

// ProgressStore 管理创作进度状态。
type ProgressStore struct{ io *IO }

func NewProgressStore(io *IO) *ProgressStore { return &ProgressStore{io: io} }

// Load 读取 meta/progress.json。不存在时返回 nil。
func (s *ProgressStore) Load() (*domain.Progress, error) {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	return s.loadUnlocked()
}

func (s *ProgressStore) loadUnlocked() (*domain.Progress, error) {
	var p domain.Progress
	if err := s.io.ReadJSONUnlocked("meta/progress.json", &p); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// Save 保存进度。
func (s *ProgressStore) Save(p *domain.Progress) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.saveUnlocked(p)
}

func (s *ProgressStore) saveUnlocked(p *domain.Progress) error {
	return s.io.WriteJSONUnlocked("meta/progress.json", p)
}

// Init 创建初始进度。
func (s *ProgressStore) Init(novelName string, totalChapters int) error {
	return s.Save(&domain.Progress{
		NovelName:     novelName,
		Phase:         domain.PhaseInit,
		TotalChapters: totalChapters,
	})
}

// SetTotalChapters 设定总章节数。
func (s *ProgressStore) SetTotalChapters(n int) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		p.TotalChapters = n
		return s.saveUnlocked(p)
	})
}

// SetNovelName 设置作品书名，空值会被忽略。
func (s *ProgressStore) SetNovelName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		p.NovelName = name
		return s.saveUnlocked(p)
	})
}

// UpdatePhase 更新创作阶段。
func (s *ProgressStore) UpdatePhase(phase domain.Phase) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		if err := domain.ValidatePhaseTransition(p.Phase, phase); err != nil {
			return err
		}
		p.Phase = phase
		return s.saveUnlocked(p)
	})
}

// StartChapter 标记某章进入写作中状态。纯 IO，不做状态验证。
func (s *ProgressStore) StartChapter(chapter int) error {
	if chapter <= 0 {
		return fmt.Errorf("chapter must be > 0")
	}
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		p.Phase = domain.PhaseWriting
		if p.Flow != domain.FlowRewriting && p.Flow != domain.FlowPolishing {
			p.Flow = domain.FlowWriting
		}
		if p.CurrentChapter < chapter {
			p.CurrentChapter = chapter
		}
		p.InProgressChapter = chapter
		p.CompletedScenes = nil
		return s.saveUnlocked(p)
	})
}

// IsChapterCompleted 检查章节是否已提交完成。
func (s *ProgressStore) IsChapterCompleted(chapter int) bool {
	p, err := s.Load()
	if err != nil || p == nil {
		return false
	}
	return slices.Contains(p.CompletedChapters, chapter)
}

// MarkChapterComplete 标记章节完成，原子性更新进度。
func (s *ProgressStore) MarkChapterComplete(chapter, wordCount int, hookType, dominantStrand string) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("progress not initialized, call Init first")
		}
		if p.ChapterWordCounts == nil {
			p.ChapterWordCounts = make(map[int]int)
		}
		if oldWC, ok := p.ChapterWordCounts[chapter]; ok {
			p.TotalWordCount -= oldWC
		}
		p.ChapterWordCounts[chapter] = wordCount
		p.TotalWordCount += wordCount
		if !slices.Contains(p.CompletedChapters, chapter) {
			p.CompletedChapters = append(p.CompletedChapters, chapter)
		}
		if chapter+1 > p.CurrentChapter {
			p.CurrentChapter = chapter + 1
		}
		p.InProgressChapter = 0
		p.CompletedScenes = nil
		if err := domain.ValidatePhaseTransition(p.Phase, domain.PhaseWriting); err != nil {
			return err
		}
		p.Phase = domain.PhaseWriting

		if dominantStrand != "" {
			for len(p.StrandHistory) < chapter-1 {
				p.StrandHistory = append(p.StrandHistory, "")
			}
			if len(p.StrandHistory) < chapter {
				p.StrandHistory = append(p.StrandHistory, dominantStrand)
			} else {
				p.StrandHistory[chapter-1] = dominantStrand
			}
		}
		if hookType != "" {
			for len(p.HookHistory) < chapter-1 {
				p.HookHistory = append(p.HookHistory, "")
			}
			if len(p.HookHistory) < chapter {
				p.HookHistory = append(p.HookHistory, hookType)
			} else {
				p.HookHistory[chapter-1] = hookType
			}
		}

		return s.saveUnlocked(p)
	})
}

// MarkComplete 标记全书创作完成。
func (s *ProgressStore) MarkComplete() error {
	return s.UpdatePhase(domain.PhaseComplete)
}

// ClearInProgress 清除进度中间状态。
func (s *ProgressStore) ClearInProgress() error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.InProgressChapter = 0
		p.CompletedScenes = nil
		return s.saveUnlocked(p)
	})
}

// UpdateVolumeArc 更新当前卷弧位置。
func (s *ProgressStore) UpdateVolumeArc(volume, arc int) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.CurrentVolume = volume
		p.CurrentArc = arc
		return s.saveUnlocked(p)
	})
}

// SetLayered 设置分层模式标志。
func (s *ProgressStore) SetLayered(layered bool) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.Layered = layered
		return s.saveUnlocked(p)
	})
}

// SetFlow 更新当前流程状态。
func (s *ProgressStore) SetFlow(flow domain.FlowState) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		if err := domain.ValidateFlowTransition(p.Flow, flow); err != nil {
			return err
		}
		p.Flow = flow
		return s.saveUnlocked(p)
	})
}

// SetPendingRewrites 设置待重写章节队列和原因。
func (s *ProgressStore) SetPendingRewrites(chapters []int, reason string) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.PendingRewrites = chapters
		p.RewriteReason = reason
		return s.saveUnlocked(p)
	})
}

// CompleteRewrite 从待重写队列中移除已完成的章节。
func (s *ProgressStore) CompleteRewrite(chapter int) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		var remaining []int
		for _, ch := range p.PendingRewrites {
			if ch != chapter {
				remaining = append(remaining, ch)
			}
		}
		p.PendingRewrites = remaining
		if len(remaining) == 0 {
			if err := domain.ValidateFlowTransition(p.Flow, domain.FlowWriting); err != nil {
				return err
			}
			p.Flow = domain.FlowWriting
			p.RewriteReason = ""
		}
		return s.saveUnlocked(p)
	})
}

// ClearPendingRewrites 强制清空重写队列。
func (s *ProgressStore) ClearPendingRewrites() error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.PendingRewrites = nil
		p.RewriteReason = ""
		if err := domain.ValidateFlowTransition(p.Flow, domain.FlowWriting); err != nil {
			return err
		}
		p.Flow = domain.FlowWriting
		return s.saveUnlocked(p)
	})
}

// ValidateChapterCommit 校验当前章节是否允许提交。
func (s *ProgressStore) ValidateChapterCommit(chapter int) error {
	p, err := s.Load()
	if err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	if p.Flow != domain.FlowRewriting && p.Flow != domain.FlowPolishing {
		return nil
	}
	if slices.Contains(p.PendingRewrites, chapter) {
		return nil
	}

	verb := "重写"
	if p.Flow == domain.FlowPolishing {
		verb = "打磨"
	}
	return apperr.New(
		apperr.CodeToolConflict,
		"store.validate_chapter_commit",
		fmt.Sprintf("第 %d 章不在待%s队列中，当前队列：%v", chapter, verb, p.PendingRewrites),
	)
}
