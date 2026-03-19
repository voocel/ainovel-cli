package store

import (
	"fmt"
	"os"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// LoadProgress 读取 meta/progress.json。不存在时返回 nil。
func (s *Store) LoadProgress() (*domain.Progress, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadProgressUnlocked()
}

func (s *Store) loadProgressUnlocked() (*domain.Progress, error) {
	var p domain.Progress
	if err := s.readJSONUnlocked("meta/progress.json", &p); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// SaveProgress 保存进度到 meta/progress.json。
func (s *Store) SaveProgress(p *domain.Progress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveProgressUnlocked(p)
}

func (s *Store) saveProgressUnlocked(p *domain.Progress) error {
	return s.writeJSONUnlocked("meta/progress.json", p)
}

// InitProgress 创建初始进度。
func (s *Store) InitProgress(novelName string, totalChapters int) error {
	return s.SaveProgress(&domain.Progress{
		NovelName:     novelName,
		Phase:         domain.PhaseInit,
		TotalChapters: totalChapters,
	})
}

// SetTotalChapters 根据大纲长度设定总章节数。
func (s *Store) SetTotalChapters(n int) error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		p.TotalChapters = n
		return s.saveProgressUnlocked(p)
	})
}

// UpdatePhase 更新创作阶段。
func (s *Store) UpdatePhase(phase domain.Phase) error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		p.Phase = phase
		return s.saveProgressUnlocked(p)
	})
}

// MarkChapterComplete 标记章节完成，原子性更新进度。
// 支持重写场景：如果章节已完成，先减去旧字数再加新字数。
// hookType 和 dominantStrand 用于节奏追踪，可为空。
func (s *Store) MarkChapterComplete(chapter, wordCount int, hookType, dominantStrand string) error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("progress not initialized, call InitProgress first")
		}
		if p.ChapterWordCounts == nil {
			p.ChapterWordCounts = make(map[int]int)
		}
		// 重写场景：减去旧字数
		if oldWC, ok := p.ChapterWordCounts[chapter]; ok {
			p.TotalWordCount -= oldWC
		}
		p.ChapterWordCounts[chapter] = wordCount
		p.TotalWordCount += wordCount
		if !slices.Contains(p.CompletedChapters, chapter) {
			p.CompletedChapters = append(p.CompletedChapters, chapter)
		}
		// 仅在正常推进时更新 CurrentChapter，重写旧章节不回退指针
		if chapter+1 > p.CurrentChapter {
			p.CurrentChapter = chapter + 1
		}
		p.InProgressChapter = 0
		p.CompletedScenes = nil
		p.Phase = domain.PhaseWriting

		// 节奏追踪：按章节顺序填充 history（确保索引对齐）
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

		return s.saveProgressUnlocked(p)
	})
}

// MarkComplete 标记全书创作完成。
func (s *Store) MarkComplete() error {
	return s.UpdatePhase(domain.PhaseComplete)
}

// SaveLastCommit 保存最近一次 commit 结果到 meta/last_commit.json。
// 用于宿主程序读取结构化信号。
func (s *Store) SaveLastCommit(result domain.CommitResult) error {
	return s.writeJSON("meta/last_commit.json", result)
}

// LoadLastCommit 读取最近一次 commit 结果。
func (s *Store) LoadLastCommit() (*domain.CommitResult, error) {
	var r domain.CommitResult
	if err := s.readJSON("meta/last_commit.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// LoadAndClearLastCommit 原子性读取并清除 commit 信号，防止 TOCTOU 竞态。
func (s *Store) LoadAndClearLastCommit() (*domain.CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r domain.CommitResult
	if err := s.readJSONUnlocked("meta/last_commit.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = s.removeFileUnlocked("meta/last_commit.json")
	return &r, nil
}

// ClearInProgress 清除进度中间状态（章节提交后调用）。
func (s *Store) ClearInProgress() error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.InProgressChapter = 0
		p.CompletedScenes = nil
		return s.saveProgressUnlocked(p)
	})
}

// ClearLastCommit 清除 commit 信号文件，防止重复消费。
func (s *Store) ClearLastCommit() error {
	return s.removeFile("meta/last_commit.json")
}

// UpdateVolumeArc 更新当前卷弧位置。
func (s *Store) UpdateVolumeArc(volume, arc int) error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.CurrentVolume = volume
		p.CurrentArc = arc
		return s.saveProgressUnlocked(p)
	})
}

// SetLayered 设置分层模式标志。
func (s *Store) SetLayered(layered bool) error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.Layered = layered
		return s.saveProgressUnlocked(p)
	})
}

// SetFlow 更新当前流程状态。
func (s *Store) SetFlow(flow domain.FlowState) error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.Flow = flow
		return s.saveProgressUnlocked(p)
	})
}

// SetPendingRewrites 设置待重写章节队列和原因。
func (s *Store) SetPendingRewrites(chapters []int, reason string) error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.PendingRewrites = chapters
		p.RewriteReason = reason
		return s.saveProgressUnlocked(p)
	})
}

// CompleteRewrite 从待重写队列中移除已完成的章节。
// 队列清空时自动将 Flow 重置为 writing 并清除 RewriteReason。
func (s *Store) CompleteRewrite(chapter int) error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
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
			p.Flow = domain.FlowWriting
			p.RewriteReason = ""
		}
		return s.saveProgressUnlocked(p)
	})
}

// ClearPendingRewrites 强制清空重写队列。
func (s *Store) ClearPendingRewrites() error {
	return s.withWriteLock(func() error {
		p, err := s.loadProgressUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.PendingRewrites = nil
		p.RewriteReason = ""
		p.Flow = domain.FlowWriting
		return s.saveProgressUnlocked(p)
	})
}

// ValidateChapterCommit 校验当前章节是否允许提交。
// 在重写/打磨流程中，只允许提交待处理队列中的章节。
func (s *Store) ValidateChapterCommit(chapter int) error {
	p, err := s.LoadProgress()
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
	return fmt.Errorf("第 %d 章不在待%s队列中，当前队列：%v", chapter, verb, p.PendingRewrites)
}
