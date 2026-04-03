package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestFinalizeSteerIfIdleClearsPendingState(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowSteering); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.RunMeta.SetPendingSteer("主角改成女性"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	newSession(nil, store, nil, nil, "", nil, nil, nil).finalizeSteerIfIdle()

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.Flow != domain.FlowWriting {
		t.Fatalf("expected flow writing, got %s", progress.Flow)
	}

	runMeta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if runMeta.PendingSteer != "" {
		t.Fatalf("expected pending steer cleared, got %q", runMeta.PendingSteer)
	}
}

func TestFinalizeSteerIfIdleKeepsActiveFlow(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.RunMeta.SetPendingSteer("加入反转"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	newSession(nil, store, nil, nil, "", nil, nil, nil).finalizeSteerIfIdle()

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.Flow != domain.FlowRewriting {
		t.Fatalf("expected flow rewriting, got %s", progress.Flow)
	}

	runMeta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if runMeta.PendingSteer != "加入反转" {
		t.Fatalf("expected pending steer preserved, got %q", runMeta.PendingSteer)
	}
}

func TestParseProgressSummaryIgnoresThinkingUpdate(t *testing.T) {
	summary := parseProgressSummary(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:     agentcore.ProgressThinking,
			Agent:    "architect",
			Thinking: "好的，我已经获得了模板。",
		},
	})
	if summary != "" {
		t.Fatalf("expected thinking update to be ignored, got %q", summary)
	}
}

func TestParseProgressSummaryKeepsToolProgress(t *testing.T) {
	summary := parseProgressSummary(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressToolStart,
			Agent: "writer",
			Tool:  "plan_chapter",
		},
	})
	if summary != "writer → plan_chapter" {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

func TestParseProgressSummaryFormatsSaveFoundationType(t *testing.T) {
	args, err := json.Marshal(map[string]any{
		"type": "characters",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	summary := parseProgressSummary(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressToolStart,
			Agent: "architect_long",
			Tool:  "save_foundation",
			Args:  args,
		},
	})
	if summary != "architect_long → save_foundation[characters]" {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

func TestParseToolProgressUsesStructuredPayload(t *testing.T) {
	progress, ok := parseToolProgress(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:    agentcore.ProgressToolError,
			Agent:   "writer",
			Tool:    "plan_chapter",
			Message: "bad args",
			IsError: true,
		},
	})
	if !ok {
		t.Fatal("expected structured tool progress to be parsed")
	}
	if progress.Agent != "writer" || progress.Tool != "plan_chapter" || !progress.Error || progress.Message != "bad args" {
		t.Fatalf("unexpected progress: %+v", progress)
	}
}

func TestParseSubAgentRetryUsesStructuredPayload(t *testing.T) {
	msg, ok := parseSubAgentRetry(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:       agentcore.ProgressRetry,
			Agent:      "writer",
			Attempt:    2,
			MaxRetries: 3,
			Message:    "temporary failure",
		},
	})
	if !ok {
		t.Fatal("expected structured retry payload to be parsed")
	}
	if msg != "writer 重试 (2/3): temporary failure" {
		t.Fatalf("unexpected retry message: %q", msg)
	}
}

func TestExtractToolErrorText(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want string
	}{
		{
			name: "json string",
			raw:  "save planning tier: permission denied",
			want: "save planning tier: permission denied",
		},
		{
			name: "json object message",
			raw: map[string]any{
				"message": "parse outline JSON: invalid character",
			},
			want: "parse outline JSON: invalid character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := json.Marshal(tt.raw)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			text := extractToolErrorText(result)
			if text != tt.want {
				t.Fatalf("unexpected error text: %q", text)
			}
		})
	}
}

func TestIsUserCanceledText(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{text: "context canceled", want: true},
		{text: "Agent \"writer\" failed: context canceled", want: true},
		{text: "[Request interrupted by user for tool use]", want: true},
		{text: "permission denied", want: false},
	}

	for _, tt := range tests {
		if got := isUserCanceledText(tt.text); got != tt.want {
			t.Fatalf("isUserCanceledText(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestLogSubAgentResultSkipsCanceledError(t *testing.T) {
	result, err := json.Marshal(map[string]any{
		"error": "Agent \"writer\" failed: context canceled",
		"usage": map[string]any{
			"input":  10,
			"output": 20,
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var emitted []UIEvent
	logSubAgentResult(result, func(ev UIEvent) {
		ev.Time = time.Time{}
		emitted = append(emitted, ev)
	})

	if len(emitted) != 0 {
		t.Fatalf("expected no UI events, got %+v", emitted)
	}
}

func TestSessionDelaysStreamClearUntilFirstVisibleDelta(t *testing.T) {
	var seq []string
	sess := newSession(nil, nil, nil, nil, "", nil,
		func(delta string) { seq = append(seq, "delta:"+delta) },
		func() { seq = append(seq, "clear") },
	)

	sess.handleMessageStart()
	if len(seq) != 0 {
		t.Fatalf("expected no clear on message start, got %v", seq)
	}

	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:    agentcore.ProgressSummary,
			Summary: "writer → draft_chapter",
		},
	})
	if len(seq) != 0 {
		t.Fatalf("expected non-display progress to not clear stream, got %v", seq)
	}

	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressToolDelta,
			Delta: `{"content":"正文第一句"}`,
		},
	})

	want := []string{"clear", "delta:正文第一句"}
	if len(seq) != len(want) {
		t.Fatalf("unexpected sequence length: got %v want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("unexpected sequence at %d: got %q want %q", i, seq[i], want[i])
		}
	}
}

func TestSessionClearsOnlyOncePerVisibleRound(t *testing.T) {
	var seq []string
	sess := newSession(nil, nil, nil, nil, "", nil,
		func(delta string) { seq = append(seq, "delta:"+delta) },
		func() { seq = append(seq, "clear") },
	)

	sess.handleMessageStart()
	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressToolDelta,
			Delta: `{"content":"第一段"}`,
		},
	})
	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressToolDelta,
			Delta: `{"content":"第二段"}`,
		},
	})

	clearCount := 0
	for _, item := range seq {
		if item == "clear" {
			clearCount++
		}
	}
	if clearCount != 1 {
		t.Fatalf("expected one clear in first round, got %d sequence=%v", clearCount, seq)
	}

	sess.handleMessageStart()
	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressToolDelta,
			Delta: `{"content":"新一轮"}`,
		},
	})

	joined := strings.Join(seq, "|")
	if !strings.Contains(joined, "clear|delta:第一段|delta:第二段|clear|delta:新一轮") {
		t.Fatalf("unexpected round transition sequence: %v", seq)
	}
}

func TestSessionDisplaysSaveFoundationPreview(t *testing.T) {
	var seq []string
	sess := newSession(nil, nil, nil, nil, "", nil,
		func(delta string) { seq = append(seq, "delta:"+delta) },
		func() { seq = append(seq, "clear") },
	)

	args, err := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{
			{"index": 1, "title": "第一卷"},
			{"index": 2, "title": "第二卷"},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	sess.handleMessageStart()
	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressToolStart,
			Tool:  "save_foundation",
			Args:  args,
			Agent: "architect_long",
		},
	})

	if len(seq) != 2 {
		t.Fatalf("unexpected sequence: %v", seq)
	}
	if seq[0] != "clear" {
		t.Fatalf("expected first event to clear stream, got %v", seq)
	}
	if !strings.Contains(seq[1], "正在保存卷弧大纲") || !strings.Contains(seq[1], "2 卷") {
		t.Fatalf("unexpected preview delta: %q", seq[1])
	}
}

func TestSessionDisplaysThinkingProgress(t *testing.T) {
	var seq []string
	sess := newSession(nil, nil, nil, nil, "", nil,
		func(delta string) { seq = append(seq, "delta:"+delta) },
		func() { seq = append(seq, "clear") },
	)

	sess.handleMessageStart()
	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:     agentcore.ProgressThinking,
			Thinking: "正在推演冲突升级路径。",
		},
	})

	if len(seq) != 2 {
		t.Fatalf("unexpected sequence: %v", seq)
	}
	if seq[0] != "clear" {
		t.Fatalf("expected first event to clear stream, got %v", seq)
	}
	if !strings.Contains(seq[1], "正在推演冲突升级路径。") {
		t.Fatalf("unexpected thinking delta: %q", seq[1])
	}
}

func TestSessionThinkingProgressUsesIncrementalDelta(t *testing.T) {
	var seq []string
	sess := newSession(nil, nil, nil, nil, "", nil,
		func(delta string) { seq = append(seq, delta) },
		func() { seq = append(seq, "clear") },
	)

	sess.handleMessageStart()
	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:     agentcore.ProgressThinking,
			Thinking: "Refining character",
		},
	})
	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:     agentcore.ProgressThinking,
			Thinking: "Refining character details",
		},
	})

	if len(seq) != 3 {
		t.Fatalf("unexpected sequence: %v", seq)
	}
	if seq[1] == seq[2] {
		t.Fatalf("expected incremental thinking delta, got duplicated chunks: %v", seq)
	}
	if got, want := seq[2], " details"; got != want {
		t.Fatalf("unexpected incremental suffix: got %q want %q", got, want)
	}
}
