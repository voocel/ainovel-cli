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
	configStepAddPicker
	configStepCustomName
	configStepHub // Provider 详情：列出各项当前值，挑一项进子编辑器，保存也在此
	configStepProtocol
	configStepAPI
	configStepKeyAction
	configStepKeyInput
	configStepBaseURL
	configStepModels
	configStepModelDetail // 单个模型详情：改上下文窗口 / 删除
	configStepModelName
	configStepModelWindow
)

type configProviderChoice struct {
	label    string
	existing *host.ProviderSnapshot
	preset   *bootstrap.ProviderPreset
	custom   bool
	add      bool // 一级菜单的“新增 Provider…”入口，选中后进入新增目录
}

type modelConfigState struct {
	snapshot host.ModelConfigurationSnapshot
	step     configStep
	cursor   int
	message  string
	input    string
	saving   bool

	providerChoices []configProviderChoice // 一级菜单：编辑已有 Provider + “新增 Provider…”入口
	presetChoices   []configProviderChoice // 二级菜单：可新增的内置/自定义 Provider 目录
	provider        string
	providerType    string
	api             string
	baseURL         string
	models          []bootstrap.ModelConfig
	currentModel    string // 顶层正在用的模型（仅当编辑的正是当前 provider 时），用于删除保护
	existing        bool
	hasAPIKey       bool
	apiKeyOptional  bool
	apiKeyAction    host.APIKeyAction
	apiKey          string

	pendingModel string
	editModelIdx int
}

func newModelConfigState(rt *host.Host) *modelConfigState {
	state := &modelConfigState{snapshot: rt.ModelConfiguration(), editModelIdx: -1}
	state.buildProviderMenus()
	return state
}

// buildProviderMenus 拆成两级：一级菜单只列已配置的 Provider（编辑）+ 一个统一的
// “新增”入口，避免一进来就把整份内置 Provider 目录铺满屏幕；二级菜单（选“新增”后
// 展示）才是可新增的内置 Provider 目录 + 自定义代理。
func (s *modelConfigState) buildProviderMenus() {
	configured := make(map[string]bool, len(s.snapshot.Providers))
	for i := range s.snapshot.Providers {
		provider := s.snapshot.Providers[i]
		configured[provider.Name] = true
		copyProvider := provider
		s.providerChoices = append(s.providerChoices, configProviderChoice{
			label: provider.Name, existing: &copyProvider,
		})
	}
	s.providerChoices = append(s.providerChoices, configProviderChoice{
		label: "+ 新增 Provider…", add: true,
	})

	for _, presetValue := range bootstrap.ProviderPresets() {
		if configured[presetValue.Name] && !presetValue.NeedType {
			continue
		}
		preset := presetValue
		choice := configProviderChoice{label: preset.Label, preset: &preset}
		if preset.NeedType {
			choice.custom = true
		}
		s.presetChoices = append(s.presetChoices, choice)
	}
}

// applyProviderChoice 选中已有 Provider → 进入其详情 hub；选中新增 → 预填默认值后进 hub
// （自定义代理先问名称）。都不再直接跳进“改协议”的线性向导。
func (s *modelConfigState) applyProviderChoice(choice configProviderChoice) {
	s.cursor = 0
	s.message = ""
	if choice.existing != nil {
		p := choice.existing
		s.provider = p.Name
		s.providerType = p.Type
		s.api = p.API
		s.baseURL = p.BaseURL
		s.models = append([]bootstrap.ModelConfig(nil), p.Models...)
		s.existing = true
		s.hasAPIKey = p.HasAPIKey
		s.apiKeyOptional = !p.RequiresAPIKey
		s.apiKeyAction = host.APIKeyKeep
		if s.snapshot.DefaultProvider == s.provider {
			s.currentModel = s.snapshot.DefaultModel
		}
		s.step = configStepHub
		return
	}

	// 新增
	s.existing = false
	s.hasAPIKey = false
	s.apiKeyAction = host.APIKeyReplace
	s.apiKey = ""
	s.api = ""
	s.models = nil
	s.currentModel = "" // 新 provider 尚未被顶层选中
	if choice.custom {
		s.apiKeyOptional = true
		s.providerType = "openai" // 自定义默认 openai，可在 hub 改
		s.baseURL = ""
		s.step = configStepCustomName
		s.input = ""
		return
	}
	s.provider = choice.preset.Name
	s.providerType = choice.preset.Type // 为空表示内置 provider 协议由名称隐含
	s.baseURL = choice.preset.BaseURL
	s.models = append([]bootstrap.ModelConfig(nil), choice.preset.Models...)
	s.apiKeyOptional = choice.preset.APIKeyOptional
	s.step = configStepHub
}

// hubField 是 Provider 详情 hub 里的一个可调项。
type hubField struct {
	id    string // protocol / api / key / baseurl / models / save
	label string
	value string
}

// hubFields 按当前 Provider 组装详情项：协议仅在显式指定时出现，Endpoint 仅 OpenAI 协议出现。
func (s *modelConfigState) hubFields() []hubField {
	var fields []hubField
	if s.providerType != "" {
		fields = append(fields, hubField{"protocol", "协议", s.providerType})
	}
	if s.isOpenAIEndpoint() {
		api := s.api
		if api == "" {
			api = "chat"
		}
		fields = append(fields, hubField{"api", "Endpoint", api})
	}
	fields = append(fields, hubField{"key", "API Key", s.keyStatus()})
	base := s.baseURL
	if base == "" {
		base = "默认地址"
	}
	fields = append(fields, hubField{"baseurl", "Base URL", base})
	fields = append(fields, hubField{"models", "模型", fmt.Sprintf("%d 个", len(s.models))})
	fields = append(fields, hubField{"save", "保存并生效", ""})
	return fields
}

func (s *modelConfigState) isOpenAIEndpoint() bool {
	return s.providerType == "openai" || (s.providerType == "" && s.provider == "openai")
}

func (s *modelConfigState) keyStatus() string {
	switch s.apiKeyAction {
	case host.APIKeyClear:
		return "已清除"
	case host.APIKeyReplace:
		if s.apiKey != "" {
			return "已输入"
		}
	}
	if s.hasAPIKey {
		return "已设置"
	}
	return "未设置"
}

// enterHubField 进入某一详情项的子编辑器（协议/Endpoint/Key/BaseURL/模型），或触发保存。
func (s *modelConfigState) enterHubField(id string) (save bool) {
	s.message = ""
	switch id {
	case "protocol":
		s.step = configStepProtocol
		s.cursor = protocolIndex(s.providerType)
	case "api":
		s.step = configStepAPI
		s.cursor = 0
		if s.api == "responses" {
			s.cursor = 1
		}
	case "key":
		s.beginAPIKey()
	case "baseurl":
		s.step = configStepBaseURL
		s.input = s.baseURL
		s.cursor = 0
	case "models":
		s.step = configStepModels
		s.cursor = 0
	case "save":
		return true
	}
	return false
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

// escapeBack 返回 Esc 的上一级；第二个返回值 false 表示应关闭整个面板。
// 层级：Provider 列表 ⊃ 详情 hub ⊃ 字段编辑器；列表 ⊃ 新增目录 ⊃ 自定义命名；
// hub 的模型列表 ⊃ 模型名/窗口。
func (s *modelConfigState) escapeBack() (configStep, bool) {
	switch s.step {
	case configStepAddPicker, configStepHub:
		return configStepProvider, true
	case configStepCustomName:
		return configStepAddPicker, true
	case configStepProtocol, configStepAPI, configStepKeyAction, configStepKeyInput, configStepBaseURL, configStepModels:
		return configStepHub, true
	case configStepModelDetail, configStepModelName:
		return configStepModels, true
	case configStepModelWindow:
		if s.editModelIdx >= 0 { // 从模型详情进来改窗口 → 退回详情；新增流程 → 退回输名称
			return configStepModelDetail, true
		}
		return configStepModelName, true
	default: // configStepProvider
		return 0, false
	}
}

// modelDetailFields 组装单个模型详情 hub 的可调项：上下文窗口 / 删除。
// 不含“设为默认”——“当前用哪个”归 /model，/config 只管定义。
func (s *modelConfigState) modelDetailFields() []hubField {
	window := "自动"
	if w := s.models[s.editModelIdx].ContextWindow; w > 0 {
		window = formatContextWindow(w)
	}
	return []hubField{
		{"window", "上下文窗口", window},
		{"delete", "删除模型", ""},
	}
}

// deleteModel 删除第 idx 个模型；被默认指向或被其他角色引用时拒绝并给出提示，返回是否删成功。
func (s *modelConfigState) deleteModel(idx int) bool {
	if idx < 0 || idx >= len(s.models) {
		return false
	}
	model := s.models[idx]
	if model.Name == s.currentModel {
		s.message = "该模型正在使用中，请先用 /model 切换后再删除"
		return false
	}
	for _, ref := range s.snapshot.ReferencesFor(s.provider, model.Name) {
		if ref == "default" {
			continue // 顶层引用已由 currentModel 拦截，避免重复提示
		}
		s.message = fmt.Sprintf("模型仍被 %s 引用，请先在 /model 切换后再删除", ref)
		return false
	}
	s.models = append(s.models[:idx], s.models[idx+1:]...)
	s.cursor = idx
	if s.cursor >= len(s.models) && s.cursor > 0 {
		s.cursor--
	}
	s.message = ""
	return true
}

func (s *modelConfigState) draft() host.ModelConfigurationDraft {
	return host.ModelConfigurationDraft{
		Provider: s.provider, Type: s.providerType, API: s.api, BaseURL: s.baseURL,
		Models:       append([]bootstrap.ModelConfig(nil), s.models...),
		APIKeyAction: s.apiKeyAction, APIKey: s.apiKey,
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
		if target, ok := state.escapeBack(); ok {
			state.step = target
			state.cursor = 0
			state.message = ""
			return m, nil
		}
		m.modelConfig = nil
		return m, m.textarea.Focus()
	}
	if state.saving {
		return m, nil
	}

	switch state.step {
	case configStepProvider:
		moveConfigCursor(state, msg, len(state.providerChoices))
		if msg.Type == tea.KeyEnter && state.cursor >= 0 && state.cursor < len(state.providerChoices) {
			choice := state.providerChoices[state.cursor]
			if choice.add {
				state.step = configStepAddPicker
				state.cursor = 0
				state.message = ""
			} else {
				state.applyProviderChoice(choice)
			}
		}
	case configStepAddPicker:
		moveConfigCursor(state, msg, len(state.presetChoices))
		if msg.Type == tea.KeyEnter && state.cursor >= 0 && state.cursor < len(state.presetChoices) {
			state.applyProviderChoice(state.presetChoices[state.cursor])
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
			state.step = configStepHub
			state.cursor = 0
			state.message = ""
		}
	case configStepHub:
		fields := state.hubFields()
		moveConfigCursor(state, msg, len(fields))
		if msg.Type == tea.KeyEnter && state.cursor >= 0 && state.cursor < len(fields) {
			if state.enterHubField(fields[state.cursor].id) {
				if len(state.models) == 0 {
					state.message = "请至少添加一个模型"
					break
				}
				state.saving = true
				state.message = "正在校验并保存配置..."
				return m, saveModelConfiguration(m.runtime, state.draft())
			}
		}
	case configStepProtocol:
		moveConfigCursor(state, msg, len(configProtocols))
		if msg.Type == tea.KeyEnter {
			state.providerType = configProtocols[state.cursor]
			if state.providerType != "openai" {
				state.api = ""
			}
			state.step = configStepHub
			state.cursor = 0
		}
	case configStepAPI:
		moveConfigCursor(state, msg, len(configAPIs))
		if msg.Type == tea.KeyEnter {
			state.api = configAPIs[state.cursor]
			state.step = configStepHub
			state.cursor = 0
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
				state.step = configStepHub
				state.cursor = 0
			}
		}
	case configStepKeyInput:
		if handleConfigInput(&state.input, msg) && msg.Type == tea.KeyEnter {
			state.apiKey = strings.TrimSpace(state.input)
			if state.apiKey == "" && !state.apiKeyOptional {
				state.message = "该 Provider 必须配置 API Key"
				return m, nil
			}
			// 输入了才算替换；留空（可选）则维持原状。
			if state.apiKey == "" {
				state.apiKeyAction = host.APIKeyKeep
			} else {
				state.apiKeyAction = host.APIKeyReplace
			}
			state.step = configStepHub
			state.cursor = 0
			state.message = ""
		}
	case configStepBaseURL:
		if handleConfigInput(&state.input, msg) && msg.Type == tea.KeyEnter {
			state.baseURL = strings.TrimSpace(state.input)
			state.step = configStepHub
			state.cursor = 0
			state.message = ""
		}
	case configStepModels:
		// 末项恒为“+ 新增模型…”入口；选中已有模型进入其详情，全程只用 ↑↓/Enter。
		moveConfigCursor(state, msg, len(state.models)+1)
		if msg.Type == tea.KeyEnter {
			if state.cursor == len(state.models) {
				state.step = configStepModelName
				state.input = ""
				state.editModelIdx = -1
				state.message = ""
			} else if state.cursor >= 0 && state.cursor < len(state.models) {
				state.editModelIdx = state.cursor
				state.step = configStepModelDetail
				state.cursor = 0
				state.message = ""
			}
		}
	case configStepModelDetail:
		if state.editModelIdx < 0 || state.editModelIdx >= len(state.models) {
			state.step = configStepModels
			state.cursor = 0
			break
		}
		fields := state.modelDetailFields()
		moveConfigCursor(state, msg, len(fields))
		if msg.Type == tea.KeyEnter && state.cursor >= 0 && state.cursor < len(fields) {
			switch fields[state.cursor].id {
			case "window":
				model := state.models[state.editModelIdx]
				state.pendingModel = model.Name
				if model.ContextWindow > 0 {
					state.input = strconv.Itoa(model.ContextWindow)
				} else {
					state.input = ""
				}
				state.step = configStepModelWindow
				state.message = ""
			case "delete":
				if state.deleteModel(state.editModelIdx) {
					state.step = configStepModels
					state.editModelIdx = -1
				}
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
				// 改已有模型窗口 → 退回其详情，可继续调整（editModelIdx 保留）。
				state.models[state.editModelIdx].ContextWindow = window
				state.step = configStepModelDetail
				state.cursor = 1
			} else {
				// 新增流程收尾 → 落入列表末尾并选中。
				state.models = append(state.models, bootstrap.ModelConfig{Name: state.pendingModel, ContextWindow: window})
				state.cursor = len(state.models) - 1
				state.step = configStepModels
			}
			state.pendingModel = ""
			state.message = ""
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

func renderModelConfigModal(width int, state *modelConfigState) string {
	if state == nil {
		return ""
	}
	// 与 /model 同款：宽度按内容自适应、夹在 [60,76] 间，高度=内容高度（浮在输入框上方，不撑满屏）。
	boxW := min(max(60, width*3/5), 76, width-4)
	contentW := paddedModalContentWidth(boxW)
	var lines []string
	title := "/config 配置模型"
	hint := "↑↓ 选择 · Enter 确认 · Esc 取消"

	switch state.step {
	case configStepProvider:
		lines = append(lines, configHeading("选择要编辑的 Provider，或新增一个"))
		lines = append(lines, renderConfigChoices(labelsForProviderChoices(state.providerChoices), state.cursor, contentW, 12)...)
	case configStepAddPicker:
		lines = append(lines, configHeading("选择要新增的 Provider"))
		lines = append(lines, renderConfigChoices(labelsForProviderChoices(state.presetChoices), state.cursor, contentW, 12)...)
	case configStepCustomName:
		lines = append(lines, configHeading("自定义 Provider 名称"), renderConfigInput(state.input, false, contentW))
		hint = configInputHint
	case configStepHub:
		heading := state.provider
		if !state.existing {
			heading += "（新增）"
		}
		lines = append(lines, configHeading(heading))
		lines = append(lines, renderFieldList(state.hubFields(), state.cursor, contentW)...)
		hint = "↑↓ 选择 · Enter 进入/保存 · Esc 返回"
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
		total := len(state.models) + 1 // 末项为“+ 新增模型…”入口
		start, end := configWindow(total, state.cursor, 10)
		for i := start; i < end; i++ {
			prefix := "  "
			selected := i == state.cursor
			if selected {
				prefix = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("› ")
			}
			if i == len(state.models) {
				style := lipgloss.NewStyle().Foreground(bodyTextColor)
				if selected {
					style = style.Foreground(colorAccent).Bold(true)
				}
				lines = append(lines, prefix+style.Render("+ 新增模型…"))
				continue
			}
			model := state.models[i]
			window := "自动"
			if model.ContextWindow > 0 {
				window = formatContextWindow(model.ContextWindow)
			}
			line := fmt.Sprintf("%s%-38s  上下文 %s", prefix, truncateWidth(model.Name, 36), window)
			lines = append(lines, truncateWidth(line, contentW))
		}
		hint = "↑↓ 选择 · Enter 进入 · Esc 返回"
	case configStepModelDetail:
		if state.editModelIdx >= 0 && state.editModelIdx < len(state.models) {
			lines = append(lines, configHeading(state.models[state.editModelIdx].Name))
			lines = append(lines, renderFieldList(state.modelDetailFields(), state.cursor, contentW)...)
		}
		hint = "↑↓ 选择 · Enter 确认 · Esc 返回"
	case configStepModelName:
		lines = append(lines, configHeading("新增模型名称"), renderConfigInput(state.input, false, contentW))
		hint = configInputHint
	case configStepModelWindow:
		lines = append(lines, configHeading("模型 "+state.pendingModel+" 的上下文窗口"))
		lines = append(lines, lipgloss.NewStyle().Foreground(colorDim).Render("留空或 0 = 自动解析；支持 128K / 1M"))
		lines = append(lines, renderConfigInput(state.input, false, contentW))
		hint = configInputHint
	}

	if state.message != "" {
		color := colorError
		if state.saving || strings.HasPrefix(state.message, "已选择") {
			color = colorAccent
		}
		lines = append(lines, "", lipgloss.NewStyle().Foreground(color).Render(truncateWidth(state.message, contentW)))
	}
	return renderPaddedModalFrame(boxW, len(lines)+2, title, hint, lines)
}

const configInputHint = "输入 · Enter 确认 · Ctrl+U 清空 · Esc 取消"

func configHeading(text string) string {
	return lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(text)
}

// renderFieldList 渲染详情 hub 的可调项列表（Provider hub 与模型详情共用）：
// 有值的项左对齐 label、右侧灰显当前值；纯动作项（value 为空）只显示 label。
func renderFieldList(fields []hubField, cursor, contentW int) []string {
	lines := make([]string, 0, len(fields))
	for i, f := range fields {
		marker := "  "
		labelStyle := lipgloss.NewStyle().Foreground(bodyTextColor)
		if i == cursor {
			marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("› ")
			labelStyle = labelStyle.Foreground(colorAccent).Bold(true)
		}
		var line string
		if f.value == "" {
			line = marker + labelStyle.Render(f.label)
		} else {
			pad := max(1, 10-lipgloss.Width(f.label))
			line = marker + labelStyle.Render(f.label) + strings.Repeat(" ", pad) +
				lipgloss.NewStyle().Foreground(colorDim).Render(f.value)
		}
		lines = append(lines, truncateWidth(line, contentW))
	}
	return lines
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

// renderConfigInput 渲染单行下划线输入字段（贴近 /model 的单行字段观感，
// 且避免在带框浮层里再套一层边框导致多行错位）。
func renderConfigInput(value string, secret bool, width int) string {
	display := value
	if secret {
		display = strings.Repeat("•", utf8.RuneCountInString(value))
	}
	display += "▌"
	shown := truncateWidth(display, max(8, width-4))
	if w := lipgloss.Width(shown); w < 24 { // 补足最小宽度，空字段也像个输入框
		shown += strings.Repeat(" ", 24-w)
	}
	field := lipgloss.NewStyle().Foreground(bodyTextColor).Underline(true).Render(shown)
	return lipgloss.NewStyle().Foreground(colorAccent).Render("› ") + field
}
