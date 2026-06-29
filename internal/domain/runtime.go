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

// FlowState Hiện tại活动流程类型，用于 checkpoint Phục hồi。
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

// Progress Tiến độ追踪，持久化到 meta/progress.json。
type Progress struct {
	NovelName         string      `json:"novel_name"`
	Phase             Phase       `json:"phase"`
	CurrentChapter    int         `json:"current_chapter"`
	TotalChapters     int         `json:"total_chapters"`
	CompletedChapters []int       `json:"completed_chapters"`
	TotalWordCount    int         `json:"total_word_count"`
	ChapterWordCounts map[int]int `json:"chapter_word_counts,omitempty"` // 每章字数，支持重写时修正总字数
	InProgressChapter int         `json:"in_progress_chapter,omitempty"` // ĐangViết的Chương（Cảnh级Phục hồi）
	CompletedScenes   []int       `json:"completed_scenes,omitempty"`    // Hiện tạiChươngĐã hoàn thành的Cảnh编号
	Flow              FlowState   `json:"flow,omitempty"`                // Hiện tại流程
	PendingRewrites   []int       `json:"pending_rewrites,omitempty"`    // 待Viết lại chương队列
	RewriteReason     string      `json:"rewrite_reason,omitempty"`      // 重写原因
	StrandHistory     []string    `json:"strand_history,omitempty"`      // 按Chương顺序记录 dominant_strand
	HookHistory       []string    `json:"hook_history,omitempty"`        // 按Chương顺序记录 hook_type
	// 长篇分层追踪（仅长篇模式使用，短篇/中篇为零值）
	CurrentVolume int  `json:"current_volume,omitempty"`
	CurrentArc    int  `json:"current_arc,omitempty"`
	Layered       bool `json:"layered,omitempty"`
	// ReopenedFromComplete 标记本书是经 reopen 从完结态重开进入返工的。返工只改已有章、
	// 不增减结构，故排Rỗng后应按"结构完整即重Mới完结"放行（避免终卷末伏笔被返工扰动后卡在
	// writing → 越界续写死循环）；正向Viết不置此标记，完结判定保持线索收束的保守语义。
	ReopenedFromComplete bool `json:"reopened_from_complete,omitempty"`
}

// IsResumable 判断Có czy không可以从断点Phục hồi。
func (p *Progress) IsResumable() bool {
	return p.Phase == PhaseWriting && p.CurrentChapter > 0
}

// NextChapter Quay lại下一个要写的Chương号。
func (p *Progress) NextChapter() int {
	return p.LatestCompleted() + 1
}

// LatestCompleted Quay lại最大Đã hoàn thànhChương号；Không cóĐã hoàn thànhChương时Quay lại 0。
func (p *Progress) LatestCompleted() int {
	max := 0
	for _, ch := range p.CompletedChapters {
		if ch > max {
			max = ch
		}
	}
	return max
}

// ExtractNovelNameFromPremise 从 premise 第一行 `# Tên sách`（可带《》包裹）提取Tên sách。
// Mô hình偶尔会照抄提示词里的占位符而非生成真名，这些值视同未提取Quay lạiRỗng，
// 交由上层兜底（UI 显示"Chưa đặt tên sách"），避免界面直接显示"Tên sách"二字。
func ExtractNovelNameFromPremise(premise string) string {
	for raw := range strings.SplitSeq(strings.ReplaceAll(premise, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "# ") {
			return ""
		}
		name := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "# ")), "《》\"")
		switch name {
		case "Tên sách", "实际Tên sách", "示例Tên sách":
			return "" // 提示词占位符，非真实Tên sách
		}
		return name
	}
	return ""
}

// ContextProfile Ngữ cảnh加载策略，根据总Chương数自适应。
type ContextProfile struct {
	SummaryWindow  int  // 加载最近 N 章Tóm tắt
	TimelineWindow int  // 加载最近 N 章时间线
	Layered        bool // true = Bật分层Tóm tắt加载（卷Tóm tắt+弧Tóm tắt+章Tóm tắt）
}

// MemoryPolicy 表示运行时共享的记忆使用策略。
// 它既用于Ngữ cảnh输出，也用于宿主层的 handoff / reminder 决策。
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

// NewContextProfile 根据总Chương数计算Ngữ cảnh策略。
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

// NewChapterMemoryPolicy 根据Tiến độ与Ngữ cảnh策略生成Chương运行时记忆策略。
func NewChapterMemoryPolicy(progress *Progress, profile ContextProfile, currentOutlineBound bool) MemoryPolicy {
	policy := MemoryPolicy{
		Mode:                "chapter",
		SummaryWindow:       profile.SummaryWindow,
		TimelineWindow:      profile.TimelineWindow,
		LayeredSummaries:    profile.Layered,
		WorkingRefresh:      "每次按Chương加载时Làm mới",
		EpisodicRefresh:     "随ChươngNộp、评审和长篇Trạng thái变更Làm mới",
		PreviousTailChars:   800,
		ChapterPlanEnabled:  true,
		CurrentOutlineBound: currentOutlineBound,
		ReadOnlyThreshold:   5,
	}
	if profile.Layered {
		policy.SummaryStrategy = "卷Tóm tắt+弧Tóm tắt+最近Tóm tắt chương"
	} else {
		policy.SummaryStrategy = "最近Tóm tắt chương"
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

// NewArchitectMemoryPolicy Quay lại规划阶段使用的记忆策略。
func NewArchitectMemoryPolicy() MemoryPolicy {
	return MemoryPolicy{
		Mode:               "architect",
		PlanningRefresh:    "卷弧结构、指南针或Tóm tắt更Mới时Làm mới",
		FoundationRefresh:  "角色、伏笔、设定变更时Làm mới",
		PlanningFocus:      "分层Đại cương、指南针、卷Tóm tắt",
		FoundationFocus:    "角色设定、角色Chụp、伏笔台账",
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
	PendingSteer string       `json:"pending_steer,omitempty"` // 未Hoàn thành的 Steer 指令，中断Phục hồi时重Mới注入
}

// SteerEntry 用户干预记录。
type SteerEntry struct {
	Input     string `json:"input"`
	Timestamp string `json:"timestamp"`
}

// UserDirective 用户下达的长效创作要求，跨Chương持续生效。
// 持久化到 meta/user_directives.json，由 novel_context 注入
// working_memory.user_directives 供所有子Proxy遵守。
//
// Chapter/TotalChapters 是下达时的Tiến độChụp：让指令有明确的生效起点（不追溯
// 之前的Chương），也让误存的相对式指令（如"增加10章"）可被Đọc方判定为已满足，
// 而不是每次重读都再执行一次。
type UserDirective struct {
	Text          string `json:"text"`
	Chapter       int    `json:"chapter"`        // 下达时的ViếtTiến độ
	TotalChapters int    `json:"total_chapters"` // 下达时的规划总章数
	CreatedAt     string `json:"created_at"`     // RFC3339
}
