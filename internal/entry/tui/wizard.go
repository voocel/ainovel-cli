package tui

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// 配置向导（workbench §4）：未配置模型时的启动落点，完成后进入首页。
// 保存仅做本地校验与装配；连接测试独立、可选，不写入配置。
type wizardState struct {
	inputs    []textinput.Model
	name      textinput.Model
	source    appconfig.Config
	original  string
	endpoint  string
	step      int
	verifying bool
	fromHome  bool
	err       string
	notice    string
	choosing  bool
	provider  int
	custom    bool
	advanced  bool
	cancel    context.CancelFunc
	request   *struct{ token byte }
}

type wizardVerifiedMsg struct {
	config  appconfig.Config
	request *struct{ token byte }
	err     error
}

var wizardFields = []struct {
	label       string
	placeholder string
	secret      bool
	optional    bool
}{
	{label: "Provider", placeholder: "openai / anthropic / deepseek / gemini / glm ...", secret: false},
	{label: "模型", placeholder: "例如 deepseek-chat", secret: false},
	{label: "API Key", placeholder: "粘贴密钥；使用环境凭据可留空", secret: true, optional: true},
	{label: "Base URL（可选）", placeholder: "默认官方地址；中转服务请填写", optional: true},
}

func newWizardState(initial appconfig.Config, initialErr string, fromHome bool) wizardState {
	state := wizardState{inputs: make([]textinput.Model, len(wizardFields)), err: initialErr, fromHome: fromHome}
	state.source = initial
	state.original = initial.Provider
	state.name = newInput("例如 my-proxy / 公司网关")
	state.name.SetValue(initial.Provider)
	pc, err := initial.ActiveProvider()
	if err != nil {
		pc = initial.Providers[initial.Provider]
		if pc.Type == "" {
			pc.Type = initial.Provider
		}
	}
	if initial.Provider != "" && err == nil {
		state.source = initial.WithProvider(initial.Provider, initial.Model, pc)
	}
	state.endpoint = pc.API
	state.custom = pc.Type != initial.Provider
	if err != nil && initial.Provider != "" {
		state.custom = true
	}
	values := []string{pc.Type, initial.Model, pc.APIKey, pc.BaseURL}
	for i, field := range wizardFields {
		input := newInput(field.placeholder)
		input.SetValue(values[i])
		if field.secret {
			input.EchoMode = textinput.EchoPassword
		}
		state.inputs[i] = input
	}
	state.choosing = initial.Provider == ""
	state.advanced = pc.BaseURL != ""
	if !state.choosing {
		state.step = 1
	}
	state.inputs[state.step].Focus()
	return state
}

// Presets select the protocol only; model IDs remain user supplied so the UI
// does not silently pin users to a model that their account may not support.
var wizardProviders = []struct{ label, id string }{
	{"OpenAI", "openai"}, {"Anthropic", "anthropic"},
	{"DeepSeek", "deepseek"}, {"Gemini", "gemini"},
	{"OpenRouter", "openrouter"}, {"OpenAI 兼容 / 中转服务", "openai"},
	{"自定义连接", ""}, {"Ollama · 本地模型", "ollama"},
	{"Qwen", "qwen"}, {"GLM", "glm"}, {"Grok", "grok"},
	{"MiniMax", "minimax"}, {"MiMo", "mimo"},
}

func (w *wizardState) focus(step int) tea.Cmd {
	for i := range w.inputs {
		w.inputs[i].Blur()
	}
	w.name.Blur()
	w.step = step
	if step == 7 {
		return w.name.Focus()
	}
	if step > 0 && step < len(w.inputs) {
		return w.inputs[step].Focus()
	}
	return nil
}

func (m model) focusWizard(step int) (tea.Model, tea.Cmd) {
	if step == 0 {
		m.wizard.custom = true
	}
	if step == 3 {
		m.wizard.advanced = true
	}
	cmd := m.wizard.focus(step)
	return m, cmd
}

func (m model) chooseWizardProvider() (tea.Model, tea.Cmd) {
	choice := m.wizardChoices()[m.wizard.provider]
	if choice.saved {
		initial := m.wizard.source
		initial.Provider = choice.name
		if pc, ok := initial.Providers[choice.name]; ok && choice.name != m.wizard.source.Provider {
			initial.Model = ""
			if len(pc.Models) > 0 {
				initial.Model = pc.Models[0]
			}
		}
		m.wizard = newWizardState(initial, "", m.wizard.fromHome)
		return m.focusWizard(1)
	}
	for i := range m.wizard.inputs {
		m.wizard.inputs[i].SetValue("")
	}
	protocol := choice.id
	if protocol == "" {
		protocol = "openai"
	}
	m.wizard.inputs[0].SetValue(protocol)
	m.wizard.name.SetValue(choice.id)
	m.wizard.original = ""
	m.wizard.endpoint = ""
	m.wizard.choosing = false
	m.wizard.custom = choice.id == "" || m.wizard.provider == 5
	m.wizard.advanced = m.wizard.provider == 5
	if m.wizard.custom {
		m.wizard.name.SetValue("")
		return m.focusWizard(7)
	}
	return m.focusWizard(1)
}

type wizardChoice struct {
	label, id, name string
	saved           bool
}

func (m model) wizardChoices() []wizardChoice {
	choices := make([]wizardChoice, 0, len(wizardProviders)+len(m.wizard.source.Providers))
	for _, p := range wizardProviders {
		choices = append(choices, wizardChoice{label: p.label, id: p.id})
	}
	names := make([]string, 0, len(m.wizard.source.Providers))
	for name := range m.wizard.source.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		choices = append(choices, wizardChoice{label: "已保存 · " + name, name: name, saved: true})
	}
	return choices
}

func (m model) updateWizard(message tea.Msg) (tea.Model, tea.Cmd) {
	if verified, ok := message.(wizardVerifiedMsg); ok {
		if !m.wizard.verifying || verified.request != m.wizard.request {
			return m, nil
		}
		return m.finishWizard(verified)
	}
	if m.wizard.verifying {
		if key, ok := message.(tea.KeyMsg); ok && key.Type == tea.KeyEsc {
			m.wizard.cancel()
			m.wizard.verifying = false
			m.wizard.request = nil
			m.wizard.err = "已取消验证，配置尚未保存"
		}
		return m, nil
	}
	if mouse, ok := message.(tea.MouseMsg); ok {
		if mouse.Action != tea.MouseActionPress || mouse.Button != tea.MouseButtonLeft {
			return m, nil
		}
		layout := m.wizardLayout()
		for _, hit := range layout.hits {
			if mouse.Y != hit.y || mouse.X < hit.x || mouse.X >= hit.x+hit.width {
				continue
			}
			if m.wizard.choosing {
				m.wizard.provider = hit.action
				return m.chooseWizardProvider()
			}
			if hit.action >= 20 && hit.action < 23 {
				m.wizard.inputs[0].SetValue(wizardProtocols[hit.action-20])
				m.wizard.endpoint = ""
				return m.focusWizard(0)
			}
			return m.activateWizard(hit.action)
		}
		return m, nil
	}
	if key, ok := message.(tea.KeyMsg); ok {
		m.wizard.notice = ""
		if m.wizard.choosing {
			switch key.Type {
			case tea.KeyUp, tea.KeyShiftTab:
				m.wizard.provider = (m.wizard.provider + len(m.wizardChoices()) - 1) % len(m.wizardChoices())
			case tea.KeyDown, tea.KeyTab:
				m.wizard.provider = (m.wizard.provider + 1) % len(m.wizardChoices())
			case tea.KeyEnter:
				return m.chooseWizardProvider()
			case tea.KeyEsc:
				if m.wizard.fromHome {
					m.page = pageHome
					return m, m.loadLibraryCmd()
				}
				return m, tea.Quit
			}
			return m, nil
		}
		if m.wizard.step == 0 && (key.Type == tea.KeyLeft || key.Type == tea.KeyRight) {
			delta := 1
			if key.Type == tea.KeyLeft {
				delta = -1
			}
			next := 0
			for i, p := range wizardProtocols {
				if p == m.wizard.inputs[0].Value() {
					next = (i + delta + len(wizardProtocols)) % len(wizardProtocols)
					break
				}
			}
			m.wizard.inputs[0].SetValue(wizardProtocols[next])
			m.wizard.endpoint = ""
			return m, nil
		}
		switch key.Type {
		case tea.KeyEsc:
			m.wizard.choosing = true
			return m, nil
		case tea.KeyTab, tea.KeyDown:
			return m.moveWizardFocus(1)
		case tea.KeyShiftTab, tea.KeyUp:
			return m.moveWizardFocus(-1)
		case tea.KeyEnter:
			if m.wizard.step >= 4 && m.wizard.step != 7 {
				return m.activateWizard(m.wizard.step)
			}
			return m.moveWizardFocus(1)
		}
	}
	if m.wizard.step == 7 {
		var cmd tea.Cmd
		m.wizard.name, cmd = m.wizard.name.Update(message)
		return m, cmd
	}
	if m.wizard.step > 0 && m.wizard.step < len(m.wizard.inputs) {
		var cmd tea.Cmd
		m.wizard.inputs[m.wizard.step], cmd = m.wizard.inputs[m.wizard.step].Update(message)
		return m, cmd
	}
	return m, nil
}

func (m model) submitWizard() (tea.Model, tea.Cmd) { return m.runWizardAction(true) }

func (m model) runWizardAction(test bool) (tea.Model, tea.Cmd) {
	for i, field := range wizardFields {
		if !field.optional && strings.TrimSpace(m.wizard.inputs[i].Value()) == "" {
			m.wizard.err = "请填写" + field.label
			return m.focusWizard(i)
		}
	}
	name := strings.TrimSpace(m.wizard.name.Value())
	if name == "" {
		m.wizard.err = "请填写连接名称"
		return m.focusWizard(7)
	}
	if _, exists := m.wizard.source.Providers[name]; exists && name != m.wizard.original {
		m.wizard.err = "连接名称已存在，请从已保存连接中选择编辑"
		return m.focusWizard(7)
	}
	pc := appconfig.ProviderConfig{
		Type:    strings.ToLower(strings.TrimSpace(m.wizard.inputs[0].Value())),
		APIKey:  strings.TrimSpace(m.wizard.inputs[2].Value()),
		BaseURL: strings.TrimSpace(m.wizard.inputs[3].Value()),
	}
	if pc.Type == "openai" {
		pc.API = m.wizard.endpoint
	}
	if previous, ok := m.wizard.source.Providers[m.wizard.original]; ok {
		pc.Models = previous.Models
	}
	if pc.BaseURL != "" {
		u, err := url.Parse(pc.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			m.wizard.err = "地址须为 http(s) URL，不能包含用户凭据或片段"
			return m.focusWizard(3)
		}
	}
	config := m.wizard.source.WithProvider(name, strings.TrimSpace(m.wizard.inputs[1].Value()), pc)
	if err := config.Validate(); err != nil {
		m.wizard.err = err.Error()
		return m, nil
	}
	m.wizard.err = ""
	m.wizard.notice = ""
	if !test {
		return m.saveWizard(config)
	}
	m.wizard.verifying = true
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	m.wizard.cancel = cancel
	m.wizard.request = &struct{ token byte }{}
	request, verify := m.wizard.request, m.deps.Verify
	return m, func() tea.Msg {
		defer cancel()
		var err error
		if verify != nil {
			err = verify(ctx, config)
		}
		return wizardVerifiedMsg{config: config, request: request, err: err}
	}
}

// Test completion stays in the form and never persists configuration.
func (m model) finishWizard(verified wizardVerifiedMsg) (tea.Model, tea.Cmd) {
	m.wizard.verifying = false
	if verified.err != nil {
		m.wizard.err = "连不上模型：" + wizardError(verified.err, verified.config)
		return m, nil
	}
	m.wizard.notice = "连接测试通过 · 尚未保存"
	return m.focusWizard(4)
}

func (m model) saveWizard(config appconfig.Config) (tea.Model, tea.Cmd) {
	api, err := m.deps.Rebuild(config)
	if err != nil {
		m.wizard.err = wizardError(err, config)
		return m, nil
	}
	if err := appconfig.SaveConfig(m.deps.ConfigDir, config); err != nil {
		m.wizard.err = wizardError(err, config)
		return m, nil
	}
	m.api = api
	m.config = config
	m.page = pageHome
	m.home = newHomeState()
	return m, m.loadLibraryCmd()
}

// Provider errors can echo request credentials. Never render the configured key.
func wizardError(err error, config appconfig.Config) string {
	text := err.Error()
	pc, _ := config.ActiveProvider()
	if pc.APIKey != "" {
		text = strings.ReplaceAll(text, pc.APIKey, "[已隐藏]")
	}
	return text
}

// Focus order follows visible controls; collapsed fields never trap keyboard focus.
func (m model) wizardControls() []int {
	controls := []int{6, 7}
	if m.wizard.custom {
		controls = append(controls, 0)
	}
	controls = append(controls, 1, 2, 5)
	if m.wizard.advanced {
		controls = append(controls, 3)
	}
	if m.wizard.inputs[0].Value() == "openai" {
		controls = append(controls, 8)
	}
	return append(controls, 4, 9)
}

func (m model) moveWizardFocus(delta int) (tea.Model, tea.Cmd) {
	controls := m.wizardControls()
	for i, control := range controls {
		if control == m.wizard.step {
			return m.focusWizard(controls[(i+delta+len(controls))%len(controls)])
		}
	}
	return m.focusWizard(controls[0])
}

func (m model) activateWizard(action int) (tea.Model, tea.Cmd) {
	switch action {
	case 8:
		if m.wizard.endpoint == "responses" {
			m.wizard.endpoint = "chat"
		} else {
			m.wizard.endpoint = "responses"
		}
		return m.focusWizard(8)
	case 4:
		return m.runWizardAction(false)
	case 9:
		return m.submitWizard()
	case 5:
		m.wizard.advanced = !m.wizard.advanced
		return m.focusWizard(5)
	case 6:
		m.wizard.choosing = true
		return m, nil
	default:
		return m.focusWizard(action)
	}
}

var wizardProtocols = []string{"openai", "anthropic", "gemini"}
