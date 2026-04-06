package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
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

	newSession(nil, store, nil, nil, "", nil, nil, nil, nil, nil).finalizeSteerIfIdle()

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

	newSession(nil, store, nil, nil, "", nil, nil, nil, nil, nil).finalizeSteerIfIdle()

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

func TestParseContextProgressUsesStructuredPayload(t *testing.T) {
	meta, err := json.Marshal(map[string]any{
		"tokens":           24000,
		"context_window":   128000,
		"percent":          18.75,
		"scope":            "projected",
		"strategy":         "light_trim",
		"active_messages":  14,
		"summary_messages": 1,
		"compacted_count":  22,
		"kept_count":       8,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	progress, ok := parseContextProgress(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressContext,
			Agent: "writer",
			Meta:  meta,
		},
	})
	if !ok {
		t.Fatal("expected structured context progress to be parsed")
	}
	if progress.Agent != "writer" || progress.Tokens != 24000 || progress.ContextWindow != 128000 {
		t.Fatalf("unexpected context progress: %+v", progress)
	}
	if progress.Scope != "projected" || progress.Strategy != "light_trim" {
		t.Fatalf("unexpected context labels: %+v", progress)
	}
}

func TestContextRewriteCallbackAppendsCommittedBoundary(t *testing.T) {
	var events []UIEvent
	var items []domain.RuntimeQueueItem
	callback := contextRewriteCallback("coordinator", func(ev UIEvent) {
		events = append(events, ev)
	}, func(item domain.RuntimeQueueItem) {
		items = append(items, item)
	})

	callback(corecontext.RewriteEvent{
		Reason:       "threshold",
		Strategy:     "light_trim",
		Changed:      true,
		Committed:    true,
		TokensBefore: 98329,
		TokensAfter:  13156,
		Info: &corecontext.SummaryInfo{
			MessagesBefore: 42,
			MessagesAfter:  9,
			CompactedCount: 33,
			KeptCount:      9,
			SummaryLen:     128,
			Duration:       250 * time.Millisecond,
		},
		Steps: []corecontext.RewriteStep{
			{Name: "tool_result_microcompact", Applied: true, TokensBefore: 98329, TokensAfter: 72000},
			{Name: "light_trim", Applied: true, TokensBefore: 72000, TokensAfter: 13156},
		},
	})

	// 2 intermediate dim events + 1 final warn event = 但 light_trim 是最后一个 applied，所以只有 microcompact 是中间步骤
	// 期望：1 个 debug (microcompact) + 1 个 warn (最终汇总) = 2
	if len(events) != 2 {
		t.Fatalf("expected 2 UI events (1 intermediate + 1 final), got %d", len(events))
	}
	if events[0].Level != "debug" || !strings.Contains(events[0].Summary, "tool_result_microcompact") {
		t.Fatalf("expected debug-level intermediate step, got level=%q summary=%q", events[0].Level, events[0].Summary)
	}
	if events[1].Level != "warn" || !strings.Contains(events[1].Summary, "已提交压缩") {
		t.Fatalf("expected warn-level final summary, got level=%q summary=%q", events[1].Level, events[1].Summary)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 boundary item, got %d", len(items))
	}
	if items[0].Kind != domain.RuntimeQueueContextEdge {
		t.Fatalf("expected context boundary kind, got %s", items[0].Kind)
	}
	if items[0].Category != "compacted" {
		t.Fatalf("expected compacted category, got %q", items[0].Category)
	}
	payload, ok := items[0].Payload.(domain.RuntimeContextBoundary)
	if !ok {
		t.Fatalf("expected typed boundary payload, got %T", items[0].Payload)
	}
	if !payload.Committed || payload.Kind != "compacted" || payload.TokensAfter != 13156 {
		t.Fatalf("unexpected boundary payload: %+v", payload)
	}
	if len(payload.Steps) != 2 {
		t.Fatalf("expected 2 steps in boundary payload, got %d", len(payload.Steps))
	}
	if payload.Steps[0].Name != "tool_result_microcompact" || !payload.Steps[0].Applied {
		t.Fatalf("unexpected first step: %+v", payload.Steps[0])
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

func TestSummarizeAskUserRequest(t *testing.T) {
	args, err := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"header":      "篇幅",
				"question":    "你希望写多长？",
				"multiSelect": false,
				"options": []map[string]any{
					{"label": "中篇"},
					{"label": "长篇"},
				},
			},
			{
				"header":      "重心",
				"question":    "你更看重哪部分？",
				"multiSelect": true,
				"options": []map[string]any{
					{"label": "剧情"},
					{"label": "人物"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	summary, payload := summarizeAskUserRequest(args)
	if summary != "等待用户补充关键信息（2 个问题）：篇幅、重心" {
		t.Fatalf("unexpected summary: %q", summary)
	}

	questions, ok := payload["questions"].([]map[string]any)
	if !ok || len(questions) != 2 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if got := questions[1]["multi_select"]; got != true {
		t.Fatalf("expected second question to be multi-select, got %#v", got)
	}
}

func TestExtractAskUserAnswer(t *testing.T) {
	result, err := json.Marshal("用户回答：[篇幅] 长篇；[重心] 剧情升级")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	answer := extractAskUserAnswer(result)
	if answer != "用户回答：[篇幅] 长篇；[重心] 剧情升级" {
		t.Fatalf("unexpected answer: %q", answer)
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
		nil, nil,
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
		nil, nil,
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
		nil, nil,
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

func TestSessionRunOperationalDiagEnqueuesFollowUpOncePerPersistentIssue(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowWriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.RunMeta.SetPendingSteer("主角性格需要调整"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	var controls []domain.ControlIntent
	sess := newSession(nil, store, nil, nil, "", nil, nil, nil, func(intent domain.ControlIntent) error {
		controls = append(controls, intent)
		return nil
	}, nil)

	sess.runOperationalDiag()
	sess.runOperationalDiag()

	if len(controls) != 1 {
		t.Fatalf("expected one follow-up control for persistent orphaned steer, got %d", len(controls))
	}
	if controls[0].Kind != domain.ControlIntentFollowUp {
		t.Fatalf("expected follow-up control, got %+v", controls[0])
	}

	if err := store.RunMeta.SetPendingSteer(""); err != nil {
		t.Fatalf("ClearPendingSteer: %v", err)
	}
	if err := store.RunMeta.SetPendingSteer("主角性格再次调整"); err != nil {
		t.Fatalf("SetPendingSteer again: %v", err)
	}

	sess.runOperationalDiag()
	if len(controls) != 2 {
		t.Fatalf("expected follow-up to fire again after issue reappears, got %d", len(controls))
	}
}

func TestSessionRunOperationalDiagEmitsNoticeForPhaseFlowMismatch(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	progress.Phase = domain.PhaseOutline
	progress.Flow = domain.FlowRewriting
	if err := store.Progress.Save(progress); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}

	var events []UIEvent
	var controls []domain.ControlIntent
	sess := newSession(nil, store, nil, nil, "", func(ev UIEvent) {
		events = append(events, ev)
	}, nil, nil, func(intent domain.ControlIntent) error {
		controls = append(controls, intent)
		return nil
	}, nil)

	sess.runOperationalDiag()

	if len(events) != 1 {
		t.Fatalf("expected one notice event, got %d", len(events))
	}
	if events[0].Category != "SYSTEM" || events[0].Level != "error" {
		t.Fatalf("unexpected notice event: %+v", events[0])
	}
	if !strings.Contains(events[0].Summary, "阶段/流程状态不匹配") {
		t.Fatalf("unexpected notice summary: %q", events[0].Summary)
	}
	if len(controls) != 1 || controls[0].Kind != domain.ControlIntentFollowUp {
		t.Fatalf("expected one follow-up control, got %+v", controls)
	}
	if !strings.Contains(controls[0].Message, "phase=outline") {
		t.Fatalf("unexpected follow-up message: %q", controls[0].Message)
	}
}

func TestSessionDisplaysThinkingProgress(t *testing.T) {
	var seq []string
	sess := newSession(nil, nil, nil, nil, "", nil,
		func(delta string) { seq = append(seq, "delta:"+delta) },
		func() { seq = append(seq, "clear") },
		nil, nil,
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
		nil, nil,
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

func TestSessionTracksAgentContextProgress(t *testing.T) {
	board := newAgentBoard()
	sess := newSession(nil, nil, nil, board, "", nil, nil, nil, nil, nil)

	meta, err := json.Marshal(map[string]any{
		"tokens":         18000,
		"context_window": 128000,
		"percent":        14.06,
		"scope":          "projected",
		"strategy":       "full_summary",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	sess.handleToolExecUpdate(agentcore.Event{
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressContext,
			Agent: "writer",
			Meta:  meta,
		},
	})

	snaps := board.Snapshot()
	var writer AgentSnapshot
	for _, snap := range snaps {
		if snap.Name == "writer" {
			writer = snap
			break
		}
	}
	if writer.Context.Tokens != 18000 || writer.Context.ContextWindow != 128000 {
		t.Fatalf("unexpected writer context: %+v", writer.Context)
	}
	if writer.Context.Scope != "projected" || writer.Context.Strategy != "full_summary" {
		t.Fatalf("unexpected writer context labels: %+v", writer.Context)
	}
}

func TestSessionRefreshesWriterRestoreAfterRelevantTools(t *testing.T) {
	var refreshCalls int
	sess := newSession(nil, nil, nil, nil, "", nil, nil, nil, nil, func() {
		refreshCalls++
	})

	sess.handleToolExecEnd(agentcore.Event{Tool: "plan_chapter"})
	if refreshCalls != 1 {
		t.Fatalf("expected refresh after plan_chapter, got %d", refreshCalls)
	}

	sess.handleToolExecEnd(agentcore.Event{Tool: "draft_chapter"})
	if refreshCalls != 1 {
		t.Fatalf("expected no refresh after draft_chapter, got %d", refreshCalls)
	}

	sess.handleToolExecEnd(agentcore.Event{Tool: "save_foundation"})
	if refreshCalls != 2 {
		t.Fatalf("expected refresh after save_foundation, got %d", refreshCalls)
	}
}
