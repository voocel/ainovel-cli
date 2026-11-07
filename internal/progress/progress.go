// Package progress carries the structured progress a run reports while it works:
// which agent and tool is active, what streamed, what failed, what retried.
//
// A tool reports with Report; the run delivers it as agentcore.ToolUpdate, and the
// host's observer turns it into the rows a reader sees. The envelope is plain data
// so any layer can build one without depending on the host.
package progress

import (
	"context"
	"encoding/json"

	"github.com/voocel/agentcore"
)

// Kind distinguishes what an Event reports.
type Kind string

const (
	// KindToolStart begins a tool call.
	KindToolStart Kind = "tool_start"
	// KindToolEnd ends a tool call.
	KindToolEnd Kind = "tool_end"
	// KindToolDelta carries streamed content; DeltaKind says which.
	KindToolDelta Kind = "tool_delta"
	// KindThinking carries accumulated reasoning text.
	KindThinking Kind = "thinking"
	// KindSummary carries a progress note with nothing else to render.
	KindSummary Kind = "summary"
	// KindToolError reports a tool call that failed.
	KindToolError Kind = "tool_error"
	// KindTurnCounter reports the turn under way.
	KindTurnCounter Kind = "turn_counter"
	// KindRetry reports a model call that failed and will be made again.
	KindRetry Kind = "retry"
	// KindContext reports the context budget in use.
	KindContext Kind = "context"
)

// DeltaKind distinguishes what a KindToolDelta Event's Delta carries.
type DeltaKind string

const (
	// DeltaText is regular response text.
	DeltaText DeltaKind = ""
	// DeltaThinking is reasoning text.
	DeltaThinking DeltaKind = "thinking"
	// DeltaToolCall is a fragment of a tool call's JSON arguments.
	DeltaToolCall DeltaKind = "toolcall"
)

// Event is one structured progress report. Fields a kind does not use stay zero;
// Message carries complete text, and presentation limits belong to consumers.
type Event struct {
	Kind       Kind            `json:"kind"`
	Agent      string          `json:"agent,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	Delta      string          `json:"delta,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
	Message    string          `json:"message,omitempty"`
	Turn       int             `json:"turn,omitempty"`
	Attempt    int             `json:"attempt,omitempty"`
	MaxRetries int             `json:"max_retries,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Meta       json.RawMessage `json:"meta,omitempty"`
	// DeltaKind distinguishes what Delta carries when Kind is KindToolDelta,
	// so consumers can render text and tool-call argument JSON differently.
	DeltaKind DeltaKind `json:"delta_kind,omitempty"`
}

// Report hands e to whatever consumes the progress of the tool call ctx runs.
// It is silently dropped when no consumer is registered.
func Report(ctx context.Context, e Event) {
	if e.Kind == "" {
		e.Kind = KindSummary
	}
	agentcore.ReportProgress(ctx, e)
}

// From returns the Event p carries, if any. Reported progress is an any: tools
// report their own types, and only ours is an Event.
func From(p any) (Event, bool) {
	switch v := p.(type) {
	case Event:
		return v, true
	case *Event:
		if v != nil {
			return *v, true
		}
	}
	return Event{}, false
}
