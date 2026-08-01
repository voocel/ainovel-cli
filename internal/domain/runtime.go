package domain

import (
	"fmt"
	"strings"
)

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

// Genre 表示创作题材类型。
type Genre string

const (
	GenreNovel      Genre = "novel"       // 长篇小说
	GenreShortStory Genre = "short_story" // 短篇小说
)

// ParseGenre 把外部输入收敛为受支持的创作类型。空值兼容旧配置，按长篇处理。
func ParseGenre(raw string) (Genre, error) {
	genre := Genre(strings.TrimSpace(raw))
	if genre == "" {
		return GenreNovel, nil
	}
	switch genre {
	case GenreNovel, GenreShortStory:
		return genre, nil
	default:
		return "", fmt.Errorf("unsupported genre %q (expected %q or %q)", raw, GenreNovel, GenreShortStory)
	}
}

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
	Genre             Genre       `json:"genre,omitempty"` // 题材类型（novel/short_story），默认 novel
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
	// ReopenedFromComplete 标记本书是经 reopen 从完结态重开进入返工的。返工只改已有章、
	// 不增减结构，故排空后应按"结构完整即重新完结"放行（避免终卷末伏笔被返工扰动后卡在
	// writing → 越界续写死循环）；正向写作不置此标记，完结判定保持线索收束的保守语义。
	ReopenedFromComplete bool `json:"reopened_from_complete,omitempty"`
	// ReopenCount 记录本书从完结态被重开的累计次数（/reopen 审计事实）。它同时保证
	// 重开后的再完结与上次完结的 progress.json 内容不同：checkpoint 对同 digest 幂等
	// 去重，字节相同的再完结不会产生新 checkpoint，StopGuard 会把成功的 complete_book
	// 误判为空转并升级终止。
	ReopenCount int `json:"reopen_count,omitempty"`
}

// IsResumable 判断是否可以从断点恢复。
func (p *Progress) IsResumable() bool {
	return p.Phase == PhaseWriting && p.CurrentChapter > 0
}

// NextChapter 返回下一个要写的章节号。
func (p *Progress) NextChapter() int {
	return p.LatestCompleted() + 1
}

// LatestCompleted 返回最大已完成章节号；无已完成章节时返回 0。
func (p *Progress) LatestCompleted() int {
	max := 0
	for _, ch := range p.CompletedChapters {
		if ch > max {
			max = ch
		}
	}
	return max
}

// ExtractNovelNameFromPremise 从 premise 第一行 `# 书名`（可带《》包裹）提取书名。
// 模型偶尔会照抄提示词里的占位符而非生成真名，这些值视同未提取返回空，
// 交由上层兜底（UI 显示"未定书名"），避免界面直接显示"书名"二字。
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
		case "书名", "实际书名", "示例书名":
			return "" // 提示词占位符，非真实书名
		}
		return name
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

// NewContextProfile 根据总章节数和题材计算上下文策略。
func NewContextProfile(totalChapters int, genre Genre) ContextProfile {
	// 短篇小说：简化策略，不启用分层
	if genre == GenreShortStory {
		return ContextProfile{SummaryWindow: 10, TimelineWindow: 10, Layered: false}
	}
	// 长篇小说：按章节数自适应
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
	StartedAt            string             `json:"started_at"`
	Provider             string             `json:"provider,omitempty"`
	Style                string             `json:"style"`
	Model                string             `json:"model"`
	PlanningTier         PlanningTier       `json:"planning_tier,omitempty"`
	StartPrompt          string             `json:"start_prompt,omitempty"`           // 用户原始创作需求（输入事实，先于启动裁定落盘；裁定失败后据此补裁）
	PlanStart            *PlanStartRecord   `json:"plan_start,omitempty"`             // 启动裁定事实，规划期崩溃恢复的唯一依据
	PendingSteer         string             `json:"pending_steer,omitempty"`          // 未完成的 Steer 指令，中断恢复时重新注入
	AdvanceMode          ChapterAdvanceMode `json:"advance_mode"`                     // 章节推进模式：auto / review
	AdvancePermitChapter int                `json:"advance_permit_chapter,omitempty"` // review 模式下一次性许可的正向章节
	AdvanceHold          *AdvanceHold       `json:"advance_hold,omitempty"`           // 当前干预签署的一次性暂停意图
}

// ChapterAdvanceMode 决定新章节是否需要逐章许可。
type ChapterAdvanceMode string

const (
	ChapterAdvanceAuto   ChapterAdvanceMode = "auto"
	ChapterAdvanceReview ChapterAdvanceMode = "review"
)

// Valid 报告章节推进模式是否受当前版本支持。
func (m ChapterAdvanceMode) Valid() bool {
	return m == ChapterAdvanceAuto || m == ChapterAdvanceReview
}

// UnsupportedAdvanceModeError 表示书的控制模式不受当前二进制支持。
// 调用方必须停止构造可写 Host，并提示用户使用匹配版本；禁止猜测降级。
type UnsupportedAdvanceModeError struct {
	Mode ChapterAdvanceMode
}

func (e *UnsupportedAdvanceModeError) Error() string {
	return fmt.Sprintf("不支持的章节推进模式 %q，请使用创建该项目的新版 ainovel", e.Mode)
}

// AdvanceHoldAfter 是一次性暂停的确定性触发条件。
type AdvanceHoldAfter string

const (
	AdvanceHoldAtBoundary           AdvanceHoldAfter = "boundary"
	AdvanceHoldAfterRewritesDrained AdvanceHoldAfter = "rewrites_drained"
)

// Valid 报告暂停条件是否受当前版本支持。
func (a AdvanceHoldAfter) Valid() bool {
	return a == AdvanceHoldAtBoundary || a == AdvanceHoldAfterRewritesDrained
}

// AdvanceHold 是当前干预签署的一次性暂停意图，由 Host 边界消费。
type AdvanceHold struct {
	After  AdvanceHoldAfter `json:"after"`
	Reason string           `json:"reason"`
}

// PlanStartRecord 启动裁定的持久化事实(裁定先落事实,再起执行;恢复不重新裁定)。
// 首个 save_foundation 落盘 scale 后,规划期恢复改由 PlanningTier 推导,本记录
// 只覆盖"裁定完成到首次落盘之间"的窗口。DecisionID 关联 decisions.jsonl 审计。
type PlanStartRecord struct {
	RawPrompt   string `json:"raw_prompt"`
	Planner     string `json:"planner"`
	PlannerTask string `json:"planner_task"`
	DecisionID  string `json:"decision_id,omitempty"`
}
