// Package app 承载用户级应用配置：全局一份模型配置与默认数据位置。
// 它不依赖任何内核包；服务装配始终发生在组合根（cmd/ainovel-cli）。
package app

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
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

// Configured 报告是否具备发起真实创作的最低配置。
func (c Config) Configured() bool {
	return strings.TrimSpace(c.Provider) != "" && strings.TrimSpace(c.Model) != ""
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Provider) == "" || strings.TrimSpace(c.Model) == "" {
		return errors.New("provider 和 model 必须同时设置")
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create app config dir: %w", err)
	}
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode app config: %w", err)
	}
	if err := os.WriteFile(ConfigPath(dir), payload, 0o600); err != nil {
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
	if strings.TrimSpace(overrides.Provider) != "" {
		config.Provider = overrides.Provider
	}
	if strings.TrimSpace(overrides.Model) != "" {
		config.Model = overrides.Model
	}
	if strings.TrimSpace(overrides.APIKey) != "" {
		config.APIKey = overrides.APIKey
	}
	if strings.TrimSpace(overrides.BaseURL) != "" {
		config.BaseURL = overrides.BaseURL
	}
	if (config.Provider == "") != (config.Model == "") {
		return Config{}, errors.New("AINOVEL_PROVIDER 和 AINOVEL_MODEL 必须同时设置")
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
