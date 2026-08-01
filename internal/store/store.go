package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// Store 是状态管理的组合根，持有所有子存储。
type Store struct {
	dir string

	Progress    *ProgressStore
	Outline     *OutlineStore
	Drafts      *DraftStore
	Summaries   *SummaryStore
	RunMeta     *RunMetaStore
	UserRules   *UserRulesStore
	Signals     *SignalStore
	Runtime     *RuntimeStore
	Characters  *CharacterStore
	Cast        *CastStore
	World       *WorldStore
	Checkpoints *CheckpointStore
	Sessions    *SessionStore
	Usage       *UsageStore
	Simulation  *SimulationStore
	Decisions   *DecisionStore

	crossMu sync.Mutex // 串行化跨域协调；不代表多个文件具备事务原子性
}

// NewStore 创建状态管理器，dir 为小说输出根目录。
func NewStore(dir string) *Store {
	io := newIO(dir)
	outline := NewOutlineStore(io)
	return &Store{
		dir:         dir,
		Progress:    NewProgressStore(newIO(dir)),
		Outline:     outline,
		Drafts:      NewDraftStore(newIO(dir)),
		Summaries:   NewSummaryStore(newIO(dir), outline),
		RunMeta:     NewRunMetaStore(newIO(dir)),
		UserRules:   NewUserRulesStore(newIO(dir)),
		Signals:     NewSignalStore(newIO(dir)),
		Runtime:     NewRuntimeStore(newIO(dir)),
		Characters:  NewCharacterStore(newIO(dir), outline),
		Cast:        NewCastStore(newIO(dir)),
		World:       NewWorldStore(newIO(dir)),
		Checkpoints: NewCheckpointStore(io),
		Sessions:    NewSessionStore(newIO(dir)),
		Usage:       NewUsageStore(newIO(dir)),
		Simulation:  NewSimulationStore(newIO(dir)),
		Decisions:   NewDecisionStore(newIO(dir)),
	}
}

// Dir 返回输出根目录。
func (s *Store) Dir() string { return s.dir }

// CheckConsistency 对事实层做一次浅层校验，用于启动/恢复时生成 warning。
// 纯只读：不修正数据，仅返回可读的问题描述。调用方决定如何展示（log / UI）。
// 为避免扫全目录带来的 IO 开销，只校验 Progress 的关键点：
//   - 最后一个完成章节必须在 chapters/ 下存在终稿
//   - Layered 模式下，当前 Volume/Arc 必须能在 layered_outline 中找到
func (s *Store) CheckConsistency() []string {
	var warnings []string
	progress, err := s.Progress.Load()
	if err != nil {
		return append(warnings, fmt.Sprintf("progress 读取失败: %v", err))
	}
	if progress == nil {
		return warnings
	}
	if n := len(progress.CompletedChapters); n > 0 {
		lastCh := progress.CompletedChapters[n-1]
		if text, err := s.Drafts.LoadChapterText(lastCh); err != nil {
			warnings = append(warnings, fmt.Sprintf("第 %d 章终稿读取失败: %v", lastCh, err))
		} else if text == "" {
			warnings = append(warnings, fmt.Sprintf("progress 标记第 %d 章已完成，但 chapters/%02d.md 不存在或为空", lastCh, lastCh))
		}
	}
	if progress.Layered && progress.CurrentVolume > 0 && progress.CurrentArc > 0 {
		volumes, err := s.Outline.LoadLayeredOutline()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("分层大纲读取失败: %v", err))
		} else if len(volumes) > 0 {
			found := false
			for _, v := range volumes {
				if v.Index != progress.CurrentVolume {
					continue
				}
				for _, a := range v.Arcs {
					if a.Index == progress.CurrentArc {
						found = true
						break
					}
				}
				break
			}
			if !found {
				warnings = append(warnings, fmt.Sprintf("progress 当前 V%d A%d 在分层大纲中找不到对应条目", progress.CurrentVolume, progress.CurrentArc))
			}
		}
	}
	return warnings
}

// FoundationMissing 返回基础设定中尚缺的项，按用于 Prompt/Reminder 的稳定顺序排列。
// 长篇模式（已有 layered_outline）额外要求 compass。读取失败必须原样返回，不能把
// 损坏或无权限读取的工件误判成“尚未创建”，否则调用方可能覆盖真实数据。
func (s *Store) FoundationMissing() ([]string, error) {
	var missing []string
	progress, err := s.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	premise, err := s.Outline.LoadPremise()
	if err != nil {
		return nil, fmt.Errorf("load premise: %w", err)
	}
	if premise == "" {
		missing = append(missing, "premise")
	}
	outline, err := s.Outline.LoadOutline()
	if err != nil {
		return nil, fmt.Errorf("load outline: %w", err)
	}
	if len(outline) == 0 {
		missing = append(missing, "outline")
	}
	characters, err := s.Characters.Load()
	if err != nil {
		return nil, fmt.Errorf("load characters: %w", err)
	}
	if len(characters) == 0 {
		missing = append(missing, "characters")
	}
	rules, err := s.World.LoadWorldRules()
	if err != nil {
		return nil, fmt.Errorf("load world rules: %w", err)
	}
	if len(rules) == 0 {
		missing = append(missing, "world_rules")
	}
	if progress != nil && progress.Layered {
		layered, err := s.Outline.LoadLayeredOutline()
		if err != nil {
			return nil, fmt.Errorf("load layered outline: %w", err)
		}
		if len(layered) == 0 {
			missing = append(missing, "layered_outline")
		}
		compass, err := s.Outline.LoadCompass()
		if err != nil {
			return nil, fmt.Errorf("load compass: %w", err)
		}
		if compass == nil {
			missing = append(missing, "compass")
		}
	}
	// 新书只有经过模型对已落盘工件的显式语义审查，才允许从规划进入写作。
	// PhaseWriting/Complete 代表旧书或已审查的新书，保持历史项目兼容；审查本身
	// 是一个动作而非文件缺失，因此只在其它工件齐全时追加。
	if len(missing) == 0 {
		if progress == nil || (progress.Phase != domain.PhaseWriting && progress.Phase != domain.PhaseComplete) {
			missing = append(missing, "foundation_audit")
		}
	}
	return missing, nil
}

// FoundationFingerprint 返回当前基础设定工件的内容指纹。Architect 必须把
// novel_context 读到的这个值原样交回审查工具，确保结论针对的是实际落盘版本，
// 而不是会话中尚未保存或已经过期的内容。
func (s *Store) FoundationFingerprint() (string, error) {
	files := []string{"premise.md", "outline.json", "characters.json", "world_rules.json"}
	progress, err := s.Progress.Load()
	if err != nil {
		return "", fmt.Errorf("load progress: %w", err)
	}
	if progress != nil && progress.Layered {
		files = append(files, "layered_outline.json", "meta/compass.json")
	}

	h := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", rel, err)
		}
		_, _ = h.Write([]byte(rel))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Init 创建所需的子目录结构。
func (s *Store) Init() error {
	if err := s.Checkpoints.InitError(); err != nil {
		return fmt.Errorf("load checkpoints: %w", err)
	}
	return s.Progress.io.EnsureDirs([]string{
		"chapters", "summaries", "drafts", "reviews", "meta", "meta/runtime", "meta/runtime/tasks", "meta/sessions", "meta/sessions/agents",
	})
}

// ── 跨域协调方法 ──

// ExpandArc 将骨架弧校准并展开为详细章节（Outline + Progress 联动）。
func (s *Store) ExpandArc(volumeIdx, arcIdx int, expansion domain.ArcExpansion) error {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()

	volumes, err := s.Outline.expandArcUnlocked(volumeIdx, arcIdx, expansion)
	if err != nil {
		return err
	}

	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return err
	}
	if p == nil {
		p = &domain.Progress{}
	}
	p.TotalChapters = domain.TotalChapters(volumes)
	return s.Progress.saveUnlocked(p)
}

// AppendVolume 追加新卷到分层大纲末尾（Outline + Progress 联动）。
func (s *Store) AppendVolume(vol domain.VolumeOutline) error {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()

	volumes, err := s.Outline.appendVolumeUnlocked(vol)
	if err != nil {
		return err
	}

	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return err
	}
	if p == nil {
		p = &domain.Progress{}
	}
	p.TotalChapters = domain.TotalChapters(volumes)
	return s.Progress.saveUnlocked(p)
}

// ReviseOutline 从 fromChapter 起替换尚未发生的计划尾段。
// 扁平大纲替换全书尾段；分层大纲只替换目标章所在弧的尾段。这个定义让同一载荷
// 重放仍得到同一结果，同时避免 JSON Patch 和 insert/delete 等操作枚举。
func (s *Store) ReviseOutline(fromChapter int, replacement []domain.OutlineEntry) (int, error) {
	if fromChapter <= 0 {
		return 0, fmt.Errorf("from_chapter must be > 0: %w", errs.ErrToolArgs)
	}

	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()
	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return 0, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if p == nil {
		return 0, fmt.Errorf("progress 未初始化: %w", errs.ErrToolPrecondition)
	}
	if p.Phase == domain.PhaseComplete {
		return 0, fmt.Errorf("全书已完结，不允许修改大纲: %w", errs.ErrToolPrecondition)
	}
	protected := p.InProgressChapter
	if latest := p.LatestCompleted(); latest > protected {
		protected = latest
	}
	if fromChapter <= protected {
		return 0, fmt.Errorf("第 %d 章已完成或正在写作；大纲修订必须从第 %d 章之后开始: %w",
			fromChapter, protected, errs.ErrToolPrecondition)
	}

	if p.Layered {
		volumes, err := s.Outline.reviseLayeredTailUnlocked(fromChapter, replacement)
		if err != nil {
			return 0, err
		}
		p.TotalChapters = domain.TotalChapters(volumes)
		if err := s.Progress.saveUnlocked(p); err != nil {
			return 0, fmt.Errorf("save progress: %w: %w", errs.ErrStoreWrite, err)
		}
		return p.TotalChapters, nil
	}

	outline, err := s.Outline.reviseFlatTailUnlocked(fromChapter, replacement)
	if err != nil {
		return 0, err
	}
	p.TotalChapters = len(outline)
	if err := s.Progress.saveUnlocked(p); err != nil {
		return 0, fmt.Errorf("save progress: %w: %w", errs.ErrStoreWrite, err)
	}
	return p.TotalChapters, nil
}

// ClearHandledSteer 清除 PendingSteer 并重置旧版 FlowSteering 状态。
// 两个文件无法组成文件系统事务，因此先写可重复的 Progress，最后才删除恢复意图；
// 任一步失败都至少保留 PendingSteer，下一次 Resume 可以安全重放。
func (s *Store) ClearHandledSteer() error {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.RunMeta.io.mu.Lock()
	defer s.RunMeta.io.mu.Unlock()
	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	meta, err := s.RunMeta.loadUnlocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	p, err := s.Progress.loadUnlocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if p != nil && p.Flow == domain.FlowSteering {
		if err := domain.ValidateFlowTransition(p.Flow, domain.FlowWriting); err != nil {
			return err
		}
		p.Flow = domain.FlowWriting
		if err := s.Progress.saveUnlocked(p); err != nil {
			return err
		}
	}
	if meta != nil && meta.PendingSteer != "" {
		meta.PendingSteer = ""
		if err := s.RunMeta.saveUnlocked(*meta); err != nil {
			return err
		}
	}
	return nil
}
