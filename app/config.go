package app

import (
	"fmt"
	"path/filepath"
)

// Config 小说应用配置。
type Config struct {
	Prompt    string // 用户的小说需求
	NovelName string // 小说名（用作输出目录名）
	OutputDir string // 输出根目录，默认 output/{NovelName}
	Provider  string // LLM 提供商：openai / anthropic / gemini
	ModelName string // LLM 模型名
	APIKey    string // API Key
	BaseURL   string // API Base URL（可选）
	Style     string // 写作风格（default/suspense/fantasy/romance）
}

// Prompts 嵌入的提示词。
type Prompts struct {
	Coordinator    string
	ArchitectShort string
	ArchitectMid   string
	ArchitectLong  string
	Writer         string
	Editor         string
}

// Validate 校验配置（CLI 模式，要求 Prompt 非空）。
func (c *Config) Validate() error {
	if c.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	return c.ValidateBase()
}

// ValidateBase 校验基础配置（TUI 模式下 Prompt 由用户输入，不在此检查）。
func (c *Config) ValidateBase() error {
	if c.APIKey == "" {
		return fmt.Errorf("api key is required (set OPENROUTER_API_KEY, Z_OPENAI_API_KEY, ANTHROPIC_API_KEY, or GEMINI_API_KEY)")
	}
	switch c.Provider {
	case "openai", "anthropic", "gemini", "openrouter":
	default:
		return fmt.Errorf("unsupported provider %q (use openai/anthropic/gemini/openrouter)", c.Provider)
	}
	return nil
}

// 各 provider 的默认模型名。
var defaultModels = map[string]string{
	"openai":    "gpt-4o",
	"anthropic": "claude-sonnet-4-20250514",
	"gemini":    "gemini-2.5-pro",
}

// FillDefaults 填充默认值。
func (c *Config) FillDefaults() {
	if c.NovelName == "" {
		c.NovelName = "novel"
	}
	if c.OutputDir == "" {
		c.OutputDir = filepath.Join("output", c.NovelName)
	}
	if c.Provider == "" {
		c.Provider = "openai"
	}
	if c.ModelName == "" {
		c.ModelName = defaultModels[c.Provider]
	}
	if c.Style == "" {
		c.Style = "default"
	}
}
