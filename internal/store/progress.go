package store

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// ProgressStore 管理创作Tiến độTrạng thái。
type ProgressStore struct{ io *IO }

func NewProgressStore(io *IO) *ProgressStore { return &ProgressStore{io: io} }

// Load Đọc meta/progress.json。不存在时Quay lại nil。
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

// Save LưuTiến độ。
func (s *ProgressStore) Save(p *domain.Progress) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.saveUnlocked(p)
}

func (s *ProgressStore) saveUnlocked(p *domain.Progress) error {
	return s.io.WriteJSONUnlocked("meta/progress.json", p)
}

// Init Tạo初始Tiến độ。
func (s *ProgressStore) Init(novelName string, totalChapters int) error {
	return s.Save(&domain.Progress{
		NovelName:     novelName,
		Phase:         domain.PhaseInit,
		TotalChapters: totalChapters,
	})
}

// SetTotalChapters 设定总Chương数。
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

// SetNovelName Thiết lập作品Tên sách，Rỗng值会被忽略。
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

// UpdatePhase 更Mới创作阶段。
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

// StartChapter 标记某章进入Viết中Trạng thái。纯 IO，不做Trạng thái验证。
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

// IsChapterCompleted Kiểm traChươngCó czy không已NộpHoàn thành。
func (s *ProgressStore) IsChapterCompleted(chapter int) bool {
	p, err := s.Load()
	if err != nil || p == nil {
		return false
	}
	return slices.Contains(p.CompletedChapters, chapter)
}

// MarkChapterComplete 标记ChươngHoàn thành，原子性更MớiTiến độ。
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

// MarkComplete 标记全书创作Hoàn thành，并清除重开返工标记（完结即不再处于返工态）。
func (s *ProgressStore) MarkComplete() error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		if err := domain.ValidatePhaseTransition(p.Phase, domain.PhaseComplete); err != nil {
			return err
		}
		p.Phase = domain.PhaseComplete
		p.ReopenedFromComplete = false
		return s.saveUnlocked(p)
	})
}

// Reopen 把已完结的书重Mới打开进入返工态：phase complete→writing + 目标章入队 + flow=rewriting，
// 在一次写锁内原子Hoàn thành。这是 phaseOrder“只前进”约束的唯一豁免出口——故意不走
// ValidatePhaseTransition；回退的合法性收敛在本方法、且受 phase=complete 前置守卫保护，
// 避免误用导致Trạng thái机失控。改完队列后 commit_chapter 会自动重Mới收尾完结。
func (s *ProgressStore) Reopen(chapters []int, reason string) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("progress 未初始化: %w", errs.ErrToolPrecondition)
		}
		if p.Phase != domain.PhaseComplete {
			return fmt.Errorf("reopen 仅适用于已完结的书（Hiện tại phase=%s）: %w", p.Phase, errs.ErrToolPrecondition)
		}
		normalized, err := normalizePendingRewrites(chapters, p.CompletedChapters)
		if err != nil {
			return err
		}
		p.Phase = domain.PhaseWriting // 唯一合法回退，受上面 complete 前置约束保护
		p.PendingRewrites = normalized
		p.RewriteReason = reason
		p.Flow = domain.FlowRewriting
		p.ReopenedFromComplete = true // 排Rỗng后按结构完整重Mới完结，见 commit_chapter drain 块
		return s.saveUnlocked(p)
	})
}

// ClearInProgress 清除Tiến độ中间Trạng thái。
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

// UpdateVolumeArc 更MớiHiện tại卷弧位置。
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

// SetLayered Thiết lập分层模式标志。
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

// SetFlow 更MớiHiện tại流程Trạng thái。
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

// SetPendingRewrites Thiết lập待Viết lại chương队列和原因。
// PendingRewrites 只允许包含Đã hoàn thànhChương；未Hoàn thànhChương还没有终稿，不能进入重写/打磨队列。
func (s *ProgressStore) SetPendingRewrites(chapters []int, reason string) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		normalized, err := normalizePendingRewrites(chapters, p.CompletedChapters)
		if err != nil {
			return err
		}
		p.PendingRewrites = normalized
		p.RewriteReason = reason
		return s.saveUnlocked(p)
	})
}

// ValidatePendingRewrites 校验Chương列表Có czy không可进入返工队列，不修改Trạng thái。
func (s *ProgressStore) ValidatePendingRewrites(chapters []int) error {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()

	p, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if p == nil {
		_, err := normalizePendingRewrites(chapters, nil)
		return err
	}
	_, err = normalizePendingRewrites(chapters, p.CompletedChapters)
	return err
}

// CompleteRewrite 从待重写队列中移除Đã hoàn thành的Chương。
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

// ClearPendingRewrites 强制清Rỗng重写队列。
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

// ValidateChapterWork 校验Hiện tạiChươngCó czy không允许被规划或Nộp。
// 打磨/重写流程下，只允许处理 PendingRewrites 中的Chương。
func (s *ProgressStore) ValidateChapterWork(chapter int) error {
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
	if _, err := normalizePendingRewrites(p.PendingRewrites, p.CompletedChapters); err != nil {
		return err
	}
	if slices.Contains(p.PendingRewrites, chapter) {
		return nil
	}

	verb := "重写"
	if p.Flow == domain.FlowPolishing {
		verb = "打磨"
	}
	return fmt.Errorf("第 %d 章不在待%s队列中，Hiện tại队列：%v。Vui lòng先处理队列内Chương，再动MớiChương: %w", chapter, verb, p.PendingRewrites, errs.ErrToolConflict)
}

func normalizePendingRewrites(chapters, completed []int) ([]int, error) {
	if len(chapters) == 0 {
		return nil, nil
	}
	completedSet := make(map[int]struct{}, len(completed))
	for _, ch := range completed {
		completedSet[ch] = struct{}{}
	}

	seen := make(map[int]struct{}, len(chapters))
	normalized := make([]int, 0, len(chapters))
	var invalid []int
	for _, ch := range chapters {
		if ch <= 0 {
			invalid = append(invalid, ch)
			continue
		}
		if _, ok := completedSet[ch]; !ok {
			invalid = append(invalid, ch)
			continue
		}
		if _, ok := seen[ch]; ok {
			continue
		}
		seen[ch] = struct{}{}
		normalized = append(normalized, ch)
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("pending_rewrites 只能包含Đã hoàn thànhChương，非法Chương：%v，completed_chapters=%v: %w", invalid, completed, errs.ErrToolPrecondition)
	}
	return normalized, nil
}
