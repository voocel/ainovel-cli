package diag

import (
	"errors"
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Snapshot 是对 output Thư mụcTất cả工件的只读Chụp。
// 所有规则函数只接收 Snapshot，不直接访问Tập tin系统。
type Snapshot struct {
	Progress      *domain.Progress
	RunMeta       *domain.RunMeta
	Compass       *domain.StoryCompass
	Outline       []domain.OutlineEntry
	Volumes       []domain.VolumeOutline
	Characters    []domain.Character
	CastLedger    []domain.CastEntry
	WorldRules    []domain.WorldRule
	Timeline      []domain.TimelineEvent
	Foreshadow    []domain.ForeshadowEntry
	Relationships []domain.RelationshipEntry
	StateChanges  []domain.StateChange
	StyleRules    *domain.WritingStyleRules
	Reviews       map[int]*domain.ReviewEntry
	Plans         map[int]*domain.ChapterPlan
	Summaries     map[int]*domain.ChapterSummary

	LoadErrors []string // 非 NotExist 的加载Thất bại，区分"Không có数据"和"Đọc出错"
}

// Load 从 store 中ĐọcTất cả工件，构建只读Chụp。
// Tập tin不存在视为"Không có数据"（字段保持零值）；KhácLỗi记录到 LoadErrors。
func Load(s *store.Store) Snapshot {
	snap := Snapshot{
		Reviews:   make(map[int]*domain.ReviewEntry),
		Plans:     make(map[int]*domain.ChapterPlan),
		Summaries: make(map[int]*domain.ChapterSummary),
	}

	check := func(name string, err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			snap.LoadErrors = append(snap.LoadErrors, fmt.Sprintf("%s: %v", name, err))
		}
	}

	var err error
	snap.Progress, err = s.Progress.Load()
	check("progress", err)
	snap.RunMeta, err = s.RunMeta.Load()
	check("run_meta", err)
	snap.Compass, err = s.Outline.LoadCompass()
	check("compass", err)
	snap.Outline, err = s.Outline.LoadOutline()
	check("outline", err)
	snap.Volumes, err = s.Outline.LoadLayeredOutline()
	check("volumes", err)
	snap.Characters, err = s.Characters.Load()
	check("characters", err)
	snap.CastLedger, err = s.Cast.Load()
	check("cast_ledger", err)
	snap.WorldRules, err = s.World.LoadWorldRules()
	check("world_rules", err)
	snap.Timeline, err = s.World.LoadTimeline()
	check("timeline", err)
	snap.Foreshadow, err = s.World.LoadForeshadowLedger()
	check("foreshadow", err)
	snap.Relationships, err = s.World.LoadRelationships()
	check("relationships", err)
	snap.StateChanges, err = s.World.LoadStateChanges()
	check("state_changes", err)
	snap.StyleRules, err = s.World.LoadStyleRules()
	check("style_rules", err)

	if snap.Progress != nil {
		for _, ch := range snap.Progress.CompletedChapters {
			if plan, err := s.Drafts.LoadChapterPlan(ch); err == nil && plan != nil {
				snap.Plans[ch] = plan
			} else {
				check(fmt.Sprintf("plan_ch%d", ch), err)
			}
			if summary, err := s.Summaries.LoadSummary(ch); err == nil && summary != nil {
				snap.Summaries[ch] = summary
			} else {
				check(fmt.Sprintf("summary_ch%d", ch), err)
			}
			if review, err := s.World.LoadReview(ch); err == nil && review != nil {
				snap.Reviews[ch] = review
			} else {
				check(fmt.Sprintf("review_ch%d", ch), err)
			}
		}
	}

	return snap
}

// CompletedCount Quay lạiĐã hoàn thànhChương数（安全访问）。
func (s *Snapshot) CompletedCount() int {
	if s.Progress == nil {
		return 0
	}
	return len(s.Progress.CompletedChapters)
}

// LatestCompleted Quay lại最大Đã hoàn thànhChương号；Không có则Quay lại 0。
func (s *Snapshot) LatestCompleted() int {
	if s.Progress == nil {
		return 0
	}
	max := 0
	for _, ch := range s.Progress.CompletedChapters {
		if ch > max {
			max = ch
		}
	}
	return max
}
