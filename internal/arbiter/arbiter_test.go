package arbiter

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// scriptedModel 按调用序号返回预设文本。
type scriptedModel struct {
	outputs        []string
	idx            int64
	lastCfg        agentcore.CallConfig
	rejectThinking bool
}

func (m *scriptedModel) take() string {
	i := int(atomic.AddInt64(&m.idx, 1) - 1)
	if i >= len(m.outputs) {
		return m.outputs[len(m.outputs)-1]
	}
	return m.outputs[i]
}

func (m *scriptedModel) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.lastCfg = agentcore.ResolveCallConfig(opts)
	if m.rejectThinking && m.lastCfg.ThinkingLevel != agentcore.ThinkingAuto {
		return nil, errors.New("thinking is only supported for reasoning chat models")
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(m.take())},
	}}, nil
}

func TestDecidePlanStartDoesNotSendThinkingToChatModel(t *testing.T) {
	m := &scriptedModel{outputs: []string{
		`{"planner":"architect_short","task":"规划短篇","reason":"篇幅较短"}`,
	}, rejectThinking: true}
	if _, err := DecidePlanStart(t.Context(), m, "sys", "写一部短篇", ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if m.lastCfg.ThinkingLevel != agentcore.ThinkingAuto {
		t.Fatalf("Arbiter 不应向普通 chat 模型发送 thinking 参数, got %q", m.lastCfg.ThinkingLevel)
	}
	if m.lastCfg.MaxTokens != decideMaxTokens {
		t.Fatalf("max_tokens = %d, want %d", m.lastCfg.MaxTokens, decideMaxTokens)
	}
}

func (m *scriptedModel) GenerateStream(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	resp, _ := m.Generate(ctx, msgs, tools, opts...)
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message}
	close(ch)
	return ch, nil
}

func (m *scriptedModel) SupportsTools() bool { return true }

type retryableTestError struct {
	retryable bool
}

func (e retryableTestError) Error() string             { return "provider unavailable" }
func (e retryableTestError) Retryable() bool           { return e.retryable }
func (e retryableTestError) RetryAfter() time.Duration { return time.Millisecond }

type failingThenValidModel struct {
	failures int64
	calls    int64
}

func (m *failingThenValidModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	call := atomic.AddInt64(&m.calls, 1)
	if call <= m.failures {
		return nil, retryableTestError{retryable: true}
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(
			`{"planner":"architect_short","task":"规划短篇","reason":"篇幅较短"}`)},
	}}, nil
}

func (m *failingThenValidModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("unused")
}

func (m *failingThenValidModel) SupportsTools() bool { return true }

func TestDecidePlanStart_ValidAndFeedbackRetry(t *testing.T) {
	// 第一次输出非法(planner 错),第二次带围栏但合法——反馈重试 + JSON 提取都要工作。
	m := &scriptedModel{outputs: []string{
		`{"planner":"writer","task":"x","reason":"r"}`,
		"```json\n{\"planner\":\"architect_short\",\"task\":\"写一个 20 章的悬疑短篇……\",\"reason\":\"用户显式要求短篇\"}\n```",
	}}
	d, err := DecidePlanStart(context.Background(), m, "sys", "20章悬疑短篇", "suspense")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if d.Planner != "architect_short" || !strings.Contains(d.Task, "悬疑") {
		t.Fatalf("裁定错误: %+v", d)
	}
	if got := atomic.LoadInt64(&m.idx); got != 2 {
		t.Fatalf("应恰好 2 次调用（1 非法 + 1 反馈重试成功），got %d", got)
	}
}

func TestDecide_FailsAfterMaxAttempts(t *testing.T) {
	m := &scriptedModel{outputs: []string{"完全不是 JSON"}}
	if _, err := DecidePlanStart(context.Background(), m, "sys", "需求", ""); err == nil {
		t.Fatal("连续非法输出应返回错误（由调用方降级）")
	}
	if got := atomic.LoadInt64(&m.idx); got != decideMaxAttempts {
		t.Fatalf("应尝试 %d 次, got %d", decideMaxAttempts, got)
	}
}

func TestDecide_RetryableModelErrorReportsSharedProgress(t *testing.T) {
	m := &failingThenValidModel{failures: 2}
	var progress []agentcore.ProgressPayload
	ctx := agentcore.WithToolProgress(context.Background(), func(p agentcore.ProgressPayload) {
		progress = append(progress, p)
	})

	if _, err := DecidePlanStart(ctx, m, "sys", "需求", ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got := atomic.LoadInt64(&m.calls); got != 3 {
		t.Fatalf("model calls = %d, want 3", got)
	}
	if len(progress) != 2 || progress[1].Kind != agentcore.ProgressRetry || progress[1].Agent != "arbiter" || progress[1].Attempt != 2 || progress[1].MaxRetries != modelMaxRetries {
		t.Fatalf("progress = %+v", progress)
	}
}

func TestDecide_NonRetryableModelErrorFailsImmediately(t *testing.T) {
	m := &errorModel{err: retryableTestError{retryable: false}}
	if _, err := DecidePlanStart(context.Background(), m, "sys", "需求", ""); err == nil {
		t.Fatal("non-retryable model error should fail")
	}
	if got := atomic.LoadInt64(&m.calls); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
}

type errorModel struct {
	err   error
	calls int64
}

func (m *errorModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	atomic.AddInt64(&m.calls, 1)
	return nil, m.err
}

func (m *errorModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, m.err
}

func (m *errorModel) SupportsTools() bool { return true }

func TestInterventionDecision_ValidateAgainst(t *testing.T) {
	writing := InterventionFacts{Phase: string(domain.PhaseWriting), CompletedChapters: 10}
	complete := InterventionFacts{Phase: string(domain.PhaseComplete), CompletedChapters: 10}

	cases := []struct {
		name    string
		d       InterventionDecision
		f       InterventionFacts
		wantErr bool
	}{
		{"空决策", InterventionDecision{Reason: "r"}, writing, true},
		{"缺 reason", InterventionDecision{Answer: "好的"}, writing, true},
		{"查询类", InterventionDecision{Answer: "已完成 10 章", Reason: "查询"}, writing, false},
		{"返工组合", InterventionDecision{
			Hold:     &AdvanceHoldOp{After: domain.AdvanceHoldAfterRewritesDrained, Reason: "重写第3章语气"},
			Dispatch: &DispatchOp{Agent: "editor", Task: "重写第3章:语气改冷"},
			Reason:   "改已写章节",
		}, writing, false},
		{"非法派单目标", InterventionDecision{Dispatch: &DispatchOp{Agent: "coordinator", Task: "x"}, Reason: "r"}, writing, true},
		{"写作期 reopen", InterventionDecision{Reopen: &ReopenOp{Chapters: []int{3}}, Reason: "r"}, writing, true},
		{"完本期 reopen", InterventionDecision{Reopen: &ReopenOp{Chapters: []int{3}}, Reason: "返工"}, complete, false},
		{"完本期 reopen 越界", InterventionDecision{Reopen: &ReopenOp{Chapters: []int{99}}, Reason: "r"}, complete, true},
		{"完本期直接派单", InterventionDecision{Dispatch: &DispatchOp{Agent: "writer", Task: "x"}, Reason: "r"}, complete, true},
		{"规划期禁止 writer", InterventionDecision{Dispatch: &DispatchOp{Agent: "writer", Task: "写第1章"}, Reason: "r"}, InterventionFacts{Phase: string(domain.PhaseOutline)}, true},
		{"规划期允许 architect", InterventionDecision{Dispatch: &DispatchOp{Agent: "architect_long", Task: "补齐大纲"}, Reason: "r"}, InterventionFacts{Phase: string(domain.PhaseOutline)}, false},
		{"一次性暂停缺条件", InterventionDecision{Hold: &AdvanceHoldOp{Reason: "停"}, Reason: "r"}, writing, true},
		{"一次性暂停缺摘要", InterventionDecision{Hold: &AdvanceHoldOp{After: domain.AdvanceHoldAtBoundary}, Reason: "r"}, writing, true},
		{"取消一次性暂停", InterventionDecision{Hold: &AdvanceHoldOp{Cancel: true}, Answer: "继续", Reason: "r"}, writing, false},
		{"完本期设置一次性暂停", InterventionDecision{Hold: &AdvanceHoldOp{After: domain.AdvanceHoldAtBoundary, Reason: "停"}, Reason: "r"}, complete, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.ValidateAgainst(tc.f)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got %v", tc.wantErr, err)
			}
		})
	}
}

func TestFailureDecision_Validate(t *testing.T) {
	facts := FailureFacts{Kind: "worker_failure", Phase: string(domain.PhaseWriting)}
	ok := FailureDecision{Action: "reroute", Dispatch: &DispatchOp{Agent: "architect_long", Task: "先 expand_arc"}, Reason: "错误指明出路"}
	if err := ok.ValidateAgainst(facts); err != nil {
		t.Fatalf("合法 reroute 被拒: %v", err)
	}
	bad := FailureDecision{Action: "reroute", Reason: "r"}
	if err := bad.ValidateAgainst(facts); err == nil {
		t.Fatal("reroute 无 dispatch 应被拒")
	}
	if err := (&FailureDecision{Action: "escalate", Reason: "r"}).ValidateAgainst(facts); err == nil {
		t.Fatal("非法 action 应被拒")
	}
	planning := FailureFacts{Kind: "worker_failure", Phase: string(domain.PhaseOutline)}
	writer := FailureDecision{Action: "reroute", Dispatch: &DispatchOp{Agent: "writer", Task: "写第1章"}, Reason: "尝试绕过规划"}
	if err := writer.ValidateAgainst(planning); err == nil {
		t.Fatal("失败裁定不得在规划期派发 writer")
	}
}

func TestCollectInterventionFacts(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("测试书", 30); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.RunMeta.Init("default", "openrouter", "m"); err != nil {
		t.Fatalf("run meta: %v", err)
	}
	if err := st.RunMeta.SetAdvanceMode(domain.ChapterAdvanceReview); err != nil {
		t.Fatalf("advance mode: %v", err)
	}
	if err := st.RunMeta.SetAdvanceHold(domain.AdvanceHold{After: domain.AdvanceHoldAtBoundary, Reason: "验收"}); err != nil {
		t.Fatalf("advance hold: %v", err)
	}
	if _, err := st.Decisions.Append(storepkg.DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "上次干预", Reason: "已入队"}); err != nil {
		t.Fatalf("append decision: %v", err)
	}

	f, err := CollectInterventionFacts(st)
	if err != nil {
		t.Fatalf("CollectInterventionFacts: %v", err)
	}
	if f.NovelName != "测试书" {
		t.Fatalf("facts 应含书名, got %+v", f)
	}
	if len(f.RecentDecisions) != 1 || f.RecentDecisions[0].Input != "上次干预" {
		t.Fatalf("干预记忆缺失: %+v", f.RecentDecisions)
	}
	if f.AdvanceMode != string(domain.ChapterAdvanceReview) || !f.HasAdvanceHold || f.AdvanceHoldAfter != string(domain.AdvanceHoldAtBoundary) {
		t.Fatalf("推进控制事实缺失: %+v", f)
	}
	if len(f.FoundationMissing) == 0 {
		t.Fatal("新书应有基础设定缺项")
	}

	// /reopen 是可枚举事实，必须进 facts：重开后的书章数已写满，缺了它模型会
	// 据 completed=total 推断"已完结"、无视 phase=writing（实测事故）。
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Progress.MarkComplete(); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := st.Progress.ReopenContinue(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	f, err = CollectInterventionFacts(st)
	if err != nil {
		t.Fatalf("CollectInterventionFacts after reopen: %v", err)
	}
	if f.ReopenCount != 1 || f.Phase != string(domain.PhaseWriting) {
		t.Fatalf("重开事实缺失: phase=%s reopen_count=%d", f.Phase, f.ReopenCount)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                        `{"a":1}`,
		"前缀 ```json\n{\"a\":\"}\"}\n```": `{"a":"}"}`, // 字符串里的花括号不干扰平衡
		"没有对象":                           "",
		`{"nested":{"b":2},"c":3} 尾巴`:    `{"nested":{"b":2},"c":3}`,
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}
