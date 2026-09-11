package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// 配置向导（workbench §4）：未配置模型时的启动落点，完成后进入首页。
// 顺序是先验证连通、再落盘、再重建服务：坏配置永远不落盘，不会锁死启动。
type wizardState struct {
	inputs    []textinput.Model
	step      int
	verifying bool
	fromHome  bool
	err       string
}

type wizardVerifiedMsg struct {
	config appconfig.Config
	err    error
}

var wizardFields = []struct {
	label       string
	placeholder string
	secret      bool
	optional    bool
}{
	{label: "Provider", placeholder: "openai / anthropic / deepseek / gemini / glm ...", secret: false},
	{label: "模型", placeholder: "例如 deepseek-chat", secret: false},
	{label: "API Key", placeholder: "sk-...", secret: true},
	{label: "Base URL（可选）", placeholder: "自建或中转网关才需要，直接回车跳过", optional: true},
}

func newWizardState(initial appconfig.Config, initialErr string, fromHome bool) wizardState {
	state := wizardState{inputs: make([]textinput.Model, len(wizardFields)), err: initialErr, fromHome: fromHome}
	values := []string{initial.Provider, initial.Model, initial.APIKey, initial.BaseURL}
	for i, field := range wizardFields {
		input := newInput(field.placeholder)
		input.SetValue(values[i])
		if field.secret {
			input.EchoMode = textinput.EchoPassword
		}
		state.inputs[i] = input
	}
	state.inputs[0].Focus()
	return state
}

func (m model) updateWizard(message tea.Msg) (tea.Model, tea.Cmd) {
	if verified, ok := message.(wizardVerifiedMsg); ok {
		return m.finishWizard(verified)
	}
	key, isKey := message.(tea.KeyMsg)
	if !isKey || m.wizard.verifying {
		return m, nil
	}
	switch key.Type {
	case tea.KeyEsc:
		if m.wizard.step == 0 {
			if m.wizard.fromHome {
				m.page = pageHome
				return m, m.loadLibraryCmd()
			}
			return m, tea.Quit
		}
		m.wizard.inputs[m.wizard.step].Blur()
		m.wizard.step--
		m.wizard.inputs[m.wizard.step].Focus()
		return m, nil
	case tea.KeyEnter:
		value := strings.TrimSpace(m.wizard.inputs[m.wizard.step].Value())
		if value == "" && !wizardFields[m.wizard.step].optional {
			m.wizard.err = wizardFields[m.wizard.step].label + " 不能为空"
			return m, nil
		}
		m.wizard.err = ""
		if m.wizard.step < len(m.wizard.inputs)-1 {
			m.wizard.inputs[m.wizard.step].Blur()
			m.wizard.step++
			m.wizard.inputs[m.wizard.step].Focus()
			return m, nil
		}
		config := appconfig.Config{
			Provider: strings.TrimSpace(m.wizard.inputs[0].Value()),
			Model:    strings.TrimSpace(m.wizard.inputs[1].Value()),
			APIKey:   strings.TrimSpace(m.wizard.inputs[2].Value()),
			BaseURL:  strings.TrimSpace(m.wizard.inputs[3].Value()),
		}
		m.wizard.verifying = true
		return m, m.verifyConfigCmd(config)
	}
	var cmd tea.Cmd
	m.wizard.inputs[m.wizard.step], cmd = m.wizard.inputs[m.wizard.step].Update(message)
	return m, cmd
}

func (m model) verifyConfigCmd(config appconfig.Config) tea.Cmd {
	verify, ctx := m.deps.Verify, m.ctx
	return func() tea.Msg {
		if verify == nil {
			return wizardVerifiedMsg{config: config}
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return wizardVerifiedMsg{config: config, err: verify(checkCtx, config)}
	}
}

// finishWizard 在连通验证通过后落盘并重建带执行器的服务；任何一步失败都留在向导。
func (m model) finishWizard(verified wizardVerifiedMsg) (tea.Model, tea.Cmd) {
	m.wizard.verifying = false
	if verified.err != nil {
		m.wizard.err = "连不上模型，请检查配置：" + verified.err.Error()
		return m, nil
	}
	if err := appconfig.SaveConfig(m.deps.ConfigDir, verified.config); err != nil {
		m.wizard.err = err.Error()
		return m, nil
	}
	api, err := m.deps.Rebuild(verified.config)
	if err != nil {
		m.wizard.err = err.Error()
		return m, nil
	}
	m.api = api
	m.config = verified.config
	m.page = pageHome
	m.home = newHomeState()
	return m, m.loadLibraryCmd()
}

func (m model) viewWizard() string {
	width := m.entryWidth()
	lines := []string{benchTheme.Muted.Render("配置一次，之后直接开始创作。"), ""}
	for i, field := range wizardFields {
		if i == m.wizard.step {
			input := m.wizard.inputs[i]
			input.Width = max(1, width-4)
			input.SetCursor(input.Position())
			lines = append(lines, benchTheme.Accent.Render("▎ "+field.label), "  "+input.View(), "")
		} else {
			value := m.wizard.inputs[i].Value()
			if field.secret && value != "" {
				value = "••••••••"
			}
			if value == "" {
				value = "待填写"
				if field.optional {
					value = "可选"
				}
			}
			lines = append(lines, benchTheme.Muted.Render(truncate("  "+field.label+" · "+value, width)))
		}
	}
	if m.wizard.verifying {
		lines = append(lines, benchTheme.Accent.Render("正在验证模型连接…"))
	}
	return m.entryPage("连接创作模型", lines, m.wizard.err, "Enter 下一项 / 验证连接 · Esc 上一步")
}
