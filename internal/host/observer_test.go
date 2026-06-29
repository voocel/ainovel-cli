package host

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestParseSubagentResultError(t *testing.T) {
	cases := []struct {
		name   string
		result string
		want   string
	}{
		{"empty", ``, ""},
		{"object form", `{"error":"unknown agent \"writer2\""}`, `unknown agent "writer2"`},
		{"object empty error field", `{"error":""}`, ""},
		{"bare string - invalid params", `"Invalid parameters: provide exactly one mode (agent+task, tasks, or chain)"`, "Invalid parameters: provide exactly one mode (agent+task, tasks, or chain)"},
		{"bare string - background", `"background mode requires agent + task"`, "background mode requires agent + task"},
		{"bare string - parallel cap", `"Too many parallel tasks (5). Max is 3."`, "Too many parallel tasks (5). Max is 3."},
		{"bare string - normal result not flagged", `"Chapter committed"`, ""},
		{"success object not flagged", `{"chapter":1,"status":"ok"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSubagentResultError(json.RawMessage(c.result))
			if got != c.want {
				t.Fatalf("parseSubagentResultError(%q) = %q, want %q", c.result, got, c.want)
			}
		})
	}
}

func testObserver(events *[]Event) *observer {
	return &observer{
		emitEv: func(ev Event) {
			*events = append(*events, ev)
		},
		emitD:               func(string) {},
		emitC:               func() {},
		agents:              make(map[string]*agentState),
		lastThinkingByAgent: make(map[string]string),
		dispatchStarts:      make(map[string]*activeCall),
		toolStarts:          make(map[string]*activeCall),
		streamExtractors:    make(map[string]*agentExtractor),
		streamArgPrefixes:   make(map[string]string),
		streamArgLabels:     make(map[string]string),
	}
}

func TestObserverSubagentToolDeltaUpdatesSaveFoundationType(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.handleSubagentDelta(&agentcore.ProgressPayload{
		Kind:      agentcore.ProgressToolDelta,
		Agent:     "architect_long",
		Tool:      "save_foundation",
		DeltaKind: agentcore.DeltaToolCall,
		Delta:     `{"type":"premise","content":"# Tên sách`,
	})

	if len(events) < 2 {
		t.Fatalf("events = %d, want start + summary update", len(events))
	}
	if events[0].Category != "TOOL" || events[0].Summary != "save_foundation" || events[0].Depth != 1 {
		t.Fatalf("start event = %+v", events[0])
	}
	if events[1].ID != events[0].ID || events[1].Summary != "save_foundation[premise]" {
		t.Fatalf("summary update = %+v, start = %+v", events[1], events[0])
	}
}

func TestObserverSubagentToolDeltaUpdatesSaveFoundationTypeAcrossChunks(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	for _, delta := range []string{`{"ty`, `pe":"premise","content":"# Tên sách`} {
		o.handleSubagentDelta(&agentcore.ProgressPayload{
			Kind:      agentcore.ProgressToolDelta,
			Agent:     "architect_long",
			Tool:      "save_foundation",
			DeltaKind: agentcore.DeltaToolCall,
			Delta:     delta,
		})
	}

	var summaries []string
	for _, ev := range events {
		summaries = append(summaries, ev.Summary)
	}
	if !strings.Contains(strings.Join(summaries, "\n"), "save_foundation[premise]") {
		t.Fatalf("summaries = %v, want save_foundation[premise]", summaries)
	}
}

func TestObserverCoordinatorToolDeltaStartsToolLoading(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	msg := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID:   "call_1",
				Name: "novel_context",
			}),
		},
	}

	o.handleMessageUpdate(agentcore.Event{
		Type:      agentcore.EventMessageUpdate,
		Message:   msg,
		Delta:     `{"chapter":`,
		DeltaKind: agentcore.DeltaToolCall,
	})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Category != "TOOL" || events[0].Agent != "coordinator" || events[0].Summary != "novel_context" {
		t.Fatalf("event = %+v", events[0])
	}
	if !events[0].Running() {
		t.Fatalf("event should be running: %+v", events[0])
	}
}

func TestObserverEventErrorClosesEarlyToolLoading(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	msg := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID:   "call_1",
				Name: "novel_context",
			}),
		},
	}

	o.handleMessageUpdate(agentcore.Event{
		Type:      agentcore.EventMessageUpdate,
		Message:   msg,
		Delta:     `{"chapter":`,
		DeltaKind: agentcore.DeltaToolCall,
	})
	o.handle(agentcore.Event{Type: agentcore.EventError, Err: errors.New("stream failed")})

	if len(events) != 3 {
		t.Fatalf("events = %d, want start + failed finish + error: %+v", len(events), events)
	}
	if events[1].ID != events[0].ID || events[1].FinishedAt.IsZero() || !events[1].Failed {
		t.Fatalf("finish event = %+v, start = %+v", events[1], events[0])
	}
	if events[2].Category != "ERROR" {
		t.Fatalf("error event = %+v", events[2])
	}
}

func TestObserverCoordinatorSubagentDeltaMergesWithExecStart(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	msg := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID:   "call_1",
				Name: "subagent",
			}),
		},
	}

	o.handleMessageUpdate(agentcore.Event{
		Type:      agentcore.EventMessageUpdate,
		Message:   msg,
		Delta:     `{"agent":"writer","task":"Tiếp tục"}`,
		DeltaKind: agentcore.DeltaToolCall,
	})
	args, err := json.Marshal(map[string]any{"agent": "writer", "task": "Tiếp tục"})
	if err != nil {
		t.Fatal(err)
	}
	o.handleToolStart(agentcore.Event{
		Type: agentcore.EventToolExecStart,
		Tool: "subagent",
		Args: args,
	})

	if len(events) != 2 {
		t.Fatalf("events = %d, want start + summary update: %+v", len(events), events)
	}
	if events[0].Category != "DISPATCH" || events[0].Summary != "subagent" {
		t.Fatalf("dispatch start = %+v", events[0])
	}
	if events[1].ID != events[0].ID || events[1].Summary != "writer（Tiếp tục）" {
		t.Fatalf("dispatch update = %+v, start = %+v", events[1], events[0])
	}
}

func TestObserverCoordinatorSubagentDeltaUpdatesDispatchSummary(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	msg := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID:   "call_1",
				Name: "subagent",
			}),
		},
	}

	for _, delta := range []string{`{"agent":"wr`, `iter","task":"Tiếp tục"}`} {
		o.handleMessageUpdate(agentcore.Event{
			Type:      agentcore.EventMessageUpdate,
			Message:   msg,
			Delta:     delta,
			DeltaKind: agentcore.DeltaToolCall,
		})
	}

	if len(events) != 2 {
		t.Fatalf("events = %d, want start + summary update: %+v", len(events), events)
	}
	if events[0].Category != "DISPATCH" || events[0].Summary != "subagent" {
		t.Fatalf("dispatch start = %+v", events[0])
	}
	if events[1].ID != events[0].ID || events[1].Summary != "writer（Tiếp tục）" {
		t.Fatalf("dispatch update = %+v, start = %+v", events[1], events[0])
	}
}
