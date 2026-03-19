package domain

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

// ContextProfile 上下文加载策略，根据总章节数自适应。
type ContextProfile struct {
	SummaryWindow  int  // 加载最近 N 章摘要
	TimelineWindow int  // 加载最近 N 章时间线
	Layered        bool // true = 启用分层摘要加载（卷摘要+弧摘要+章摘要）
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
