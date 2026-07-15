package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/utils"
)

type configStep int

const (
	configStepProvider configStep = iota
	configStepCustomName
	configStepProtocol
	configStepAPI
	configStepKeyAction
	configStepKeyInput
	configStepBaseURL
	configStepModels
	configStepModelName
	configStepModelWindow
	configStepTarget
)

type configProviderChoice struct {
	label    string
	existing *host.ProviderSnapshot
	preset   *bootstrap.ProviderPreset
	custom   bool
}

type modelConfigState struct {
	snapshot host.ModelConfigurationSnapshot
	step     configStep
	cursor   int
	message  string
	input    string
	saving   bool

	providerChoices []configProviderChoice
	provider        string
	providerType    string
	api             string
	baseURL         string
	models          []bootstrap.ModelConfig
	defaultModel    string
	existing        bool
	hasAPIKey       bool
	apiKeyOptional  bool
	apiKeyAction    host.APIKeyAction
	apiKey          string

	pendingModel string
	editModelIdx int
	targetIdx    int
}

func newModelConfigState(rt *host.Host) *modelConfigState {
	snapshot := rt.ModelConfiguration()
	state := &modelConfigState{snapshot: snapshot, editModelIdx: -1}
	configured := make(map[string]bool, len(snapshot.Providers))
	for i := range snapshot.Providers {
		provider := snapshot.Providers[i]
		configured[provider.Name] = true
		copyProvider := provider
		state.providerChoices = append(state.providerChoices, configProviderChoice{
			label: "编辑 " + provider.Name, existing: &copyProvider,
		})
	}
	for _, presetValue := range bootstrap.ProviderPresets() {
		if configured[presetValue.Name] && !presetValue.NeedType {
			continue
		}
		preset := presetValue
		choice := configProviderChoice{label: "新增 " + preset.Label, preset: &preset}
		if preset.NeedType {
			choice.custom = true
		}
		state.providerChoices = append(state.providerChoices, choice)
	}
	return state
}

func (s *modelConfigState) selectProvider() {
	if s.cursor < 0 || s.cursor >= len(s.providerChoices) {
		return
	}
	choice := s.providerChoices[s.cursor]
	s.cursor = 0
	s.message = ""
	if choice.existing != nil {
		provider := choice.existing
		s.provider = provider.Name
		s.providerType = provider.Type
		s.api = provider.API
		s.baseURL = provider.BaseURL
		s.models = append([]bootstrap.ModelConfig(nil), provider.Models...)
		s.existing = true
		s.hasAPIKey = provider.HasAPIKey
		s.apiKeyOptional = !provider.RequiresAPIKey
		if s.snapshot.DefaultProvider == s.provider {
			s.defaultModel = s.snapshot.DefaultModel
		}
		if s.defaultModel == "" && len(s.models) > 0 {
			s.defaultModel = s.models[0].Name
		}
		if s.providerType != "" {
			s.step = configStepProtocol
			s.cursor = protocolIndex(s.providerType)
			return
		}
		s.afterProtocol()
		return
	}

	s.existing = false
	s.hasAPIKey = false
	s.apiKeyAction = host.APIKeyReplace
	if choice.custom {
		s.apiKeyOptional = true
		s.step = configStepCustomName
		s.input = ""
		return
	}
	s.provider = choice.preset.Name
	s.baseURL = choice.preset.BaseURL
	s.apiKeyOptional = choice.preset.APIKeyOptional
	s.afterProtocol()
}

func (s *modelConfigState) afterProtocol() {
	if s.providerType == "openai" || (s.providerType == "" && s.provider == "openai") {
		s.step = configStepAPI
		if s.api == "responses" {
			s.cursor = 1
		} else {
			s.cursor = 0
		}
		return
	}
	s.beginAPIKey()
}

func (s *modelConfigState) beginAPIKey() {
	s.cursor = 0
	if s.existing && s.hasAPIKey {
		s.step = configStepKeyAction
		s.apiKeyAction = host.APIKeyKeep
		return
	}
	s.step = configStepKeyInput
	s.input = ""
	s.apiKeyAction = host.APIKeyReplace
}

func (s *modelConfigState) beginBaseURL() {
	s.step = configStepBaseURL
	s.input = s.baseURL
	s.cursor = 0
}

func (s *modelConfigState) beginTargets() bool {
	if len(s.models) == 0 || s.defaultModel == "" {
		s.message = "请至少添加一个模型并选择默认模型"
		return false
	}
	s.step = configStepTarget
	s.cursor = 0
	s.targetIdx = 0
	for i, target := range s.snapshot.Targets {
		if target.Exists {
			s.targetIdx = i
			break
		}
	}
	s.cursor = s.targetIdx
	s.message = ""
	return true
}

func (s *modelConfigState) selectedTarget() (bootstrap.ConfigTarget, bool) {
	if s.cursor < 0 || s.cursor >= len(s.snapshot.Targets) {
		return bootstrap.ConfigTarget{}, false
	}
	return s.snapshot.Targets[s.cursor], true
}

func (s *modelConfigState) targetWarning() string {
	target, ok := s.selectedTarget()
	if !ok {
		return ""
	}
	for _, candidate := range s.snapshot.Targets {
		if candidate.Exists && candidate.Precedence > target.Precedence {
			return "警告：重启时可能被更高优先级的 " + candidate.Label + " 覆盖"
		}
	}
	return ""
}

func (s *modelConfigState) deleteSelectedModel() {
	if s.cursor < 0 || s.cursor >= len(s.models) {
		return
	}
	model := s.models[s.cursor]
	if model.Name == s.defaultModel {
		s.message = "请先用 Enter 选择另一个默认模型"
		return
	}
	for _, ref := range s.snapshot.ReferencesFor(s.provider, model.Name) {
		if ref == "default" {
			continue
		}
		s.message = fmt.Sprintf("模型仍被 %s 引用，不能删除", ref)
		return
	}
	s.models = append(s.models[:s.cursor], s.models[s.cursor+1:]...)
	if s.cursor >= len(s.models) && s.cursor > 0 {
		s.cursor--
	}
	s.message = ""
}

func (s *modelConfigState) draft(targetID string) host.ModelConfigurationDraft {
	return host.ModelConfigurationDraft{
		Provider: s.provider, Type: s.providerType, API: s.api, BaseURL: s.baseURL,
		Models: append([]bootstrap.ModelConfig(nil), s.models...), DefaultModel: s.defaultModel,
		APIKeyAction: s.apiKeyAction, APIKey: s.apiKey, TargetID: targetID,
	}
}

type modelConfigSavedMsg struct{ err error }

func saveModelConfiguration(rt *host.Host, draft host.ModelConfigurationDraft) tea.Cmd {
	return func() tea.Msg { return modelConfigSavedMsg{err: rt.ConfigureModels(draft)} }
}

func (m Model) handleModelConfigKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := m.modelConfig
	if state == nil {
		return m, nil
	}
	if msg.Type == tea.KeyEsc {
		m.modelConfig = nil
		return m, m.textarea.Focus()
	}
	if state.saving {
		return m, nil
	}

	switch state.step {
	case configStepProvider:
		moveConfigCursor(state, msg, len(state.providerChoices))
		if msg.Type == tea.KeyEnter {
			state.selectProvider()
		}
	case configStepCustomName:
		if handleConfigInput(&state.input, msg) && msg.Type == tea.KeyEnter {
			name := strings.TrimSpace(state.input)
			if name == "" {
				state.message = "Provider 名称不能为空"
				break
			}
			for _, provider := range state.snapshot.Providers {
				if provider.Name == name {
					state.message = "Provider 已存在，请返回后选择编辑"
					return m, nil
				}
			}
			state.provider = name
			state.step = configStepProtocol
			state.cursor = 0
			state.message = ""
		}
	case configStepProtocol:
		moveConfigCursor(state, msg, len(configProtocols))
		if msg.Type == tea.KeyEnter {
			state.providerType = configProtocols[state.cursor]
			if state.providerType != "openai" {
				state.api = ""
			}
			state.afterProtocol()
		}
	case configStepAPI:
		moveConfigCursor(state, msg, len(configAPIs))
		if msg.Type == tea.KeyEnter {
			state.api = configAPIs[state.cursor]
			state.beginAPIKey()
		}
	case configStepKeyAction:
		moveConfigCursor(state, msg, len(configKeyActions))
		if msg.Type == tea.KeyEnter {
			state.apiKeyAction = configKeyActions[state.cursor].action
			if state.apiKeyAction == host.APIKeyClear && !state.apiKeyOptional {
				state.message = "该 Provider 必须配置 API Key，不能清除"
				return m, nil
			}
			if state.apiKeyAction == host.APIKeyReplace {
				state.step = configStepKeyInput
				state.input = ""
			} else {
				state.beginBaseURL()
			}
		}
	case configStepKeyInput:
		if handleConfigInput(&state.input, msg) && msg.Type == tea.KeyEnter {
			state.apiKey = strings.TrimSpace(state.input)
			if state.apiKey == "" && !state.apiKeyOptional {
				state.message = "该 Provider 必须配置 API Key"
				return m, nil
			}
			state.beginBaseURL()
		}
	case configStepBaseURL:
		if handleConfigInput(&state.input, msg) && msg.Type == tea.KeyEnter {
			state.baseURL = strings.TrimSpace(state.input)
			state.step = configStepModels
			state.cursor = 0
			state.message = ""
		}
	case configStepModels:
		moveConfigCursor(state, msg, len(state.models))
		switch strings.ToLower(msg.String()) {
		case "a":
			state.step = configStepModelName
			state.input = ""
			state.editModelIdx = -1
			state.message = ""
		case "e":
			if len(state.models) > 0 {
				state.editModelIdx = state.cursor
				state.pendingModel = state.models[state.cursor].Name
				if window := state.models[state.cursor].ContextWindow; window > 0 {
					state.input = strconv.Itoa(window)
				} else {
					state.input = ""
				}
				state.step = configStepModelWindow
			}
		case "d":
			state.deleteSelectedModel()
		case "s", "ctrl+s":
			state.beginTargets()
		default:
			if msg.Type == tea.KeyEnter && len(state.models) > 0 {
				state.defaultModel = state.models[state.cursor].Name
				state.message = "已选择默认模型：" + state.defaultModel
			}
		}
	case configStepModelName:
		if handleConfigInput(&state.input, msg) && msg.Type == tea.KeyEnter {
			name := strings.TrimSpace(state.input)
			if name == "" {
				state.message = "模型名称不能为空"
				break
			}
			for _, model := range state.models {
				if model.Name == name {
					state.message = "模型已存在"
					return m, nil
				}
			}
			state.pendingModel = name
			state.input = ""
			state.step = configStepModelWindow
			state.message = ""
		}
	case configStepModelWindow:
		if handleConfigInput(&state.input, msg) && msg.Type == tea.KeyEnter {
			window, err := parseContextWindowInput(state.input)
			if err != nil {
				state.message = err.Error()
				break
			}
			if state.editModelIdx >= 0 {
				state.models[state.editModelIdx].ContextWindow = window
				state.cursor = state.editModelIdx
			} else {
				state.models = append(state.models, bootstrap.ModelConfig{Name: state.pendingModel, ContextWindow: window})
				state.cursor = len(state.models) - 1
				if state.defaultModel == "" {
					state.defaultModel = state.pendingModel
				}
			}
			state.step = configStepModels
			state.editModelIdx = -1
			state.pendingModel = ""
			state.message = ""
		}
	case configStepTarget:
		moveConfigCursor(state, msg, len(state.snapshot.Targets))
		if msg.Type == tea.KeyEnter {
			target, ok := state.selectedTarget()
			if !ok {
				state.message = "没有可用的配置保存位置"
				break
			}
			state.saving = true
			state.message = "正在校验并保存配置..."
			return m, saveModelConfiguration(m.runtime, state.draft(target.ID))
		}
	}
	return m, nil
}

var configProtocols = []string{"openai", "anthropic", "gemini"}
var configAPIs = []string{"chat", "responses"}

var configKeyActions = []struct {
	label  string
	action host.APIKeyAction
}{
	{"保留现有 API Key", host.APIKeyKeep},
	{"输入新的 API Key", host.APIKeyReplace},
	{"清除 API Key", host.APIKeyClear},
}

func protocolIndex(protocol string) int {
	for i, item := range configProtocols {
		if item == protocol {
			return i
		}
	}
	return 0
}

func moveConfigCursor(state *modelConfigState, msg tea.KeyMsg, total int) {
	if total <= 0 {
		state.cursor = 0
		return
	}
	switch msg.Type {
	case tea.KeyUp:
		state.cursor = (state.cursor - 1 + total) % total
	case tea.KeyDown:
		state.cursor = (state.cursor + 1) % total
	}
}

// handleConfigInput 更新单行输入；返回 true 表示该键已被输入控件消费。
func handleConfigInput(value *string, msg tea.KeyMsg) bool {
	if msg.String() == "ctrl+u" {
		*value = ""
		return true
	}
	switch msg.Type {
	case tea.KeyEnter:
		return true
	case tea.KeyBackspace, tea.KeyDelete:
		runes := []rune(*value)
		if len(runes) > 0 {
			*value = string(runes[:len(runes)-1])
		}
		return true
	case tea.KeySpace:
		*value += " "
		return true
	case tea.KeyRunes:
		*value += utils.CleanInputRunes(msg.Runes)
		return true
	default:
		return false
	}
}

func parseContextWindowInput(input string) (int, error) {
	value := strings.ToLower(strings.TrimSpace(input))
	if value == "" || value == "0" || value == "auto" {
		return 0, nil
	}
	multiplier := float64(1)
	if strings.HasSuffix(value, "k") {
		multiplier = 1000
		value = strings.TrimSpace(strings.TrimSuffix(value, "k"))
	} else if strings.HasSuffix(value, "m") {
		multiplier = 1_000_000
		value = strings.TrimSpace(strings.TrimSuffix(value, "m"))
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("上下文窗口请输入正整数、128K、1M，或留空使用自动值")
	}
	result := number * multiplier
	if result > float64(math.MaxInt) || math.Trunc(result) != result {
		return 0, fmt.Errorf("上下文窗口超出有效整数范围")
	}
	return int(result), nil
}

func renderModelConfigModal(width, height int, state *modelConfigState) string {
	if state == nil {
		return ""
	}
	boxW := min(max(72, width*3/4), width-4)
	boxH := min(max(20, height*3/4), height-2)
	contentW := paddedModalContentWidth(boxW)
	var lines []string
	title := "/config 配置模型"
	hint := "↑↓ 选择 · Enter 确认 · Esc 取消"

	switch state.step {
	case configStepProvider:
		lines = append(lines, configHeading("选择要编辑或新增的 Provider"))
		lines = append(lines, renderConfigChoices(labelsForProviderChoices(state.providerChoices), state.cursor, contentW, 12)...)
	case configStepCustomName:
		lines = append(lines, configHeading("自定义 Provider 名称"), renderConfigInput(state.input, false, contentW))
		hint = configInputHint
	case configStepProtocol:
		lines = append(lines, configHeading("API 协议类型"))
		lines = append(lines, renderConfigChoices(configProtocols, state.cursor, contentW, 8)...)
	case configStepAPI:
		lines = append(lines, configHeading("OpenAI Endpoint"))
		lines = append(lines, renderConfigChoices([]string{"chat · /v1/chat/completions", "responses · /v1/responses"}, state.cursor, contentW, 8)...)
	case configStepKeyAction:
		lines = append(lines, configHeading("API Key"))
		var labels []string
		for _, item := range configKeyActions {
			labels = append(labels, item.label)
		}
		lines = append(lines, renderConfigChoices(labels, state.cursor, contentW, 8)...)
	case configStepKeyInput:
		label := "输入 API Key（内容已隐藏）"
		if state.apiKeyOptional {
			label += "，可留空"
		}
		lines = append(lines, configHeading(label), renderConfigInput(state.input, true, contentW))
		hint = configInputHint
	case configStepBaseURL:
		lines = append(lines, configHeading("Base URL（留空使用 Provider 默认地址）"), renderConfigInput(state.input, false, contentW))
		hint = configInputHint
	case configStepModels:
		lines = append(lines, configHeading("管理模型列表"))
		if len(state.models) == 0 {
			lines = append(lines, lipgloss.NewStyle().Foreground(colorDim).Render("  尚未添加模型，按 A 添加"))
		} else {
			start, end := configWindow(len(state.models), state.cursor, 10)
			for i := start; i < end; i++ {
				model := state.models[i]
				prefix := "  "
				if i == state.cursor {
					prefix = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("› ")
				}
				marker := "  "
				if model.Name == state.defaultModel {
					marker = "★ "
				}
				window := "自动"
				if model.ContextWindow > 0 {
					window = formatContextWindow(model.ContextWindow)
				}
				line := fmt.Sprintf("%s%s%-36s  上下文 %s", prefix, marker, truncateWidth(model.Name, 34), window)
				lines = append(lines, truncateWidth(line, contentW))
			}
		}
		hint = "↑↓ 选择 · Enter 设为默认 · A 新增 · E 编辑窗口 · D 删除 · S 保存 · Esc 取消"
	case configStepModelName:
		lines = append(lines, configHeading("新增模型名称"), renderConfigInput(state.input, false, contentW))
		hint = configInputHint
	case configStepModelWindow:
		lines = append(lines, configHeading("模型 "+state.pendingModel+" 的上下文窗口"))
		lines = append(lines, lipgloss.NewStyle().Foreground(colorDim).Render("留空或 0 = 自动解析；支持 128K / 1M"))
		lines = append(lines, renderConfigInput(state.input, false, contentW))
		hint = configInputHint
	case configStepTarget:
		lines = append(lines, configHeading("选择配置保存位置"))
		var labels []string
		for _, target := range state.snapshot.Targets {
			exists := "新建"
			if target.Exists {
				exists = "已有"
			}
			labels = append(labels, fmt.Sprintf("%s · %s · %s", target.Label, exists, target.Path))
		}
		lines = append(lines, renderConfigChoices(labels, state.cursor, contentW, 8)...)
		if warning := state.targetWarning(); warning != "" {
			lines = append(lines, lipgloss.NewStyle().Foreground(colorReview).Render(truncateWidth(warning, contentW)))
		}
	}

	if state.message != "" {
		color := colorError
		if state.saving || strings.HasPrefix(state.message, "已选择") {
			color = colorAccent
		}
		lines = append(lines, "", lipgloss.NewStyle().Foreground(color).Render(truncateWidth(state.message, contentW)))
	}
	modal := renderPaddedModalFrame(boxW, boxH, title, hint, lines)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

const configInputHint = "输入 · Enter 确认 · Ctrl+U 清空 · Esc 取消"

func configHeading(text string) string {
	return lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(text)
}

func labelsForProviderChoices(choices []configProviderChoice) []string {
	out := make([]string, 0, len(choices))
	for _, choice := range choices {
		out = append(out, choice.label)
	}
	return out
}

func renderConfigChoices(labels []string, cursor, width, limit int) []string {
	if len(labels) == 0 {
		return []string{lipgloss.NewStyle().Foreground(colorDim).Render("没有可用选项")}
	}
	start, end := configWindow(len(labels), cursor, limit)
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		prefix := "  "
		style := lipgloss.NewStyle().Foreground(bodyTextColor)
		if i == cursor {
			prefix = "› "
			style = style.Foreground(colorAccent).Bold(true)
		}
		lines = append(lines, prefix+style.Render(truncateWidth(labels[i], max(8, width-2))))
	}
	return lines
}

func configWindow(total, cursor, limit int) (int, int) {
	if total <= limit {
		return 0, total
	}
	start := max(0, cursor-limit/2)
	end := min(total, start+limit)
	if end-start < limit {
		start = max(0, end-limit)
	}
	return start, end
}

func renderConfigInput(value string, secret bool, width int) string {
	display := value
	if secret {
		display = strings.Repeat("•", utf8.RuneCountInString(value))
	}
	if display == "" {
		display = "▌"
	} else {
		display += "▌"
	}
	return lipgloss.NewStyle().
		Width(max(20, width-4)).
		Border(baseBorder).
		BorderForeground(colorDim).
		Padding(0, 1).
		Foreground(bodyTextColor).
		Render(truncateWidth(display, max(16, width-8)))
}
