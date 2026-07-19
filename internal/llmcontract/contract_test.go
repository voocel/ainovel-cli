package llmcontract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/agentcore/schema"
)

type baseModel struct{}

func (baseModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return nil, nil
}

func (baseModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, nil
}

func (baseModel) SupportsTools() bool { return true }

type capsModel struct {
	baseModel
	caps llm.Capabilities
}

func (m capsModel) Capabilities() llm.Capabilities { return m.caps }

type overrideModel struct {
	capsModel
	override *bool
}

func (m overrideModel) JSONSchemaOverride() *bool { return m.override }

type infoModel struct {
	baseModel
	info llm.ModelInfo
}

func (m infoModel) Info() llm.ModelInfo { return m.info }

type factsModel struct {
	baseModel
	facts ModelFacts
}

func (m factsModel) StructuredOutputFacts() ModelFacts { return m.facts }

func structured(jsonSchema, strict llm.Support) llm.Capabilities {
	return llm.Capabilities{
		Provider:   "prov",
		Model:      "mod",
		Structured: llm.StructuredCapabilities{JSONSchema: jsonSchema, Strict: strict},
	}
}

func boolPtr(v bool) *bool { return &v }

// TestResolveMatrix 覆盖 config 三态 × adapter 能力的全部组合
// (docs/structured-output-refactor.md §11.3)。
func TestResolveMatrix(t *testing.T) {
	cases := []struct {
		name       string
		model      agentcore.ChatModel
		wantMode   Mode
		wantSource Source
		wantStrict bool
	}{
		{"无任何能力接口", baseModel{}, ModePromptContract, SourceUnknown, false},
		{"adapter yes + strict yes", capsModel{caps: structured(llm.SupportYes, llm.SupportYes)}, ModeNativeJSONSchema, SourceAdapter, true},
		{"adapter yes + strict no（Gemini 形态）", capsModel{caps: structured(llm.SupportYes, llm.SupportNo)}, ModeNativeJSONSchema, SourceAdapter, false},
		{"adapter yes + strict unknown", capsModel{caps: structured(llm.SupportYes, llm.SupportUnknown)}, ModeNativeJSONSchema, SourceAdapter, false},
		{"adapter no", capsModel{caps: structured(llm.SupportNo, llm.SupportNo)}, ModePromptContract, SourceAdapter, false},
		{"adapter unknown", capsModel{caps: structured(llm.SupportUnknown, llm.SupportUnknown)}, ModePromptContract, SourceUnknown, false},
		{"config true 无能力信息默认 strict", overrideModel{override: boolPtr(true)}, ModeNativeJSONSchema, SourceConfig, true},
		{"config true + adapter strict no 关 strict", overrideModel{capsModel{caps: structured(llm.SupportUnknown, llm.SupportNo)}, boolPtr(true)}, ModeNativeJSONSchema, SourceConfig, false},
		{"config false 覆盖 adapter yes", overrideModel{capsModel{caps: structured(llm.SupportYes, llm.SupportYes)}, boolPtr(false)}, ModePromptContract, SourceConfig, false},
		{"config 未配置回落 adapter", overrideModel{capsModel{caps: structured(llm.SupportYes, llm.SupportYes)}, nil}, ModeNativeJSONSchema, SourceAdapter, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Resolve(tc.model)
			if res.Mode != tc.wantMode || res.Source != tc.wantSource || res.Strict != tc.wantStrict {
				t.Fatalf("Resolve = %+v, want mode=%s source=%s strict=%v", res, tc.wantMode, tc.wantSource, tc.wantStrict)
			}
		})
	}
}

func TestResolveIdentity(t *testing.T) {
	res := Resolve(capsModel{caps: structured(llm.SupportYes, llm.SupportYes)})
	if res.Provider != "prov" || res.Model != "mod" {
		t.Fatalf("caps 身份未取到: %+v", res)
	}
	res = Resolve(infoModel{info: llm.ModelInfo{Provider: "p2", Name: "m2"}})
	if res.Provider != "p2" || res.Model != "m2" {
		t.Fatalf("Info 兜底身份未取到: %+v", res)
	}
}

func TestResolveUsesAtomicFactsSnapshot(t *testing.T) {
	res := Resolve(factsModel{facts: ModelFacts{
		Capabilities:       structured(llm.SupportYes, llm.SupportYes),
		Info:               llm.ModelInfo{Provider: "snapshot-provider", Name: "snapshot-model"},
		JSONSchemaOverride: boolPtr(false),
	}})
	if res.Mode != ModePromptContract || res.Source != SourceConfig {
		t.Fatalf("config false 快照应关闭原生 schema: %+v", res)
	}
	if res.Provider != "prov" || res.Model != "mod" {
		t.Fatalf("能力身份应来自同一快照: %+v", res)
	}
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

func TestPlanNativeOptions(t *testing.T) {
	opts, res := Plan(capsModel{caps: structured(llm.SupportYes, llm.SupportYes)}, testContract())
	if res.Mode != ModeNativeJSONSchema || len(opts) == 0 {
		t.Fatalf("native 模式应产出 opts: res=%+v opts=%d", res, len(opts))
	}
	cfg := agentcore.ResolveCallConfig(opts)
	rf := cfg.ResponseFormat
	if rf == nil || rf.Type != agentcore.ResponseFormatJSONSchema || rf.JSONSchema == nil {
		t.Fatalf("ResponseFormat = %+v", rf)
	}
	if rf.JSONSchema.Name != "test_decision" || rf.JSONSchema.Strict == nil || !*rf.JSONSchema.Strict {
		t.Fatalf("JSONSchema = %+v", rf.JSONSchema)
	}
}

func TestPlanPromptContractNoOptions(t *testing.T) {
	opts, res := Plan(baseModel{}, testContract())
	if res.Mode != ModePromptContract || opts != nil {
		t.Fatalf("prompt 模式不应产出 opts: res=%+v opts=%v", res, opts)
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

type executionModel struct {
	responses []string
	stops     []agentcore.StopReason
	calls     int
	messages  []agentcore.Message
	config    agentcore.CallConfig
}

func (m *executionModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.messages = messages
	m.config = agentcore.ResolveCallConfig(opts)
	response := m.responses[m.calls]
	var stop agentcore.StopReason
	if m.calls < len(m.stops) {
		stop = m.stops[m.calls]
	}
	m.calls++
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(response)},
		StopReason: stop,
	}}, nil
}

type nativeExecutionModel struct{ *executionModel }

func (m *nativeExecutionModel) Capabilities() llm.Capabilities {
	return structured(llm.SupportYes, llm.SupportYes)
}

func TestExecutePromptModeSelfHealsSchemaViolation(t *testing.T) {
	model := &executionModel{responses: []string{
		`{}`,
		`{"action":"a","reason":"fixed"}`,
	}}
	type output struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	out, err := Execute(t.Context(), model, Request[output]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入", Agent: "test",
	})
	if err != nil || out.Reason != "fixed" {
		t.Fatalf("自愈失败: %+v %v", out, err)
	}
	if model.calls != 2 {
		t.Fatalf("应在第二次成功，calls=%d", model.calls)
	}
	if len(model.messages) != 4 || !strings.Contains(model.messages[3].TextContent(), "$.action") {
		t.Fatalf("重问未携带精确 Schema 错误: %+v", model.messages)
	}
}

func TestExecuteNativeSchemaViolationFailsImmediately(t *testing.T) {
	model := &nativeExecutionModel{executionModel: &executionModel{responses: []string{`{}`}}}
	_, err := Execute(t.Context(), model, Request[map[string]any]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != FailureContract {
		t.Fatalf("应返回原生契约错误，得 %T %v", err, err)
	}
	if model.calls != 1 {
		t.Fatalf("原生契约违约不应重问，calls=%d", model.calls)
	}
}

func TestExecuteExposesModelErrorStopReason(t *testing.T) {
	model := &executionModel{
		responses: []string{`{"action":"a","reason":"unused"}`},
		stops:     []agentcore.StopReason{agentcore.StopReasonError},
	}
	_, err := Execute(t.Context(), model, Request[map[string]any]{
		Contract: testContract(), SystemPrompt: "判断。", Payload: "输入",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != FailureProtocol || !strings.Contains(err.Error(), "stop_reason=error") {
		t.Fatalf("应暴露模型错误终止原因，得 %T %v", err, err)
	}
	if model.calls != 1 {
		t.Fatalf("错误终止不应作为 JSON 错误重问，calls=%d", model.calls)
	}
}
