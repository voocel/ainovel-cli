package llm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/llm"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
)

type scriptedChat struct {
	response string
	nilReply bool
	config   agentcore.CallConfig
	system   string
}

func (m *scriptedChat) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.config = agentcore.ResolveCallConfig(opts)
	m.system = messages[0].TextContent()
	if m.nilReply {
		return nil, nil
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(m.response)},
		Usage:   &agentcore.Usage{Input: 10, Output: 3},
	}}, nil
}

func (*scriptedChat) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("streaming is not used by structured calls")
}

func (*scriptedChat) SupportsTools() bool { return false }

var call = llm.Call{
	System:    "判定器",
	Input:     `{"x":1}`,
	Schema:    llm.Schema{Name: "verdict", Description: "测试", JSON: map[string]any{"type": "object"}},
	CacheKey:  "cache-1",
	SessionID: "session-1",
}

func TestStructuredRequestsSchemaAndDecodesStrictly(t *testing.T) {
	chat := &scriptedChat{response: `{"status":"pass"}`}
	var out struct {
		Status string `json:"status"`
	}
	usage, err := llm.Structured(context.Background(), models.Binding{Chat: chat}, call, &out)
	if err != nil || out.Status != "pass" || usage == nil || usage.Input != 10 {
		t.Fatalf("structured call: err=%v out=%+v usage=%+v", err, out, usage)
	}
	if chat.config.ResponseFormat == nil || chat.config.ResponseFormat.Type != agentcore.ResponseFormatJSONSchema {
		t.Fatalf("JSON schema not requested: %+v", chat.config.ResponseFormat)
	}
	if chat.config.PromptCacheKey != "cache-1" || chat.config.SessionID != "session-1" || chat.system != "判定器" {
		t.Fatalf("call identity not forwarded: %+v system=%q", chat.config, chat.system)
	}
	chat.response = `{"status":"pass","extra":1}`
	if _, err := llm.Structured(context.Background(), models.Binding{Chat: chat}, call, &out); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestStructuredRejectsMissingModelAndEmptyReply(t *testing.T) {
	var out struct{}
	if _, err := llm.Structured(context.Background(), models.Binding{}, call, &out); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("nil model error = %v", err)
	}
	if _, err := llm.Structured(context.Background(), models.Binding{Chat: &scriptedChat{nilReply: true}}, call, &out); err == nil {
		t.Fatal("nil response accepted")
	}
}
