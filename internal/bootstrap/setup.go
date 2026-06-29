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
// 单一数据源：直接嵌入同Thư mục的 config.example.jsonc，避免与文档样本漂移。
//
//go:embed config.example.jsonc
var exampleConfig string

// NeedsSetup Kiểm traCó czy khôngCần首次引导（Tập tin cấu hình不存在时触发）。
func NeedsSetup(flagPath string) bool {
	if flagPath != "" {
		_, err := os.Stat(flagPath)
		return os.IsNotExist(err)
	}
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
	baseURL        string // 预填的 base_url
	needType       bool   // Tuỳ chỉnhProxyCần额外问 type 和 base_url
	apiKeyOptional bool   // true 表示 API Key 允许留Rỗng
}

var setupProviders = []setupProvider{
	{name: "openrouter", label: "OpenRouter", baseURL: "https://openrouter.ai/api/v1"},
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

// RunSetup 运行首次引导，Quay lại生成的Cấu hình。
func RunSetup() (Config, error) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99")).
		Render("未Phát hiệnTập tin cấu hình，Bắt đầu初始化Thiết lập..."))
	fmt.Fprintf(os.Stderr, "  Tập tin cấu hìnhĐường dẫn：%s\n", lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(DefaultConfigPath()))
	fmt.Fprintf(os.Stderr, "  Hoàn thành后可随时Sửa该Tập tin调整高级Thiết lập。\n")
	fmt.Fprintln(os.Stderr)

	// Step 1: Chọn Provider
	sp, err := runProviderSelect()
	if err != nil {
		return Config{}, err
	}

	providerName := sp.name
	var pc ProviderConfig
	printStepDone("Provider", sp.label)

	// Tuỳ chỉnhProxy：额外问名称和 API 协议类型
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
	}

	// Step 2: Nhập API Key
	var apiKey string
	if sp.apiKeyOptional {
		apiKey, err = runOptionalTextInput("[2/4] API Key（可留Rỗng）", "留Rỗng表示不使用 API Key")
	} else {
		apiKey, err = runTextInput("[2/4] API Key", "sk-xxx")
	}
	if err != nil {
		return Config{}, err
	}
	pc.APIKey = apiKey
	if apiKey == "" {
		printStepDone("API Key", "未Thiết lập")
	} else {
		printStepDone("API Key", maskKey(apiKey))
	}

	// Step 3: Base URL（直接回车使用官方Mặc định地址）
	baseDefault := sp.baseURL
	baseHint := "留Rỗng使用官方地址"
	if baseDefault != "" {
		baseHint = baseDefault
	}
	baseURL, err := runTextInputWithDefault("[3/4] Base URL（直接回车使用Mặc định，Proxy用户填写Proxy地址）", baseHint, baseDefault)
	if err != nil {
		return Config{}, err
	}
	pc.BaseURL = baseURL
	if baseURL != "" {
		printStepDone("Base URL", baseURL)
	} else {
		printStepDone("Base URL", "Mặc định")
	}

	// Step 4: Mô hình名（必填）
	modelName, err := runTextInput("[4/4] Mô hình名称", "例如：gpt-4o / claude-sonnet-4 / gemini-2.5-pro")
	if err != nil {
		return Config{}, err
	}
	printStepDone("Model", modelName)

	cfg := Config{
		Provider:  providerName,
		ModelName: modelName,
		Providers: map[string]ProviderConfig{providerName: pc},
		Roles:     map[string]RoleConfig{},
		Style:     "default",
	}

	// Lưu
	path := DefaultConfigPath()
	if err := SaveConfig(path, cfg); err != nil {
		return cfg, fmt.Errorf("save config: %w", err)
	}

	// 生成注释模板
	saveExampleConfig()

	// 全局偏好Thư mục由启动流程（runWithConfig）统一Tạo，这里仅取Đường dẫn用于提示
	rulesDir := rules.DefaultHomeRulesDir()

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%s Cấu hình已Lưu到 %s\n",
		lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("✓"), path)
	fmt.Fprintf(os.Stderr, "  Mặc địnhMô hình：%s\n", modelName)
	fmt.Fprintln(os.Stderr, "  如需按角色Cấu hình不同Mô hình，SửaTập tin cấu hình即可。")
	if rulesDir != "" {
		fmt.Fprintf(os.Stderr, "  全局Viết偏好可放 %s 下的 .md Tập tin（见其中 README.txt）\n", rulesDir)
	}
	fmt.Fprintln(os.Stderr)

	return cfg, nil
}

func saveExampleConfig() {
	dir, err := configDir()
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "config.example.jsonc"), []byte(exampleConfig), 0o644)
}

// printStepDone 打印一步Hoàn thành的Xác nhận行。
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
		title: "[1/4] Chọn Provider",
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

// ---------- Chọn器 ----------

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
	b.WriteString(setupDimStyle.Render("\n  ↑↓ Chọn  Enter Xác nhận  Esc Hủy"))
	return b.String()
}

// ---------- 文本Nhập ----------

type setupInputModel struct {
	label        string
	placeholder  string
	defaultValue string // 直接回车时使用的Mặc định值
	allowEmpty   bool   // 允许直接NhậpRỗng值
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
	b.WriteString(setupDimStyle.Render("  (Enter Xác nhận, Esc Hủy)"))
	b.WriteString("\n")
	return b.String()
}
