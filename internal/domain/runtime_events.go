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

// RuntimeContextBoundary 是上下文正式边界/投影视图的显式记录。
// 它补充 UIEvent，便于恢复、审计和后续识别“这次是否已提交到 baseline”。
type RuntimeContextBoundary struct {
	Agent          string `json:"agent,omitempty"`
	Kind           string `json:"kind"` // projected / compacted / recovered
	Reason         string `json:"reason,omitempty"`
	Strategy       string `json:"strategy,omitempty"`
	Committed      bool   `json:"committed,omitempty"`
	TokensBefore   int    `json:"tokens_before,omitempty"`
	TokensAfter    int    `json:"tokens_after,omitempty"`
	MessagesBefore int    `json:"messages_before,omitempty"`
	MessagesAfter  int    `json:"messages_after,omitempty"`
	CompactedCount int    `json:"compacted_count,omitempty"`
	KeptCount      int    `json:"kept_count,omitempty"`
	SplitTurn      bool   `json:"split_turn,omitempty"`
	Incremental    bool   `json:"incremental,omitempty"`
	SummaryRunes   int    `json:"summary_runes,omitempty"`
}

// ControlIntentKind 表示控制队列中的指令类型。
type ControlIntentKind string

const (
	ControlIntentResumePrompt ControlIntentKind = "resume_prompt"
	ControlIntentSteerMessage ControlIntentKind = "steer_message"
	ControlIntentFollowUp     ControlIntentKind = "follow_up"
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
