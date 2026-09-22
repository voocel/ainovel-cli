package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/agentcore"
)

// rawToolUseID 取自真实故障日志：tool_use id 中带 '#'，而请求校验只接受 [A-Za-z0-9_-]。
const rawToolUseID = "call_3543acedc27c4404bfe7317c#235532d85de245bda8ff64f6a683623a"

var validToolUseIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// TestCreateModelNormalizesInvalidToolUseIDs 锁定模型工厂的行为：含非法字符的 tool_use id
// 必须在发出前归一化为合法形态，且 tool_use / tool_result 两侧成对改写，否则整轮会话会在
// 本地校验阶段反复失败（messages[i]: tool use id "..." is invalid）。
func TestCreateModelNormalizesInvalidToolUseIDs(t *testing.T) {
	var (
		mu   sync.Mutex
		body string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw, err := io.ReadAll(r.Body); err == nil {
			mu.Lock()
			body = string(raw)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"stub","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	model, err := createModelFromConfig("stub", "stub-model", ProviderConfig{
		Type:    "openai",
		APIKey:  "stub-key",
		BaseURL: srv.URL,
	}, map[string]agentcore.ChatModel{})
	if err != nil {
		t.Fatalf("createModelFromConfig: %v", err)
	}

	messages := []agentcore.Message{
		agentcore.UserMsg("hi"),
		{
			Role: agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{
				agentcore.ToolCallBlock(agentcore.ToolCall{
					ID:   rawToolUseID,
					Name: "novel_context",
					Args: json.RawMessage(`{}`),
				}),
			},
		},
		agentcore.ToolResultMsg(rawToolUseID, json.RawMessage(`"ok"`), false),
	}

	if _, err := model.Generate(context.Background(), messages, nil); err != nil {
		t.Fatalf("请求未发出（若为 tool use id ... is invalid 则说明归一化未生效）: %v", err)
	}

	mu.Lock()
	got := body
	mu.Unlock()
	if got == "" {
		t.Fatal("provider 未收到请求体")
	}
	if strings.Contains(got, rawToolUseID) {
		t.Fatalf("请求体仍带非法 id: %s", got)
	}

	var payload struct {
		Messages []struct {
			ToolCalls []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}

	var useIDs, resultIDs []string
	for _, m := range payload.Messages {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				useIDs = append(useIDs, tc.ID)
			}
		}
		if m.ToolCallID != "" {
			resultIDs = append(resultIDs, m.ToolCallID)
		}
	}
	if len(useIDs) == 0 || len(resultIDs) == 0 {
		t.Fatalf("请求体缺少 tool_use / tool_result: %s", got)
	}
	for _, id := range append(append([]string{}, useIDs...), resultIDs...) {
		if id == rawToolUseID || !validToolUseIDPattern.MatchString(id) {
			t.Fatalf("tool id 仍不合法: %q", id)
		}
	}
	if useIDs[0] != resultIDs[0] {
		t.Fatalf("tool_use 与 tool_result 的 id 未成对改写: %v / %v", useIDs, resultIDs)
	}
}
