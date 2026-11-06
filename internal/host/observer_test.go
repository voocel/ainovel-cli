package host

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/progress"
	"github.com/voocel/litellm"
)

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
		modelStarts:         make(map[string]*activeCall),
		toolStarts:          make(map[string]*activeCall),
		streamExtractors:    make(map[string]*agentExtractor),
		streamTools:         make(map[string]map[int]string),
		retryEvents:         make(map[string]string),
	}
}

func TestObserverSubagentRetryEventsUpdateSameLinePerAgent(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	for i := 1; i <= 2; i++ {
		o.handleToolUpdate(progress.Event{
			Kind:       progress.KindRetry,
			Agent:      "writer",
			Attempt:    i,
			MaxRetries: 7,
			Message:    "stream read error: INTERNAL_ERROR; received from peer [network, openai]",
			Meta:       json.RawMessage(`{"retry_delay_ms":2000}`),
		})
	}

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 raw update events", len(events))
	}
	if events[0].ID == "" || events[1].ID != events[0].ID {
		t.Fatalf("writer retry events should share ID: %+v", events)
	}
	// Summary 不嵌静态延时（UI 依 RetryAt 倒计时）；延时以截止时刻形式携带，静态快照留在 Detail 供日志。
	if events[1].Agent != "writer" || !strings.Contains(events[1].Summary, "重试 (2/7)") {
		t.Fatalf("event = %+v, want writer retry 2/7 without inline delay", events[1])
	}
	if events[1].RetryAt.IsZero() || !strings.Contains(events[1].Detail, "重试 (2/7，2s后)") {
		t.Fatalf("event = %+v, want RetryAt deadline + static delay in Detail", events[1])
	}
	if events[1].Kind != "network" {
		t.Fatalf("event kind = %q, want network", events[1].Kind)
	}
}

func TestRetryProgressDelayRequiresReportedDelay(t *testing.T) {
	p := &progress.Event{Attempt: 3, MaxRetries: 7}
	if got := retryProgressDelay(p); got != 0 {
		t.Fatalf("unreported delay = %s, want 0", got)
	}
	p.Meta = json.RawMessage(`{"retry_delay_ms":4500}`)
	if got := retryProgressDelay(p); got.String() != "4.5s" {
		t.Fatalf("reported delay = %s, want 4.5s", got)
	}
}

func TestErrorKindFromFlattenedMessage(t *testing.T) {
	tests := []struct {
		message string
		want    string
	}{
		{message: "tool argument validation failed: invalid JSON", want: "tool_validation"},
		{message: "bad_response_status_code: Too many concurrent requests [provider, HTTP 500, openai]", want: "overloaded"},
	}
	for _, tt := range tests {
		if got := errorKind(nil, tt.message); got != tt.want {
			t.Fatalf("errorKind(%q) = %q, want %q", tt.message, got, tt.want)
		}
	}
}

func TestObserverDispatchErrorUpdatesSingleEventWithDetail(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.dispatchStart("architect_long", "规划长篇小说", "需要生成分层大纲")
	runErr := errors.New("stream read error: INTERNAL_ERROR; received from peer [network, openai]")
	o.dispatchFinish("architect_long", runErr)

	if len(events) != 2 {
		t.Fatalf("start + failed update 应只有 2 个原始事件，got %d: %+v", len(events), events)
	}
	start, failed := events[0], events[1]
	if !strings.Contains(start.Detail, "需要生成分层大纲") || !strings.Contains(start.Detail, "规划长篇小说") {
		t.Fatalf("DISPATCH 开始日志应保留完整原因与任务: %+v", start)
	}
	if failed.ID != start.ID || failed.Category != "DISPATCH" || !failed.Failed || failed.Level != "error" {
		t.Fatalf("失败应原地更新 DISPATCH: start=%+v failed=%+v", start, failed)
	}
	if failed.Detail != runErr.Error() || failed.Kind != "network" || !strings.Contains(failed.Summary, "INTERNAL_ERROR") {
		t.Fatalf("DISPATCH 应携带完整错误和分类: %+v", failed)
	}
}

func TestObserverSeparatesModelResponseFromToolExecution(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.handleWorkerEvent("writer", agentcore.MessageStart{})
	o.handleThinkingProgress(progress.Event{
		Kind: progress.KindThinking, Agent: "writer", Thinking: "正在分析章节计划",
	})
	o.handleSubagentDelta(&progress.Event{
		Kind:      progress.KindToolDelta,
		Agent:     "writer",
		Tool:      "draft_chapter",
		DeltaKind: progress.DeltaToolCall,
		Delta:     `{"chapter":11,"content":"第十一章`,
	})

	if len(events) != 3 {
		t.Fatalf("events = %d, want MODEL start + two state updates", len(events))
	}
	if events[0].Category != "MODEL" || events[0].Summary != "等待模型" || !events[0].Running() {
		t.Fatalf("model start = %+v", events[0])
	}
	if events[1].ID != events[0].ID || events[1].Summary != "思考中" {
		t.Fatalf("thinking update = %+v", events[1])
	}
	if events[2].ID != events[0].ID || events[2].Summary != "生成 draft_chapter" {
		t.Fatalf("generation update = %+v", events[2])
	}
	if len(o.toolStarts) != 0 {
		t.Fatal("生成工具参数时不应启动 TOOL 计时")
	}

	o.handleWorkerEvent("writer", agentcore.MessageEnd{
		Message: agentcore.Message{Role: litellm.RoleAssistant},
	})
	o.handleToolUpdate(progress.Event{
		Kind: progress.KindToolStart, Agent: "writer", Tool: "draft_chapter",
		Args: json.RawMessage(`{"chapter":11}`),
	})
	o.handleToolUpdate(progress.Event{
		Kind: progress.KindToolEnd, Agent: "writer", Tool: "draft_chapter",
	})

	if len(events) != 6 {
		t.Fatalf("events = %d, want MODEL finish + TOOL start/finish", len(events))
	}
	modelEnd, toolStart, toolEnd := events[3], events[4], events[5]
	if modelEnd.ID != events[0].ID || modelEnd.Summary != "模型响应" || modelEnd.FinishedAt.IsZero() {
		t.Fatalf("model end = %+v", modelEnd)
	}
	if toolStart.Category != "TOOL" || toolStart.Summary != "draft_chapter(第11章)" || !toolStart.Running() {
		t.Fatalf("tool start = %+v", toolStart)
	}
	if toolEnd.ID != toolStart.ID || toolEnd.FinishedAt.IsZero() {
		t.Fatalf("tool end = %+v", toolEnd)
	}
}

func TestObserverToolErrorUpdatesSingleToolEventWithFullDetail(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	fullError := "tool argument validation failed: unexpected end of JSON input\nraw args: " +
		`{"chapter":1,"summary":"` + strings.Repeat("秦越在材料中发现线索", 30) + "<TAIL>"

	o.handleToolUpdate(progress.Event{
		Kind:  progress.KindToolStart,
		Agent: "writer",
		Tool:  "edit_chapter",
	})
	o.handleToolUpdate(progress.Event{
		Kind:    progress.KindToolError,
		Agent:   "writer",
		Tool:    "edit_chapter",
		Message: fullError,
	})

	if len(events) != 2 {
		t.Fatalf("start + failed update 应只有 2 个原始事件，got %d: %+v", len(events), events)
	}
	start, failed := events[0], events[1]
	if failed.ID == "" || failed.ID != start.ID || !failed.Failed || failed.Category != "TOOL" || failed.Level != "error" {
		t.Fatalf("失败事件应原地更新 TOOL 行: start=%+v failed=%+v", start, failed)
	}
	if !strings.Contains(failed.Summary, "tool argument validation failed") ||
		!strings.Contains(failed.Detail, fullError) || !strings.Contains(failed.Detail, "<TAIL>") {
		t.Fatalf("失败事件应同时保留 UI 摘要和完整日志详情: %+v", failed)
	}
	if len(failed.Summary) >= len(failed.Detail) {
		t.Fatalf("UI Summary 应短于完整 Detail: summary=%d detail=%d", len(failed.Summary), len(failed.Detail))
	}
}
