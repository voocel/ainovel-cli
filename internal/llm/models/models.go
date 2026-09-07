package models

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	agentllm "github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/domain"
)

type Config struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
	Timeout  time.Duration
	Extra    map[string]any
}

func New(config Config) (agentcore.ChatModel, error) {
	if strings.TrimSpace(config.Provider) == "" || strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("model provider and name are required: %w", domain.ErrInvalid)
	}
	options := make([]agentllm.ModelOption, 0, 4)
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
		return "", fmt.Errorf("model provider and name are required: %w", domain.ErrInvalid)
	}
	digest, err := domain.DigestJSON(struct {
		Provider string         `json:"provider"`
		Model    string         `json:"model"`
		BaseURL  string         `json:"base_url,omitempty"`
		Timeout  time.Duration  `json:"timeout,omitempty"`
		Extra    map[string]any `json:"extra,omitempty"`
	}{
		Provider: config.Provider, Model: config.Model, BaseURL: config.BaseURL,
		Timeout: config.Timeout, Extra: config.Extra,
	})
	if err != nil {
		return "", fmt.Errorf("encode model config digest: %w", err)
	}
	return digest, nil
}
