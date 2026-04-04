package domain

import "strings"

// Phase 表示小说创作阶段。
type Phase string

const (
	PhaseInit     Phase = "init"
	PhasePremise  Phase = "premise"
	PhaseOutline  Phase = "outline"
	PhaseWriting  Phase = "writing"
	PhaseComplete Phase = "complete"
)

// FlowState 当前活动流程类型，用于 checkpoint 恢复。
type FlowState string

const (
	FlowWriting   FlowState = "writing"
	FlowReviewing FlowState = "reviewing"
	FlowRewriting FlowState = "rewriting"
	FlowPolishing FlowState = "polishing"
	FlowSteering  FlowState = "steering"
)

// PlanningTier 表示作品规划的长度级别。
type PlanningTier string

const (
	PlanningTierShort PlanningTier = "short"
	PlanningTierMid   PlanningTier = "mid"
	PlanningTierLong  PlanningTier = "long"
)

// Progress 进度追踪，持久化到 meta/progress.json。
type Progress struct {
	NovelName         string      `json:"novel_name"`
	Phase             Phase       `json:"phase"`
	CurrentChapter    int         `json:"current_chapter"`
	TotalChapters     int         `json:"total_chapters"`
	CompletedChapters []int       `json:"completed_chapters"`
	TotalWordCount    int         `json:"total_word_count"`
	ChapterWordCounts map[int]int `json:"chapter_word_counts,omitempty"` // 每章字数，支持重写时修正总字数
	InProgressChapter int         `json:"in_progress_chapter,omitempty"` // 正在写作的章节（场景级恢复）
	CompletedScenes   []int       `json:"completed_scenes,omitempty"`    // 当前章节已完成的场景编号
	Flow              FlowState   `json:"flow,omitempty"`                // 当前流程
	PendingRewrites   []int       `json:"pending_rewrites,omitempty"`    // 待重写章节队列
	RewriteReason     string      `json:"rewrite_reason,omitempty"`      // 重写原因
	StrandHistory     []string    `json:"strand_history,omitempty"`      // 按章节顺序记录 dominant_strand
	HookHistory       []string    `json:"hook_history,omitempty"`        // 按章节顺序记录 hook_type
	// 长篇分层追踪（仅长篇模式使用，短篇/中篇为零值）
	CurrentVolume int  `json:"current_volume,omitempty"`
	CurrentArc    int  `json:"current_arc,omitempty"`
	Layered       bool `json:"layered,omitempty"`
}

// IsResumable 判断是否可以从断点恢复。
func (p *Progress) IsResumable() bool {
	return p.Phase == PhaseWriting && p.CurrentChapter > 0
}

// NextChapter 返回下一个要写的章节号。
func (p *Progress) NextChapter() int {
	if len(p.CompletedChapters) == 0 {
		return 1
	}
	max := 0
	for _, ch := range p.CompletedChapters {
		if ch > max {
			max = ch
		}
	}
	return max + 1
}

// ExtractNovelNameFromPremise 只按第一条非空行提取书名。
// 兼容两种格式：
// 1. # 书名
// 2. 书名：书名 / 书名:书名
func ExtractNovelNameFromPremise(premise string) string {
	lines := strings.Split(strings.ReplaceAll(premise, "\r\n", "\n"), "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
		for _, prefix := range []string{"书名：", "书名:"} {
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
		return ""
	}
	return ""
}

// ContextProfile 上下文加载策略，根据总章节数自适应。
type ContextProfile struct {
	SummaryWindow  int  // 加载最近 N 章摘要
	TimelineWindow int  // 加载最近 N 章时间线
	Layered        bool // true = 启用分层摘要加载（卷摘要+弧摘要+章摘要）
}

// MemoryPolicy 表示运行时共享的记忆使用策略。
// 它既用于上下文输出，也用于宿主层的 handoff / reminder 决策。
type MemoryPolicy struct {
	Mode                string `json:"mode,omitempty"`
	SummaryWindow       int    `json:"summary_window,omitempty"`
	TimelineWindow      int    `json:"timeline_window,omitempty"`
	LayeredSummaries    bool   `json:"layered_summaries,omitempty"`
	SummaryStrategy     string `json:"summary_strategy,omitempty"`
	WorkingRefresh      string `json:"working_refresh,omitempty"`
	EpisodicRefresh     string `json:"episodic_refresh,omitempty"`
	PlanningRefresh     string `json:"planning_refresh,omitempty"`
	FoundationRefresh   string `json:"foundation_refresh,omitempty"`
	PlanningFocus       string `json:"planning_focus,omitempty"`
	FoundationFocus     string `json:"foundation_focus,omitempty"`
	PreviousTailChars   int    `json:"previous_tail_chars,omitempty"`
	ChapterPlanEnabled  bool   `json:"chapter_plan_enabled,omitempty"`
	RelatedLookup       bool   `json:"related_chapter_lookup,omitempty"`
	CurrentOutlineBound bool   `json:"current_outline_bound,omitempty"`
	TotalChapters       int    `json:"total_chapters,omitempty"`
	HandoffPreferred    bool   `json:"handoff_preferred,omitempty"`
	ReadOnlyThreshold   int    `json:"read_only_threshold,omitempty"`
}

// NewContextProfile 根据总章节数计算上下文策略。
func NewContextProfile(totalChapters int) ContextProfile {
	switch {
	case totalChapters <= 15:
		return ContextProfile{SummaryWindow: 10, TimelineWindow: 10}
	case totalChapters <= 50:
		return ContextProfile{SummaryWindow: 5, TimelineWindow: 8}
	default:
		return ContextProfile{SummaryWindow: 3, TimelineWindow: 5, Layered: true}
	}
}

// NewChapterMemoryPolicy 根据进度与上下文策略生成章节运行时记忆策略。
func NewChapterMemoryPolicy(progress *Progress, profile ContextProfile, currentOutlineBound bool) MemoryPolicy {
	policy := MemoryPolicy{
		Mode:                "chapter",
		SummaryWindow:       profile.SummaryWindow,
		TimelineWindow:      profile.TimelineWindow,
		LayeredSummaries:    profile.Layered,
		WorkingRefresh:      "每次按章节加载时刷新",
		EpisodicRefresh:     "随章节提交、评审和长篇状态变更刷新",
		PreviousTailChars:   800,
		ChapterPlanEnabled:  true,
		CurrentOutlineBound: currentOutlineBound,
		ReadOnlyThreshold:   5,
	}
	if profile.Layered {
		policy.SummaryStrategy = "卷摘要+弧摘要+最近章节摘要"
	} else {
		policy.SummaryStrategy = "最近章节摘要"
	}
	if progress != nil {
		policy.TotalChapters = progress.TotalChapters
		if progress.TotalChapters > 30 {
			policy.RelatedLookup = true
		}
		if progress.Flow == FlowReviewing || progress.Flow == FlowRewriting || progress.Flow == FlowPolishing {
			policy.HandoffPreferred = true
		}
		if progress.Layered && len(progress.CompletedChapters) >= 6 {
			policy.HandoffPreferred = true
		}
		if len(progress.CompletedChapters) >= 12 {
			policy.HandoffPreferred = true
		}
		if progress.Layered && len(progress.CompletedChapters) >= 6 {
			policy.ReadOnlyThreshold = 4
		}
		if len(progress.CompletedChapters) >= 12 {
			policy.ReadOnlyThreshold = 4
		}
	}
	return policy
}

// NewArchitectMemoryPolicy 返回规划阶段使用的记忆策略。
func NewArchitectMemoryPolicy() MemoryPolicy {
	return MemoryPolicy{
		Mode:               "architect",
		PlanningRefresh:    "卷弧结构、指南针或摘要更新时刷新",
		FoundationRefresh:  "角色、伏笔、设定变更时刷新",
		PlanningFocus:      "分层大纲、指南针、卷摘要",
		FoundationFocus:    "角色设定、角色快照、伏笔台账",
		HandoffPreferred:   true,
		ChapterPlanEnabled: false,
		ReadOnlyThreshold:  4,
	}
}

// RunMeta 运行元信息，持久化到 meta/run.json。
type RunMeta struct {
	StartedAt    string       `json:"started_at"`
	Provider     string       `json:"provider,omitempty"`
	Style        string       `json:"style"`
	Model        string       `json:"model"`
	PlanningTier PlanningTier `json:"planning_tier,omitempty"`
	SteerHistory []SteerEntry `json:"steer_history,omitempty"`
	PendingSteer string       `json:"pending_steer,omitempty"` // 未完成的 Steer 指令，中断恢复时重新注入
}

// SteerEntry 用户干预记录。
type SteerEntry struct {
	Input     string `json:"input"`
	Timestamp string `json:"timestamp"`
}
