package sim

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func TestSimulationContractsAreStrictReady(t *testing.T) {
	for _, contract := range []llmcontract.Contract{sourceReportContract, synthesisContract} {
		if err := llmcontract.ValidateStrictReady(contract.Schema); err != nil {
			t.Fatalf("%s: %v", contract.Name, err)
		}
	}
}

type nativeSimulationModel struct {
	response string
	messages []agentcore.Message
	config   agentcore.CallConfig
}

func (m *nativeSimulationModel) Capabilities() llm.Capabilities {
	return llm.Capabilities{
		Provider:   "openai",
		Model:      "gpt-test",
		Structured: llm.StructuredCapabilities{JSONSchema: llm.SupportYes, Strict: llm.SupportYes},
	}
}

func (m *nativeSimulationModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.messages = messages
	m.config = agentcore.ResolveCallConfig(opts)
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(m.response)},
		StopReason: agentcore.StopReasonStop,
	}}, nil
}

func TestAnalyzeSourceUsesNativeSchema(t *testing.T) {
	model := &nativeSimulationModel{response: validSourceReportJSON("清晰摘要")}
	report, err := AnalyzeSource(t.Context(), model, "只分析写作方法。", scannedSource{})
	if err != nil {
		t.Fatalf("AnalyzeSource: %v", err)
	}
	if report.Summary == "" {
		t.Fatal("summary 为空")
	}
	format := model.config.ResponseFormat
	if format == nil || format.JSONSchema == nil || format.JSONSchema.Name != sourceReportContract.Name {
		t.Fatalf("response format = %#v", format)
	}
	if strings.Contains(model.messages[0].TextContent(), "<output-json-schema>") {
		t.Fatalf("native prompt 不应注入 schema: %s", model.messages[0].TextContent())
	}
}

func TestAnalyzeSourcePromptModeRepairsMissingRequiredFields(t *testing.T) {
	model := &scriptedLLM{responses: []string{
		`{}`,
		validSourceReportJSON("修正后的摘要"),
	}}
	report, err := AnalyzeSource(t.Context(), model, "只分析写作方法。", scannedSource{})
	if err != nil {
		t.Fatalf("AnalyzeSource: %v", err)
	}
	if report.Summary != "修正后的摘要" || model.calls.Load() != 2 {
		t.Fatalf("缺字段后应反馈自愈: report=%+v calls=%d", report, model.calls.Load())
	}
}

var _ LLMChat = (*nativeSimulationModel)(nil)
