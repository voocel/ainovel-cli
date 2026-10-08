package models

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/provider"
)

// ThinkingLevels 是全部思考强度，空串是自动（沿用模型默认）。某个模型可选的见 Levels。
var ThinkingLevels = []string{"", "off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Levels 是 client 可选的思考强度：自动总在；关闭与各档强度要适配器发得出，未声明
// 能力的适配器不受限。哪些档位模型真接受仍由厂商决定。
func Levels(client *litellm.Client) []string {
	caps, known := client.Capabilities()
	if !known {
		return ThinkingLevels
	}
	levels := []string{""}
	for _, level := range ThinkingLevels[1:] {
		if level == "off" && caps.DisableThinking || level != "off" && caps.ThinkingEffort {
			levels = append(levels, level)
		}
	}
	return levels
}

// effective 是 level 在 client 上的生效值：发不出的档位退回自动。意图留在配置里，
// 换回支持的模型即恢复。
func effective(client *litellm.Client, level string) string {
	if !slices.Contains(Levels(client), level) {
		return ""
	}
	return level
}

type Config struct {
	Provider string
	API      string
	Model    string
	APIKey   string
	BaseURL  string
	// Thinking 是思考强度意图（空 = 沿用模型默认），见 ThinkingLevels；生效值按适配器折算。
	Thinking string
}

// Binding 是一次尝试实际使用的模型：实例、身份与生效的思考强度。Digest 是执行
// 配置摘要（不含密钥），记进 agent.run_started 供诊断比对不同尝试的配置。
type Binding struct {
	Provider string
	Model    string
	Thinking string
	Digest   string
	Chat     agentcore.Model
}

// Bindings 是运行时的整套模型绑定（D57）：默认一套，角色可覆盖。
type Bindings struct {
	Default Binding
	Roles   map[string]Binding
}

// For 取角色的绑定，未覆盖的角色落到默认。
func (b Bindings) For(role string) Binding {
	if binding, ok := b.Roles[role]; ok {
		return binding
	}
	return b.Default
}

// Bind 按配置构建可执行的模型绑定。
func Bind(config Config) (Binding, error) {
	digest, err := config.Digest()
	if err != nil {
		return Binding{}, err
	}
	chat, err := New(config)
	if err != nil {
		return Binding{}, err
	}
	return Binding{Provider: config.Provider, Model: config.Model, Thinking: effective(chat.Client, config.Thinking), Digest: digest, Chat: chat}, nil
}

// Routed 返回按 cacheKey 路由提示缓存、按 sessionID 归属会话的模型。只设厂商
// 列出的选项：其余厂商拒绝不认识的选项。空值不设。
func (b Binding) Routed(cacheKey, sessionID string) agentcore.Model {
	chat := b.Chat
	caps, _ := chat.Client.Capabilities()
	options := maps.Clone(chat.Request.ProviderOptions)
	if options == nil {
		options = litellm.ProviderOptions{}
	}
	for key, value := range map[string]string{"prompt_cache_key": cacheKey, "session_id": sessionID} {
		if value == "" || !slices.Contains(caps.ProviderOptions, key) {
			continue
		}
		if err := options.Set(key, value); err != nil {
			panic(err) // 字符串总能编码
		}
	}
	chat.Request.ProviderOptions = options
	return chat
}

const requiredMaxTokens = 32000

func New(config Config) (agentcore.Model, error) {
	if strings.TrimSpace(config.Provider) == "" || strings.TrimSpace(config.Model) == "" {
		return agentcore.Model{}, fmt.Errorf("model provider and name are required: %w", model.ErrInvalid)
	}
	p, err := provider.New(adapter(config), provider.Config{APIKey: config.APIKey, BaseURL: config.BaseURL, API: config.API})
	if err != nil {
		return agentcore.Model{}, err
	}
	client, err := litellm.New(p)
	if err != nil {
		return agentcore.Model{}, err
	}
	chat := agentcore.Model{Client: client, Request: litellm.Request{Model: config.Model, Thinking: thinking(effective(client, config.Thinking))}}
	// 必须带输出上限的厂商（如 Anthropic）用这个值；Claude 4 起的模型都接受，写一章绰绰有余。
	if caps, _ := client.Capabilities(); caps.MaxTokensRequired {
		chat.Request.MaxTokens = new(requiredMaxTokens)
	}
	return chat, nil
}

// adapter 是连接实际使用的 litellm 实现。用户只选格式；OpenAI 格式的连接指向官方地址、
// 或走 Responses 接口时按 OpenAI 官方接口说话，其余地址是中转、网关或自部署，按通用的
// Chat Completions（compat）说话：它们的思考放在 reasoning_content / reasoning 里，输出上限
// 用 max_tokens，官方实现都不认。
func adapter(config Config) string {
	if config.Provider != "openai" || config.API == "responses" || config.BaseURL == "" {
		return config.Provider
	}
	if u, err := url.Parse(config.BaseURL); err == nil && u.Hostname() == "api.openai.com" {
		return config.Provider
	}
	return "compat"
}

// thinking 是思考强度的请求设置；自动是 nil，沿用厂商默认。
func thinking(level string) *litellm.Thinking {
	switch level {
	case "":
		return nil
	case "off":
		return &litellm.Thinking{Disabled: true}
	}
	return &litellm.Thinking{Effort: level}
}

// Verify 用最小请求验证配置可真实连通：配置向导先验证再落盘，
// 避免把坏配置写进文件导致启动锁死。
func Verify(ctx context.Context, config Config) error {
	chat, err := New(config)
	if err != nil {
		return err
	}
	request := chat.Request
	request.Messages = []litellm.Message{litellm.UserText("ping")}
	// 只验连通：8 个 token 容不下思考预算。
	request.MaxTokens, request.Thinking = new(8), nil
	_, err = chat.Client.Chat(ctx, request)
	return err
}

func (config Config) Digest() (string, error) {
	if strings.TrimSpace(config.Provider) == "" || strings.TrimSpace(config.Model) == "" {
		return "", fmt.Errorf("model provider and name are required: %w", model.ErrInvalid)
	}
	digest, err := model.DigestJSON(struct {
		Provider string `json:"provider"`
		API      string `json:"api,omitempty"`
		Model    string `json:"model"`
		BaseURL  string `json:"base_url,omitempty"`
		Thinking string `json:"thinking,omitempty"`
	}{
		Provider: config.Provider, API: config.API, Model: config.Model, BaseURL: config.BaseURL,
		Thinking: config.Thinking,
	})
	if err != nil {
		return "", fmt.Errorf("encode model config digest: %w", err)
	}
	return digest, nil
}
