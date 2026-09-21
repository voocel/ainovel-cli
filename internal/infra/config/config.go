// Package config 承载用户级模型连接配置与默认数据位置。
// 它不依赖任何内核包；服务装配始终发生在组合根（cmd/ainovel-cli）。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config 是用户级模型配置。作品数据与它无关：换模型不换书。
// Provider/Model/Thinking 是默认绑定；Roles 按角色覆盖，未配的角色跟随默认。
type Config struct {
	Provider  string                    `json:"provider,omitempty"`
	Model     string                    `json:"model,omitempty"`
	Thinking  string                    `json:"thinking,omitempty"` // 思考强度意图：空 = 自动
	Providers map[string]ProviderConfig `json:"providers,omitempty"`
	Roles     map[string]RoleConfig     `json:"roles,omitempty"`
}

// ProviderConfig 将连接身份与底层协议分开，允许同一协议配置多个连接。
type ProviderConfig struct {
	Type    string   `json:"type,omitempty"`
	API     string   `json:"api,omitempty"`
	APIKey  string   `json:"api_key,omitempty"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models,omitempty"`
}

// RoleConfig 是某个角色（architect / writer / editor）的模型覆盖；Thinking 留空继承顶层。
type RoleConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Thinking string `json:"thinking,omitempty"`
}

// ActiveProvider 返回默认绑定所用的连接。
func (c Config) ActiveProvider() (ProviderConfig, error) { return c.Connection(c.Provider) }

// Connection 返回并校验一个已命名连接：协议、api 选项都在这里把关。
func (c Config) Connection(name string) (ProviderConfig, error) {
	pc, err := c.providerConfig(name)
	if err != nil {
		return ProviderConfig{}, err
	}
	switch pc.Type {
	case "openai", "anthropic", "gemini":
	case "deepseek", "openrouter", "qwen", "glm", "grok", "minimax", "mimo", "ollama", "bedrock":
		if pc.Type != name {
			return ProviderConfig{}, fmt.Errorf("自定义连接协议只支持 openai、anthropic、gemini")
		}
	default:
		return ProviderConfig{}, fmt.Errorf("不支持的协议类型 %q", pc.Type)
	}
	if pc.API != "" && pc.API != "chat" && pc.API != "responses" {
		return ProviderConfig{}, fmt.Errorf("连接 %q 的 api 必须为 chat 或 responses", name)
	}
	if pc.API != "" && pc.Type != "openai" {
		return ProviderConfig{}, fmt.Errorf("连接 %q 只有 openai 协议支持 api 选项", name)
	}
	return pc, nil
}

// providerConfig 查找已命名连接；type 留空表示连接名就是内置服务商。
func (c Config) providerConfig(name string) (ProviderConfig, error) {
	pc, ok := c.Providers[name]
	if !ok {
		return ProviderConfig{}, fmt.Errorf("连接 %q 未配置", name)
	}
	if pc.Type == "" {
		pc.Type = name
	}
	return pc, nil
}

// WithProvider 把连接写入独立的映射并设为默认绑定，保留其他连接、不改输入。
func (c Config) WithProvider(name, model string, pc ProviderConfig) Config {
	providers := c.copyProviders()
	pc.Models = withModel(pc.Models, model)
	providers[name] = pc
	c.Provider, c.Model, c.Providers = name, model, providers
	return c
}

// WithRole 让角色改用指定连接的模型（模型并入该连接的已保存列表）；连接必须已存在。
func (c Config) WithRole(role, connection, model, thinking string) Config {
	providers := c.copyProviders()
	if pc, ok := providers[connection]; ok {
		pc.Models = withModel(pc.Models, model)
		providers[connection] = pc
	}
	roles := make(map[string]RoleConfig, len(c.Roles)+1)
	for key, value := range c.Roles {
		roles[key] = value
	}
	roles[role] = RoleConfig{Provider: connection, Model: model, Thinking: thinking}
	c.Providers, c.Roles = providers, roles
	return c
}

// WithoutRole 清除角色覆盖，让它跟随默认绑定。
func (c Config) WithoutRole(role string) Config {
	roles := make(map[string]RoleConfig, len(c.Roles))
	for key, value := range c.Roles {
		if key != role {
			roles[key] = value
		}
	}
	if len(roles) == 0 {
		roles = nil
	}
	c.Roles = roles
	return c
}

func (c Config) copyProviders() map[string]ProviderConfig {
	providers := make(map[string]ProviderConfig, len(c.Providers)+1)
	for key, value := range c.Providers {
		providers[key] = value
	}
	return providers
}

// withModel 返回独立的模型列表，缺席时把 model 追加到末尾。
func withModel(models []string, model string) []string {
	models = append([]string(nil), models...)
	if model == "" {
		return models
	}
	for _, item := range models {
		if item == model {
			return models
		}
	}
	return append(models, model)
}

// Configured 报告是否具备发起真实创作的最低配置。
func (c Config) Configured() bool {
	return c.Validate() == nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Provider) == "" || strings.TrimSpace(c.Model) == "" {
		return errors.New("provider 和 model 必须同时设置")
	}
	if _, err := c.ActiveProvider(); err != nil {
		return err
	}
	for role, rc := range c.Roles {
		if strings.TrimSpace(role) == "" || strings.TrimSpace(rc.Provider) == "" || strings.TrimSpace(rc.Model) == "" {
			return fmt.Errorf("角色 %q 必须同时设置 provider 和 model", role)
		}
		if _, err := c.Connection(rc.Provider); err != nil {
			return fmt.Errorf("角色 %q：%w", role, err)
		}
	}
	return nil
}

// DefaultDir 是配置与作品库的默认位置（~/.ainovel/v1）。v1 与 v0 的配置格式
// 不同且互不迁移，目录独立后两边各写各的文件，互不覆盖。
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".ainovel", "v1"), nil
}

func ConfigPath(dir string) string { return filepath.Join(dir, "config.json") }

// DataPath 是作品库数据库位置：全部作品共用一库，作品身份与工作目录无关。
func DataPath(dir string) string { return filepath.Join(dir, "ainovel.db") }

// LoadConfig 读取配置文件；文件不存在视为空配置而非错误。
func LoadConfig(dir string) (Config, error) {
	payload, err := os.ReadFile(ConfigPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read app config: %w", err)
	}
	var config Config
	if err := json.Unmarshal(payload, &config); err != nil {
		return Config{}, fmt.Errorf("decode app config %s: %w", ConfigPath(dir), err)
	}
	return config, nil
}

// SaveConfig 持久化配置（0600：内含 API Key）。
func SaveConfig(dir string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	pc, err := config.ActiveProvider()
	if err != nil {
		return err
	}
	config = config.WithProvider(config.Provider, config.Model, pc)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create app config dir: %w", err)
	}
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode app config: %w", err)
	}
	file, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return fmt.Errorf("create app config: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(payload); err != nil {
		file.Close()
		return fmt.Errorf("write app config: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync app config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close app config: %w", err)
	}
	if err := os.Rename(file.Name(), ConfigPath(dir)); err != nil {
		return fmt.Errorf("write app config: %w", err)
	}
	return nil
}

// ResolveConfig 合并配置文件与环境变量（环境变量优先），供每次启动使用。
func ResolveConfig(dir string) (Config, error) {
	config, err := LoadConfig(dir)
	if err != nil {
		return Config{}, err
	}
	envProvider := strings.TrimSpace(os.Getenv("AINOVEL_PROVIDER"))
	apiKey := strings.TrimSpace(os.Getenv("AINOVEL_API_KEY"))
	baseURL := strings.TrimSpace(os.Getenv("AINOVEL_BASE_URL"))
	providerType := strings.TrimSpace(os.Getenv("AINOVEL_PROVIDER_TYPE"))
	api := strings.TrimSpace(os.Getenv("AINOVEL_API"))
	if envProvider != "" {
		config.Provider = envProvider
	}
	if model := strings.TrimSpace(os.Getenv("AINOVEL_MODEL")); model != "" {
		config.Model = model
	}
	if thinking := strings.TrimSpace(os.Getenv("AINOVEL_THINKING")); thinking != "" {
		config.Thinking = thinking
	}
	if (config.Provider == "") != (config.Model == "") {
		return Config{}, errors.New("AINOVEL_PROVIDER 和 AINOVEL_MODEL 必须同时设置")
	}
	if config.Provider == "" {
		return config, nil
	}
	// 环境变量指向文件里没有的连接时，连接完全由环境变量定义：不从别的连接继承密钥或地址。
	pc, err := config.providerConfig(config.Provider)
	if err != nil {
		if envProvider == "" {
			return Config{}, err
		}
		pc = ProviderConfig{}
	}
	if apiKey != "" {
		pc.APIKey = apiKey
	}
	if baseURL != "" {
		pc.BaseURL = baseURL
	}
	if providerType != "" {
		pc.Type = providerType
	}
	if api != "" {
		pc.API = api
	}
	config = config.WithProvider(config.Provider, config.Model, pc)
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// DefaultUserID 取系统用户名作为默认创作者身份。
func DefaultUserID() string {
	for _, key := range []string{"AINOVEL_USER", "USER", "USERNAME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "user"
}
