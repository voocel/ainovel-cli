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
type Config struct {
	Provider  string                    `json:"provider,omitempty"`
	Model     string                    `json:"model,omitempty"`
	APIKey    string                    `json:"api_key,omitempty"`
	BaseURL   string                    `json:"base_url,omitempty"`
	Providers map[string]ProviderConfig `json:"providers,omitempty"`
}

// ProviderConfig 将连接身份与底层协议分开，允许同一协议配置多个连接。
type ProviderConfig struct {
	Type    string   `json:"type,omitempty"`
	API     string   `json:"api,omitempty"`
	APIKey  string   `json:"api_key,omitempty"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models,omitempty"`
}

func (c Config) ActiveProvider() (ProviderConfig, error) {
	pc, err := c.providerConfig()
	if err != nil {
		return ProviderConfig{}, err
	}
	switch pc.Type {
	case "openai", "anthropic", "gemini":
	case "deepseek", "openrouter", "qwen", "glm", "grok", "minimax", "mimo", "ollama", "bedrock":
		if pc.Type != c.Provider {
			return ProviderConfig{}, fmt.Errorf("自定义连接协议只支持 openai、anthropic、gemini")
		}
	default:
		return ProviderConfig{}, fmt.Errorf("不支持的协议类型 %q", pc.Type)
	}
	if pc.API != "" && pc.API != "chat" && pc.API != "responses" {
		return ProviderConfig{}, fmt.Errorf("连接 %q 的 api 必须为 chat 或 responses", c.Provider)
	}
	if pc.API != "" && pc.Type != "openai" {
		return ProviderConfig{}, fmt.Errorf("连接 %q 只有 openai 协议支持 api 选项", c.Provider)
	}
	return pc, nil
}

func (c Config) providerConfig() (ProviderConfig, error) {
	pc := ProviderConfig{Type: c.Provider, APIKey: c.APIKey, BaseURL: c.BaseURL}
	if c.Providers != nil {
		var ok bool
		pc, ok = c.Providers[c.Provider]
		if !ok {
			return ProviderConfig{}, fmt.Errorf("连接 %q 未配置", c.Provider)
		}
	}
	if pc.Type == "" {
		pc.Type = c.Provider
	}
	return pc, nil
}

// WithProvider 返回独立的连接映射，保留其他连接并将旧扁平配置迁入映射。
func (c Config) WithProvider(name, model string, pc ProviderConfig) Config {
	providers := make(map[string]ProviderConfig, len(c.Providers)+1)
	for key, value := range c.Providers {
		providers[key] = value
	}
	if c.Providers == nil && c.Provider != "" && c.Provider != name {
		old := ProviderConfig{Type: c.Provider, APIKey: c.APIKey, BaseURL: c.BaseURL}
		if c.Model != "" {
			old.Models = []string{c.Model}
		}
		providers[c.Provider] = old
	}
	pc.Models = append([]string(nil), pc.Models...)
	found := false
	for _, item := range pc.Models {
		if item == model {
			found = true
			break
		}
	}
	if model != "" && !found {
		pc.Models = append(pc.Models, model)
	}
	providers[name] = pc
	c.Provider, c.Model, c.Providers = name, model, providers
	c.APIKey, c.BaseURL = "", ""
	return c
}

// Configured 报告是否具备发起真实创作的最低配置。
func (c Config) Configured() bool {
	return c.Validate() == nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Provider) == "" || strings.TrimSpace(c.Model) == "" {
		return errors.New("provider 和 model 必须同时设置")
	}
	_, err := c.ActiveProvider()
	return err
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
	overrides := Config{
		Provider: os.Getenv("AINOVEL_PROVIDER"),
		Model:    os.Getenv("AINOVEL_MODEL"),
		APIKey:   os.Getenv("AINOVEL_API_KEY"),
		BaseURL:  os.Getenv("AINOVEL_BASE_URL"),
	}
	providerType := strings.TrimSpace(os.Getenv("AINOVEL_PROVIDER_TYPE"))
	api := strings.TrimSpace(os.Getenv("AINOVEL_API"))
	if strings.TrimSpace(overrides.Provider) != "" && overrides.Provider != config.Provider {
		// 切换连接不能继承原连接的密钥或地址。
		if config.Providers == nil {
			config.APIKey, config.BaseURL = "", ""
		}
		config.Provider = overrides.Provider
	}
	if strings.TrimSpace(overrides.Model) != "" {
		config.Model = overrides.Model
	}
	if (config.Provider == "") != (config.Model == "") {
		return Config{}, errors.New("AINOVEL_PROVIDER 和 AINOVEL_MODEL 必须同时设置")
	}
	if config.Provider == "" {
		return config, nil
	}
	pc, err := config.providerConfig()
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(overrides.APIKey) != "" {
		pc.APIKey = overrides.APIKey
	}
	if strings.TrimSpace(overrides.BaseURL) != "" {
		pc.BaseURL = overrides.BaseURL
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
