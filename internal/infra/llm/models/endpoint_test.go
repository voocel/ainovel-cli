package models

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/voocel/agentcore"
)

func TestVerifyUsesSelectedOpenAIEndpoint(t *testing.T) {
	for _, endpoint := range []string{"chat", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			path := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				if r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("key not forwarded")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"test rejection","type":"authentication_error"}}`))
			}))
			defer server.Close()
			err := Verify(context.Background(), Config{Provider: "openai", Model: "custom", API: endpoint, APIKey: "test-key", BaseURL: server.URL + "/v1"})
			expected := "/v1/chat/completions"
			if endpoint == "responses" {
				expected = "/v1/responses"
			}
			if err == nil || path != expected {
				t.Fatalf("path=%q want=%q err=%v", path, expected, err)
			}
		})
	}
}

// Anthropic accepts only [A-Za-z0-9_-] tool ids and requires max_tokens:
// history with ids from other upstreams must reach it rewritten in pairs.
func TestNewSendsAnthropicValidRequests(t *testing.T) {
	const raw = "call_3543acedc27c4404bfe7317c#235532d85de245bda8ff64f6a683623a"
	bodies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()
	chat, err := New(Config{Provider: "anthropic", Model: "claude-sonnet-4-6", APIKey: "k", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentcore.Message{
		agentcore.UserMsg("hi"),
		{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: raw, Name: "read", Args: json.RawMessage(`{}`)}),
		}},
		agentcore.ToolResultMsg(raw, json.RawMessage(`"ok"`), false),
	}
	if _, err := chat.Generate(context.Background(), messages, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var body struct {
		MaxTokens int `json:"max_tokens"`
		Messages  []struct {
			Content []struct {
				ID        string `json:"id"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(<-bodies), &body); err != nil || len(body.Messages) != 3 {
		t.Fatalf("body: %v", err)
	}
	id, result := body.Messages[1].Content[0].ID, body.Messages[2].Content[0].ToolUseID
	if body.MaxTokens != requiredMaxTokens || id == "" || id != result || !validToolID.MatchString(id) {
		t.Fatalf("max_tokens = %d, tool_use id = %q, tool_result id = %q", body.MaxTokens, id, result)
	}
}

var validToolID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
