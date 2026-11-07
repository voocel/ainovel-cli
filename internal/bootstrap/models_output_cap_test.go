package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/litellm"
)

// anthropicMessageStub 是 Messages API 的最小合法响应：只为让真实适配器完成
// 一次发请求 + 解码，测试关心的是请求体里的 max_tokens。
const anthropicMessageStub = `{"id":"msg_stub","type":"message","role":"assistant",` +
	`"model":"stub","content":[{"type":"text","text":"ok"}],` +
	`"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

// chatCompletionStub 是 Chat Completions 的最小合法响应。
const chatCompletionStub = `{"id":"stub","object":"chat.completion","choices":[{"index":0,` +
	`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// requestRecorder 记录假端点收到的请求体（最后一次）。
type requestRecorder struct {
	mu   sync.Mutex
	body string
}

func (r *requestRecorder) record(raw []byte) {
	r.mu.Lock()
	r.body = string(raw)
	r.mu.Unlock()
}

// payload 返回请求体的 JSON 对象；未收到请求时直接失败。
func (r *requestRecorder) payload(t *testing.T) map[string]any {
	t.Helper()
	r.mu.Lock()
	body := r.body
	r.mu.Unlock()
	if body == "" {
		t.Fatal("provider 未收到请求体")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("请求体不是 JSON 对象: %v (%s)", err, body)
	}
	return decoded
}

// newRecordingServer 起一个 hermetic 假端点：记录请求体并返回固定响应。
func newRecordingServer(t *testing.T, response string) (*httptest.Server, *requestRecorder) {
	t.Helper()
	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取请求体失败: %v", err)
			return
		}
		recorder.record(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

// requireNumberField 断言 JSON 对象里的整数字段等于 want。
func requireNumberField(t *testing.T, payload map[string]any, key string, want int) {
	t.Helper()
	value, ok := payload[key]
	if !ok {
		t.Fatalf("请求体缺少 %s: %v", key, payload)
	}
	number, ok := value.(float64)
	if !ok || int(number) != want {
		t.Fatalf("请求体 %s = %v，期望 %d", key, value, want)
	}
}

// requireNoKey 断言 JSON 对象里不存在这些键（值为 null 也算存在）。
func requireNoKey(t *testing.T, payload map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			t.Fatalf("请求体不应包含 %s，实际为 %v: %v", key, value, payload)
		}
	}
}

// TestModelOutputCap 固定输出上限（max_output_tokens）的整条链路：
// 配置覆盖 → 协议必填的 anthropic 请求、非必填协议的省略、日期后缀回退、
// 未登记模型 fail closed、负数在配置校验阶段被拒绝。
//
// 这些回归必须走真实适配器 + httptest：fake provider 全绿曾漏掉厂商侧的格式
// 拒绝，只有请求体断言能证明上限真的（或真的没有）发到线上。
func TestModelOutputCap(t *testing.T) {
	t.Run("AnthropicOverrideAndRegistryFallback", func(t *testing.T) {
		server, recorder := newRecordingServer(t, anthropicMessageStub)

		const override = 12345
		cfg := Config{
			Provider: "claude-proxy", ModelName: "claude-sonnet-4",
			Providers: map[string]ProviderConfig{"claude-proxy": {
				Type: "anthropic", APIKey: "stub-key", BaseURL: server.URL,
				// 显式声明优先于注册表（claude-sonnet-4 登记的是 64000）。
				Models: []ModelConfig{{Name: "claude-sonnet-4", MaxOutputTokens: override}},
			}},
		}
		models, err := NewModelSet(cfg)
		if err != nil {
			t.Fatalf("NewModelSet(override): %v", err)
		}
		model := models.ForRole("default")
		if model.Core.Request.MaxTokens == nil {
			t.Fatalf("anthropic 协议必须设置 MaxTokens，配置覆盖被丢弃")
		}
		if got := *model.Core.Request.MaxTokens; got != override {
			t.Fatalf("MaxTokens = %d，期望配置值 %d", got, override)
		}

		// Anthropic 的 max_tokens 是必填字段：模板值必须真的写进请求体。
		// 请求体由模型模板派生（与运行时一致），不是测试自行填的上限。
		request := model.Core.Request
		request.Messages = []litellm.Message{litellm.UserText("hi")}
		if _, err := model.Core.Client.Chat(context.Background(), request); err != nil {
			t.Fatalf("请求未发出: %v", err)
		}
		requireNumberField(t, recorder.payload(t), "max_tokens", override)

		// 未配置时回退注册表的物理上限：claude-sonnet-4 → 64000（不是旧的
		// 65536 估算值）。
		fallbackCfg := Config{
			Provider: "claude-proxy", ModelName: "claude-sonnet-4",
			Providers: map[string]ProviderConfig{"claude-proxy": {
				Type: "anthropic", APIKey: "stub-key", BaseURL: server.URL,
				Models: []ModelConfig{{Name: "claude-sonnet-4"}},
			}},
		}
		fallbackModels, err := NewModelSet(fallbackCfg)
		if err != nil {
			t.Fatalf("NewModelSet(registry fallback): %v", err)
		}
		fallback := fallbackModels.ForRole("default")
		if fallback.Core.Request.MaxTokens == nil {
			t.Fatalf("未配置 max_output_tokens 时应回退注册表，实际为 nil")
		}
		if got := *fallback.Core.Request.MaxTokens; got != 64000 {
			t.Fatalf("MaxTokens = %d，期望注册表的 64000（65536 是旧的估算兜底）", got)
		}
		request = fallback.Core.Request
		request.Messages = []litellm.Message{litellm.UserText("hi")}
		if _, err := fallback.Core.Client.Chat(context.Background(), request); err != nil {
			t.Fatalf("回退请求未发出: %v", err)
		}
		requireNumberField(t, recorder.payload(t), "max_tokens", 64000)
	})

	t.Run("NonRequiredProtocolsOmitLimit", func(t *testing.T) {
		for _, providerType := range []string{"openai", "compat"} {
			t.Run(providerType, func(t *testing.T) {
				server, recorder := newRecordingServer(t, chatCompletionStub)
				cfg := Config{
					Provider: providerType, ModelName: "stub-model",
					Providers: map[string]ProviderConfig{providerType: {
						Type: providerType, APIKey: "stub-key", BaseURL: server.URL,
						// 配了也不发：这两个协议不是"必须提供上限"，由服务端/模型
						// 决定生成预算。
						Models: []ModelConfig{{Name: "stub-model", MaxOutputTokens: 4096}},
					}},
				}
				models, err := NewModelSet(cfg)
				if err != nil {
					t.Fatalf("NewModelSet: %v", err)
				}
				model := models.ForRole("default")
				if model.Core.Request.MaxTokens != nil {
					t.Fatalf("%s 协议不得设置 MaxTokens，实际为 %d", providerType, *model.Core.Request.MaxTokens)
				}
				request := model.Core.Request
				request.Messages = []litellm.Message{litellm.UserText("hi")}
				if _, err := model.Core.Client.Chat(context.Background(), request); err != nil {
					t.Fatalf("请求未发出: %v", err)
				}
				requireNoKey(t, recorder.payload(t),
					"max_tokens", "max_completion_tokens", "max_output_tokens")
			})
		}
	})

	t.Run("DatedSuffixRegistryFallback", func(t *testing.T) {
		// 厂商实际模型 ID 带发布日：注册表里的档位不带日期，必须去后缀后命中。
		cases := []struct {
			model string
			want  int
		}{
			{"claude-3-5-haiku-2024-10-22", 8192}, // claude-3.5-haiku 档
			{"claude-sonnet-4-20250514", 64000},   // claude-sonnet-4 档
		}
		for _, tc := range cases {
			t.Run(tc.model, func(t *testing.T) {
				cfg := Config{
					Provider: "claude-proxy", ModelName: tc.model,
					Providers: map[string]ProviderConfig{"claude-proxy": {
						Type: "anthropic", APIKey: "stub-key",
						BaseURL: "https://claude.invalid",
						Models:  []ModelConfig{{Name: tc.model}},
					}},
				}
				models, err := NewModelSet(cfg)
				if err != nil {
					t.Fatalf("NewModelSet: %v", err)
				}
				model := models.ForRole("default")
				if model.Core.Request.MaxTokens == nil {
					t.Fatalf("%s 未解析出物理上限", tc.model)
				}
				if got := *model.Core.Request.MaxTokens; got != tc.want {
					t.Fatalf("%s MaxTokens = %d，期望 %d", tc.model, got, tc.want)
				}
			})
		}
	})

	t.Run("FailClosedUnregisteredAndNegativeValidation", func(t *testing.T) {
		// 未登记 + 无覆盖：不猜上限，构建直接失败，并提示逃生口。
		const unregistered = "claude-3-7-sonnet-20250219"
		cfg := Config{
			Provider: "claude-proxy", ModelName: unregistered,
			Providers: map[string]ProviderConfig{"claude-proxy": {
				Type: "anthropic", APIKey: "stub-key",
				BaseURL: "https://claude.invalid",
				Models:  []ModelConfig{{Name: unregistered}},
			}},
		}
		_, err := NewModelSet(cfg)
		if err == nil {
			t.Fatalf("%s 不在注册表中，必须 fail closed", unregistered)
		}
		if !strings.Contains(err.Error(), "max_output_tokens") {
			t.Fatalf("错误应提示 max_output_tokens 逃生口，得到: %v", err)
		}
		if !strings.Contains(err.Error(), unregistered) {
			t.Fatalf("错误应指出具体模型，得到: %v", err)
		}
		if !errors.Is(err, errs.ErrConfig) {
			t.Fatalf("构建失败应包装 errs.ErrConfig，得到: %v", err)
		}

		// 显式覆盖后同一模型可以构建。
		overrideCfg := cfg
		overrideCfg.Providers = map[string]ProviderConfig{"claude-proxy": {
			Type: "anthropic", APIKey: "stub-key",
			BaseURL: "https://claude.invalid",
			Models:  []ModelConfig{{Name: unregistered, MaxOutputTokens: 64000}},
		}}
		models, err := NewModelSet(overrideCfg)
		if err != nil {
			t.Fatalf("显式 max_output_tokens 后应构建成功: %v", err)
		}
		model := models.ForRole("default")
		if model.Core.Request.MaxTokens == nil || *model.Core.Request.MaxTokens != 64000 {
			t.Fatalf("覆盖值未生效: %v", model.Core.Request.MaxTokens)
		}

		// 负数在配置校验阶段就被拒绝，而不是留到请求阶段。
		negative := Config{
			Provider: "anthropic", ModelName: unregistered,
			Providers: map[string]ProviderConfig{"anthropic": {
				APIKey: "stub-key",
				Models: []ModelConfig{{Name: unregistered, MaxOutputTokens: -1}},
			}},
		}
		err = negative.ValidateBase()
		if err == nil {
			t.Fatal("负数 max_output_tokens 应在配置校验阶段报错")
		}
		if !strings.Contains(err.Error(), "max_output_tokens") {
			t.Fatalf("校验错误应点名 max_output_tokens，得到: %v", err)
		}
		if !errors.Is(err, errs.ErrConfig) {
			t.Fatalf("校验错误应包装 errs.ErrConfig，得到: %v", err)
		}
	})
}
