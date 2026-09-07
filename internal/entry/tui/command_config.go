package tui

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

type configStep int

const (
	configStepProvider configStep = iota
	configStepAddPicker
	configStepCustomName
	configStepHub // Provider 详情：列出各项当前值，挑一项进子编辑器，保存也在此
	configStepProtocol
	configStepAPI
	configStepModels
)

const (
	configModelNameField   = "model_name"
	configModelWindowField = "model_window"
)

type configProviderChoice struct {
	label    string
	existing *host.ProviderSnapshot
	preset   *bootstrap.ProviderPreset
	custom   bool
	add      bool // 一级菜单的“新增 Provider…”入口，选中后进入新增目录
}

type modelConfigBaseline struct {
	providerType string
	api          string
	baseURL      string
	models       []bootstrap.ModelConfig
}

type modelConfigState struct {
	snapshot   host.ModelConfigurationSnapshot
	step       configStep
	cursor     int
	message    string
	input      textinput.Model
	saving     bool
	testing    bool
	testCancel context.CancelFunc

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
	apiKeyHint      string
	apiKeyOptional  bool
	apiKeyAction    host.APIKeyAction
	apiKey          string
	editingField    string
	baseline        *modelConfigBaseline

	modelOrigins []string // 与 models 对齐；已有模型保留原名，新增模型为空，用于生成显式重命名
	modelColumn  int      // 0=模型 ID，1=上下文窗口
	editModelIdx int
	addingModel  bool
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
	s.editingField = ""
	s.editModelIdx = -1
	s.modelColumn = 0
	s.addingModel = false
	if choice.existing != nil {
		p := choice.existing
		s.provider = p.Name
		s.providerType = p.Type
		s.api = p.API
		s.baseURL = p.BaseURL
		s.models = append([]bootstrap.ModelConfig(nil), p.Models...)
		s.existing = true
		s.hasAPIKey = p.HasAPIKey
		s.apiKeyHint = p.APIKeyHint
		s.apiKeyOptional = !p.RequiresAPIKey
		s.apiKeyAction = host.APIKeyKeep
		s.apiKey = ""
		s.modelOrigins = make([]string, len(s.models))
		for i, model := range s.models {
			s.modelOrigins[i] = model.Name
		}
		s.currentModel = ""
		if s.snapshot.DefaultProvider == s.provider {
			s.currentModel = s.snapshot.DefaultModel
		}
		s.captureBaseline()
		s.step = configStepHub
		return
	}

	// 新增
	s.existing = false
	s.hasAPIKey = false
	s.apiKeyHint = ""
	s.apiKeyAction = host.APIKeyReplace
	s.apiKey = ""
	s.baseline = nil
	s.api = ""
	s.models = nil
	s.modelOrigins = nil
	s.currentModel = "" // 新 provider 尚未被顶层选中
	if choice.custom {
		s.apiKeyOptional = true
		s.providerType = "openai" // 自定义默认 openai，可在 hub 改
		s.baseURL = ""
		s.step = configStepCustomName
		s.startTextInput("", "Provider 名称", false)
		return
	}
	s.provider = choice.preset.Name
	s.providerType = choice.preset.Type // 为空表示内置 provider 协议由名称隐含
	s.baseURL = choice.preset.BaseURL
	s.models = append([]bootstrap.ModelConfig(nil), choice.preset.Models...)
	s.apiKeyOptional = choice.preset.APIKeyOptional
	s.step = configStepHub
}

func (s *modelConfigState) captureBaseline() {
	s.baseline = &modelConfigBaseline{
		providerType: s.providerType,
		api:          s.api,
		baseURL:      s.baseURL,
		models:       append([]bootstrap.ModelConfig(nil), s.models...),
	}
}

func (s *modelConfigState) isDirty() bool {
	if !s.existing || s.baseline == nil {
		return true
	}
	if s.apiKeyAction != host.APIKeyKeep {
		return true
	}
	baseURL := s.baseURL
	if s.editingField == "baseurl" {
		baseURL = strings.TrimSpace(s.input.Value())
	}
	if s.editingField == "key" && strings.TrimSpace(s.input.Value()) != "" {
		return true
	}
	return s.providerType != s.baseline.providerType ||
		s.api != s.baseline.api ||
		baseURL != s.baseline.baseURL ||
		!slices.Equal(s.models, s.baseline.models)
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
	testModel := s.testModelName()
	if testModel == "" {
		testModel = "请先添加模型"
	}
	fields = append(fields, hubField{"test", "测试连接", testModel})
	fields = append(fields, hubField{"save", "保存配置", ""})
	return fields
}

func (s *modelConfigState) testModelName() string {
	for _, model := range s.models {
		if model.Name == s.currentModel {
			return model.Name
		}
	}
	if len(s.models) > 0 {
		return s.models[0].Name
	}
	return ""
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
			return host.MaskAPIKey(s.apiKey)
		}
	}
	if s.apiKeyHint != "" {
		return s.apiKeyHint
	}
	return "未设置"
}

// enterHubField 进入选中项；Key 与 Base URL 直接在 hub 当前行编辑。
func (s *modelConfigState) enterHubField(id string) (save bool, cmd tea.Cmd) {
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
		return false, s.beginInlineEdit("key")
	case "baseurl":
		return false, s.beginInlineEdit("baseurl")
	case "models":
		s.ensureModelOrigins()
		s.step = configStepModels
		s.cursor = 0
		s.modelColumn = 0
	case "test":
		return false, nil
	case "save":
		return true, nil
	}
	return false, nil
}

func (s *modelConfigState) beginInlineEdit(field string) tea.Cmd {
	s.editingField = field

	switch field {
	case "key":
		placeholder := "输入 API Key"
		if s.hasEffectiveAPIKey() {
			placeholder = "输入新 Key，留空保留"
		}
		return s.startTextInput("", placeholder, true)
	case "baseurl":
		return s.startTextInput(s.baseURL, "留空使用默认地址", false)
	}
	return nil
}

func (s *modelConfigState) startTextInput(value, placeholder string, secret bool) tea.Cmd {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = placeholder
	input.CharLimit = 0
	input.Width = 36
	input.TextStyle = lipgloss.NewStyle().Foreground(bodyTextColor).Underline(true)
	input.PlaceholderStyle = lipgloss.NewStyle().Foreground(colorDim).Underline(true)
	input.Cursor.Style = lipgloss.NewStyle().Foreground(colorAccent)
	if secret {
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '•'
	}
	input.SetValue(value)
	input.CursorEnd()
	s.input = input
	return s.input.Focus()
}

func (s *modelConfigState) hasEffectiveAPIKey() bool {
	switch s.apiKeyAction {
	case host.APIKeyClear:
		return false
	case host.APIKeyReplace:
		return strings.TrimSpace(s.apiKey) != ""
	default:
		return s.hasAPIKey
	}
}

func (s *modelConfigState) finishInlineEdit() bool {
	value := strings.TrimSpace(s.input.Value())
	switch s.editingField {
	case "key":
		if value == "" {
			if !s.apiKeyOptional && !s.hasEffectiveAPIKey() {
				s.message = "该 Provider 必须配置 API Key"
				return false
			}
		} else {
			s.apiKey = value
			s.apiKeyAction = host.APIKeyReplace
		}
	case "baseurl":
		s.baseURL = value
	}
	s.input.Blur()
	s.editingField = ""
	s.message = ""
	return true
}

// escapeBack 返回 Esc 的上一级；第二个返回值 false 表示应关闭整个面板。
// 层级：Provider 列表 ⊃ 详情 hub ⊃ 字段编辑器；列表 ⊃ 新增目录 ⊃ 自定义命名。
// 模型名和窗口直接在模型列表内编辑，不再增加详情层级。
func (s *modelConfigState) escapeBack() (configStep, bool) {
	switch s.step {
	case configStepAddPicker, configStepHub:
		return configStepProvider, true
	case configStepCustomName:
		return configStepAddPicker, true
	case configStepProtocol, configStepAPI, configStepModels:
		return configStepHub, true
	default: // configStepProvider
		return 0, false
	}
}

func (s *modelConfigState) ensureModelOrigins() {
	if len(s.modelOrigins) == len(s.models) {
		return
	}
	s.modelOrigins = make([]string, len(s.models))
	for i, model := range s.models {
		s.modelOrigins[i] = model.Name
	}
}

func (s *modelConfigState) beginModelEdit(idx, column int) tea.Cmd {
	if idx < 0 || idx >= len(s.models) {
		return nil
	}
	s.editModelIdx = idx
	s.modelColumn = column
	s.message = ""
	if column == 0 {
		s.editingField = configModelNameField
		return s.startTextInput(s.models[idx].Name, "模型 ID", false)
	}
	s.editingField = configModelWindowField
	value := ""
	if window := s.models[idx].ContextWindow; window > 0 {
		value = strconv.Itoa(window)
	}
	return s.startTextInput(value, "auto / 128K / 1M", false)
}

// finishModelEdit 只提交当前单元格。新增模型提交名称后顺手进入窗口列，
// 已有模型重命名则保留其原始身份，保存时由 Host 原子迁移所有引用。
func (s *modelConfigState) finishModelEdit() (tea.Cmd, bool) {
	idx := s.editModelIdx
	if idx < 0 || idx >= len(s.models) {
		return nil, false
	}
	switch s.editingField {
	case configModelNameField:
		name := strings.TrimSpace(s.input.Value())
		if name == "" {
			s.message = "模型名称不能为空"
			return nil, false
		}
		for i, model := range s.models {
			if i != idx && model.Name == name {
				s.message = "模型已存在"
				return nil, false
			}
		}
		s.models[idx].Name = name
		if s.addingModel {
			s.modelColumn = 1
			s.editingField = configModelWindowField
			s.message = ""
			return s.startTextInput("", "auto / 128K / 1M", false), true
		}
		s.input.Blur()
		s.editingField = ""
		s.message = ""
		origin := s.modelOrigins[idx]
		if origin != "" && origin != name {
			if refs := s.snapshot.ReferencesFor(s.provider, origin); len(refs) > 0 {
				s.message = "保存时将同步更新引用：" + strings.Join(refs, "、")
			}
		}
		return nil, true
	case configModelWindowField:
		window, err := parseContextWindowInput(s.input.Value())
		if err != nil {
			s.message = err.Error()
			return nil, false
		}
		s.models[idx].ContextWindow = window
		s.input.Blur()
		s.editingField = ""
		s.addingModel = false
		s.message = ""
		return nil, true
	}
	return nil, false
}

func (s *modelConfigState) cancelModelEdit() {
	// 新增行尚未提交名称时没有有效数据，Esc 直接撤掉这条临时行；名称已经提交、
	// 正在编辑窗口时则保留“自动”窗口，用户之后仍可在同一页继续改。
	if s.addingModel && s.editingField == configModelNameField &&
		s.editModelIdx >= 0 && s.editModelIdx < len(s.models) {
		idx := s.editModelIdx
		s.models = append(s.models[:idx], s.models[idx+1:]...)
		s.modelOrigins = append(s.modelOrigins[:idx], s.modelOrigins[idx+1:]...)
		s.cursor = len(s.models)
	}
	s.input.Blur()
	s.editingField = ""
	s.editModelIdx = -1
	s.addingModel = false
	s.message = ""
}

// deleteModel 删除第 idx 个模型；被默认指向或被其他角色引用时拒绝并给出提示，返回是否删成功。
func (s *modelConfigState) deleteModel(idx int) bool {
	if idx < 0 || idx >= len(s.models) {
		return false
	}
	s.ensureModelOrigins()
	model := s.models[idx]
	identity := s.modelOrigins[idx]
	if identity == "" {
		identity = model.Name
	}
	if identity == s.currentModel {
		s.message = "该模型正在使用中，请先用 /model 切换后再删除"
		return false
	}
	for _, ref := range s.snapshot.ReferencesFor(s.provider, identity) {
		if ref == "default" {
			continue // 顶层引用已由 currentModel 拦截，避免重复提示
		}
		s.message = fmt.Sprintf("模型仍被 %s 引用，请先在 /model 切换后再删除", ref)
		return false
	}
	s.models = append(s.models[:idx], s.models[idx+1:]...)
	s.modelOrigins = append(s.modelOrigins[:idx], s.modelOrigins[idx+1:]...)
	s.cursor = idx
	if s.cursor > len(s.models) {
		s.cursor = len(s.models)
	}
	s.message = ""
	return true
}

func (s *modelConfigState) draft() host.ModelConfigurationDraft {
	s.ensureModelOrigins()
	renames := make([]host.ModelRename, 0)
	for i, model := range s.models {
		if origin := s.modelOrigins[i]; origin != "" && origin != model.Name {
			renames = append(renames, host.ModelRename{From: origin, To: model.Name})
		}
	}
	return host.ModelConfigurationDraft{
		Provider: s.provider, Type: s.providerType, API: s.api, BaseURL: s.baseURL,
		Models:       append([]bootstrap.ModelConfig(nil), s.models...),
		Renames:      renames,
		APIKeyAction: s.apiKeyAction, APIKey: s.apiKey,
	}
}

type modelConfigSavedMsg struct{ err error }

type modelConfigConnectionMsg struct {
	model string
	err   error
}

func saveModelConfiguration(rt *host.Host, draft host.ModelConfigurationDraft) tea.Cmd {
	return func() tea.Msg { return modelConfigSavedMsg{err: rt.ConfigureModels(draft)} }
}

func testModelConnection(ctx context.Context, rt *host.Host, draft host.ModelConfigurationDraft, model string) tea.Cmd {
	return func() tea.Msg {
		return modelConfigConnectionMsg{model: model, err: rt.TestModelConnection(ctx, draft, model)}
	}
}

func (m Model) handleModelConfigKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := m.modelConfig
	if state == nil {
		return m, nil
	}
	if msg.Type == tea.KeyEsc {
		if state.testing {
			if state.testCancel != nil {
				state.testCancel()
			}
			state.message = "正在取消连接测试..."
			return m, nil
		}
		if state.editingField != "" && (state.step == configStepHub || state.step == configStepModels) {
			if state.step == configStepModels {
				state.cancelModelEdit()
			} else {
				state.input.Blur()
				state.editingField = ""
				state.message = ""
			}
			return m, nil
		}
		if target, ok := state.escapeBack(); ok {
			state.input.Blur()
			state.step = target
			state.cursor = 0
			state.message = ""
			return m, nil
		}
		m.modelConfig = nil
		return m, m.textarea.Focus()
	}
	if state.saving || state.testing {
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
		if msg.Type == tea.KeyEnter {
			name := strings.TrimSpace(state.input.Value())
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
			return m, nil
		}
		var cmd tea.Cmd
		state.input, cmd = state.input.Update(msg)
		return m, cmd
	case configStepHub:
		fields := state.hubFields()
		if state.editingField != "" {
			if msg.Type == tea.KeyEnter {
				state.finishInlineEdit()
				return m, nil
			}
			var cmd tea.Cmd
			state.input, cmd = state.input.Update(msg)
			return m, cmd
		}
		moveConfigCursor(state, msg, len(fields))
		if msg.Type == tea.KeyDelete && state.cursor >= 0 && state.cursor < len(fields) && fields[state.cursor].id == "key" {
			if !state.apiKeyOptional {
				state.message = "该 Provider 必须配置 API Key，不能清除"
				break
			}
			state.apiKeyAction = host.APIKeyClear
			state.apiKey = ""
			state.message = "API Key 已标记清除，保存配置后生效"
			break
		}
		if msg.Type == tea.KeyEnter && state.cursor >= 0 && state.cursor < len(fields) {
			fieldID := fields[state.cursor].id
			if fieldID == "test" {
				model := state.testModelName()
				if model == "" {
					state.message = "请至少添加一个模型后再测试连接"
					break
				}
				if !state.apiKeyOptional && !state.hasEffectiveAPIKey() {
					state.message = "该 Provider 必须配置 API Key"
					break
				}
				state.testing = true
				state.message = fmt.Sprintf("正在测试连接：%s/%s...", state.provider, model)
				ctx, cancel := context.WithCancel(context.Background())
				state.testCancel = cancel
				return m, testModelConnection(ctx, m.runtime, state.draft(), model)
			}
			save, cmd := state.enterHubField(fieldID)
			if save {
				if len(state.models) == 0 {
					state.message = "请至少添加一个模型"
					break
				}
				if !state.apiKeyOptional && !state.hasEffectiveAPIKey() {
					state.message = "该 Provider 必须配置 API Key"
					break
				}
				state.saving = true
				state.message = "正在校验并保存配置..."
				return m, saveModelConfiguration(m.runtime, state.draft())
			}
			return m, cmd
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
	case configStepModels:
		state.ensureModelOrigins()
		if state.editingField != "" {
			if msg.Type == tea.KeyEnter {
				cmd, _ := state.finishModelEdit()
				return m, cmd
			}
			var cmd tea.Cmd
			state.input, cmd = state.input.Update(msg)
			return m, cmd
		}
		moveConfigCursor(state, msg, len(state.models)+1)
		if state.cursor < len(state.models) {
			switch msg.Type {
			case tea.KeyLeft:
				state.modelColumn = 0
			case tea.KeyRight:
				state.modelColumn = 1
			case tea.KeyDelete:
				state.deleteModel(state.cursor)
				return m, nil
			}
		}
		if msg.Type == tea.KeyEnter {
			if state.cursor == len(state.models) {
				state.models = append(state.models, bootstrap.ModelConfig{})
				state.modelOrigins = append(state.modelOrigins, "")
				state.cursor = len(state.models) - 1
				state.addingModel = true
				state.message = ""
				return m, state.beginModelEdit(state.cursor, 0)
			} else if state.cursor >= 0 && state.cursor < len(state.models) {
				return m, state.beginModelEdit(state.cursor, state.modelColumn)
			}
		}
	}
	return m, nil
}

var configProtocols = []string{"openai", "anthropic", "gemini"}
var configAPIs = []string{"chat", "responses"}

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
		lines = append(lines, configHeading("自定义 Provider 名称"), renderConfigTextInput(&state.input, contentW))
		hint = configInputHint
	case configStepHub:
		heading := state.provider
		if !state.existing {
			heading += "（新增）"
		}
		lines = append(lines, configHeading(heading))
		lines = append(lines, renderProviderHubFields(state, contentW)...)
		if state.snapshot.ConfigPath != "" {
			advanced := "高级配置（extra / extra_body / stream_idle_timeout）：" + state.snapshot.ConfigPath
			lines = append(lines, "")
			lines = appendWrappedConfigText(lines, advanced, contentW, lipgloss.NewStyle().Foreground(colorDim))
		}
		if state.editingField != "" {
			hint = "输入 · Enter 确认 · Esc 取消"
		} else {
			hint = "↑↓ 选择 · Enter 编辑/进入 · Esc 返回"
			fields := state.hubFields()
			if state.apiKeyOptional && state.cursor >= 0 && state.cursor < len(fields) && fields[state.cursor].id == "key" {
				hint += " · Delete 清除"
			}
			if state.cursor >= 0 && state.cursor < len(fields) && fields[state.cursor].id == "test" {
				lines = append(lines, lipgloss.NewStyle().Foreground(colorDim).Render("测试会发送最小请求，可能产生少量 API 用量"))
			}
		}
	case configStepProtocol:
		lines = append(lines, configHeading("API 协议类型"))
		lines = append(lines, renderConfigChoices(configProtocols, state.cursor, contentW, 8)...)
	case configStepAPI:
		lines = append(lines, configHeading("OpenAI Endpoint"))
		lines = append(lines, renderConfigChoices([]string{"chat · /v1/chat/completions", "responses · /v1/responses"}, state.cursor, contentW, 8)...)
	case configStepModels:
		lines = append(lines, configHeading("管理模型列表"))
		lines = append(lines, renderModelConfigRows(state, contentW)...)
		if state.editingField != "" {
			hint = "输入 · Enter 确认 · Esc 取消"
		} else {
			hint = "↑↓ 行 · ←→ 字段 · Enter 编辑 · Delete 删除 · Esc 返回"
		}
	}

	if state.message != "" {
		color := colorError
		if strings.HasPrefix(state.message, "连接测试成功") {
			color = colorSuccess
		} else if state.saving || state.testing || strings.HasPrefix(state.message, "已选择") ||
			strings.HasPrefix(state.message, "API Key 已") || strings.HasPrefix(state.message, "连接测试已取消") {
			color = colorAccent
		} else if strings.HasPrefix(state.message, "保存时将同步更新引用") {
			color = colorAccent
		}
		lines = append(lines, "")
		lines = appendWrappedConfigText(lines, state.message, contentW, lipgloss.NewStyle().Foreground(color))
	}
	return renderPaddedModalFrame(boxW, len(lines)+2, title, hint, lines)
}

const configInputHint = "输入 · Enter 确认 · Ctrl+U 清空 · Esc 取消"

func configHeading(text string) string {
	return lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(text)
}

func appendWrappedConfigText(lines []string, text string, width int, style lipgloss.Style) []string {
	for _, line := range strings.Split(wrapText(text, width), "\n") {
		lines = append(lines, style.Render(line))
	}
	return lines
}

func renderModelConfigRows(state *modelConfigState, contentW int) []string {
	state.ensureModelOrigins()
	contextW := 14
	refsW := 18
	nameW := contentW - 2 - contextW - refsW - 4
	if nameW < 20 {
		refsW = 0
		nameW = max(12, contentW-2-contextW-2)
	}

	header := "  " + padConfigCell("模型 ID", nameW) + "  " + padConfigCell("上下文窗口", contextW)
	if refsW > 0 {
		header += "  " + padConfigCell("引用", refsW)
	}
	lines := []string{lipgloss.NewStyle().Foreground(colorDim).Render(header)}

	total := len(state.models) + 1
	start, end := configWindow(total, state.cursor, 10)
	for i := start; i < end; i++ {
		selected := i == state.cursor
		marker := "  "
		if selected {
			marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("› ")
		}
		if i == len(state.models) {
			style := lipgloss.NewStyle().Foreground(bodyTextColor)
			if selected {
				style = style.Foreground(colorAccent).Bold(true)
			}
			lines = append(lines, marker+style.Render("+ 新增模型…"))
			continue
		}

		model := state.models[i]
		name := padConfigCell(model.Name, nameW)
		window := "自动"
		if model.ContextWindow > 0 {
			window = formatContextWindow(model.ContextWindow)
		}
		window = padConfigCell(window, contextW)

		nameCell := lipgloss.NewStyle().Foreground(bodyTextColor).Render(name)
		windowCell := lipgloss.NewStyle().Foreground(colorDim).Render(window)
		if selected && state.modelColumn == 0 {
			nameCell = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(name)
		}
		if selected && state.modelColumn == 1 {
			windowCell = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(window)
		}
		if state.editModelIdx == i && state.editingField == configModelNameField {
			nameCell = renderConfigInputCell(&state.input, nameW)
		}
		if state.editModelIdx == i && state.editingField == configModelWindowField {
			windowCell = renderConfigInputCell(&state.input, contextW)
		}

		line := marker + nameCell + "  " + windowCell
		if refsW > 0 {
			identity := state.modelOrigins[i]
			if identity == "" {
				identity = model.Name
			}
			refs := strings.Join(state.snapshot.ReferencesFor(state.provider, identity), "、")
			line += "  " + lipgloss.NewStyle().Foreground(colorDim).Render(padConfigCell(refs, refsW))
		}
		lines = append(lines, truncateStyledWidth(line, contentW))
	}
	return lines
}

func padConfigCell(value string, width int) string {
	value = truncateWidth(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

// renderConfigInputCell 在固定宽度表格单元格里渲染 textinput。
// textinput 的 Width 不包含末尾光标列，因此预留一列；直接处理其 ANSI 输出，
// 不再套 lipgloss.Width，避免嵌套样式被误判为可换行文本。
func renderConfigInputCell(input *textinput.Model, width int) string {
	input.Width = max(1, width-1)
	view := truncateStyledWidth(input.View(), width)
	return view + strings.Repeat(" ", max(0, width-lipgloss.Width(view)))
}

// renderProviderHubFields 在 Provider 详情原位置渲染 Key/Base URL 输入框，
// textinput 自带光标移动和横向视口，长 URL 不会再截掉正在编辑的尾部。
func renderProviderHubFields(state *modelConfigState, contentW int) []string {
	fields := state.hubFields()
	lines := make([]string, 0, len(fields))
	dirty := state.isDirty()
	for i, f := range fields {
		marker := "  "
		labelStyle := lipgloss.NewStyle().Foreground(bodyTextColor)
		primarySave := f.id == "save" && dirty
		if primarySave {
			labelStyle = labelStyle.Foreground(colorSuccess)
		}
		if i == state.cursor {
			selectedColor := colorAccent
			if primarySave {
				selectedColor = colorSuccess
			}
			marker = lipgloss.NewStyle().Foreground(selectedColor).Bold(true).Render("› ")
			labelStyle = labelStyle.Foreground(selectedColor).Bold(true)
		}
		pad := max(1, 10-lipgloss.Width(f.label))
		if state.editingField == f.id {
			state.input.Width = max(8, contentW-2-lipgloss.Width(f.label)-pad)
			line := marker + labelStyle.Render(f.label) + strings.Repeat(" ", pad) + state.input.View()
			lines = append(lines, truncateStyledWidth(line, contentW))
			continue
		}
		var line string
		if f.value == "" {
			line = marker + labelStyle.Render(f.label)
		} else {
			line = marker + labelStyle.Render(f.label) + strings.Repeat(" ", pad) +
				lipgloss.NewStyle().Foreground(colorDim).Render(f.value)
		}
		lines = append(lines, truncateStyledWidth(line, contentW))
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

func renderConfigTextInput(input *textinput.Model, width int) string {
	input.Width = max(8, width-4)
	return lipgloss.NewStyle().Foreground(colorAccent).Render("› ") + input.View()
}
