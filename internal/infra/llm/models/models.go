package models

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	agentllm "github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type Config struct {
	Provider string
	API      string
	Model    string
	APIKey   string
	BaseURL  string
	Timeout  time.Duration
	Extra    map[string]any
	// Thinking 是思考强度意图（空 = 沿用模型默认）；生效值由执行时按模型能力折算。
	Thinking agentcore.ThinkingLevel
}

// Binding 是一次尝试实际使用的模型：实例、身份与思考强度意图。Digest 是执行
// 配置摘要（不含密钥），记进 agent.run_started 供诊断比对不同尝试的配置。
type Binding struct {
	Provider string
	Model    string
	Thinking agentcore.ThinkingLevel
	Digest   string
	Chat     agentcore.ChatModel
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
	return Binding{Provider: config.Provider, Model: config.Model, Thinking: config.Thinking, Digest: digest, Chat: chat}, nil
}

func New(config Config) (agentcore.ChatModel, error) {
	if strings.TrimSpace(config.Provider) == "" || strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("model provider and name are required: %w", model.ErrInvalid)
	}
	if config.API != "" && (config.Provider != "openai" || (config.API != "chat" && config.API != "responses")) {
		return nil, fmt.Errorf("invalid API endpoint for provider %q", config.Provider)
	}
	options := make([]agentllm.ModelOption, 0, 4)
	if config.API != "" {
		options = append(options, agentllm.WithProviderExtra(map[string]any{"api": config.API}))
	}
	if config.APIKey != "" {
		options = append(options, agentllm.WithAPIKey(config.APIKey))
	}
	if config.BaseURL != "" {
		options = append(options, agentllm.WithBaseURL(config.BaseURL))
	}
	if config.Timeout != 0 {
		options = append(options, agentllm.WithRequestTimeout(config.Timeout))
	}
	if len(config.Extra) != 0 {
		options = append(options, agentllm.WithExtra(config.Extra))
	}
	model, err := agentllm.NewModel(config.Provider, config.Model, options...)
	if err != nil {
		return nil, err
	}
	return model, nil
}

// Verify 用最小请求验证配置可真实连通：配置向导先验证再落盘，
// 避免把坏配置写进文件导致启动锁死。
func Verify(ctx context.Context, config Config) error {
	model, err := New(config)
	if err != nil {
		return err
	}
	_, err = model.Generate(ctx,
		[]agentcore.Message{agentcore.UserMsg("ping")}, nil, agentcore.WithMaxTokens(8))
	return err
}

func (config Config) Digest() (string, error) {
	if strings.TrimSpace(config.Provider) == "" || strings.TrimSpace(config.Model) == "" {
		return "", fmt.Errorf("model provider and name are required: %w", model.ErrInvalid)
	}
	digest, err := model.DigestJSON(struct {
		Provider string                  `json:"provider"`
		API      string                  `json:"api,omitempty"`
		Model    string                  `json:"model"`
		BaseURL  string                  `json:"base_url,omitempty"`
		Timeout  time.Duration           `json:"timeout,omitempty"`
		Extra    map[string]any          `json:"extra,omitempty"`
		Thinking agentcore.ThinkingLevel `json:"thinking,omitempty"`
	}{
		Provider: config.Provider, API: config.API, Model: config.Model, BaseURL: config.BaseURL,
		Timeout: config.Timeout, Extra: config.Extra, Thinking: config.Thinking,
	})
	if err != nil {
		return "", fmt.Errorf("encode model config digest: %w", err)
	}
	return digest, nil
}
