package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/voocel/ainovel-cli/internal/app/binding"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// 模型设置（workbench §4）：未配置模型时是启动落点，配好即进首页；从首页进入时先看总览——
// 默认与各角色用的模型、思考强度、各连接和它们的模型，切换、分配、编辑、添加、删除都在这一页。
// 添加连接走「服务商 → 连接 → 模型」三步：填好密钥回车，就向服务商列出这组凭据能用的模型
// （顺带验证密钥），挑一个或直接输入模型 ID（先试一句确认能用）。绑定成功才落盘。

type wizardStep int

const (
	wizardOverview   wizardStep = iota // 总览：当前模型、思考强度、连接与模型
	wizardProviders                    // 选择服务商
	wizardConnection                   // 连接：密钥、地址、名称
	wizardModels                       // 选择模型：服务商列出的模型，可筛选或直接输入
	wizardRole                         // 给默认或某个角色换模型：从已保存的模型里选
)

type wizardMode int

const (
	addConnection  wizardMode = iota // 新连接：服务商 → 连接 → 模型
	editConnection                   // 改已保存连接的密钥与地址：验证后保存，不换模型
	addModel                         // 给已保存连接添一个模型并用上它
)

// 连接表单的行；哪些行出现由连接的格式与用途决定（formRows）。
type formRow int

const (
	rowFormat formRow = iota
	rowAddress
	rowKey
	rowEndpoint
	rowName
	rowSubmit
)

// 连接表单里的文本输入。
const (
	inputAddress = iota
	inputKey
	inputName
	inputCount
)

// 总览的行。
const (
	itemRole = iota // 默认（role 为空）或某个角色用的模型
	itemThinking
	itemConnection
	itemModel
	itemAddModel
	itemAddConnection
)

type overviewItem struct {
	kind                    int
	role, connection, model string
}

type wizardState struct {
	step     wizardStep
	mode     wizardMode
	fromHome bool // 从首页进入：总览能回首页；首次配置没有可返回的页面
	cursor   int  // 总览、服务商与模型列表的选中行
	err      string
	notice   string
	confirm  overviewItem // 待二次确认删除的总览行：再按 d 执行，按其他键取消
	role     string       // 正在换模型的角色，空是默认

	// 连接草稿：preset 是服务商（wizardPresets 下标），format 是中转的格式（wizardFormats
	// 下标），original 是编辑或添加模型的已保存连接，models 是它原有的模型。
	preset   int
	format   int
	endpoint string
	original string
	models   []string
	inputs   [inputCount]textinput.Model
	row      formRow
	named    bool // 用户改过名称：地址变化不再替它起名

	// 列模型与试用手动输入的模型：request 标识在途请求，取消或过期的结果丢弃。
	listing  bool
	trying   string // 正在试用的模型 ID
	request  *struct{ token byte }
	cancel   context.CancelFunc
	options  []binding.ModelOption
	unlisted string // 列不出模型的原因：只能直接输入模型 ID
	filter   textinput.Model
	clicked  listClick
}

type wizardListedMsg struct {
	request *struct{ token byte }
	options []binding.ModelOption
	err     error
}

type wizardTriedMsg struct {
	request *struct{ token byte }
	err     error
}

// wizardPresets 是可选的服务商；kind 为空的是「中转 / 自定义」，格式由用户选。预设只定
// 协议，模型由服务商列出、用户挑选，不替用户钉死模型。
var wizardPresets = []struct{ label, kind, note string }{
	{"DeepSeek", "deepseek", ""},
	{"OpenAI", "openai", ""},
	{"Anthropic", "anthropic", "Claude"},
	{"Gemini", "gemini", ""},
	{"Qwen", "qwen", "通义千问"},
	{"GLM", "glm", "智谱"},
	{"MiniMax", "minimax", ""},
	{"MiMo", "mimo", "小米"},
	{"Grok", "grok", "xAI"},
	{"OpenRouter", "openrouter", "一个密钥用多家模型"},
	{"Ollama", "ollama", "本机模型，无需密钥"},
	{"中转 / 自定义", "", "OpenAI、Claude 或 Gemini 格式的任意地址"},
}

var relayPreset = len(wizardPresets) - 1

// wizardFormats 是中转与自定义连接可选的接口格式。官方地址与中转的差别由装配按地址推出
// （models.adapter），不交给用户选。
var wizardFormats = []struct{ kind, label string }{
	{"openai", "OpenAI 格式"}, {"anthropic", "Claude 格式"}, {"gemini", "Gemini 格式"},
}

// wizardEndpoints 是 OpenAI 格式的接口；空是 Chat Completions。
var wizardEndpoints = []struct{ api, label string }{{"", "Chat Completions"}, {"responses", "Responses"}}

// renamable 报告这种格式的连接能否自取名字：其余内置服务商的连接名就是服务商名（配置约束）。
func renamable(kind string) bool { return kind == "openai" || kind == "anthropic" || kind == "gemini" }

func newWizardState(config appconfig.Config, problem string, fromHome bool) wizardState {
	w := wizardState{fromHome: fromHome, err: problem, filter: newInput("输入筛选，或直接输入模型 ID")}
	for i := range w.inputs {
		w.inputs[i] = newInput("")
		w.inputs[i].Prompt = ""
	}
	w.inputs[inputKey].EchoMode, w.inputs[inputKey].EchoCharacter = textinput.EchoPassword, '•'
	if len(config.Providers) == 0 {
		w.step = wizardProviders
	}
	return w
}

// busy 报告是否有请求在途（列模型或试用模型）。
func (w *wizardState) busy() bool { return w.listing || w.trying != "" }

// stopRequest 取消在途请求，迟到的结果按 request 丢弃。
func (w *wizardState) stopRequest() {
	w.cancel()
	w.listing, w.trying, w.request, w.cancel = false, "", nil, nil
}

func (w *wizardState) kind() string {
	if w.preset == relayPreset {
		return wizardFormats[w.format].kind
	}
	return wizardPresets[w.preset].kind
}

// draft 是表单此刻的连接：名称与连接配置（保留原有模型）。
func (w *wizardState) draft() (string, appconfig.ProviderConfig) {
	kind := w.kind()
	pc := appconfig.ProviderConfig{
		Type: kind, APIKey: strings.TrimSpace(w.inputs[inputKey].Value()),
		BaseURL: strings.TrimSpace(w.inputs[inputAddress].Value()), Models: w.models,
	}
	if kind == "openai" {
		pc.API = w.endpoint
	}
	name := w.original
	switch {
	case name != "":
	case renamable(kind):
		name = strings.TrimSpace(w.inputs[inputName].Value())
	default:
		name = kind
	}
	return name, pc
}

// keyOptional 报告连接能否不带密钥：本机 Ollama，以及按通用 Chat Completions 说话的
// 中转与自部署服务（它们未必要求密钥）；其余服务商的适配器都要求密钥。
func (w *wizardState) keyOptional() bool {
	return w.kind() == "ollama" || w.preset == relayPreset && w.kind() == "openai"
}

// formRows 是表单可见的行：中转先问格式与地址，内置服务商先问密钥；新连接且能自取名字
// 时才问名称。
func (w *wizardState) formRows() []formRow {
	var rows []formRow
	if w.preset == relayPreset {
		rows = append(rows, rowFormat, rowAddress, rowKey)
	} else {
		rows = append(rows, rowKey, rowAddress)
	}
	if w.kind() == "openai" {
		rows = append(rows, rowEndpoint)
	}
	if w.mode == addConnection && w.original == "" && renamable(w.kind()) {
		rows = append(rows, rowName)
	}
	return append(rows, rowSubmit)
}

// firstInput 是表单打开时的焦点：第一个要填的文本行（格式有默认，不先停在它上面）。
func (w *wizardState) firstInput() formRow {
	if w.preset == relayPreset {
		return rowAddress
	}
	return rowKey
}

func (w *wizardState) focusRow(row formRow) tea.Cmd {
	for i := range w.inputs {
		w.inputs[i].Blur()
	}
	w.row = row
	switch row {
	case rowAddress:
		return w.inputs[inputAddress].Focus()
	case rowKey:
		return w.inputs[inputKey].Focus()
	case rowName:
		return w.inputs[inputName].Focus()
	}
	return nil
}

func (m model) updateWizard(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case wizardListedMsg:
		return m.wizardListed(message)
	case wizardTriedMsg:
		return m.wizardTried(message)
	case tea.MouseMsg:
		return m.wizardMouse(message)
	case tea.PasteMsg:
		return m.wizardInput(message)
	case tea.KeyPressMsg:
		if message.String() != "esc" && m.wizard.listing && m.wizard.mode == editConnection {
			return m, nil
		}
		m.wizard.err, m.wizard.notice = "", ""
		switch m.wizard.step {
		case wizardOverview:
			return m.overviewKey(message)
		case wizardProviders:
			return m.providersKey(message)
		case wizardConnection:
			return m.connectionKey(message)
		case wizardModels:
			return m.modelsKey(message)
		default:
			return m.roleKey(message)
		}
	}
	return m, nil
}

// wizardInput 把按键与粘贴交给正在输入的那一栏：表单的文本行或模型筛选。
func (m model) wizardInput(message tea.Msg) (tea.Model, tea.Cmd) {
	w := &m.wizard
	var cmd tea.Cmd
	switch {
	case w.step == wizardModels && !w.busy():
		before := w.filter.Value()
		w.filter, cmd = w.filter.Update(message)
		if w.filter.Value() != before {
			w.cursor = 0
		}
	case w.step == wizardConnection:
		index := map[formRow]int{rowAddress: inputAddress, rowKey: inputKey, rowName: inputName}
		input, ok := index[w.row]
		if !ok {
			return m, nil
		}
		before := w.inputs[input].Value()
		w.inputs[input], cmd = w.inputs[input].Update(message)
		changed := w.inputs[input].Value() != before
		if changed && input == inputName {
			w.named = true
		}
		if changed && input == inputAddress && !w.named && slices.Contains(w.formRows(), rowName) {
			w.inputs[inputName].SetValue(m.suggestName(w.inputs[inputAddress].Value(), w.kind()))
		}
	}
	return m, cmd
}

// suggestName 给新连接起名：中转取地址的主域名（api.wool.dev → wool），否则用格式名；
// 重名时加序号。
func (m model) suggestName(address, kind string) string {
	base := kind
	if u, err := url.Parse(strings.TrimSpace(address)); err == nil && u.Hostname() != "" && net.ParseIP(u.Hostname()) == nil {
		labels := strings.Split(u.Hostname(), ".")
		base = labels[max(0, len(labels)-2)]
	}
	saved := m.api.Models.Config().Providers
	name := base
	for i := 2; ; i++ {
		if _, taken := saved[name]; !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

// --- 总览 ---

// overviewItems 是总览的行：默认模型、思考强度与各角色的模型；每个连接、它的模型与「添加
// 模型」；最后是「添加连接」。
func (m model) overviewItems() []overviewItem {
	items := []overviewItem{{kind: itemRole}, {kind: itemThinking}}
	for _, role := range m.api.Models.Roles()[1:] {
		items = append(items, overviewItem{kind: itemRole, role: role})
	}
	for _, name := range m.api.Models.Connections() {
		items = append(items, overviewItem{kind: itemConnection, connection: name})
		for _, model := range m.api.Models.Models(name) {
			items = append(items, overviewItem{kind: itemModel, connection: name, model: model})
		}
		items = append(items, overviewItem{kind: itemAddModel, connection: name})
	}
	return append(items, overviewItem{kind: itemAddConnection})
}

func (m model) overviewKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	w := &m.wizard
	items := m.overviewItems()
	item, armed := items[w.cursor], w.confirm
	w.confirm = overviewItem{}
	switch key.String() {
	case "up", "shift+tab":
		w.cursor = max(0, w.cursor-1)
	case "down", "tab":
		w.cursor = min(len(items)-1, w.cursor+1)
	case "home":
		w.cursor = 0
	case "end":
		w.cursor = len(items) - 1
	case "left", "right":
		delta := 1
		if key.String() == "left" {
			delta = -1
		}
		switch {
		case item.kind == itemThinking:
			return m.cycleThinking("", delta)
		case item.kind == itemRole && item.role != "":
			return m.cycleThinking(item.role, delta)
		}
	case "enter":
		return m.activateOverview(item)
	case "d":
		if item.kind != itemConnection && item.kind != itemModel {
			return m, nil
		}
		if armed == item {
			return m.removeItem(item)
		}
		w.confirm, w.notice = item, m.removalPrompt(item)
	case "esc":
		switch {
		case w.fromHome:
			return m.leaveWizard()
		case m.api.Models.Current("").Bound:
			return m.enterHome()
		}
	}
	return m, nil
}

func (m model) leaveWizard() (tea.Model, tea.Cmd) {
	m.page = pageHome
	return m, m.loadLibraryCmd()
}

// enterHome 是首次配置完成：进一个新的首页。
func (m model) enterHome() (tea.Model, tea.Cmd) {
	m.home = newHomeState(m.width)
	return m.leaveWizard()
}

func (m model) activateOverview(item overviewItem) (tea.Model, tea.Cmd) {
	switch item.kind {
	case itemRole:
		return m.openRolePicker(item.role)
	case itemModel:
		if err := m.api.Models.Use("", item.connection, item.model); err != nil {
			m.wizard.err = m.redact(err, item.connection)
			return m, nil
		}
		if !m.wizard.fromHome {
			return m.enterHome()
		}
		m.wizard.notice = "已换用 " + item.connection + " / " + item.model
	case itemConnection:
		return m.openSaved(item.connection, editConnection)
	case itemAddModel:
		next, _ := m.openSaved(item.connection, addModel)
		if m = next.(model); m.wizard.step != wizardConnection {
			return m, nil
		}
		return m.listModels()
	case itemAddConnection:
		m.wizard.step, m.wizard.cursor = wizardProviders, 0
	}
	return m, nil
}

// thinkingLevels 是默认（role 为空）或角色可选的思考强度与当前设置所在的位置（不在其中时
// 为 -1）。角色的首档是跟随默认（它与 auto 同存为空，不另列 auto）；跟随默认模型的角色没有
// 自己的档位。
func (m model) thinkingLevels(role string) ([]string, int) {
	current := m.api.Models.Current(role)
	levels := m.api.Models.Levels(current.Connection, current.Model)
	if role == "" {
		return levels, slices.Index(levels, m.api.Models.Config().Thinking)
	}
	rc, ok := m.api.Models.Config().Roles[role]
	if !ok {
		return nil, -1
	}
	levels = append([]string{levelInherit}, levels[1:]...)
	if rc.Thinking == "" {
		return levels, 0
	}
	return levels, slices.Index(levels, rc.Thinking)
}

func (m model) cycleThinking(role string, delta int) (tea.Model, tea.Cmd) {
	levels, index := m.thinkingLevels(role)
	if len(levels) == 0 {
		return m, nil
	}
	return m.setThinking(role, levels[(max(0, index)+delta+len(levels))%len(levels)])
}

func (m model) setThinking(role, level string) (tea.Model, tea.Cmd) {
	if err := m.api.Models.SetThinking(role, level); err != nil {
		m.wizard.err = err.Error()
		return m, nil
	}
	m.wizard.notice = "思考强度：" + thinkingLabel(level)
	if role != "" {
		m.wizard.notice = roleNames[role] + " " + m.wizard.notice
	}
	return m, nil
}

// removalPrompt 是删除前的确认提示。
func (m model) removalPrompt(item overviewItem) string {
	if item.kind == itemModel {
		return "再按一次 d 删除「" + item.connection + " / " + item.model + "」，按其他键取消"
	}
	text := "再按一次 d 删除连接「" + item.connection + "」"
	if count := len(m.api.Models.Models(item.connection)); count > 0 {
		text += fmt.Sprintf("和它的 %d 个模型", count)
	}
	return text + "，按其他键取消"
}

// removeItem 删除连接或模型；默认或角色还在用的，服务拒绝并说明先换掉谁。
func (m model) removeItem(item overviewItem) (tea.Model, tea.Cmd) {
	var err error
	var notice string
	if item.kind == itemModel {
		err, notice = m.api.Models.RemoveModel(item.connection, item.model), "已删除 "+item.connection+" / "+item.model
	} else {
		err, notice = m.api.Models.RemoveConnection(item.connection), "已删除连接 "+item.connection
	}
	if err != nil {
		m.wizard.err = err.Error()
		return m, nil
	}
	m.wizard.notice = notice
	m.wizard.cursor = min(m.wizard.cursor, len(m.overviewItems())-1)
	return m, nil
}

// --- 角色 ---

// roleChoices 是给默认或角色换模型的选项：全部已保存的模型，角色另有首项「跟随默认」
// （连接为空）。
func (m model) roleChoices() []binding.Choice {
	choices := m.api.Models.Choices()
	if m.wizard.role != "" {
		choices = append([]binding.Choice{{}}, choices...)
	}
	return choices
}

// roleUses 报告角色此刻是否用着这个选项。
func (m model) roleUses(role string, choice binding.Choice) bool {
	current := m.api.Models.Current(role)
	if current.Inherited {
		return choice.Connection == ""
	}
	return choice.Connection == current.Connection && choice.Model == current.Model
}

func (m model) openRolePicker(role string) (tea.Model, tea.Cmd) {
	w := &m.wizard
	w.step, w.role = wizardRole, role
	w.cursor = max(0, slices.IndexFunc(m.roleChoices(), func(choice binding.Choice) bool { return m.roleUses(role, choice) }))
	return m, nil
}

func (m model) roleKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	w := &m.wizard
	choices := m.roleChoices()
	switch key.String() {
	case "esc":
		return m.showOverview("", overviewItem{kind: itemRole, role: w.role}), nil
	case "up", "shift+tab":
		w.cursor = max(0, w.cursor-1)
	case "down", "tab":
		w.cursor = min(len(choices)-1, w.cursor+1)
	case "pgup":
		w.cursor = max(0, w.cursor-m.wizardFrame().capacity)
	case "pgdown":
		w.cursor = min(len(choices)-1, w.cursor+m.wizardFrame().capacity)
	case "enter":
		return m.assignRole(choices[w.cursor])
	}
	return m, nil
}

// assignRole 让默认或角色改用所选模型；角色选「跟随默认」即清除覆盖。默认绑定修好了首次
// 配置就进首页，其余回到总览。
func (m model) assignRole(choice binding.Choice) (tea.Model, tea.Cmd) {
	role := m.wizard.role
	if err := m.api.Models.Use(role, choice.Connection, choice.Model); err != nil {
		m.wizard.err = m.redact(err, choice.Connection)
		return m, nil
	}
	if role == "" && !m.wizard.fromHome {
		return m.enterHome()
	}
	notice := "已换用 " + choice.Connection + " / " + choice.Model
	switch {
	case role != "" && choice.Connection == "":
		notice = roleNames[role] + " 跟随默认"
	case role != "":
		notice = roleNames[role] + " 改用 " + choice.Connection + " / " + choice.Model
	}
	return m.showOverview(notice, overviewItem{kind: itemRole, role: role}), nil
}

// --- 服务商 ---

func (m model) providersKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	w := &m.wizard
	switch key.String() {
	case "up", "shift+tab":
		w.cursor = (w.cursor + len(wizardPresets) - 1) % len(wizardPresets)
	case "down", "tab":
		w.cursor = (w.cursor + 1) % len(wizardPresets)
	case "enter":
		return m.newConnection(w.cursor)
	case "esc":
		// 首次配置没有可返回的页面：退出走 Ctrl+C。
		if len(m.api.Models.Connections()) > 0 {
			w.step, w.cursor = wizardOverview, 0
		}
	}
	return m, nil
}

// newConnection 从服务商开始一个新连接。内置服务商的连接名就是服务商名，已有这个连接时
// 带出它的密钥与地址，继续下去就是更新它。
func (m model) newConnection(preset int) (tea.Model, tea.Cmd) {
	kind := wizardPresets[preset].kind
	if _, saved := m.api.Models.Config().Providers[kind]; saved && kind != "" && !renamable(kind) {
		next, cmd := m.openSaved(kind, addConnection)
		return next, cmd
	}
	w := &m.wizard
	w.step, w.mode, w.preset, w.format, w.endpoint = wizardConnection, addConnection, preset, 0, ""
	w.original, w.models, w.named = "", nil, false
	for i := range w.inputs {
		w.inputs[i].SetValue("")
	}
	if renamable(kind) || kind == "" {
		w.inputs[inputName].SetValue(m.suggestName("", w.kind()))
	}
	m.placeholders()
	return m, w.focusRow(w.firstInput())
}

// openSaved 打开一个已保存的连接：带出它的格式、地址、密钥与模型。
func (m model) openSaved(name string, mode wizardMode) (tea.Model, tea.Cmd) {
	pc, err := m.api.Models.Config().Connection(name)
	if err != nil {
		m.wizard.err = err.Error()
		return m, nil
	}
	preset, format := relayPreset, slices.IndexFunc(wizardFormats, func(f struct{ kind, label string }) bool { return f.kind == pc.Type })
	if !renamable(pc.Type) || name == pc.Type && pc.BaseURL == "" {
		preset = slices.IndexFunc(wizardPresets, func(p struct{ label, kind, note string }) bool { return p.kind == pc.Type })
	}
	if preset < 0 {
		m.wizard.err = "连接「" + name + "」的格式（" + pc.Type + "）在这里改不了，请直接编辑配置文件"
		return m, nil
	}
	w := &m.wizard
	w.step, w.mode, w.original, w.models, w.endpoint, w.named = wizardConnection, mode, name, pc.Models, pc.API, true
	w.preset, w.format = preset, max(0, format)
	w.inputs[inputAddress].SetValue(pc.BaseURL)
	w.inputs[inputKey].SetValue(pc.APIKey)
	w.inputs[inputName].SetValue(name)
	m.placeholders()
	return m, w.focusRow(w.firstInput())
}

// placeholders 按服务商写输入框的占位提示。
func (m *model) placeholders() {
	w := &m.wizard
	address, key := "留空使用官方地址", "粘贴服务商给的 API Key"
	switch w.kind() {
	case "ollama":
		address, key = "留空使用本机默认地址", "本机模型通常不需要"
	}
	if w.preset == relayPreset {
		address = "https://"
	}
	if w.preset == relayPreset && w.keyOptional() {
		key = "粘贴密钥；服务不要求时留空"
	}
	w.inputs[inputAddress].Placeholder = address
	w.inputs[inputKey].Placeholder = key
	w.inputs[inputName].Placeholder = "用来区分多个连接"
}

// --- 连接 ---

func (m model) connectionKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	w := &m.wizard
	rows := w.formRows()
	at := max(0, slices.Index(rows, w.row))
	switch key.String() {
	case "esc":
		return m.backFromConnection()
	case "tab", "down":
		return m, w.focusRow(rows[(at+1)%len(rows)])
	case "shift+tab", "up":
		return m, w.focusRow(rows[(at+len(rows)-1)%len(rows)])
	case "enter":
		return m.submitConnection()
	case "left", "right":
		delta := 1
		if key.String() == "left" {
			delta = -1
		}
		switch w.row {
		case rowFormat:
			w.format = (w.format + delta + len(wizardFormats)) % len(wizardFormats)
			m.refreshName()
			return m, nil
		case rowEndpoint:
			index := slices.IndexFunc(wizardEndpoints, func(e struct{ api, label string }) bool { return e.api == w.endpoint })
			w.endpoint = wizardEndpoints[(index+delta+len(wizardEndpoints))%len(wizardEndpoints)].api
			return m, nil
		}
	}
	return m.wizardInput(key)
}

// refreshName 换了格式后，没改过的名称跟着换。
func (m *model) refreshName() {
	w := &m.wizard
	if !w.named && w.original == "" {
		w.inputs[inputName].SetValue(m.suggestName(w.inputs[inputAddress].Value(), w.kind()))
	}
	m.placeholders()
}

func (m model) backFromConnection() (tea.Model, tea.Cmd) {
	w := &m.wizard
	if w.listing {
		w.stopRequest()
		return m, nil
	}
	if w.mode == addConnection && w.original == "" {
		w.step, w.cursor = wizardProviders, w.preset
		return m, nil
	}
	w.step = wizardOverview
	return m, nil
}

// submitConnection 先在本地查必填与重名，再向服务商列模型：新连接与添加模型接着选模型，
// 编辑连接验证后直接保存。
func (m model) submitConnection() (tea.Model, tea.Cmd) {
	w := &m.wizard
	name, pc := w.draft()
	_, taken := m.api.Models.Config().Providers[name]
	switch {
	case w.preset == relayPreset && pc.BaseURL == "":
		w.err = "填写中转或自部署服务的地址"
		return m, w.focusRow(rowAddress)
	case pc.APIKey == "" && !w.keyOptional():
		w.err = "粘贴服务商给的 API Key"
		return m, w.focusRow(rowKey)
	case name == "":
		w.err = "给连接起个名字"
		return m, w.focusRow(rowName)
	case taken && w.original == "":
		w.err = "已有名为「" + name + "」的连接：换个名字，或回到总览编辑它"
		return m, w.focusRow(rowName)
	}
	return m.listModels()
}

// listModels 用表单里的连接向服务商列模型；除了编辑连接，都先进模型页等结果。
func (m model) listModels() (tea.Model, tea.Cmd) {
	w := &m.wizard
	name, pc := w.draft()
	ctx, cancel := context.WithCancel(m.ctx)
	request := &struct{ token byte }{}
	w.listing, w.request, w.cancel = true, request, cancel
	if w.mode != editConnection {
		w.step, w.cursor, w.options, w.unlisted = wizardModels, 0, nil, ""
		w.filter.SetValue("")
	}
	list := m.deps.ListModels
	return m, tea.Batch(w.filter.Focus(), func() tea.Msg {
		defer cancel()
		options, err := list(ctx, name, pc)
		return wizardListedMsg{request: request, options: options, err: err}
	})
}

// wizardListed 收下模型列表。密钥被拒、地址不合法回到表单；其余失败（网络、中转不支持
// 列模型）不拦路：选模型改为直接输入，编辑连接照样保存并说明没能验证。
func (m model) wizardListed(msg wizardListedMsg) (tea.Model, tea.Cmd) {
	w := &m.wizard
	if !w.listing || msg.request != w.request {
		return m, nil
	}
	w.listing, w.request, w.cancel = false, nil, nil
	name, _ := w.draft()
	if msg.err != nil {
		text := m.redactDraft(msg.err)
		var failure *domainmodel.Failure
		switch {
		case errors.Is(msg.err, appconfig.ErrBaseURL):
			w.step, w.err = wizardConnection, text
			return m, w.focusRow(rowAddress)
		case errors.As(msg.err, &failure) && failure.Code == domainmodel.FailureModelRefused:
			w.step, w.err = wizardConnection, "服务商拒绝了这组凭据："+text
			return m, w.focusRow(rowKey)
		case w.mode == editConnection:
			return m.saveConnection("已保存连接 " + name + " · 没能连上服务商验证：" + text)
		}
		w.unlisted = text
		return m, nil
	}
	if w.mode == editConnection {
		return m.saveConnection("已更新连接 " + name)
	}
	w.options = msg.options
	if len(w.options) == 0 {
		w.unlisted = "服务商没有列出可用的模型"
	}
	if current := m.api.Models.Current(""); current.Connection == name {
		w.cursor = max(0, slices.IndexFunc(w.options, func(o binding.ModelOption) bool { return o.ID == current.Model }))
	}
	return m, nil
}

// --- 模型 ---

// pickerRow 是模型列表的一行；option 为空的是「直接使用」输入的模型 ID。
type pickerRow struct {
	id     string
	option *binding.ModelOption
}

// pickerRows 是筛选后的模型；输入的文字不是列表里的某个模型时，末尾多一行直接使用它。
func (w *wizardState) pickerRows() []pickerRow {
	query := strings.TrimSpace(w.filter.Value())
	lower := strings.ToLower(query)
	var rows []pickerRow
	exact := false
	for i := range w.options {
		option := &w.options[i]
		if strings.Contains(strings.ToLower(option.ID+" "+option.Name), lower) {
			rows = append(rows, pickerRow{id: option.ID, option: option})
			exact = exact || option.ID == query
		}
	}
	if query != "" && !exact {
		rows = append(rows, pickerRow{id: query})
	}
	return rows
}

func (m model) modelsKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	w := &m.wizard
	switch {
	case w.trying != "":
		if key.String() == "esc" {
			w.stopRequest()
		}
		return m, nil
	case w.listing:
		if key.String() == "esc" {
			return m.backFromModels()
		}
		return m, nil
	}
	rows := w.pickerRows()
	switch key.String() {
	case "esc":
		return m.backFromModels()
	case "up":
		w.cursor = max(0, w.cursor-1)
	case "down":
		w.cursor = min(max(0, len(rows)-1), w.cursor+1)
	case "pgup":
		w.cursor = max(0, w.cursor-m.wizardFrame().capacity)
	case "pgdown":
		w.cursor = min(max(0, len(rows)-1), w.cursor+m.wizardFrame().capacity)
	case "enter":
		if w.cursor < len(rows) {
			return m.pickModel(rows[w.cursor])
		}
	default:
		return m.wizardInput(key)
	}
	return m, nil
}

func (m model) backFromModels() (tea.Model, tea.Cmd) {
	w := &m.wizard
	if w.listing {
		w.stopRequest()
	}
	if w.mode == addModel {
		w.step = wizardOverview
		return m, nil
	}
	w.step = wizardConnection
	return m, w.focusRow(w.row)
}

// pickModel 用上所选的模型：服务商列出的直接用；手动输入的先发一句极短的对话，确认能用再
// 保存，ID 拼错当场报出，不用等开写。
func (m model) pickModel(row pickerRow) (tea.Model, tea.Cmd) {
	if row.option != nil {
		return m.useModel(row.id)
	}
	w := &m.wizard
	name, pc := w.draft()
	ctx, cancel := context.WithCancel(m.ctx)
	request := &struct{ token byte }{}
	w.trying, w.request, w.cancel = row.id, request, cancel
	try := m.deps.TryModel
	return m, func() tea.Msg {
		defer cancel()
		return wizardTriedMsg{request: request, err: try(ctx, name, pc, row.id)}
	}
}

// wizardTried 收下试用结果：能用就保存，不能用留在模型页并给出服务商的原话。
func (m model) wizardTried(msg wizardTriedMsg) (tea.Model, tea.Cmd) {
	w := &m.wizard
	if w.trying == "" || msg.request != w.request {
		return m, nil
	}
	id := w.trying
	w.trying, w.request, w.cancel = "", nil, nil
	if msg.err != nil {
		w.err = "「" + id + "」用不了：" + m.redactDraft(msg.err)
		return m, nil
	}
	return m.useModel(id)
}

// useModel 用上所选模型并设为默认：添加模型只是换用，新建或更新的连接连同模型一起写入。
func (m model) useModel(id string) (tea.Model, tea.Cmd) {
	name, pc := m.wizard.draft()
	var err error
	if m.wizard.mode == addModel {
		err = m.api.Models.Use("", name, id)
	} else {
		err = m.api.Models.SaveConnection(name, pc, id)
	}
	return m.finishWizard(err, "已换用 "+name+" / "+id, overviewItem{kind: itemModel, connection: name, model: id})
}

// saveConnection 只保存连接本身，默认绑定不变。
func (m model) saveConnection(notice string) (tea.Model, tea.Cmd) {
	name, pc := m.wizard.draft()
	return m.finishWizard(m.api.Models.SaveConnection(name, pc, ""), notice, overviewItem{kind: itemConnection, connection: name})
}

// finishWizard 收尾一次保存：成功后首次配置进首页，从首页来的回到总览、停在 target 上；
// 失败留在原页。
func (m model) finishWizard(err error, notice string, target overviewItem) (tea.Model, tea.Cmd) {
	if err != nil {
		m.wizard.err = m.redactDraft(err)
		return m, nil
	}
	if !m.wizard.fromHome {
		return m.enterHome()
	}
	return m.showOverview(notice, target), nil
}

// showOverview 换一个新的总览，停在 target 上。
func (m model) showOverview(notice string, target overviewItem) model {
	m.wizard = newWizardState(m.api.Models.Config(), "", m.wizard.fromHome)
	m.wizard.notice = notice
	m.wizard.cursor = max(0, slices.Index(m.overviewItems(), target))
	return m
}

// 服务商的报错可能带回请求里的凭据，界面上一律不出现密钥。
func (m model) redactDraft(err error) string {
	_, pc := m.wizard.draft()
	return redactKey(err.Error(), pc.APIKey)
}

func (m model) redact(err error, connection string) string {
	return redactKey(err.Error(), m.api.Models.Config().Providers[connection].APIKey)
}

func redactKey(text, key string) string {
	if key == "" {
		return text
	}
	return strings.ReplaceAll(text, key, "[已隐藏]")
}

// wizardMouse：总览的行点了即生效（换模型、换用、编辑连接、添加），思考强度点选；服务商点了
// 进下一步；表单点行聚焦、点选项即选；模型单击选中、双击用上。滚轮移动列表的选中行。
func (m model) wizardMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	w := &m.wizard
	mouse := msg.Mouse()
	frame := m.wizardFrame()
	if mouse.Button == tea.MouseWheelUp || mouse.Button == tea.MouseWheelDown {
		if !frame.list.contains(mouse.X, mouse.Y) {
			return m, nil
		}
		key := tea.KeyPressMsg{Code: tea.KeyDown}
		if mouse.Button == tea.MouseWheelUp {
			key = tea.KeyPressMsg{Code: tea.KeyUp}
		}
		return m.updateWizard(key)
	}
	if !leftClick(msg) || w.busy() {
		return m, nil
	}
	last := w.clicked
	w.clicked, w.confirm = listClick{}, overviewItem{}
	for _, hit := range frame.hits {
		if mouse.Y != hit.y || mouse.X < hit.x || mouse.X >= hit.x+hit.width {
			continue
		}
		w.err, w.notice = "", ""
		switch w.step {
		case wizardOverview:
			items := m.overviewItems()
			w.cursor = hit.row
			if items[hit.row].kind == itemThinking {
				if hit.option < 0 {
					return m, nil
				}
				levels, _ := m.thinkingLevels("")
				return m.setThinking("", levels[hit.option])
			}
			return m.activateOverview(items[hit.row])
		case wizardProviders:
			w.cursor = hit.row
			return m.newConnection(hit.row)
		case wizardConnection:
			row := formRow(hit.focus)
			switch {
			case row == rowSubmit:
				return m.submitConnection()
			case row == rowFormat && hit.option >= 0:
				w.format = hit.option
				m.refreshName()
			case row == rowEndpoint && hit.option >= 0:
				w.endpoint = wizardEndpoints[hit.option].api
			}
			return m, w.focusRow(row)
		case wizardModels:
			w.cursor = hit.row
			now := time.Now()
			if last.again(hit.row, now) {
				return m.pickModel(w.pickerRows()[hit.row])
			}
			w.clicked = listClick{row: hit.row, at: now}
		case wizardRole:
			w.cursor = hit.row
			now := time.Now()
			if last.again(hit.row, now) {
				return m.assignRole(m.roleChoices()[hit.row])
			}
			w.clicked = listClick{row: hit.row, at: now}
		}
		return m, nil
	}
	return m, nil
}
