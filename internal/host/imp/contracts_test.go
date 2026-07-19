package imp

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func TestStructuredContractsAreStrictReady(t *testing.T) {
	for _, contract := range []llmcontract.Contract{segmentContract, analysisContract, rangeContract, synthesisContract} {
		if err := llmcontract.ValidateStrictReady(contract.Schema); err != nil {
			t.Fatalf("%s: %v", contract.Name, err)
		}
	}
}

type nativeImportModel struct {
	*mockModel
	messages []agentcore.Message
	config   agentcore.CallConfig
}

func (m *nativeImportModel) Capabilities() llm.Capabilities {
	return llm.Capabilities{
		Provider:   "openai",
		Model:      "gpt-test",
		Structured: llm.StructuredCapabilities{JSONSchema: llm.SupportYes, Strict: llm.SupportYes},
	}
}

func (m *nativeImportModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.messages = messages
	m.config = agentcore.ResolveCallConfig(opts)
	return m.mockModel.Generate(ctx, messages, tools, opts...)
}

func TestCallStructuredUsesNativeSchemaWithoutPromptDuplication(t *testing.T) {
	model := &nativeImportModel{mockModel: &mockModel{responses: []string{`{"boundaries":[]}`}}}
	const prompt = "判断真实边界。"
	_, err := callStructured[boundaryBatch](t.Context(), model, segmentContract, prompt, `{}`, 100, callProfile{}, nil)
	if err != nil {
		t.Fatalf("callStructured: %v", err)
	}
	format := model.config.ResponseFormat
	if format == nil || format.JSONSchema == nil || format.JSONSchema.Name != segmentContract.Name {
		t.Fatalf("response format = %#v", format)
	}
	if got := model.messages[0].TextContent(); got != prompt {
		t.Fatalf("native prompt 被重复注入 schema: %s", got)
	}
}

func TestCallStructuredPromptModeInjectsContract(t *testing.T) {
	model := &nativeImportModel{mockModel: &mockModel{responses: []string{`{"boundaries":[]}`}}}
	modelCaps := &promptImportModel{nativeImportModel: model}
	_, err := callStructured[boundaryBatch](t.Context(), modelCaps, segmentContract, "判断真实边界。", `{}`, 100, callProfile{}, nil)
	if err != nil {
		t.Fatalf("callStructured: %v", err)
	}
	if model.config.ResponseFormat != nil {
		t.Fatalf("prompt mode 不应发送 response_format: %#v", model.config.ResponseFormat)
	}
	if !strings.Contains(model.messages[0].TextContent(), "<output-json-schema>") {
		t.Fatalf("prompt mode 未注入契约: %s", model.messages[0].TextContent())
	}
}

type promptImportModel struct{ *nativeImportModel }

func (m *promptImportModel) Capabilities() llm.Capabilities { return llm.Capabilities{} }
