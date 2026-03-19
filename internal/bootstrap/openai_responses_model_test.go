package bootstrap

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/voocel/agentcore"
)

func TestCreateModelFromConfigUsesResponsesAPIWhenConfigured(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if gotPath != "/v1/responses" {
			t.Fatalf("expected /v1/responses, got %s", gotPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp_123",
			"object":"response",
			"model":"gpt-5.4",
			"status":"completed",
			"output":[
				{
					"id":"msg_1",
					"type":"message",
					"role":"assistant",
					"content":[{"type":"output_text","text":"hello from responses"}]
				}
			],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`)
	}))
	defer server.Close()

	model, err := createModelFromConfig("openai", "gpt-5.4", ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		WireAPI: "responses",
	}, map[string]agentcore.ChatModel{})
	if err != nil {
		t.Fatalf("createModelFromConfig: %v", err)
	}

	resp, err := model.Generate(context.Background(), []agentcore.Message{
		{
			Role:    agentcore.RoleUser,
			Content: []agentcore.ContentBlock{agentcore.TextBlock("hello")},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got := resp.Message.TextContent(); got != "hello from responses" {
		t.Fatalf("expected responses content, got %q", got)
	}
}

func TestCreateModelFromConfigDefaultsToChatCompletions(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if gotPath != "/v1/chat/completions" {
			t.Fatalf("expected /v1/chat/completions, got %s", gotPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl_123",
			"object":"chat.completion",
			"model":"gpt-5.4",
			"choices":[
				{
					"index":0,
					"message":{"role":"assistant","content":"hello from chat"},
					"finish_reason":"stop"
				}
			],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer server.Close()

	model, err := createModelFromConfig("openai", "gpt-5.4", ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
	}, map[string]agentcore.ChatModel{})
	if err != nil {
		t.Fatalf("createModelFromConfig: %v", err)
	}

	resp, err := model.Generate(context.Background(), []agentcore.Message{
		{
			Role:    agentcore.RoleUser,
			Content: []agentcore.ContentBlock{agentcore.TextBlock("hello")},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got := resp.Message.TextContent(); got != "hello from chat" {
		t.Fatalf("expected chat content, got %q", got)
	}
}
