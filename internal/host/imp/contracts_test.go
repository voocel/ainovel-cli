package imp

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

func TestStructuredContractsAreStrictReady(t *testing.T) {
	for _, contract := range []llmcontract.Contract{segmentContract, analysisContract, rangeContract, synthesisContract} {
		if err := llmcontract.ValidateStrictReady(contract.Schema); err != nil {
			t.Fatalf("%s: %v", contract.Name, err)
		}
	}
}

// requestText 拼接一条请求消息里的文本块，用于断言提示词注入。
func requestText(m litellm.Message) string {
	var out strings.Builder
	for _, block := range m.Blocks {
		if text, ok := block.(litellm.TextBlock); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
}

func TestCallStructuredUsesNativeSchemaWithoutPromptDuplication(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(`{"boundaries":[]}`))
	model := modelHandle(t, provider, new(true))
	const prompt = "判断真实边界。"
	if _, err := callStructured[boundaryBatch](t.Context(), model, segmentContract, prompt, `{}`, callProfile{}, nil); err != nil {
		t.Fatalf("callStructured: %v", err)
	}
	req := provider.Requests()[0]
	if format := req.ResponseFormat; format == nil || format.JSONSchema == nil || format.JSONSchema.Name != segmentContract.Name {
		t.Fatalf("response format = %#v", format)
	}
	if got := requestText(req.Messages[0]); got != prompt {
		t.Fatalf("native prompt 被重复注入 schema: %s", got)
	}
}

func TestCallStructuredPromptModeInjectsContract(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(`{"boundaries":[]}`))
	model := modelHandle(t, provider, new(false))
	if _, err := callStructured[boundaryBatch](t.Context(), model, segmentContract, "判断真实边界。", `{}`, callProfile{}, nil); err != nil {
		t.Fatalf("callStructured: %v", err)
	}
	req := provider.Requests()[0]
	if format := req.ResponseFormat; format != nil && format.JSONSchema != nil {
		t.Fatalf("prompt mode 不应发送原生 schema: %#v", format)
	}
	if !strings.Contains(requestText(req.Messages[0]), "<output-json-schema>") {
		t.Fatalf("prompt mode 未注入契约: %s", requestText(req.Messages[0]))
	}
}
