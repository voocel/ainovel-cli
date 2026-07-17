package bootstrap

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// exampleConfig 是引导后写入 ~/.ainovel/config.example.jsonc 的带注释模板。
// 嵌入文件必须与仓库根目录 config.example.jsonc 保持一致，测试会防止漂移。
//
//go:embed config.example.jsonc
var exampleConfig string

// NeedsSetup 检查是否需要首次引导（全局与项目级配置都不存在时触发）。
func NeedsSetup() bool {
	if p := DefaultConfigPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return false
		}
	}
	if _, err := os.Stat(projectConfigPath()); err == nil {
		return false
	}
	return true
}

type setupProvider struct {
	name           string
	label          string
	providerType   string
	baseURL        string // 预填的 base_url
	defaultModel   string
	models         []ModelConfig
	needType       bool   // 自定义代理需要额外问 type 和 base_url
	apiKeyOptional bool   // true 表示 API Key 允许留空
}

// ProviderPreset 是首次引导和运行时 /config 共用的 provider 目录项。
type ProviderPreset struct {
	Name           string
	Label          string
	Type           string
	BaseURL        string
	DefaultModel   string
	Models         []ModelConfig
	NeedType       bool
	APIKeyOptional bool
}

var setupProviders = []setupProvider{
	{name: "openrouter", label: "OpenRouter", baseURL: "https://openrouter.ai/api/v1"},
	{
		name:         "atlascloud",
		label:        "Atlas Cloud",
		providerType: "openai",
		baseURL:      "https://api.atlascloud.ai/v1",
		defaultModel: "qwen/qwen3.5-flash",
		models: []ModelConfig{
			{Name: "qwen/qwen3.5-flash", ContextWindow: 1000000},
			{Name: "deepseek-ai/deepseek-v4-pro", ContextWindow: 1048576},
		},
	},
	{name: "anthropic", label: "Anthropic"},
	{name: "gemini", label: "Gemini"},
	{name: "openai", label: "OpenAI"},
	{name: "deepseek", label: "DeepSeek"},
	{name: "qwen", label: "Qwen"},
	{name: "glm", label: "GLM"},
	{name: "grok", label: "Grok"},
	{name: "ollama", label: "Ollama", baseURL: "http://localhost:11434/v1", apiKeyOptional: true},
	{name: "bedrock", label: "Bedrock", apiKeyOptional: true},
	{name: "custom", label: "Custom Proxy", needType: true, apiKeyOptional: true},
}

// ProviderPresets 返回一份可安全修改的预设列表。
func ProviderPresets() []ProviderPreset {
	out := make([]ProviderPreset, 0, len(setupProviders))
	for _, preset := range setupProviders {
		out = append(out, ProviderPreset{
			Name:           preset.name,
			Label:          preset.label,
			Type:           preset.providerType,
			BaseURL:        preset.baseURL,
			DefaultModel:   preset.defaultModel,
			Models:         append([]ModelConfig(nil), preset.models...),
			NeedType:       preset.needType,
			APIKeyOptional: preset.apiKeyOptional,
		})
	}
	return out
}

// RunSetup 运行首次引导，返回生成的配置。
func RunSetup() (Config, error) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99")).
		Render("未检测到配置文件，开始初始化设置..."))
	fmt.Fprintf(os.Stderr, "  配置文件路径：%s\n", lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(DefaultConfigPath()))
	fmt.Fprintf(os.Stderr, "  完成后可随时编辑该文件调整高级设置。\n")
	fmt.Fprintln(os.Stderr)

	// Step 1: 选择 Provider
	sp, err := runProviderSelect()
	if err != nil {
		return Config{}, err
	}

	providerName := sp.name
	var pc ProviderConfig
	printStepDone("Provider", sp.label)

	// 自定义代理：额外问名称和 API 协议类型
	if sp.needType {
		providerName, err = runTextInput("Provider 名称", "my-proxy")
		if err != nil {
			return Config{}, err
		}
		providerType, err := runTypeSelect()
		if err != nil {
			return Config{}, err
		}
		pc.Type = providerType
	} else if sp.providerType != "" {
		pc.Type = sp.providerType
	}

	// Step 2: 输入 API Key
	var apiKey string
	if sp.apiKeyOptional {
		apiKey, err = runOptionalTextInput("[2/4] API Key（可留空）", "留空表示不使用 API Key")
	} else {
		apiKey, err = runTextInput("[2/4] API Key", "sk-xxx")
	}
	if err != nil {
		return Config{}, err
	}
	pc.APIKey = apiKey
	if apiKey == "" {
		printStepDone("API Key", "未设置")
	} else {
		printStepDone("API Key", maskKey(apiKey))
	}

	// Step 3: Base URL（直接回车使用官方默认地址）
	baseDefault := sp.baseURL
	baseHint := "留空使用官方地址"
	if baseDefault != "" {
		baseHint = baseDefault
	}
	baseURL, err := runTextInputWithDefault("[3/4] Base URL（直接回车使用默认，代理用户填写代理地址）", baseHint, baseDefault)
	if err != nil {
		return Config{}, err
	}
	pc.BaseURL = baseURL
	if baseURL != "" {
		printStepDone("Base URL", baseURL)
	} else {
		printStepDone("Base URL", "默认")
	}

	// Step 4: 模型名（必填）
	modelHint := "例如：gpt-4o / claude-sonnet-4 / gemini-2.5-pro"
	if sp.defaultModel != "" {
		modelHint = sp.defaultModel
	}
	modelName, err := runTextInputWithDefault("[4/4] 模型名称", modelHint, sp.defaultModel)
	if err != nil {
		return Config{}, err
	}
	printStepDone("Model", modelName)
	pc.Models = presetModelsWithSelection(sp.models, modelName)

	cfg := Config{
		Provider:  providerName,
		ModelName: modelName,
		Providers: map[string]ProviderConfig{providerName: pc},
		Roles:     map[string]RoleConfig{},
		Style:     "default",
	}

	// 保存
	path := DefaultConfigPath()
	if err := SaveConfig(path, cfg); err != nil {
		return cfg, fmt.Errorf("save config: %w", err)
	}

	// 生成注释模板
	saveExampleConfig()

	// 全局偏好目录由启动流程（runWithConfig）统一创建，这里仅取路径用于提示
	rulesDir := rules.DefaultHomeRulesDir()

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%s 配置已保存到 %s\n",
		lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("✓"), path)
	fmt.Fprintf(os.Stderr, "  默认模型：%s\n", modelName)
	fmt.Fprintln(os.Stderr, "  如需按角色配置不同模型，编辑配置文件即可。")
	if rulesDir != "" {
		fmt.Fprintf(os.Stderr, "  全局写作偏好可放 %s 下的 .md 文件（见其中 README.txt）\n", rulesDir)
	}
	fmt.Fprintln(os.Stderr)

	return cfg, nil
}

func presetModelsWithSelection(preset []ModelConfig, selected string) []ModelConfig {
	selected = strings.TrimSpace(selected)
	out := make([]ModelConfig, 0, len(preset)+1)
	seen := make(map[string]bool, len(preset)+1)
	for _, model := range preset {
		name := strings.TrimSpace(model.Name)
		if name == "" || seen[name] {
			continue
		}
		model.Name = name
		out = append(out, model)
		seen[name] = true
	}
	if selected != "" && !seen[selected] {
		out = append(out, ModelConfig{Name: selected})
	}
	return out
}

func saveExampleConfig() {
	dir, err := configDir()
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "config.example.jsonc"), []byte(exampleConfig), 0o644)
}

// printStepDone 打印一步完成的确认行。
func printStepDone(label, value string) {
	fmt.Fprintf(os.Stderr, "  %s %s: %s\n",
		lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("✓"),
		label,
		lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(value))
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// ---------- TUI 组件 ----------

func runProviderSelect() (setupProvider, error) {
	m := setupSelectModel{
		title: "[1/4] 选择 Provider",
		items: setupProviders,
	}
	p := tea.NewProgram(m, tea.WithOutput(os.Stderr))
	final, err := p.Run()
	if err != nil {
		return setupProvider{}, err
	}
	result := final.(setupSelectModel)
	if result.cancelled {
		return setupProvider{}, fmt.Errorf("setup cancelled")
	}
	return result.items[result.cursor], nil
}

var apiTypeOptions = []setupProvider{
	{name: "openai", label: "OpenAI 兼容"},
	{name: "anthropic", label: "Anthropic 兼容"},
	{name: "gemini", label: "Gemini 兼容"},
}

func runTypeSelect() (string, error) {
	m := setupSelectModel{
		title: "API 协议类型",
		items: apiTypeOptions,
	}
	p := tea.NewProgram(m, tea.WithOutput(os.Stderr))
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	result := final.(setupSelectModel)
	if result.cancelled {
		return "", fmt.Errorf("setup cancelled")
	}
	return result.items[result.cursor].name, nil
}

func runTextInput(label, placeholder string) (string, error) {
	return runTextInputWithDefault(label, placeholder, "")
}

func runOptionalTextInput(label, placeholder string) (string, error) {
	m := setupInputModel{label: label, placeholder: placeholder, allowEmpty: true}
	p := tea.NewProgram(m, tea.WithOutput(os.Stderr))
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	result := final.(setupInputModel)
	if result.cancelled {
		return "", fmt.Errorf("setup cancelled")
	}
	return utils.CleanInputLine(result.value), nil
}

func runTextInputWithDefault(label, placeholder, defaultValue string) (string, error) {
	m := setupInputModel{label: label, placeholder: placeholder, defaultValue: defaultValue}
	p := tea.NewProgram(m, tea.WithOutput(os.Stderr))
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	result := final.(setupInputModel)
	if result.cancelled {
		return "", fmt.Errorf("setup cancelled")
	}
	if result.value == "" && result.defaultValue != "" {
		return result.defaultValue, nil
	}
	return utils.CleanInputLine(result.value), nil
}

// ---------- 选择器 ----------

var (
	setupCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	setupDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	setupHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	setupInputStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
)

type setupSelectModel struct {
	title     string
	items     []setupProvider
	cursor    int
	cancelled bool
}

func (m setupSelectModel) Init() tea.Cmd { return nil }

func (m setupSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			return m, tea.Quit
		case "q", "esc", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m setupSelectModel) View() string {
	var b strings.Builder
	b.WriteString(setupHeaderStyle.Render(m.title))
	b.WriteString("\n\n")
	for i, item := range m.items {
		cursor := "  "
		label := item.label
		if i == m.cursor {
			cursor = setupCursorStyle.Render("❯ ")
			label = setupCursorStyle.Render(label)
		}
		b.WriteString(cursor + label + "\n")
	}
	b.WriteString(setupDimStyle.Render("\n  ↑↓ 选择  Enter 确认  Esc 取消"))
	return b.String()
}

// ---------- 文本输入 ----------

type setupInputModel struct {
	label        string
	placeholder  string
	defaultValue string // 直接回车时使用的默认值
	allowEmpty   bool   // 允许直接输入空值
	value        string
	cancelled    bool
}

func (m setupInputModel) Init() tea.Cmd { return nil }

func (m setupInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "enter":
			if utils.CleanInputLine(m.value) != "" || m.defaultValue != "" || m.allowEmpty {
				return m, tea.Quit
			}
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "backspace":
			if len(m.value) > 0 {
				runes := []rune(m.value)
				m.value = string(runes[:len(runes)-1])
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.value += utils.CleanInputRunes(msg.Runes)
			} else if msg.Type == tea.KeySpace {
				m.value += " "
			}
		}
	}
	return m, nil
}

func (m setupInputModel) View() string {
	var b strings.Builder
	b.WriteString(setupHeaderStyle.Render(m.label))
	b.WriteString("\n\n")
	b.WriteString(setupInputStyle.Render("❯ "))
	if m.value == "" {
		b.WriteString(setupCursorStyle.Render("▌"))
		b.WriteString(setupDimStyle.Render(m.placeholder))
	} else {
		b.WriteString(m.value)
		b.WriteString(setupCursorStyle.Render("▌"))
	}
	b.WriteString(setupDimStyle.Render("  (Enter 确认, Esc 取消)"))
	b.WriteString("\n")
	return b.String()
}
