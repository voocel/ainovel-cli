package diag

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Snapshot 是对 output 目录全部工件的只读快照。
// 所有规则函数只接收 Snapshot，不直接访问文件系统。
type Snapshot struct {
	Progress          *domain.Progress
	RunMeta           *domain.RunMeta
	Compass           *domain.StoryCompass
	Outline           []domain.OutlineEntry
	Volumes           []domain.VolumeOutline
	Characters        []domain.Character
	WorldRules        []domain.WorldRule
	Timeline          []domain.TimelineEvent
	Foreshadow        []domain.ForeshadowEntry
	Relationships     []domain.RelationshipEntry
	StateChanges      []domain.StateChange
	StyleRules        *domain.WritingStyleRules
	Reviews           map[int]*domain.ReviewEntry
	Plans             map[int]*domain.ChapterPlan
	Summaries         map[int]*domain.ChapterSummary
	ContextBoundaries []domain.RuntimeContextBoundary
	ContextEvidence   map[int]domain.ContextBuildEvidence
	ReviewEvidence    map[int]domain.ReviewOutcomeEvidence
	ChapterTraces     map[int]*chapterTrace

	LoadErrors []string // 非 NotExist 的加载失败，区分"无数据"和"读取出错"
}

type chapterTrace struct {
	Chapter               int
	Context               *domain.ContextBuildEvidence
	ReviewOutcome         *domain.ReviewOutcomeEvidence
	Review                *domain.ReviewEntry
	ContextCompacted      bool
	ContextCompactionKind string
}

// Load 从 store 中读取全部工件，构建只读快照。
// 文件不存在视为"无数据"（字段保持零值）；其他错误记录到 LoadErrors。
func Load(s *store.Store) Snapshot {
	snap := Snapshot{
		Reviews:         make(map[int]*domain.ReviewEntry),
		Plans:           make(map[int]*domain.ChapterPlan),
		Summaries:       make(map[int]*domain.ChapterSummary),
		ContextEvidence: make(map[int]domain.ContextBuildEvidence),
		ReviewEvidence:  make(map[int]domain.ReviewOutcomeEvidence),
		ChapterTraces:   make(map[int]*chapterTrace),
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
				trace := snap.ensureChapterTrace(ch)
				trace.Review = review
			} else {
				check(fmt.Sprintf("review_ch%d", ch), err)
			}
		}
	}

	// 加载上下文边界记录
	if s.Runtime != nil {
		items, err := s.Runtime.LoadQueue()
		check("runtime_queue", err)
		var pendingCompactionKind string
		for _, item := range items {
			switch item.Kind {
			case domain.RuntimeQueueContextEdge:
				raw, err := json.Marshal(item.Payload)
				if err != nil {
					continue
				}
				var b domain.RuntimeContextBoundary
				if json.Unmarshal(raw, &b) == nil {
					snap.ContextBoundaries = append(snap.ContextBoundaries, b)
					if b.Kind == "compacted" || b.Kind == "recovered" {
						pendingCompactionKind = b.Kind
					}
				}
			case domain.RuntimeQueueEvidence:
				raw, err := json.Marshal(item.Payload)
				if err != nil {
					continue
				}
				switch item.Category {
				case "context_build":
					var evidence domain.ContextBuildEvidence
					if json.Unmarshal(raw, &evidence) != nil || evidence.Chapter <= 0 || evidence.Mode != "chapter" {
						continue
					}
					snap.ContextEvidence[evidence.Chapter] = evidence
					trace := snap.ensureChapterTrace(evidence.Chapter)
					trace.Context = cloneContextEvidence(evidence)
					if pendingCompactionKind != "" {
						trace.ContextCompacted = true
						trace.ContextCompactionKind = pendingCompactionKind
						pendingCompactionKind = ""
					}
				case "review_outcome":
					var evidence domain.ReviewOutcomeEvidence
					if json.Unmarshal(raw, &evidence) != nil || evidence.Chapter <= 0 {
						continue
					}
					snap.ReviewEvidence[evidence.Chapter] = evidence
					trace := snap.ensureChapterTrace(evidence.Chapter)
					trace.ReviewOutcome = cloneReviewEvidence(evidence)
				}
			}
		}
	}

	return snap
}

func (s *Snapshot) ensureChapterTrace(chapter int) *chapterTrace {
	if s.ChapterTraces == nil {
		s.ChapterTraces = make(map[int]*chapterTrace)
	}
	if trace, ok := s.ChapterTraces[chapter]; ok {
		return trace
	}
	trace := &chapterTrace{Chapter: chapter}
	s.ChapterTraces[chapter] = trace
	return trace
}

func cloneContextEvidence(evidence domain.ContextBuildEvidence) *domain.ContextBuildEvidence {
	copy := evidence
	copy.WarnSections = append([]string(nil), evidence.WarnSections...)
	copy.TrimmedSections = append([]string(nil), evidence.TrimmedSections...)
	return &copy
}

func cloneReviewEvidence(evidence domain.ReviewOutcomeEvidence) *domain.ReviewOutcomeEvidence {
	copy := evidence
	copy.AffectedChapters = append([]int(nil), evidence.AffectedChapters...)
	copy.LowDimensions = append([]string(nil), evidence.LowDimensions...)
	copy.FailedDimensions = append([]string(nil), evidence.FailedDimensions...)
	copy.CriticalIssueTypes = append([]string(nil), evidence.CriticalIssueTypes...)
	copy.TopReasonCodes = append([]string(nil), evidence.TopReasonCodes...)
	return &copy
}

// CompletedCount 返回已完成章节数（安全访问）。
func (s *Snapshot) CompletedCount() int {
	if s.Progress == nil {
		return 0
	}
	return len(s.Progress.CompletedChapters)
}

// LatestCompleted 返回最大已完成章节号；无则返回 0。
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
