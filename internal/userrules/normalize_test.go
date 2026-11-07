package userrules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

func TestExtractJSON_StripsCodeFences(t *testing.T) {
	cases := []struct{ in, wantHas string }{
		{"```json\n{\"a\":1}\n```", `"a":1`},
		{"```\n{\"a\":1}\n```", `"a":1`},
		{"前缀解释\n{\"a\":1}\n后缀", `"a":1`},
		{"{\"a\":1}", `"a":1`},
	}
	for _, c := range cases {
		got := llmcontract.ExtractJSONObject(c.in)
		if got == "" {
			t.Fatalf("extractJSON(%q) 返回空", c.in)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Fatalf("extractJSON(%q)=%q 不是合法 JSON: %v", c.in, got, err)
		}
	}
	if llmcontract.ExtractJSONObject("没有任何 JSON") != "" {
		t.Fatal("无 JSON 时应返回空串")
	}
}

func TestParseNormalizerJSON_FullOutput(t *testing.T) {
	raw := "```json\n" + `{
  "structured": {
    "genre": "都市",
    "forbidden_chars": [],
    "forbidden_phrases": ["某种程度上"],
    "fatigue_words": [{"word": "竟然", "max_per_chapter": 2}]
  },
  "preferences": "主角冷静克制",
  "uncertain": ["少用比喻：无阈值"]
}` + "\n```"
	body := llmcontract.ExtractJSONObject(raw)
	if err := llmcontract.ValidateJSON(normalizeContract.Schema, []byte(body)); err != nil {
		t.Fatalf("应解析成功: %v", err)
	}
	var out normalizerOutput
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("应解码成功: %v", err)
	}
	cand, err := out.toCandidate("startup_prompt")
	if err != nil {
		t.Fatalf("toCandidate: %v", err)
	}
	if cand.Structured.Genre != "都市" {
		t.Fatalf("genre 解析错误：%+v", cand.Structured)
	}
	if len(cand.Structured.ForbiddenPhrases) != 1 || cand.Structured.ForbiddenPhrases[0] != "某种程度上" {
		t.Fatalf("forbidden_phrases 解析错误：%v", cand.Structured.ForbiddenPhrases)
	}
	if cand.Structured.FatigueWords["竟然"] != 2 {
		t.Fatalf("fatigue_words 数组应转成 map：%v", cand.Structured.FatigueWords)
	}
	if cand.Preferences != "主角冷静克制" {
		t.Fatalf("preferences 解析错误：%q", cand.Preferences)
	}
	if len(cand.Uncertain) != 1 {
		t.Fatalf("uncertain 应有 1 条，得到 %v", cand.Uncertain)
	}
}

// fatigue 条目校验：空词与非正整数阈值都是可反馈修正的业务错误。
func TestToCandidateRejectsInvalidFatigueEntries(t *testing.T) {
	bad := normalizerOutput{Structured: normalizerStructured{
		FatigueWords: []fatigueWordEntry{{Word: " ", MaxPerChapter: 2}},
	}}
	if _, err := bad.toCandidate("x"); err == nil {
		t.Fatal("空词条目应报错")
	}
	bad = normalizerOutput{Structured: normalizerStructured{
		FatigueWords: []fatigueWordEntry{{Word: "竟然", MaxPerChapter: 0}},
	}}
	if _, err := bad.toCandidate("x"); err == nil {
		t.Fatal("非正整数阈值应报错")
	}
}

func TestParseNormalizerJSON_GarbageFails(t *testing.T) {
	if body := llmcontract.ExtractJSONObject("模型只回了一句话，没有 JSON"); body != "" {
		t.Fatal("无 JSON 应解析失败（触发降级）")
	}
	if body := llmcontract.ExtractJSONObject("{ 不完整"); body != "" {
		t.Fatal("残缺 JSON 应解析失败")
	}
}

// 契约测试(RFC §11.1):根为 object、全属性(含嵌套 structured/fatigue_words 条目)required。
func TestNormalizeContractIsStrictReady(t *testing.T) {
	if normalizeContract.Schema["type"] != "object" {
		t.Fatal("根必须是 object")
	}
	if err := llmcontract.ValidateStrictReady(normalizeContract.Schema); err != nil {
		t.Fatal(err)
	}
}

func TestNormalize_ZeroModelErrors(t *testing.T) {
	// 无模型可用：返回明确错误，由 Service 层降级为 raw preferences。
	var n *Normalizer = NewNormalizer(llmcontract.Model{})
	if _, err := n.Normalize(t.Context(), "startup_prompt", "每章1200字，主角冷静"); err == nil {
		t.Fatal("无模型应返回错误")
	}
}

// rulesModel 组装归一化用的模型句柄：假 provider + 请求模板，无网络。
// override 决定结构化协议：nil 交给 adapter，true 原生 schema，false 提示词契约。
func rulesModel(t *testing.T, provider litellm.Provider, override *bool) llmcontract.Model {
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

// requestTexts 按顺序拼出一次请求里所有消息的文本，便于断言反馈重试把坏输出与纠正提示
// 并入下一轮对话。
func requestTexts(req litellm.Request) []string {
	out := make([]string, 0, len(req.Messages))
	for _, msg := range req.Messages {
		out = append(out, messageText(msg))
	}
	return out
}

// rateLimitErr 是 provider 声明的临时错误；RetryAfter 取毫秒级，避免测试等退避。
func rateLimitErr() error {
	err := litellm.NewError("test", litellm.ErrorTypeRateLimit, "provider unavailable", nil)
	err.RetryAfter = time.Millisecond
	return err
}

const validNormalizerJSON = `{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":["某种程度上"],"fatigue_words":[]},"preferences":"","uncertain":[]}`

// 反馈式重试：首轮吐坏 JSON、次轮才合法。Normalize 应成功，且次轮对话里带上了上一轮的
// 坏输出与纠正提示（反馈式，而非原样盲重试）。
func TestNormalize_FeedbackRetryRecovers(t *testing.T) {
	provider := litellmtest.New(
		litellmtest.Text("这不是 JSON"),
		litellmtest.Text(validNormalizerJSON),
	)
	n := NewNormalizer(rulesModel(t, provider, new(false)))

	cand, err := n.Normalize(t.Context(), "startup_prompt", "不要出现某种程度上")
	if err != nil {
		t.Fatalf("次轮已返回合法 JSON，不应失败: %v", err)
	}
	if len(cand.Structured.ForbiddenPhrases) != 1 {
		t.Fatalf("应解析出 forbidden_phrases，got %+v", cand.Structured)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("应在第 2 次成功，实际调用 %d 次", len(requests))
	}

	var sawBad, sawHint bool
	for _, msg := range requestTexts(requests[1]) {
		if msg == "这不是 JSON" {
			sawBad = true
		}
		if strings.Contains(msg, "JSON Schema") && strings.Contains(msg, "错误：") {
			sawHint = true
		}
	}
	if !sawBad || !sawHint {
		t.Errorf("次轮应并入上一轮坏输出与纠正提示，sawBad=%v sawHint=%v", sawBad, sawHint)
	}
	system := messageText(requests[1].Messages[0])
	if !strings.Contains(system, "<output-json-schema>") || !strings.Contains(system, `"fatigue_words"`) {
		t.Fatalf("prompt contract 应从 Contract 自动附加 schema:\n%s", system)
	}
}

// 归一化不覆盖模型的 thinking 默认（普通 chat 模型会拒绝显式 off），且不设输出上限。
func TestNormalize_LeavesThinkingUnspecifiedAndNoOutputLimit(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(
		`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":[],"fatigue_words":[]},"preferences":"x","uncertain":[]}`))
	n := NewNormalizer(rulesModel(t, provider, new(false)))

	if _, err := n.Normalize(t.Context(), "startup_prompt", "随便一条规则"); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	req := provider.Requests()[0]
	if req.Thinking != nil {
		t.Errorf("不应发送 thinking 参数，got %+v", req.Thinking)
	}
	if req.MaxTokens != nil {
		t.Errorf("不应设置输出上限，got max_tokens=%d", *req.MaxTokens)
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if _, ok := req.ProviderOptions[key]; ok {
			t.Errorf("不应设置输出上限，请求携带 %s", key)
		}
	}
}

// cancelProvider 在脚本 provider 之外加一个钩子：第 n 次调用前取消 context。
type cancelProvider struct {
	*litellmtest.Provider
	cancel context.CancelFunc
	mu     sync.Mutex
	calls  int
}

func (p *cancelProvider) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n >= 4 {
		p.cancel()
	}
	return p.Provider.Chat(ctx, req)
}

// 全程坏 JSON：没有固定次数上限，持续反馈重问，直到 context 取消。
func TestNormalize_FeedbackRetryContinuesUntilContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &cancelProvider{
		Provider: litellmtest.New(
			litellmtest.Text("坏"), litellmtest.Text("坏"), litellmtest.Text("坏"), litellmtest.Text("坏"),
		),
		cancel: cancel,
	}
	n := NewNormalizer(rulesModel(t, provider, new(false)))

	_, err := n.Normalize(ctx, "startup_prompt", "每章1200字")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应由 context 结束自愈循环，得 %v", err)
	}
	if calls := len(provider.Provider.Requests()); calls != 4 {
		t.Fatalf("context 取消前应持续调用，实际 %d", calls)
	}
}

// 终止错误（401 等）不得盲重试：恰好 1 次调用即返回错误。
func TestNormalize_TerminalErrorStopsImmediately(t *testing.T) {
	provider := litellmtest.New(litellmtest.Fail(
		litellm.NewError("test", litellm.ErrorTypeAuth, "401 authentication failed", nil)))
	n := NewNormalizer(rulesModel(t, provider, new(false)))

	_, err := n.Normalize(t.Context(), "startup_prompt", "规则")
	if err == nil {
		t.Fatal("应透出终止错误")
	}
	var apiErr *litellm.Error
	if !errors.As(err, &apiErr) || apiErr.Type != litellm.ErrorTypeAuth {
		t.Fatalf("应透出鉴权错误，得 %v", err)
	}
	if calls := len(provider.Requests()); calls != 1 {
		t.Fatalf("终止错误不应重试，实际调用 %d 次", calls)
	}
}

// retryable 请求错误由 llmretry 退避重试。
func TestNormalize_RetryableErrorRecovers(t *testing.T) {
	provider := litellmtest.New(
		litellmtest.Fail(rateLimitErr()),
		litellmtest.Fail(rateLimitErr()),
		litellmtest.Text(`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":[],"fatigue_words":[]},"preferences":"x","uncertain":[]}`),
	)
	n := NewNormalizer(rulesModel(t, provider, new(false)))

	cand, err := n.Normalize(t.Context(), "startup_prompt", "规则")
	if err != nil || cand.Preferences != "x" {
		t.Fatalf("退避后应成功: %+v %v", cand, err)
	}
	if calls := len(provider.Requests()); calls != 3 {
		t.Fatalf("应在第 3 次成功，实际调用 %d 次", calls)
	}
}

func TestNormalize_NativeSendsSchemaAndRejectsFences(t *testing.T) {
	// 原生模式：schema 进请求；裸 JSON 成功。
	provider := litellmtest.New(litellmtest.Text(
		`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":[],"fatigue_words":[]},"preferences":"x","uncertain":[]}`))
	n := NewNormalizer(rulesModel(t, provider, new(true)))
	cand, err := n.Normalize(t.Context(), "startup_prompt", "规则")
	if err != nil || cand.Preferences != "x" {
		t.Fatalf("native 归一化失败: %+v %v", cand, err)
	}
	rf := provider.Requests()[0].ResponseFormat
	if rf == nil || rf.JSONSchema == nil || rf.JSONSchema.Name != "userrules_normalize" {
		t.Fatalf("native 模式应发送 schema: %+v", rf)
	}
	if got := messageText(provider.Requests()[0].Messages[0]); got != normalizerSystemPrompt {
		t.Fatalf("native 模式不应向提示词重复注入 schema:\n%s", got)
	}

	// 围栏输出=契约违约：立即报错，不走 extractJSON、不重问。
	fenced := litellmtest.New(litellmtest.Text(
		"```json\n{\"structured\":{},\"preferences\":\"x\",\"uncertain\":[]}\n```"))
	n = NewNormalizer(rulesModel(t, fenced, new(true)))
	_, err = n.Normalize(t.Context(), "startup_prompt", "规则")
	if err == nil || !strings.Contains(err.Error(), "契约违约") {
		t.Fatalf("期望契约违约错误, got %v", err)
	}
	if calls := len(fenced.Requests()); calls != 1 {
		t.Fatalf("契约违约不应重问，实际 %d 次", calls)
	}
}
