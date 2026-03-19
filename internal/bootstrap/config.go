package bootstrap

import (
	"fmt"
	"path/filepath"
)

// ProviderConfig 定义单个 LLM 提供商的凭证。
type ProviderConfig struct {
	Type    string `json:"type,omitempty"`     // API 协议类型（openai/anthropic/gemini），自定义代理时指定
	APIKey  string `json:"api_key,omitempty"`  // API Key
	BaseURL string `json:"base_url,omitempty"` // API Base URL
}

// ProviderType 返回有效的 API 协议类型。
// 优先使用显式 Type，否则从 provider 名称推断，最终回退到 openai。
func (pc ProviderConfig) ProviderType(name string) string {
	if pc.Type != "" {
		return pc.Type
	}
	if _, ok := knownProviderTypes[name]; ok {
		return name
	}
	return "openai"
}

// knownProviderTypes 已知 provider 名称到 API 协议类型的映射。
var knownProviderTypes = map[string]bool{
	"openai":     true,
	"anthropic":  true,
	"gemini":     true,
	"openrouter": true,
	"deepseek":   true,
	"qwen":       true,
	"glm":        true,
	"grok":       true,
	"ollama":     true,
	"bedrock":    true,
}

// RoleConfig 定义单个角色的模型覆盖。
type RoleConfig struct {
	Provider string `json:"provider"` // provider 名称（Providers map 中的 key）
	Model    string `json:"model"`    // 模型名（原样透传，不做任何解析）
}

// knownRoles 支持的角色名。
var knownRoles = map[string]bool{
	"coordinator": true,
	"architect":   true,
	"writer":      true,
	"editor":      true,
}

// Config 小说应用配置。
type Config struct {
	// 运行时字段（不序列化到 JSON）
	Prompt    string `json:"-"` // 用户的小说需求
	NovelName string `json:"-"` // 小说名（用作输出目录名）
	OutputDir string `json:"-"` // 输出根目录

	// 默认 LLM 配置
	Provider  string `json:"provider"` // 默认 provider（Providers map 中的 key）
	ModelName string `json:"model"`    // 默认模型名

	// Provider 凭证库
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// 角色级模型覆盖
	Roles map[string]RoleConfig `json:"roles,omitempty"`

	// 创作参数
	Style         string `json:"style,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
}

// Validate 校验配置（CLI 模式，要求 Prompt 非空）。
func (c *Config) Validate() error {
	if c.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	return c.ValidateBase()
}

// ValidateBase 校验基础配置。
func (c *Config) ValidateBase() error {
	if c.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if c.ModelName == "" {
		return fmt.Errorf("model is required")
	}

	// 默认 provider 必须有凭证
	pc, ok := c.Providers[c.Provider]
	if !ok || pc.APIKey == "" {
		return fmt.Errorf("provider %q has no api_key configured", c.Provider)
	}

	// 校验角色覆盖
	for role, rc := range c.Roles {
		if !knownRoles[role] {
			return fmt.Errorf("unknown role %q in roles config (valid: coordinator/architect/writer/editor)", role)
		}
		if rc.Provider == "" || rc.Model == "" {
			return fmt.Errorf("role %q must have both provider and model", role)
		}
		rpc, ok := c.Providers[rc.Provider]
		if !ok || rpc.APIKey == "" {
			return fmt.Errorf("role %q references provider %q which has no api_key", role, rc.Provider)
		}
	}

	return nil
}

// DefaultProviderConfig 返回默认 provider 的凭证配置。
func (c *Config) DefaultProviderConfig() ProviderConfig {
	if c.Providers == nil {
		return ProviderConfig{}
	}
	return c.Providers[c.Provider]
}

// FillDefaults 填充默认值。
func (c *Config) FillDefaults() {
	if c.NovelName == "" {
		c.NovelName = "novel"
	}
	if c.OutputDir == "" {
		c.OutputDir = filepath.Join("output", c.NovelName)
	}
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig)
	}
	if c.Roles == nil {
		c.Roles = make(map[string]RoleConfig)
	}
	if c.Style == "" {
		c.Style = "default"
	}
	if c.ContextWindow <= 0 {
		c.ContextWindow = 128000
	}
}
