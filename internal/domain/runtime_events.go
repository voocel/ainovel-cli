package domain

import "time"

// RuntimeQueuePriority 表示运行时队列优先级。
type RuntimeQueuePriority string

const (
	RuntimePriorityControl    RuntimeQueuePriority = "control"
	RuntimePriorityBackground RuntimeQueuePriority = "background"
)

// RuntimeQueueKind 表示运行时队列项类型。
type RuntimeQueueKind string

const (
	RuntimeQueueUIEvent     RuntimeQueueKind = "ui_event"
	RuntimeQueueStreamDelta RuntimeQueueKind = "stream_delta"
	RuntimeQueueStreamClear RuntimeQueueKind = "stream_clear"
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
