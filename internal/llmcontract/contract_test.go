package llmcontract

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
	"github.com/voocel/litellm/provider/anthropic"
)

// testModel 构造一次结构化调用的模型句柄：test client + 请求模板。
func testModel(t *testing.T, provider litellm.Provider, override *bool) Model {
	t.Helper()
	client, err := litellm.New(provider)
	if err != nil {
		t.Fatalf("构造 client: %v", err)
	}
	return Model{
		Core:               agentcore.Model{Client: client, Request: litellm.Request{Model: "test-model"}},
		Provider:           provider.Name(),
		ModelName:          "test-model",
		JSONSchemaOverride: override,
	}
}

// fallbackProvider 模拟把 schema 挪进提示词的 adapter：响应带 schema_prompt_fallback。
type fallbackProvider struct {
	*litellmtest.Provider
}

func (p fallbackProvider) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	resp, err := p.Provider.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.Warnings = append(resp.Warnings, litellm.Warning{
		Code:     warningSchemaPromptFallback,
		Provider: p.Name(),
		Message:  "JSON Schema uses prompt; schema adherence is not enforced",
	})
	return resp, nil
}

func testContract() Contract {
	return Contract{
		Name:        "test_decision",
		Description: "测试契约",
		Schema: schema.Object(
			schema.Property("action", schema.Enum("动作", "a", "b")).Required(),
			schema.Property("reason", schema.String("理由")).Required(),
		),
	}
}

// TestResolveMatrix 覆盖 config 三态 × schema strict 可满足性的组合。
func TestResolveMatrix(t *testing.T) {
	provider := litellmtest.New()
	loose := testContract()
	loose.Schema = map[string]any{
		"type":       "object",
		"properties": map[string]any{"action": map[string]any{"type": "string"}},
	}

	cases := []struct {
		name       string
		contract   Contract
		override   *bool
		wantMode   Mode
		wantSource Source
		wantStrict bool
	}{
		{"未声明覆盖走 adapter 映射", testContract(), nil, ModeNativeJSONSchema, SourceAdapter, true},
		{"未声明覆盖但 schema 非 strict 就绪不声明 strict", loose, nil, ModeNativeJSONSchema, SourceAdapter, false},
		{"config true 强制原生 schema", testContract(), new(true), ModeNativeJSONSchema, SourceConfig, true},
		{"config true + schema 非 strict 就绪", loose, new(true), ModeNativeJSONSchema, SourceConfig, false},
		{"config false 关闭原生 schema", testContract(), new(false), ModePromptContract, SourceConfig, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Resolve(testModel(t, provider, tc.override), tc.contract)
			if res.Mode != tc.wantMode || res.Source != tc.wantSource || res.Strict != tc.wantStrict {
				t.Fatalf("Resolve = %+v, want mode=%s source=%s strict=%v", res, tc.wantMode, tc.wantSource, tc.wantStrict)
			}
		})
	}

	res := Resolve(Model{}, testContract())
	if res.Source != SourceUnknown {
		t.Fatalf("无 client 时应标记 unknown: %+v", res)
	}
}

func TestResolveIdentity(t *testing.T) {
	res := Resolve(testModel(t, litellmtest.New(), nil), testContract())
	if res.Provider != "test" || res.Model != "test-model" {
		t.Fatalf("身份未取到: %+v", res)
	}
	res = Resolve(Model{Core: agentcore.Model{Request: litellm.Request{Model: "template-model"}}}, testContract())
	if res.Provider != "" || res.Model != "template-model" {
		t.Fatalf("请求模板兜底身份未取到: %+v", res)
	}
}

func TestPlanNativeFormat(t *testing.T) {
	format, res, err := Plan(testModel(t, litellmtest.New(), new(true)), testContract())
	if err != nil || res.Mode != ModeNativeJSONSchema {
		t.Fatalf("Plan = %+v %v", res, err)
	}
	if format == nil || format.Type != litellm.ResponseFormatJSONSchema || format.JSONSchema == nil {
		t.Fatalf("ResponseFormat = %+v", format)
	}
	if format.JSONSchema.Name != "test_decision" || format.JSONSchema.Description != "测试契约" {
		t.Fatalf("JSONSchema = %+v", format.JSONSchema)
	}
	if format.JSONSchema.Strict == nil || !*format.JSONSchema.Strict {
		t.Fatalf("strict 应声明为 true: %+v", format.JSONSchema)
	}
}

func TestPlanPromptContractSendsNoFormat(t *testing.T) {
	format, res, err := Plan(testModel(t, litellmtest.New(), new(false)), testContract())
	if err != nil || res.Mode != ModePromptContract {
		t.Fatalf("Plan = %+v %v", res, err)
	}
	if format != nil {
		t.Fatalf("prompt contract 不应发送 response_format: %+v", format)
	}
}

func TestPlanRejectsUnmarshalableSchema(t *testing.T) {
	bad := Contract{Name: "bad", Schema: map[string]any{"bad": make(chan int)}}
	if _, _, err := Plan(testModel(t, litellmtest.New(), nil), bad); err == nil || !strings.Contains(err.Error(), "response_format") {
		t.Fatalf("应暴露 schema 序列化错误, got %v", err)
	}
}

func TestPreparePromptUsesSchemaOnlyForPromptContract(t *testing.T) {
	contract := testContract()
	base := "只负责判断动作与理由。"

	prompt, err := PreparePrompt(base, contract, Resolution{Mode: ModePromptContract})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{base, "<output-json-schema>", `"action"`, `"required"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt contract 缺少 %q:\n%s", want, prompt)
		}
	}

	native, err := PreparePrompt(base, contract, Resolution{Mode: ModeNativeJSONSchema})
	if err != nil {
		t.Fatal(err)
	}
	if native != base {
		t.Fatalf("native 模式不应改写语义提示词: %q", native)
	}
}

func TestPreparePromptRejectsUnmarshalableSchema(t *testing.T) {
	_, err := PreparePrompt("semantic", Contract{
		Name:   "bad",
		Schema: map[string]any{"bad": make(chan int)},
	}, Resolution{Mode: ModePromptContract})
	if err == nil || !strings.Contains(err.Error(), "marshal bad prompt schema") {
		t.Fatalf("应暴露 schema 序列化错误, got %v", err)
	}
}

func TestNullableCopies(t *testing.T) {
	orig := schema.String("可空字段")
	out := Nullable(orig)
	got, ok := out["type"].([]string)
	if !ok || len(got) != 2 || got[0] != "string" || got[1] != "null" {
		t.Fatalf("Nullable type = %v", out["type"])
	}
	if orig["type"] != "string" {
		t.Fatalf("Nullable 修改了传入 map: %v", orig["type"])
	}
}

func TestNullableExtendsEnumWithNull(t *testing.T) {
	orig := schema.Enum("可空枚举", "a", "b")
	out := Nullable(orig)
	enum, ok := out["enum"].([]any)
	if !ok || len(enum) != 3 || enum[2] != nil {
		t.Fatalf("Nullable enum = %#v", out["enum"])
	}
	if _, ok := orig["enum"].([]string); !ok {
		t.Fatalf("Nullable 修改了传入 enum: %#v", orig["enum"])
	}
	if err := ValidateJSON(out, []byte("null")); err != nil {
		t.Fatalf("可空枚举应接受 null: %v", err)
	}
	if err := ValidateJSON(out, []byte(`"other"`)); err == nil {
		t.Fatal("可空枚举不应接受枚举外字符串")
	}
}

func TestFingerprintStable(t *testing.T) {
	a, b := testContract(), testContract()
	if a.Fingerprint() != b.Fingerprint() || len(a.Fingerprint()) != 12 {
		t.Fatalf("同一契约 fingerprint 应稳定: %s vs %s", a.Fingerprint(), b.Fingerprint())
	}
	c := testContract()
	c.Schema = schema.Object(schema.Property("other", schema.String("x")).Required())
	if a.Fingerprint() == c.Fingerprint() {
		t.Fatalf("不同 schema fingerprint 不应相同")
	}
}

func TestValidateJSONEnforcesStrictContract(t *testing.T) {
	contract := testContract()
	if err := ValidateJSON(contract.Schema, []byte(`{"action":"a","reason":"ok"}`)); err != nil {
		t.Fatalf("合法 JSON 被拒绝: %v", err)
	}
	for name, raw := range map[string]string{
		"缺 required": `{"action":"a"}`,
		"非法 enum":    `{"action":"other","reason":"x"}`,
		"字段类型错误":     `{"action":1,"reason":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateJSON(contract.Schema, []byte(raw)); err == nil {
				t.Fatalf("应拒绝 %s", raw)
			}
		})
	}
}

func TestValidateJSONRejectsInvalidEnumContract(t *testing.T) {
	contract := testContract()
	contract.Schema["enum"] = []any{1}
	if err := ValidateJSON(contract.Schema, []byte(`{"action":"a","reason":"ok"}`)); err == nil || !strings.Contains(err.Error(), "enum 契约非法") {
		t.Fatalf("应暴露非法 enum 契约，err=%v", err)
	}
}

func TestExtractJSONObjectBalanced(t *testing.T) {
	raw := "前缀 ```json\n{\"a\":\"}\",\"nested\":{\"b\":1}}\n``` 尾部"
	want := `{"a":"}","nested":{"b":1}}`
	if got := ExtractJSONObject(raw); got != want {
		t.Fatalf("ExtractJSONObject = %q, want %q", got, want)
	}
}

type decision struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

func TestExecutePromptModeSelfHealsSchemaViolation(t *testing.T) {
	provider := litellmtest.New(
		litellmtest.Reply{Blocks: []litellm.Block{litellm.Text(`{}`)}},
		litellmtest.Reply{Blocks: []litellm.Block{litellm.Text(`{"action":"a","reason":"fixed"}`)}},
	)
	out, err := Execute(t.Context(), testModel(t, provider, new(false)), Request[decision]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入", Agent: "test",
	})
	if err != nil || out.Reason != "fixed" {
		t.Fatalf("自愈失败: %+v %v", out, err)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("应在第二次成功，calls=%d", len(requests))
	}
	if requests[0].ResponseFormat != nil {
		t.Fatalf("prompt contract 不应发送 response_format: %+v", requests[0].ResponseFormat)
	}
	if got := requests[0].Messages[0].Blocks[0].(litellm.TextBlock).Text; !strings.Contains(got, "<output-json-schema>") {
		t.Fatalf("prompt 模式应把契约写进系统提示词:\n%s", got)
	}
	second := requests[1].Messages
	if len(second) != 4 || !strings.Contains(second[3].Blocks[0].(litellm.TextBlock).Text, "$.action") {
		t.Fatalf("重问未携带精确 Schema 错误: %+v", second)
	}
}

func TestExecuteNativeSchemaViolationFailsImmediately(t *testing.T) {
	provider := litellmtest.New(litellmtest.Reply{Blocks: []litellm.Block{litellm.Text(`{}`)}})
	_, err := Execute(t.Context(), testModel(t, provider, new(true)), Request[map[string]any]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != FailureContract {
		t.Fatalf("应返回原生契约错误，得 %T %v", err, err)
	}
	if len(provider.Requests()) != 1 {
		t.Fatalf("原生契约违约不应重问，calls=%d", len(provider.Requests()))
	}
}

// 端点把 schema 挪进提示词时不具备强制力：结构违约应反馈重问，而不是当作代码缺陷。
func TestExecuteSchemaPromptFallbackSelfHeals(t *testing.T) {
	provider := fallbackProvider{Provider: litellmtest.New(
		litellmtest.Reply{Blocks: []litellm.Block{litellm.Text(`{}`)}},
		litellmtest.Reply{Blocks: []litellm.Block{litellm.Text(`{"action":"b","reason":"ok"}`)}},
	)}
	var modes []Mode
	out, err := Execute(t.Context(), testModel(t, provider, nil), Request[decision]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
		Hooks: Hooks{Correction: func(ev Correction) { modes = append(modes, ev.Mode) }},
	})
	if err != nil || out.Action != "b" {
		t.Fatalf("回退后应自愈: %+v %v", out, err)
	}
	if len(modes) != 1 || modes[0] != ModePromptContract {
		t.Fatalf("修正记录应报告 prompt contract: %+v", modes)
	}
	if len(provider.Requests()) != 2 {
		t.Fatalf("回退后应重问一次, calls=%d", len(provider.Requests()))
	}
}

func TestExecuteExposesModelErrorStopReason(t *testing.T) {
	provider := litellmtest.New(litellmtest.Reply{
		Blocks:       []litellm.Block{litellm.Text(`{"action":"a","reason":"unused"}`)},
		FinishReason: litellm.FinishReasonError,
	})
	_, err := Execute(t.Context(), testModel(t, provider, new(true)), Request[map[string]any]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != FailureProtocol || !strings.Contains(err.Error(), "stop_reason=error") {
		t.Fatalf("应暴露模型错误终止原因，得 %T %v", err, err)
	}
	if len(provider.Requests()) != 1 {
		t.Fatalf("错误终止不应作为 JSON 错误重问，calls=%d", len(provider.Requests()))
	}
}

func TestExecuteRejectsEmptyNativeOutput(t *testing.T) {
	provider := litellmtest.New(litellmtest.Reply{})
	_, err := Execute(t.Context(), testModel(t, provider, new(true)), Request[map[string]any]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != FailureContract || !strings.Contains(err.Error(), "原生 schema 返回空内容") {
		t.Fatalf("空原生输出应报契约错误，得 %T %v", err, err)
	}
}

func TestExecuteRejectsUnmarshalableContractBeforeRequest(t *testing.T) {
	bad := Contract{Name: "bad", Schema: map[string]any{"bad": make(chan int)}}
	provider := litellmtest.New()
	_, err := Execute(t.Context(), testModel(t, provider, nil), Request[map[string]any]{
		Contract: bad, SystemPrompt: "判断。", Payload: "输入",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != FailureContract {
		t.Fatalf("不可序列化契约应由本地拦下，得 %T %v", err, err)
	}
	if len(provider.Requests()) != 0 {
		t.Fatalf("契约不可序列化时不应发请求, calls=%d", len(provider.Requests()))
	}
}

// 端点校验失败属于请求构造缺陷，不是可重试的请求失败。
func TestExecuteClassifiesValidationErrorAsProtocol(t *testing.T) {
	provider := litellmtest.New(litellmtest.Fail(
		litellm.NewError("test", litellm.ErrorTypeValidation, "response_format json_object has no equivalent", nil),
	))
	_, err := Execute(t.Context(), testModel(t, provider, new(false)), Request[map[string]any]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != FailureProtocol {
		t.Fatalf("校验错误应归为协议失败，得 %T %v", err, err)
	}
	if litellm.ErrorTypeOf(failure.Err) != litellm.ErrorTypeValidation {
		t.Fatalf("应保留 litellm 错误类型: %v", failure.Err)
	}
	if len(provider.Requests()) != 1 {
		t.Fatalf("校验错误不应重试, calls=%d", len(provider.Requests()))
	}
}

// Options 是请求模板之上的覆盖：思考等级必须落到真正发出的请求里。
func TestExecuteAppliesOptionsToRequest(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(`{"action":"a","reason":"ok"}`))
	_, err := Execute(t.Context(), testModel(t, provider, nil), Request[decision]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
		Options: []Option{WithThinking(ThinkingHigh)},
	})
	if err != nil {
		t.Fatal(err)
	}
	sent := provider.Requests()[0]
	if sent.Thinking == nil || sent.Thinking.Effort != "high" {
		t.Fatalf("thinking 未落到请求: %+v", sent.Thinking)
	}
	if sent.MaxTokens != nil {
		t.Fatalf("结构化调用不应设置输出上限: %d", *sent.MaxTokens)
	}
}

func TestWithThinkingMapsLevels(t *testing.T) {
	base := litellm.Request{Model: "m"}
	cases := []struct {
		level  ThinkingLevel
		want   *litellm.Thinking
		absent bool
	}{
		{ThinkingAuto, nil, true},
		{ThinkingOff, &litellm.Thinking{Disabled: true}, false},
		{ThinkingHigh, &litellm.Thinking{Effort: "high"}, false},
	}
	for _, tc := range cases {
		req := base
		WithThinking(tc.level)(&req)
		if tc.absent {
			if req.Thinking != nil {
				t.Fatalf("%s 不应设置 thinking: %+v", tc.level, req.Thinking)
			}
			continue
		}
		if req.Thinking == nil || *req.Thinking != *tc.want {
			t.Fatalf("%s → %+v, want %+v", tc.level, req.Thinking, tc.want)
		}
	}
}

func TestWithProviderOptionsMerges(t *testing.T) {
	base, err := litellm.NewProviderOptions(map[string]any{"a": 1, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	extra, err := litellm.NewProviderOptions(map[string]any{"b": 3})
	if err != nil {
		t.Fatal(err)
	}
	req := litellm.Request{Model: "m", ProviderOptions: base}
	WithProviderOptions(extra)(&req)
	if string(req.ProviderOptions["a"]) != "1" || string(req.ProviderOptions["b"]) != "3" {
		t.Fatalf("合并结果不符: %s", req.ProviderOptions)
	}
	if string(base["b"]) != "2" {
		t.Fatalf("不应修改传入的模板选项: %s", base)
	}
}

// messagesRequest 是假 Messages 端点收到的一次请求。
type messagesRequest struct {
	Raw  string
	Body map[string]any
}

// newMessagesServer 起一个假的 Messages 端点：记录请求体，回一段合法 JSON 文本。
func newMessagesServer(t *testing.T, reply string) (*httptest.Server, func() []messagesRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []messagesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, messagesRequest{Raw: string(raw), Body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "msg_test",
			"type":        "message",
			"role":        "assistant",
			"model":       "claude-test",
			"content":     []any{map[string]any{"type": "text", "text": reply}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []messagesRequest {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// anthropicModel 用真实 Anthropic 适配器（打到 httptest 端点）构造模型句柄。
// Messages API 要求 max_tokens，模板按 bootstrap 的物理上限习惯填一个值。
func anthropicModel(t *testing.T, baseURL string, override *bool) Model {
	t.Helper()
	provider, err := anthropic.New(anthropic.Config{APIKey: "test-key", BaseURL: baseURL})
	if err != nil {
		t.Fatalf("构造 anthropic provider: %v", err)
	}
	client, err := litellm.New(provider)
	if err != nil {
		t.Fatalf("构造 client: %v", err)
	}
	return Model{
		Core: agentcore.Model{
			Client:  client,
			Request: litellm.Request{Model: "claude-test", MaxTokens: new(1024)},
		},
		Provider:           "anthropic",
		ModelName:          "claude-test",
		JSONSchemaOverride: override,
	}
}

// systemText 取请求体里 system 字段的文本：单块时适配器会折叠成字符串。
func systemText(body map[string]any) string {
	switch system := body["system"].(type) {
	case string:
		return system
	case []any:
		var out strings.Builder
		for _, block := range system {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					out.WriteString(text)
				}
			}
		}
		return out.String()
	}
	return ""
}

// 回归：Messages API 没有 json_object 等价物，prompt contract 必须不发任何格式字段。
func TestExecutePromptContractOnAnthropicSendsNoFormat(t *testing.T) {
	srv, requests := newMessagesServer(t, `{"action":"a","reason":"ok"}`)
	out, err := Execute(t.Context(), anthropicModel(t, srv.URL, new(false)), Request[decision]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
	})
	if err != nil || out.Action != "a" || out.Reason != "ok" {
		t.Fatalf("prompt contract 调用失败: %+v %v", out, err)
	}
	sent := requests()
	if len(sent) != 1 {
		t.Fatalf("应只发一次请求, calls=%d", len(sent))
	}
	if _, ok := sent[0].Body["response_format"]; ok {
		t.Fatalf("不应发送 response_format: %s", sent[0].Raw)
	}
	if config, ok := sent[0].Body["output_config"].(map[string]any); ok {
		if _, exists := config["format"]; exists {
			t.Fatalf("不应发送 output_config.format: %s", sent[0].Raw)
		}
	}
	prompt := systemText(sent[0].Body)
	if !strings.Contains(prompt, "<output-json-schema>") || !strings.Contains(prompt, `"action"`) {
		t.Fatalf("prompt contract 应把 schema 契约写进提示词:\n%s", prompt)
	}
}

// 原生模式：schema 走 output_config.format，提示词不再重复注入契约。
func TestExecuteNativeContractOnAnthropicSendsJSONSchema(t *testing.T) {
	srv, requests := newMessagesServer(t, `{"action":"b","reason":"ok"}`)
	out, err := Execute(t.Context(), anthropicModel(t, srv.URL, new(true)), Request[decision]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
	})
	if err != nil || out.Action != "b" {
		t.Fatalf("原生调用失败: %+v %v", out, err)
	}
	sent := requests()
	if len(sent) != 1 {
		t.Fatalf("应只发一次请求, calls=%d", len(sent))
	}
	config, _ := sent[0].Body["output_config"].(map[string]any)
	format, _ := config["format"].(map[string]any)
	if format == nil || format["type"] != "json_schema" {
		t.Fatalf("原生模式应发送 output_config.format: %s", sent[0].Raw)
	}
	schema, _ := format["schema"].(map[string]any)
	if schema == nil || schema["properties"] == nil {
		t.Fatalf("schema 未随请求发出: %v", format["schema"])
	}
	if prompt := systemText(sent[0].Body); prompt != "判断。" {
		t.Fatalf("原生模式不应向提示词重复注入 schema:\n%s", prompt)
	}
}
