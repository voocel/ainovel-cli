// Package diag interprets persisted execution facts without changing them.
package diag

import (
	"encoding/json"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

const FormatVersion = 1
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
	Usage            *Usage `json:"usage,omitempty"`
}

// Usage is one persisted attempt's end summary, never a full-run estimate.
type Usage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cache_read"`
	CacheWrite  int64 `json:"cache_write"`
	TotalTokens int64 `json:"total_tokens"`
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

func decodeUsage(payload []byte) (*Usage, error) {
	var record struct {
		Usage *struct {
			Input       *int64 `json:"input"`
			Output      *int64 `json:"output"`
			CacheRead   *int64 `json:"cache_read"`
			CacheWrite  *int64 `json:"cache_write"`
			TotalTokens *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &record); err != nil {
		return nil, err
	}
	u := record.Usage
	if u == nil || u.Input == nil || u.Output == nil || u.CacheRead == nil || u.CacheWrite == nil || u.TotalTokens == nil {
		return nil, model.ErrInvalid
	}
	if *u.Input < 0 || *u.Output < 0 || *u.CacheRead < 0 || *u.CacheWrite < 0 || *u.TotalTokens < 0 {
		return nil, model.ErrInvalid
	}
	return &Usage{*u.Input, *u.Output, *u.CacheRead, *u.CacheWrite, *u.TotalTokens}, nil
}
