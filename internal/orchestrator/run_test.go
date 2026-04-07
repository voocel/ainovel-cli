package orchestrator

import (
	"encoding/json"
	"errors"
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
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowSteering); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.RunMeta.SetPendingSteer("主角改成女性"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}
	if _, err := rt.Queue(domain.TaskSteerApply, "coordinator", "处理用户干预", "主角改成女性", taskLocation{}); err != nil {
		t.Fatalf("Queue steer task: %v", err)
	}

	newSession(nil, store, rt, nil, "", nil, nil, nil, nil, nil).finalizeSteerIfIdle()

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

	for _, task := range rt.Snapshot() {
		if task.Kind == domain.TaskSteerApply && task.Status == domain.TaskQueued {
			t.Fatalf("expected queued steer task cleared, got %+v", task)
		}
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

	var followUps []string
	sess := newSession(nil, store, nil, nil, "", nil, nil, nil, func(message string) error {
		followUps = append(followUps, message)
		return nil
	}, nil)

	sess.runOperationalDiag()
	sess.runOperationalDiag()

	if len(followUps) != 1 {
		t.Fatalf("expected one follow-up for persistent orphaned steer, got %d", len(followUps))
	}

	if err := store.RunMeta.SetPendingSteer(""); err != nil {
		t.Fatalf("ClearPendingSteer: %v", err)
	}
	if err := store.RunMeta.SetPendingSteer("主角性格再次调整"); err != nil {
		t.Fatalf("SetPendingSteer again: %v", err)
	}

	sess.runOperationalDiag()
	if len(followUps) != 2 {
		t.Fatalf("expected follow-up to fire again after issue reappears, got %d", len(followUps))
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
	var followUps []string
	sess := newSession(nil, store, nil, nil, "", func(ev UIEvent) {
		events = append(events, ev)
	}, nil, nil, func(message string) error {
		followUps = append(followUps, message)
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
	if len(followUps) != 1 {
		t.Fatalf("expected one follow-up, got %+v", followUps)
	}
	if !strings.Contains(followUps[0], "phase=outline") {
		t.Fatalf("unexpected follow-up message: %q", followUps[0])
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

func TestSessionProviderErrorUsesFailoverProviderAndResetsOnNextMessage(t *testing.T) {
	var events []UIEvent
	sess := newSession(nil, nil, nil, nil, "openrouter", func(ev UIEvent) {
		events = append(events, ev)
	}, nil, nil, nil, nil)
	sess.providerSource = func() string { return "openrouter" }

	sess.setCurrentProvider("openai")
	sess.handleProviderError(agentcore.Event{Err: errors.New("429 too many requests")})

	sess.handleMessageStart()
	sess.handleProviderError(agentcore.Event{Err: errors.New("429 too many requests")})

	if len(events) != 2 {
		t.Fatalf("expected 2 error events, got %d", len(events))
	}
	if !strings.Contains(events[0].Summary, "[openai][PROVIDER_RATE_LIMIT]") {
		t.Fatalf("unexpected first summary: %q", events[0].Summary)
	}
	if !strings.Contains(events[1].Summary, "[openrouter][PROVIDER_RATE_LIMIT]") {
		t.Fatalf("unexpected second summary: %q", events[1].Summary)
	}
}

func TestHandleSubAgentEventEndEnqueuesFollowUpWhenFoundationPlanIncomplete(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 12); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}
	if _, err := rt.Start(domain.TaskFoundationPlan, "architect", "规划故事基础设定", "", taskLocation{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var followUps []string
	sess := newSession(agentcore.NewAgent(), store, rt, nil, "", nil, nil, nil, func(message string) error {
		followUps = append(followUps, message)
		return nil
	}, nil)

	args := mustMarshalJSON(map[string]any{
		"agent": "architect_long",
		"task":  "完成基础规划",
	})
	sess.handleSubAgentEventEnd(agentcore.Event{
		Tool: "subagent",
		Args: args,
	})

	if len(followUps) != 1 {
		t.Fatalf("expected 1 follow-up, got %d", len(followUps))
	}
	if !strings.Contains(followUps[0], "save_foundation") {
		t.Fatalf("expected save_foundation guidance, got %q", followUps[0])
	}

	task, ok := rt.ActiveTask("architect")
	if !ok {
		t.Fatal("expected architect task still active")
	}
	if task.Status != domain.TaskRunning {
		t.Fatalf("expected architect task still running, got %s", task.Status)
	}
}

func TestHandleArchitectDoneResumesWritingAfterArcExpand(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 12); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatalf("UpdatePhase outline: %v", err)
	}
	if err := store.Progress.SetTotalChapters(12); err != nil {
		t.Fatalf("SetTotalChapters: %v", err)
	}
	if err := store.Progress.SetLayered(true); err != nil {
		t.Fatalf("SetLayered: %v", err)
	}
	if err := store.Progress.UpdateVolumeArc(1, 1); err != nil {
		t.Fatalf("UpdateVolumeArc: %v", err)
	}
	if err := store.Outline.SaveLayeredOutline([]domain.VolumeOutline{
		{
			Index: 1,
			Arcs: []domain.ArcOutline{
				{
					Index: 1,
					Chapters: []domain.OutlineEntry{
						{Chapter: 1, Title: "第一章"},
						{Chapter: 2, Title: "第二章"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := store.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "第一章"},
		{Chapter: 2, Title: "第二章"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := store.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "终局"}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	if err := store.Characters.Save([]domain.Character{{Name: "主角"}}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := store.World.SaveWorldRules([]domain.WorldRule{{Rule: "规则"}}); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}

	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}
	if _, err := rt.Start(domain.TaskArcExpand, "architect", "展开第 1 卷第 1 弧", "", taskLocation{Volume: 1, Arc: 1}); err != nil {
		t.Fatalf("Start architect task: %v", err)
	}

	sess := newSession(agentcore.NewAgent(), store, rt, nil, "", nil, nil, nil, nil, nil)
	sess.handleSubAgentEventEnd(agentcore.Event{
		Tool: "subagent",
		Args: mustMarshalJSON(map[string]any{
			"agent": "architect_long",
			"task":  "展开第 1 卷第 1 弧",
		}),
	})

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.Phase != domain.PhaseWriting {
		t.Fatalf("expected phase writing, got %s", progress.Phase)
	}
	if progress.Flow != domain.FlowWriting {
		t.Fatalf("expected flow writing, got %s", progress.Flow)
	}

	task, ok := nextDispatchableTask(rt.Snapshot())
	if !ok {
		t.Fatal("expected queued writing task")
	}
	if task.Owner != "writer" || task.Kind != domain.TaskChapterWrite || task.Chapter != 1 {
		t.Fatalf("unexpected queued task: %+v", task)
	}
}

func TestNextDispatchableTaskAllowsCoordinatorSteerTask(t *testing.T) {
	tasks := []domain.TaskRecord{
		{Owner: coordinatorRuntimeOwner, Kind: domain.TaskCoordinatorDecision, Status: domain.TaskQueued},
		{Owner: "coordinator", Kind: domain.TaskSteerApply, Status: domain.TaskQueued, Title: "处理用户干预"},
	}

	task, ok := nextDispatchableTask(tasks)
	if !ok {
		t.Fatal("expected queued task")
	}
	if task.Owner != "coordinator" || task.Kind != domain.TaskSteerApply {
		t.Fatalf("unexpected task selected: %+v", task)
	}
}

func TestNextRetryableTaskSkipsRuntimeOwner(t *testing.T) {
	tasks := []domain.TaskRecord{
		{Owner: coordinatorRuntimeOwner, Kind: domain.TaskCoordinatorDecision, Status: domain.TaskRunning},
		{Owner: "writer", Kind: domain.TaskChapterWrite, Status: domain.TaskRunning, Title: "创作第 3 章"},
	}

	task, ok := nextRetryableTask(tasks)
	if !ok {
		t.Fatal("expected running task")
	}
	if task.Owner != "writer" || task.Kind != domain.TaskChapterWrite {
		t.Fatalf("unexpected task selected: %+v", task)
	}
}

func TestDecideNextAfterIdleDoesNotDispatchQueuedTaskWhenRunningTaskIsStuck(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}

	running, err := rt.Start(domain.TaskChapterWrite, "writer", "创作第 1 章", "继续写作", taskLocation{Chapter: 1})
	if err != nil {
		t.Fatalf("Start running task: %v", err)
	}
	if _, err := rt.Queue(domain.TaskChapterReview, "editor", "评审第 1 章", "等待审阅", taskLocation{Chapter: 1}); err != nil {
		t.Fatalf("Queue review task: %v", err)
	}

	eng := &Engine{
		store:     store,
		taskRT:    rt,
		lifecycle: runtimeRunning,
		lastRetry: taskRetryState{
			TaskID:    running.ID,
			UpdatedAt: running.UpdatedAt,
		},
	}

	if eng.decideNextAfterIdle() {
		t.Fatal("expected no auto-continue when running task is stuck")
	}

	var reviewTask domain.TaskRecord
	foundReview := false
	for _, task := range rt.Snapshot() {
		if task.Kind != domain.TaskChapterReview {
			continue
		}
		reviewTask = task
		foundReview = true
		break
	}
	if !foundReview {
		t.Fatal("expected queued review task to remain present")
	}
	if reviewTask.Status != domain.TaskQueued {
		t.Fatalf("expected queued review task to stay queued, got %s", reviewTask.Status)
	}
}

func TestMarkTaskRetryRejectsSameTaskWithoutProgress(t *testing.T) {
	eng := &Engine{}
	now := time.Now()
	task := domain.TaskRecord{
		ID:        "task-1",
		Status:    domain.TaskRunning,
		UpdatedAt: now,
	}

	if !eng.markTaskRetry(task) {
		t.Fatal("expected first retry to pass")
	}
	if eng.markTaskRetry(task) {
		t.Fatal("expected same task without progress to be rejected")
	}

	task.UpdatedAt = now.Add(time.Second)
	if !eng.markTaskRetry(task) {
		t.Fatal("expected retry after progress update to pass")
	}
}

func TestApplyControlIntentRejectsSteerMessageWhenIdle(t *testing.T) {
	eng := &Engine{lifecycle: runtimeIdle}

	err := eng.applyControlIntent(domain.ControlIntent{
		Kind:    domain.ControlIntentSteerMessage,
		Message: "调整主角设定",
	})
	if err == nil {
		t.Fatal("expected steer_message to be rejected when coordinator is idle")
	}
	if !strings.Contains(err.Error(), "active coordinator") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskRuntimeStartPromotesQueuedTaskToRunning(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}

	queued, err := rt.Queue(domain.TaskChapterWrite, "writer", "创作第 2 章", "等待写作", taskLocation{Chapter: 2})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	started, err := rt.Start(domain.TaskChapterWrite, "writer", "创作第 2 章", "等待写作", taskLocation{Chapter: 2})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if started.ID != queued.ID {
		t.Fatalf("expected queued task promoted in place, got queued=%s started=%s", queued.ID, started.ID)
	}
	if started.Status != domain.TaskRunning {
		t.Fatalf("expected running status, got %s", started.Status)
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
