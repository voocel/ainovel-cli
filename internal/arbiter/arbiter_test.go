package arbiter

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/progress"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

// testModel 组装一次结构化裁定的模型句柄：假 provider + 请求模板。
func testModel(t *testing.T, provider litellm.Provider, override *bool) llmcontract.Model {
	t.Helper()
	client, err := litellm.New(provider)
	if err != nil {
		t.Fatalf("构造 client: %v", err)
	}
	return llmcontract.Model{
		Core:               agentcore.Model{Client: client, Request: litellm.Request{Model: "test-model"}},
		Provider:           provider.Name(),
		ModelName:          "test-model",
		JSONSchemaOverride: override,
	}
}

// hookProvider 在 litellmtest 脚本 provider 之外加两个钩子：第 n 次调用前的
// 副作用（取消 context）与按调用序直接失败。
type hookProvider struct {
	*litellmtest.Provider
	mu     sync.Mutex
	calls  int
	before func(n int)
	fail   func(n int) error
}

func (p *hookProvider) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if p.before != nil {
		p.before(n)
	}
	if p.fail != nil {
		if err := p.fail(n); err != nil {
			return nil, err
		}
	}
	return p.Provider.Chat(ctx, req)
}

func (p *hookProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// messageText 拼接一条消息里的文本块。
func messageText(m litellm.Message) string {
	var out string
	for _, block := range m.Blocks {
		if text, ok := block.(litellm.TextBlock); ok {
			out += text.Text
		}
	}
	return out
}

// temporaryErr 是 provider 声明的临时错误；RetryAfter 取毫秒级，避免测试等退避。
func temporaryErr() error {
	err := litellm.NewError("test", litellm.ErrorTypeRateLimit, "provider unavailable", nil)
	err.RetryAfter = time.Millisecond
	return err
}

func TestDecidePlanStartSendsNoThinkingAndNoOutputLimit(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(
		`{"planner":"architect_short","task":"规划短篇","reason":"篇幅较短"}`))
	if _, err := DecidePlanStart(t.Context(), testModel(t, provider, nil), "sys", "写一部短篇", ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	req := provider.Requests()[0]
	if req.Thinking != nil {
		t.Fatalf("Arbiter 不应向普通 chat 模型发送 thinking 参数, got %+v", req.Thinking)
	}
	if req.MaxTokens != nil {
		t.Fatalf("Arbiter 不应设置输出上限, got max_tokens=%d", *req.MaxTokens)
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if _, ok := req.ProviderOptions[key]; ok {
			t.Fatalf("Arbiter 不应设置输出上限, 请求携带 %s", key)
		}
	}
}

func TestDecidePromptContractAppendsSchema(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(
		`{"planner":"architect_short","task":"规划短篇","reason":"篇幅较短"}`))
	const semanticPrompt = "只根据需求判断规划方式。"
	if _, err := DecidePlanStart(t.Context(), testModel(t, provider, new(false)), semanticPrompt, "写一部短篇", ""); err != nil {
		t.Fatal(err)
	}
	got := messageText(provider.Requests()[0].Messages[0])
	for _, want := range []string{semanticPrompt, "<output-json-schema>", `"planner"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt contract 缺少 %q:\n%s", want, got)
		}
	}
}

func TestDecidePlanStart_ValidAndFeedbackRetry(t *testing.T) {
	// 第一次输出非法(planner 错),第二次带围栏但合法——反馈重试 + JSON 提取都要工作。
	provider := litellmtest.New(
		litellmtest.Text(`{"planner":"writer","task":"x","reason":"r"}`),
		litellmtest.Text("```json\n{\"planner\":\"architect_short\",\"task\":\"写一个 20 章的悬疑短篇……\",\"reason\":\"用户显式要求短篇\"}\n```"),
	)
	d, err := DecidePlanStart(context.Background(), testModel(t, provider, new(false)), "sys", "20章悬疑短篇", "suspense")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if d.Planner != "architect_short" || !strings.Contains(d.Task, "悬疑") {
		t.Fatalf("裁定错误: %+v", d)
	}
	if got := len(provider.Requests()); got != 2 {
		t.Fatalf("应恰好 2 次调用（1 非法 + 1 反馈重试成功），got %d", got)
	}
}

func TestDecide_InvalidOutputContinuesUntilContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &hookProvider{
		Provider: litellmtest.New(
			litellmtest.Text("完全不是 JSON"),
			litellmtest.Text("完全不是 JSON"),
			litellmtest.Text("完全不是 JSON"),
			litellmtest.Text("完全不是 JSON"),
		),
		before: func(n int) {
			if n >= 4 {
				cancel()
			}
		},
	}
	if _, err := DecidePlanStart(ctx, testModel(t, provider, new(false)), "sys", "需求", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("应由 context 结束自愈循环，得 %v", err)
	}
	if got := provider.Calls(); got != 4 {
		t.Fatalf("context 取消前应持续调用，got %d", got)
	}
}

func TestDecide_RetryableModelErrorReportsSharedProgress(t *testing.T) {
	provider := &hookProvider{
		Provider: litellmtest.New(litellmtest.Text(
			`{"planner":"architect_short","task":"规划短篇","reason":"篇幅较短"}`)),
		fail: func(n int) error {
			if n <= 8 {
				return temporaryErr()
			}
			return nil
		},
	}
	var reports []progress.Event
	ctx := agentcore.WithProgress(context.Background(), func(p any) {
		if ev, ok := progress.From(p); ok {
			reports = append(reports, ev)
		}
	})

	if _, err := DecidePlanStart(ctx, testModel(t, provider, nil), "sys", "需求", ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got := provider.Calls(); got != 9 {
		t.Fatalf("model calls = %d, want 9", got)
	}
	if len(reports) != 8 {
		t.Fatalf("progress = %+v", reports)
	}
	last := reports[7]
	if last.Kind != progress.KindRetry || last.Agent != "arbiter" || last.Attempt != 8 || last.MaxRetries != 0 {
		t.Fatalf("progress = %+v", reports)
	}
}

func TestDecide_NonRetryableModelErrorFailsImmediately(t *testing.T) {
	terminal := litellm.NewError("test", litellm.ErrorTypeValidation, "bad request", nil)
	provider := &hookProvider{
		Provider: litellmtest.New(litellmtest.Text(`{}`)),
		fail:     func(int) error { return terminal },
	}
	if _, err := DecidePlanStart(context.Background(), testModel(t, provider, nil), "sys", "需求", ""); !errors.Is(err, terminal) {
		t.Fatalf("non-retryable model error should fail immediately, got %v", err)
	}
	if got := provider.Calls(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
}

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
		{"目标章节暂停", InterventionDecision{Hold: &AdvanceHoldOp{After: domain.AdvanceHoldAtChapter, TargetChapter: 15, Reason: "写到第15章"}, Reason: "r"}, writing, false},
		{"目标章节未填写", InterventionDecision{Hold: &AdvanceHoldOp{After: domain.AdvanceHoldAtChapter, Reason: "写到目标章"}, Reason: "r"}, writing, true},
		{"目标章节已经完成", InterventionDecision{Hold: &AdvanceHoldOp{After: domain.AdvanceHoldAtChapter, TargetChapter: 10, Reason: "写到第10章"}, Reason: "r"}, writing, true},
		{"非目标暂停携带章节", InterventionDecision{Hold: &AdvanceHoldOp{After: domain.AdvanceHoldAtBoundary, TargetChapter: 15, Reason: "停"}, Reason: "r"}, writing, true},
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

func TestDecideInterventionAcceptsTargetChapterHold(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(`{
		"answer":"将连续写到第15章后暂停",
		"rules":null,
		"hold":{"cancel":false,"after":"chapter","target_chapter":15,"reason":"写到第15章后暂停"},
		"reopen":null,
		"dispatch":null,
		"reason":"用户指定了一次性运行终点"
	}`))
	d, err := DecideIntervention(t.Context(), testModel(t, provider, new(false)), "sys", InterventionFacts{
		Phase: string(domain.PhaseWriting), CompletedChapters: 10, NextChapter: 11,
	}, "写到第15章")
	if err != nil {
		t.Fatal(err)
	}
	if d.Hold == nil || d.Hold.After != domain.AdvanceHoldAtChapter || d.Hold.TargetChapter != 15 {
		t.Fatalf("目标章节 hold 解码错误: %+v", d.Hold)
	}
}

func TestFailureDecision_Validate(t *testing.T) {
	facts := FailureFacts{Kind: "worker_failure", Phase: string(domain.PhaseWriting)}
	ok := FailureDecision{Action: "reroute", Dispatch: &DispatchOp{Agent: "architect_long", Task: "先 expand_next_arc"}, Reason: "错误指明出路"}
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
	if err := st.Progress.Init(30); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "测试书", Synopsis: "测试简介"}); err != nil {
		t.Fatalf("book: %v", err)
	}
	if err := st.RunMeta.Init("default", "openrouter", "m"); err != nil {
		t.Fatalf("run meta: %v", err)
	}
	if err := st.RunMeta.SetAdvanceMode(domain.ChapterAdvanceReview); err != nil {
		t.Fatalf("advance mode: %v", err)
	}
	if err := st.RunMeta.SetAdvanceHold(domain.AdvanceHold{After: domain.AdvanceHoldAtChapter, TargetChapter: 20, Reason: "写到第20章"}); err != nil {
		t.Fatalf("advance hold: %v", err)
	}
	if _, err := st.Decisions.Append(storepkg.DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "上次干预", Reason: "已入队"}); err != nil {
		t.Fatalf("append decision: %v", err)
	}

	f, err := CollectInterventionFacts(st)
	if err != nil {
		t.Fatalf("CollectInterventionFacts: %v", err)
	}
	if f.Title != "测试书" {
		t.Fatalf("facts 应含书名, got %+v", f)
	}
	if len(f.RecentDecisions) != 1 || f.RecentDecisions[0].Input != "上次干预" {
		t.Fatalf("干预记忆缺失: %+v", f.RecentDecisions)
	}
	if f.AdvanceMode != string(domain.ChapterAdvanceReview) || !f.HasAdvanceHold || f.AdvanceHoldAfter != string(domain.AdvanceHoldAtChapter) || f.AdvanceHoldTargetChapter != 20 {
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

func TestCollectInterventionFactsDoesNotExposeLayeredEstimateAsTotal(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(66); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1, Title: "卷一", Arcs: []domain.ArcOutline{
			{Index: 1, Title: "当前弧", Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}}},
			{Index: 2, Title: "骨架弧", EstimatedChapters: 64},
		},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	p, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Layered = true
	if err := st.Progress.Save(p); err != nil {
		t.Fatal(err)
	}

	facts, err := CollectInterventionFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.DynamicPlanning || facts.OutlinedChapters != 2 {
		t.Fatalf("动态规划事实错误: %+v", facts)
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "total_chapters") || strings.Contains(string(raw), `:66`) {
		t.Fatalf("内部估算不得作为总章数进入 Arbiter: %s", raw)
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
		if got := llmcontract.ExtractJSONObject(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

// 契约测试(RFC §11.1):根为 object、全属性(含嵌套)required、dispatch 为可空对象。
func TestContractSchemasAreStrictReady(t *testing.T) {
	for _, c := range []llmcontract.Contract{planStartContract, failureContract, interventionContract} {
		if c.Schema["type"] != "object" {
			t.Fatalf("%s: 根必须是 object", c.Name)
		}
		if err := llmcontract.ValidateStrictReady(c.Schema); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if len(c.Fingerprint()) != 12 {
			t.Fatalf("%s: fingerprint 异常: %q", c.Name, c.Fingerprint())
		}
	}
	dispatch := failureContract.Schema["properties"].(map[string]any)["dispatch"].(map[string]any)
	types, ok := dispatch["type"].([]string)
	if !ok || !slices.Contains(types, "null") || !slices.Contains(types, "object") {
		t.Fatalf("dispatch 应为可空对象: %v", dispatch["type"])
	}
	var d FailureDecision
	if err := json.Unmarshal([]byte(`{"action":"retry","dispatch":null,"reason":"瞬时错误"}`), &d); err != nil {
		t.Fatalf("含 dispatch:null 的样本应可解码: %v", err)
	}
	if err := d.ValidateAgainst(FailureFacts{Phase: "writing"}); err != nil {
		t.Fatalf("样本应过校验: %v", err)
	}
}

func TestDecideNativeSendsSchemaAndDecodesFullOutput(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(
		`{"planner":"architect_short","task":"规划短篇","reason":"篇幅较短"}`))
	const semanticPrompt = "只根据需求判断规划方式。"
	d, err := DecidePlanStart(t.Context(), testModel(t, provider, nil), semanticPrompt, "写一部短篇", "")
	if err != nil || d.Planner != "architect_short" {
		t.Fatalf("native 裁定失败: %+v %v", d, err)
	}
	req := provider.Requests()[0]
	rf := req.ResponseFormat
	if rf == nil || rf.Type != litellm.ResponseFormatJSONSchema || rf.JSONSchema == nil {
		t.Fatalf("native 模式应发送 response_format: %+v", rf)
	}
	if rf.JSONSchema.Name != "arbiter_plan_start" || rf.JSONSchema.Strict == nil || !*rf.JSONSchema.Strict {
		t.Fatalf("schema 参数不符: %+v", rf.JSONSchema)
	}
	if got := messageText(req.Messages[0]); got != semanticPrompt {
		t.Fatalf("native 模式不应向提示词重复注入 schema:\n%s", got)
	}
}

// native 模式下解码失败=provider 契约违约:立即报错,不走 extractJSON 兜底、不重问。
func TestDecideNativeFencedOutputIsContractViolation(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(
		"```json\n{\"planner\":\"architect_short\",\"task\":\"x\",\"reason\":\"y\"}\n```"))
	_, err := DecidePlanStart(t.Context(), testModel(t, provider, nil), "sys", "写一部短篇", "")
	if err == nil || !strings.Contains(err.Error(), "契约违约") {
		t.Fatalf("期望契约违约错误, got %v", err)
	}
	if got := len(provider.Requests()); got != 1 {
		t.Fatalf("契约违约不应重问, 调用了 %d 次", got)
	}
}

// native 模式业务校验失败仍反馈重问,且重问请求保留 schema。
func TestDecideNativeValidateFailureFeedbackKeepsSchema(t *testing.T) {
	provider := litellmtest.New(
		litellmtest.Text(`{"action":"reroute","dispatch":null,"reason":"需要换路"}`),
		litellmtest.Text(`{"action":"retry","dispatch":null,"reason":"瞬时错误可重试"}`),
	)
	d, err := DecideFailure(t.Context(), testModel(t, provider, nil), "sys", FailureFacts{Kind: "worker_failure", Phase: "writing"})
	if err != nil || d.Action != "retry" {
		t.Fatalf("反馈重问后应成功: %+v %v", d, err)
	}
	reqs := provider.Requests()
	if len(reqs) != 2 {
		t.Fatalf("应恰好两次调用, got %d", len(reqs))
	}
	if reqs[1].ResponseFormat == nil {
		t.Fatal("重问请求丢失了 schema")
	}
}

// native 模式先分类终止原因:截断/拒答/空响应是独立错误事实,不进重问循环。
func TestDecideNativeStopReasonClassification(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		stop    litellm.FinishReason
		wantErr string
	}{
		{"length 截断", `{"planner":`, litellm.FinishReasonLength, "截断"},
		{"safety 拒答", `无法协助`, litellm.FinishReasonSafety, "拒答"},
		{"空响应", ``, litellm.FinishReasonStop, "空内容"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := litellmtest.New(litellmtest.Reply{
				Blocks:       []litellm.Block{litellm.Text(tc.output)},
				FinishReason: tc.stop,
			})
			_, err := DecidePlanStart(t.Context(), testModel(t, provider, nil), "sys", "写一部短篇", "")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("期望 %q 错误, got %v", tc.wantErr, err)
			}
			if got := len(provider.Requests()); got != 1 {
				t.Fatalf("终止原因不应重问, 调用了 %d 次", got)
			}
		})
	}
}

// marshalPayload 失败必须暴露:静默伪造 {} 会让模型基于假事实误判。
func TestMarshalPayloadErrors(t *testing.T) {
	if _, err := marshalPayload(func() {}); err == nil {
		t.Fatal("不可序列化载荷应报错")
	}
	s, err := marshalPayload(map[string]int{"a": 1})
	if err != nil || !strings.Contains(s, `"a"`) {
		t.Fatalf("正常载荷: %q %v", s, err)
	}
}
