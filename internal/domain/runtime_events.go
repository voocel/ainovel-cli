package domain

import "time"

// RuntimeQueuePriority 表示运行时队列优先级。
type RuntimeQueuePriority string

const (
	RuntimePriorityInterrupt  RuntimeQueuePriority = "interrupt"
	RuntimePriorityControl    RuntimeQueuePriority = "control"
	RuntimePriorityBackground RuntimeQueuePriority = "background"
)

// RuntimeQueueKind 表示运行时队列项类型。
type RuntimeQueueKind string

const (
	RuntimeQueueUIEvent     RuntimeQueueKind = "ui_event"
	RuntimeQueueStreamDelta RuntimeQueueKind = "stream_delta"
	RuntimeQueueStreamClear RuntimeQueueKind = "stream_clear"
	RuntimeQueueContextEdge RuntimeQueueKind = "context_boundary"
	RuntimeQueueControl     RuntimeQueueKind = "control"
	RuntimeQueueEvidence    RuntimeQueueKind = "evidence"
)

// RuntimeQueueItem 是统一运行时队列的持久化记录。
type RuntimeQueueItem struct {
	Seq      int64                `json:"seq"`
	Time     time.Time            `json:"time"`
	Kind     RuntimeQueueKind     `json:"kind"`
	Priority RuntimeQueuePriority `json:"priority"`
	TaskID   string               `json:"task_id,omitempty"`
	Agent    string               `json:"agent,omitempty"`
	Category string               `json:"category,omitempty"`
	Summary  string               `json:"summary,omitempty"`
	Payload  any                  `json:"payload,omitempty"`
}

// RuntimeTaskLogEntry 是单任务运行日志的持久化记录。
type RuntimeTaskLogEntry struct {
	Time    time.Time `json:"time"`
	TaskID  string    `json:"task_id,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	Event   string    `json:"event"`
	Tool    string    `json:"tool,omitempty"`
	Summary string    `json:"summary,omitempty"`
	Payload any       `json:"payload,omitempty"`
}

// ContextBuildEvidence 记录一次 novel_context 实际注入了哪些关键上下文。
// 只保存结构化摘要，不保存长文本，便于诊断 prompt / context 设计问题。
type ContextBuildEvidence struct {
	Agent              string   `json:"agent,omitempty"`
	Chapter            int      `json:"chapter,omitempty"`
	Mode               string   `json:"mode,omitempty"` // chapter / architect
	SummaryWindow      int      `json:"summary_window,omitempty"`
	TimelineWindow     int      `json:"timeline_window,omitempty"`
	LayeredSummaries   bool     `json:"layered_summaries,omitempty"`
	HasCurrentOutline  bool     `json:"has_current_outline,omitempty"`
	HasNextOutline     bool     `json:"has_next_outline,omitempty"`
	HasChapterPlan     bool     `json:"has_chapter_plan,omitempty"`
	HasChapterContract bool     `json:"has_chapter_contract,omitempty"`
	RecentSummaryCount int      `json:"recent_summary_count,omitempty"`
	TimelineCount      int      `json:"timeline_count,omitempty"`
	ForeshadowCount    int      `json:"foreshadow_count,omitempty"`
	RelationshipCount  int      `json:"relationship_count,omitempty"`
	StateChangeCount   int      `json:"state_change_count,omitempty"`
	StoryThreadCount   int      `json:"story_thread_count,omitempty"`
	ReviewLessonCount  int      `json:"review_lesson_count,omitempty"`
	WarnSections       []string `json:"warn_sections,omitempty"`
	TrimmedSections    []string `json:"trimmed_sections,omitempty"`
}

// ReviewOutcomeEvidence 是对 Editor 评审结果的轻量结构化归纳。
type ReviewOutcomeEvidence struct {
	Chapter            int      `json:"chapter"`
	Verdict            string   `json:"verdict"`
	ContractStatus     string   `json:"contract_status,omitempty"`
	AffectedChapters   []int    `json:"affected_chapters,omitempty"`
	LowDimensions      []string `json:"low_dimensions,omitempty"`
	FailedDimensions   []string `json:"failed_dimensions,omitempty"`
	CriticalIssueTypes []string `json:"critical_issue_types,omitempty"`
	TopReasonCodes     []string `json:"top_reason_codes,omitempty"`
}

// RuntimeContextBoundary 是上下文正式边界/投影视图的显式记录。
// 它补充 UIEvent，便于恢复、审计和后续识别“这次是否已提交到 baseline”。
type RuntimeContextBoundary struct {
	Agent          string               `json:"agent,omitempty"`
	Kind           string               `json:"kind"` // projected / compacted / recovered
	Reason         string               `json:"reason,omitempty"`
	Strategy       string               `json:"strategy,omitempty"`
	Committed      bool                 `json:"committed,omitempty"`
	TokensBefore   int                  `json:"tokens_before,omitempty"`
	TokensAfter    int                  `json:"tokens_after,omitempty"`
	MessagesBefore int                  `json:"messages_before,omitempty"`
	MessagesAfter  int                  `json:"messages_after,omitempty"`
	CompactedCount int                  `json:"compacted_count,omitempty"`
	KeptCount      int                  `json:"kept_count,omitempty"`
	SplitTurn      bool                 `json:"split_turn,omitempty"`
	Incremental    bool                 `json:"incremental,omitempty"`
	SummaryRunes   int                  `json:"summary_runes,omitempty"`
	Steps          []ContextRewriteStep `json:"steps,omitempty"`
}

// ContextRewriteStep 记录压缩管线中单个策略的执行情况。
type ContextRewriteStep struct {
	Name         string `json:"name"`
	Applied      bool   `json:"applied"`
	TokensBefore int    `json:"tokens_before"`
	TokensAfter  int    `json:"tokens_after"`
}

// ControlIntentKind 表示控制队列中的指令类型。
type ControlIntentKind string

const (
	ControlIntentResumePrompt ControlIntentKind = "resume_prompt"
	ControlIntentSteerMessage ControlIntentKind = "steer_message"
	ControlIntentFollowUp     ControlIntentKind = "follow_up"
	ControlIntentRunTask      ControlIntentKind = "run_task"
)

// ControlIntent 是持久化控制队列中的一项。
type ControlIntent struct {
	ID        string               `json:"id"`
	Kind      ControlIntentKind    `json:"kind"`
	Priority  RuntimeQueuePriority `json:"priority"`
	Summary   string               `json:"summary,omitempty"`
	Message   string               `json:"message,omitempty"`
	Prompt    string               `json:"prompt,omitempty"`
	TaskKind  TaskKind             `json:"task_kind,omitempty"`
	TaskTitle string               `json:"task_title,omitempty"`
	TaskInput string               `json:"task_input,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
	Payload   map[string]string    `json:"payload,omitempty"`
}
