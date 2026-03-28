package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestFinalizeSteerIfIdleClearsPendingState(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.InitProgress("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.SetFlow(domain.FlowSteering); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.SetPendingSteer("主角改成女性"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	newSession(nil, store, "", nil, nil, nil).finalizeSteerIfIdle()

	progress, err := store.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.Flow != domain.FlowWriting {
		t.Fatalf("expected flow writing, got %s", progress.Flow)
	}

	runMeta, err := store.LoadRunMeta()
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
	if err := store.InitProgress("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.SetPendingSteer("加入反转"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	newSession(nil, store, "", nil, nil, nil).finalizeSteerIfIdle()

	progress, err := store.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.Flow != domain.FlowRewriting {
		t.Fatalf("expected flow rewriting, got %s", progress.Flow)
	}

	runMeta, err := store.LoadRunMeta()
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
