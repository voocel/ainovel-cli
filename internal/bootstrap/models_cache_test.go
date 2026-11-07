package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

// 通过真实适配器检查线上请求体：模拟 #141 的端点会拒绝显式断点，
// Responses 还会在 tool_result 带断点时本地拒绝请求。
func TestCreateModelPromptCacheCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, provider, api string
		wantExplicit        bool
	}{
		{"openai_default", "openai", "", false},
		{"openai_chat", "openai", "chat", false},
		{"openai_responses", "openai", "responses", false},
		{"anthropic", "anthropic", "", true},
		{"openrouter", "openrouter", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
					protocol := tc.provider
					if tc.api == "responses" {
						protocol = "responses"
					}
					srv, requests := newCacheWireServer(t, protocol)
					model, err := createModelFromConfig("alias", "gpt-5.4", ProviderConfig{
						Type: tc.provider, API: tc.api, APIKey: "test-key", BaseURL: srv.URL,
						Extra: map[string]any{"prompt_cache_params": true},
					}, map[string]agentcore.ChatModel{})
					if err != nil {
						t.Fatal(err)
					}
					messages := cacheWireMessages()
					before, err := json.Marshal(messages)
					if err != nil {
						t.Fatal(err)
					}
					// 首轮与工具返回后的下一轮都必须可用。
					for _, input := range [][]agentcore.Message{messages[:2], messages} {
						callCacheWireModel(t, model, input, streaming)
						body := <-requests
						if tc.provider == "openai" {
							if strings.Contains(body, "prompt_cache_breakpoint") || strings.Contains(body, "cache_control") {
								t.Errorf("OpenAI 请求不应携带显式断点: %s", body)
							}
							if !strings.Contains(body, `"prompt_cache_key":"cache-wire-test"`) {
								t.Errorf("缓存路由键必须保留: %s", body)
							}
						} else if strings.Contains(body, `"cache_control"`) != tc.wantExplicit {
							t.Errorf("原生缓存断点未保留: %s", body)
						}
						if len(input) > 2 && !strings.Contains(body, "call_cache") {
							t.Errorf("移除断点不得丢失工具调用/结果: %s", body)
						}
					}
					after, err := json.Marshal(messages)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Fatal("请求兼容处理不得改写调用方历史或元数据")
					}
				})
			}
		})
	}
}

func TestPromptCacheFollowsModelSwap(t *testing.T) {
	chat, chatRequests := newCacheWireServer(t, "openai")
	responses, responseRequests := newCacheWireServer(t, "responses")
	anthropic, anthropicRequests := newCacheWireServer(t, "anthropic")
	cfg := Config{
		Provider: "responses", ModelName: "gpt-5.4",
		Providers: map[string]ProviderConfig{
			"chat":      {Type: "openai", APIKey: "test-key", BaseURL: chat.URL},
			"responses": {Type: "openai", API: "responses", APIKey: "test-key", BaseURL: responses.URL},
			"anthropic": {Type: "anthropic", APIKey: "test-key", BaseURL: anthropic.URL},
		},
		Roles: map[string]RoleConfig{"writer": {Provider: "responses", Model: "gpt-5.4"}},
	}
	models, err := NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 持有已装配模型，确保切换后策略由新客户端决定。
	model := models.ForRole("writer")
	for _, tc := range []struct {
		provider string
		requests <-chan string
		explicit bool
	}{
		{"responses", responseRequests, false},
		{"anthropic", anthropicRequests, true},
		{"chat", chatRequests, false},
		{"responses", responseRequests, false},
	} {
		if err := models.Swap("writer", tc.provider, "gpt-5.4"); err != nil {
			t.Fatal(err)
		}
		callCacheWireModel(t, model, cacheWireMessages(), true)
		body := <-tc.requests
		if strings.Contains(body, "prompt_cache_breakpoint") || strings.Contains(body, `"cache_control"`) != tc.explicit {
			t.Fatalf("切换到 %s 后缓存策略不正确: %s", tc.provider, body)
		}
	}
}

func TestPromptCacheFollowsFallbackProvider(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"rate limit"}}`)
			}))
			defer primary.Close()
			fallback, requests := newCacheWireServer(t, "responses")
			models, err := NewModelSet(Config{
				Provider: "primary", ModelName: "test-model",
				Providers: map[string]ProviderConfig{
					"primary":  {Type: "anthropic", APIKey: "test-key", BaseURL: primary.URL},
					"fallback": {Type: "openai", API: "responses", APIKey: "test-key", BaseURL: fallback.URL},
				},
				Roles: map[string]RoleConfig{"writer": {
					Provider: "primary", Model: "test-model",
					Fallbacks: []ModelRef{{Provider: "fallback", Model: "gpt-5.4"}},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			callCacheWireModel(t, models.ForRoleWithFailover("writer", nil), cacheWireMessages(), streaming)
			if body := <-requests; strings.Contains(body, "prompt_cache_breakpoint") || !strings.Contains(body, "call_cache") {
				t.Fatalf("fallback 请求必须保留工具历史并移除不兼容断点: %s", body)
			}
		})
	}
}

func cacheWireMessages() []agentcore.Message {
	messages := []agentcore.Message{
		{Role: agentcore.RoleSystem, Content: []agentcore.ContentBlock{agentcore.TextBlock("system")}, Metadata: map[string]any{"cache_control": "ephemeral", "trace": "keep"}},
		agentcore.UserMsg("continue"),
		{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{ID: "call_cache", Name: "novel_context", Args: json.RawMessage(`{}`)})}},
		agentcore.ToolResultMsg("call_cache", json.RawMessage(`{"ok":true}`), false),
	}
	messages[1].Metadata = map[string]any{"cache_control": "ephemeral"}
	messages[3].Metadata["cache_control"] = "ephemeral"
	return messages
}

func callCacheWireModel(t *testing.T, model agentcore.ChatModel, messages []agentcore.Message, streaming bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := []agentcore.CallOption{agentcore.WithCallPromptCacheKey("cache-wire-test")}
	if !streaming {
		if _, err := model.Generate(ctx, messages, nil, opts...); err != nil {
			t.Fatal(err)
		}
		return
	}
	events, err := model.GenerateStream(ctx, messages, nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	var done bool
	for event := range events {
		if event.Err != nil {
			t.Error(event.Err)
		}
		if event.Type == agentcore.StreamEventDone {
			done = true
		}
	}
	if !done {
		t.Fatal("流式请求未正常完成")
	}
}

func newCacheWireServer(t *testing.T, protocol string) (*httptest.Server, <-chan string) {
	t.Helper()
	requests := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- string(raw)
		if (protocol == "openai" || protocol == "responses") && bytes.Contains(raw, []byte("prompt_cache_breakpoint")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"invalid_parameter","message":"prompt_cache_breakpoint is not supported"}}`)
			return
		}
		var payload struct {
			Stream bool `json:"stream"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			switch protocol {
			case "responses":
				_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"ok"}

data: {"type":"response.completed","response":{"id":"resp_test","model":"gpt-5.4","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}

`)
			case "anthropic":
				_, _ = io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`)
			default:
				_, _ = io.WriteString(w, `data: {"id":"chat_test","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}

data: {"id":"chat_test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}

data: [DONE]

`)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch protocol {
		case "responses":
			_, _ = io.WriteString(w, `{"id":"resp_test","model":"gpt-5.4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		case "anthropic":
			_, _ = io.WriteString(w, `{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		default:
			_, _ = io.WriteString(w, `{"id":"chat_test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, requests
}
