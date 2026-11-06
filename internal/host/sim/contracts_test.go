package sim

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

// blocksText 拼接消息里的文本块，用于断言提示词内容。
func blocksText(blocks []litellm.Block) string {
	var out strings.Builder
	for _, block := range blocks {
		if text, ok := block.(litellm.TextBlock); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
}

func TestSimulationContractsAreStrictReady(t *testing.T) {
	for _, contract := range []llmcontract.Contract{sourceReportContract, synthesisContract} {
		if err := llmcontract.ValidateStrictReady(contract.Schema); err != nil {
			t.Fatalf("%s: %v", contract.Name, err)
		}
	}
}

func TestAnalyzeSourceUsesNativeSchema(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(validSourceReportJSON("清晰摘要")))
	model := testModel(t, provider)
	report, err := AnalyzeSource(t.Context(), model, "只分析写作方法。", scannedSource{})
	if err != nil {
		t.Fatalf("AnalyzeSource: %v", err)
	}
	if report.Summary == "" {
		t.Fatal("summary 为空")
	}
	requests := provider.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	format := requests[0].ResponseFormat
	if format == nil || format.JSONSchema == nil || format.JSONSchema.Name != sourceReportContract.Name {
		t.Fatalf("response format = %#v", format)
	}
	if prompt := blocksText(requests[0].Messages[0].Blocks); strings.Contains(prompt, "<output-json-schema>") {
		t.Fatalf("native prompt 不应注入 schema: %s", prompt)
	}
}

func TestAnalyzeSourcePromptModeRepairsMissingRequiredFields(t *testing.T) {
	model, provider := scriptedModel(t, `{}`, validSourceReportJSON("修正后的摘要"))
	// 旧 fake 靠 llm.Capabilities 零值（不支持原生 schema）进入 prompt 模式；
	// 新模型句柄用 JSONSchemaOverride=false 表达同一配置意图。
	model.JSONSchemaOverride = new(false)
	report, err := AnalyzeSource(t.Context(), model, "只分析写作方法。", scannedSource{})
	if err != nil {
		t.Fatalf("AnalyzeSource: %v", err)
	}
	if report.Summary != "修正后的摘要" || len(provider.Requests()) != 2 {
		t.Fatalf("缺字段后应反馈自愈: report=%+v calls=%d", report, len(provider.Requests()))
	}
	for i, req := range provider.Requests() {
		if req.ResponseFormat == nil || req.ResponseFormat.Type != litellm.ResponseFormatJSONObject {
			t.Fatalf("第 %d 次请求应走 prompt contract(json_object): %#v", i+1, req.ResponseFormat)
		}
	}
}
