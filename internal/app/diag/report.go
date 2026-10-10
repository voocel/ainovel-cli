// Package diag interprets persisted execution facts without changing them.
package diag

import (
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// FormatVersion 2：用量取事件用量列，事件与范围合计同一形状（含已定价的花费）。
const FormatVersion = 2
const RulesVersion = 1

type Request struct {
	ProjectID   string `json:"project_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	After       string `json:"after,omitempty"`
	EventAfter  int64  `json:"event_after,omitempty"`
}

type Header struct {
	FormatVersion    int            `json:"format_version"`
	RulesVersion     int            `json:"rules_version"`
	BuildVersion     string         `json:"build_version"`
	Platform         string         `json:"platform"`
	SchemaVersion    int            `json:"schema_version,omitempty"`
	CapturedAt       time.Time      `json:"captured_at"`
	Scope            Request        `json:"scope"`
	Revision         model.Revision `json:"revision"`
	RunEventBoundary int64          `json:"run_event_boundary"`
	Shareable        bool           `json:"shareable"`
	DatabaseIssue    string         `json:"database_issue,omitempty"`
	Selection        string         `json:"selection"`
}

type Summary struct {
	State       string     `json:"state"`
	Reason      string     `json:"reason,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
}

type Finding struct {
	Code        string     `json:"code"`
	Severity    string     `json:"severity"`
	Certainty   string     `json:"certainty"`
	OperationID string     `json:"operation_id,omitempty"`
	Attempt     int        `json:"attempt,omitempty"`
	Count       int        `json:"count"`
	Observed    string     `json:"observed"`
	Suggestion  string     `json:"suggestion"`
	Evidence    []Evidence `json:"evidence,omitempty"`
}

type Evidence struct {
	Source      string `json:"source"`
	OperationID string `json:"operation_id"`
	Attempt     int    `json:"attempt"`
	Sequence    int64  `json:"sequence,omitempty"`
}

type Metrics struct {
	Operations        int                          `json:"operations"`
	States            map[model.OperationState]int `json:"states"`
	Attempts          int                          `json:"attempts"`
	RetriedOperations int                          `json:"retried_operations"`
	Events            int                          `json:"events"`
	ToolErrors        int                          `json:"tool_errors"`
	// Usage 是所选运行或任务全部尝试的模型用量，含中断与恢复的尝试。
	Usage model.Usage `json:"usage,omitzero"`
}

type Coverage struct {
	Source string `json:"source"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Operation struct {
	ID                 string               `json:"id"`
	RunID              string               `json:"run_id"`
	Kind               model.OperationKind  `json:"kind"`
	State              model.OperationState `json:"state"`
	Attempt            int                  `json:"attempt"`
	FailureCode        model.FailureCode    `json:"failure_code,omitempty"`
	Error              string               `json:"error,omitempty"`
	Executor           string               `json:"executor"`
	ConfigDigest       string               `json:"config_digest"`
	LeaseUntil         *time.Time           `json:"lease_until,omitempty"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
	EventCount         int                  `json:"event_count"`
	EventBoundary      int64                `json:"event_boundary"`
	HasCompletionEvent bool                 `json:"has_completion_event"`
}

type Event struct {
	OperationID string    `json:"operation_id"`
	Sequence    int64     `json:"sequence"`
	Attempt     int       `json:"attempt"`
	Kind        string    `json:"kind"`
	CreatedAt   time.Time `json:"created_at"`
	// Text contains local details only. Never included in ShareReport.
	Text             string `json:"text,omitempty"`
	PayloadTruncated bool   `json:"payload_truncated,omitempty"`
	// Usage 是这条事件自身的模型用量（模型消息、合规核对），其余事件为零。
	Usage model.Usage `json:"usage,omitzero"`
}

type Report struct {
	Header            Header      `json:"header"`
	Summary           Summary     `json:"summary"`
	Findings          []Finding   `json:"findings"`
	Metrics           Metrics     `json:"metrics"`
	Coverage          []Coverage  `json:"coverage"`
	Operations        []Operation `json:"operations"`
	Events            []Event     `json:"events"`
	NextOperationID   string      `json:"next_operation_id,omitempty"`
	NextEventSequence int64       `json:"next_event_sequence,omitempty"`
}
