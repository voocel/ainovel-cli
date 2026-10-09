package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/jsonc"
)

// 首页（workbench §4）：左边写一个新故事，右边是作品库。新故事的输入只有一句话、
// 篇幅、推进方式与可选设定；它们只产生不同的初始化输入，最终创建相同的 Project 与
// CreationRun。选项全部摊开并附一句说明，不藏在要猜的标签后面。
type homeMode int

const (
	homeMain homeMode = iota
	homeSettings
	homeImport
)

// 焦点按阅读顺序排列：左栏自上而下，再到右栏与页眉。
const (
	focusPremise = iota
	focusLength
	focusApproval
	focusSettings
	focusStart
	focusLibrary
	focusSearch
	focusImport
	focusConfig
)

type homeState struct {
	premise textarea.Model
	// fixed 为 true 时篇幅固定为 chapters 章（0 表示还没填），否则交给 AI（D63）。
	fixed    bool
	chapters int
	approval domainmodel.ApprovalPolicy
	// settings 是更多设定的输入，全部可空；它们是用户的原话，原样进 Intent（D70）。
	settings    []textinput.Model
	settingStep int
	focus       int
	mode        homeMode
	search      textinput.Model
	searching   bool
	importIn    textinput.Model
	importing   bool
	library     []libraryEntry
	cursor      int
	loaded      bool
	// lastOpened 是上次打开的作品：作品库加载后预选它，回车即恢复落点。
	lastOpened string
	// confirmDelete 是待二次确认删除的作品 ID：再按 d 执行，按其他键取消。
	confirmDelete string
	notice        string
	err           string
}

type libraryEntry struct {
	id      string
	premise string
	target  int
	written int
	state   domainmodel.CreationRunState // 空表示还没开始创作
	updated time.Time
}

type libraryLoadedMsg struct {
	entries []libraryEntry
	err     error
}

type projectDeletedMsg struct {
	projectID string
	err       error
}

type importDoneMsg struct {
	projectID string
	proposal  domainmodel.Proposal
	view      workbench.ProposalView
	err       error
}

var approvalOrder = []domainmodel.ApprovalPolicy{domainmodel.ApprovalAuto, domainmodel.ApprovalMilestone, domainmodel.ApprovalManual}

// settingFields 是更多设定（workbench §4）：更完整的初始化输入，全部可空。
var settingFields = []struct{ label, placeholder string }{
	{"受众", "写给谁看，例如：喜欢都市悬疑的读者"},
	{"期待体验", "逗号分隔，例如：紧张, 反转, 治愈"},
	{"必须出现", "逗号分隔的人物、元素或情节"},
	{"禁止出现", "逗号分隔"},
	{"结局方向", "例如：主角赢了，但付出代价"},
}

// newHomeState 的 width 是终端宽度：故事输入框按首页几何折行（窗口变化时 Update 重设）。
func newHomeState(width int) homeState {
	settings := make([]textinput.Model, len(settingFields))
	for i, field := range settingFields {
		settings[i] = newInput(field.placeholder)
	}
	return homeState{
		premise: newPremiseInput(premiseWidth(width)), approval: domainmodel.ApprovalAuto, settings: settings,
		search: newInput("标题或 ID"),
	}
}

// newPremiseInput 是首页的故事输入：多行自动折行，回车留给「开始创作」。
func newPremiseInput(width int) textarea.Model {
	input := textarea.New()
	input.SetWidth(width)
	input.Placeholder = "一句话说想写什么。比如：一个失忆的邮差替亡者送信，每送出一封，就想起一段自己的过去"
	input.ShowLineNumbers, input.Prompt = false, ""
	input.SetHeight(premiseRows)
	input.KeyMap.InsertNewline.SetEnabled(false)
	surface := lipgloss.NewStyle().Background(benchColors.InputBackground)
	style := textarea.Style{
		Base: surface, CursorLine: surface, EndOfBuffer: surface, Prompt: surface,
		Text:        surface.Foreground(benchColors.Text),
		Placeholder: surface.Foreground(benchColors.Faint),
	}
	input.FocusedStyle, input.BlurredStyle = style, style
	input.Cursor.Style = benchTheme.Text
	input.Focus()
	return input
}

// intent 只在填了更多设定时给出完整 Intent；只有一句话时走快速创建（结果相同）。
func (h homeState) intent(premise string) *domainmodel.Intent {
	values := make([]string, len(h.settings))
	filled := false
	for i, input := range h.settings {
		values[i] = strings.TrimSpace(input.Value())
		filled = filled || values[i] != ""
	}
	if !filled {
		return nil
	}
	return &domainmodel.Intent{
		Premise: premise, Audience: values[0], DesiredExperience: splitList(values[1]),
		Required: splitList(values[2]), Forbidden: splitList(values[3]), EndingDirection: values[4],
	}
}

// loadLibraryCmd 读取作品库：每本书的目标、进度与运行状态。
func (m model) loadLibraryCmd() tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		records, err := api.Workbench.Library(ctx)
		if err != nil {
			return libraryLoadedMsg{err: err}
		}
		entries := make([]libraryEntry, 0, len(records))
		for _, record := range records {
			entries = append(entries, libraryEntry{
				id: record.ID, premise: record.Premise, target: record.Target, written: record.Written,
				state: record.State, updated: record.UpdatedAt,
			})
		}
		return libraryLoadedMsg{entries: entries}
	}
}

func (m model) updateHome(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case libraryLoadedMsg:
		if message.err != nil {
			m.home.err = message.err.Error()
			return m, nil
		}
		selected := m.home.lastOpened
		first := !m.home.loaded
		if m.home.loaded && m.home.cursor < len(m.home.library) {
			selected = m.home.library[m.home.cursor].id
		}
		m.home.library, m.home.loaded, m.home.err = message.entries, true, ""
		indices := m.libraryIndices()
		m.home.cursor = 0
		if len(indices) > 0 {
			m.home.cursor = indices[0]
		}
		for _, i := range indices {
			if message.entries[i].id == selected {
				m.home.cursor = i
				// A late library response must not steal a new story already being typed.
				if first && !m.home.searching && m.home.focus == focusPremise && m.home.premise.Value() == "" {
					m.home.focus = focusLibrary
					m.home.premise.Blur()
				}
				break
			}
		}
		return m, nil
	case projectDeletedMsg:
		if message.err != nil {
			m.home.err = "删除失败：" + message.err.Error()
			return m, nil
		}
		m.home.notice = "已删除"
		if m.home.lastOpened == message.projectID {
			// 只清落点，读改写保住其他作品的折叠偏好；读失败不回写不覆盖。
			m.home.lastOpened = ""
			state, err := appconfig.LoadState(m.deps.ConfigDir)
			if err == nil {
				state.LastProjectID = ""
				err = appconfig.SaveState(m.deps.ConfigDir, state)
			}
			if err != nil {
				m.home.notice = "已删除（落点偏好未能更新：" + err.Error() + "）"
			}
		}
		return m, m.loadLibraryCmd()
	case importDoneMsg:
		m.home.importing = false
		if message.err != nil {
			m.home.err = message.err.Error()
			return m, nil
		}
		// 导入产出一份待批准草案：进入该作品工作台，用决定卡裁决。
		watch := m.openBench(message.projectID)
		m.bench.presentDecision(&decisionState{
			reason: "导入的草案等你批准", proposal: message.proposal, view: message.view, hasProposal: true,
		})
		return m, tea.Batch(m.refreshBenchCmd(), watch)
	case tea.MouseMsg:
		return m.handleHomeMouse(message)
	case tea.KeyMsg:
		switch m.home.mode {
		case homeSettings:
			return m.handleSettingsKey(message)
		case homeImport:
			return m.handleImportKey(message)
		}
		return m.handleHomeKey(message)
	}
	return m, nil
}

func (m model) handleHomeKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	home := &m.home
	if home.searching {
		return m.handleSearchKey(key)
	}
	if key.String() == "/" && home.focus != focusPremise {
		return m.beginLibrarySearch()
	}
	// 作品库焦点下 d=删除选中作品（危险操作，二次确认）；其他任意键取消确认。
	if home.focus == focusLibrary && len(m.libraryIndices()) > 0 && key.String() == "d" {
		entry := home.library[home.cursor]
		if home.confirmDelete == entry.id {
			home.confirmDelete, home.notice = "", ""
			return m.deleteProject(entry.id)
		}
		home.confirmDelete = entry.id
		home.err, home.notice = "", fmt.Sprintf("再按一次 d 永久删除《%s》，按其他键取消", truncate(entry.title(), 20))
		return m, nil
	}
	if home.confirmDelete != "" {
		home.confirmDelete, home.notice = "", ""
	}
	switch key.Type {
	case tea.KeyEsc:
		return m, tea.Quit
	case tea.KeyTab, tea.KeyShiftTab:
		delta := 1
		if key.Type == tea.KeyShiftTab {
			delta = -1
		}
		ring := m.focusRing()
		index := max(0, slices.Index(ring, home.focus))
		return m, home.setFocus(ring[(index+delta+len(ring))%len(ring)])
	case tea.KeyEnter:
		return m.activate(home.focus)
	case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd:
		if home.focus == focusLibrary {
			return m.moveLibrary(key.Type), nil
		}
	case tea.KeyLeft, tea.KeyRight:
		delta := 1
		if key.Type == tea.KeyLeft {
			delta = -1
		}
		switch home.focus {
		case focusLength:
			home.fixed = !home.fixed
			return m, nil
		case focusApproval:
			index := slices.Index(approvalOrder, home.approval)
			home.approval = approvalOrder[(index+delta+len(approvalOrder))%len(approvalOrder)]
			return m, nil
		}
	case tea.KeyBackspace:
		if home.focus == focusLength && home.fixed {
			home.chapters /= 10
			return m, nil
		}
	}
	// 在篇幅行直接敲数字就是固定篇幅并填章数。
	if home.focus == focusLength && len(key.Runes) == 1 && unicode.IsDigit(key.Runes[0]) {
		home.fixed = true
		if home.chapters < 1000 {
			home.chapters = home.chapters*10 + int(key.Runes[0]-'0')
		}
		return m, nil
	}
	// 焦点在别处时直接打字＝想写新书：自动聚焦故事输入框并录入，
	// 免得作品库预选把首次输入吞掉。
	if home.focus != focusPremise && key.Type == tea.KeyRunes {
		home.setFocus(focusPremise)
	}
	if home.focus == focusPremise {
		var cmd tea.Cmd
		home.premise, cmd = home.premise.Update(key)
		return m, cmd
	}
	return m, nil
}

// focusRing 是 Tab 遍历的控件；作品库没有可选作品时跳过它。
func (m model) focusRing() []int {
	ring := []int{focusPremise, focusLength, focusApproval, focusSettings, focusStart}
	if len(m.libraryIndices()) > 0 {
		ring = append(ring, focusLibrary)
	}
	return append(ring, focusSearch, focusImport, focusConfig)
}

func (h *homeState) setFocus(focus int) tea.Cmd {
	h.focus = focus
	if focus == focusPremise {
		return h.premise.Focus()
	}
	h.premise.Blur()
	return nil
}

// activate 是回车与点击的同一入口：新故事一栏里的回车都是开始创作。
func (m model) activate(focus int) (tea.Model, tea.Cmd) {
	home := &m.home
	switch focus {
	case focusLibrary:
		if len(m.libraryIndices()) > 0 {
			return m.openProject(home.library[home.cursor].id)
		}
		return m, nil
	case focusSearch:
		return m.beginLibrarySearch()
	case focusSettings:
		home.mode, home.err = homeSettings, ""
		return m, home.settings[home.settingStep].Focus()
	case focusImport:
		home.mode, home.err = homeImport, ""
		home.importIn = newInput("作品文件路径（Projection JSONC，例如 export 产物）")
		return m, home.importIn.Focus()
	case focusConfig:
		m.page = pageWizard
		m.wizard = newWizardState(m.api.Models.Config(), "", true)
		return m, nil
	}
	return m.createProject()
}

// handleSettingsKey 更多设定：回车或 Tab 到下一项，最后一项回车、或 Esc 随时带着已填内容回首页。
func (m model) handleSettingsKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	home := &m.home
	step := home.settingStep
	switch key.Type {
	case tea.KeyEsc:
		return m.closeSettings()
	case tea.KeyEnter, tea.KeyTab, tea.KeyDown:
		if key.Type == tea.KeyEnter && step == len(home.settings)-1 {
			return m.closeSettings()
		}
		step = min(step+1, len(home.settings)-1)
	case tea.KeyShiftTab, tea.KeyUp:
		step = max(step-1, 0)
	default:
		var cmd tea.Cmd
		home.settings[step], cmd = home.settings[step].Update(key)
		return m, cmd
	}
	home.settings[home.settingStep].Blur()
	home.settingStep = step
	return m, home.settings[step].Focus()
}

// closeSettings 回到首页并停在「开始创作」上：再按一次回车就开写。
func (m model) closeSettings() (tea.Model, tea.Cmd) {
	home := &m.home
	home.settings[home.settingStep].Blur()
	home.settingStep, home.mode = 0, homeMain
	return m, home.setFocus(focusStart)
}

func (m model) handleImportKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	home := &m.home
	switch key.Type {
	case tea.KeyEsc:
		if !home.importing {
			home.mode = homeMain
		}
		return m, nil
	case tea.KeyEnter:
		path := strings.TrimSpace(home.importIn.Value())
		if path == "" || home.importing {
			return m, nil
		}
		home.importing, home.err = true, ""
		return m, m.importProjectCmd(path)
	}
	var cmd tea.Cmd
	home.importIn, cmd = home.importIn.Update(key)
	return m, cmd
}

// importProjectCmd 导入作品文件：库中已有的作品走差异导入，新作品先建再导；
// 与 Headless `project import` 调同一 应用用例（workbench §9）。
func (m model) importProjectCmd(path string) tea.Cmd {
	api, ctx, user := m.api, m.ctx, m.deps.UserID
	known := make(map[string]bool, len(m.home.library))
	for _, entry := range m.home.library {
		known[entry.id] = true
	}
	return func() tea.Msg {
		var projection projectdoc.ProjectProjection
		if err := jsonc.DecodeFile(path, &projection); err != nil {
			return importDoneMsg{err: err}
		}
		now := time.Now().UTC()
		changeID := projectdoc.NewID("import", now)
		var proposal domainmodel.Proposal
		var err error
		if known[projection.ProjectID] {
			proposal, err = api.Projects.ImportProject(ctx, changeID, user, "导入作品文件", projection, now)
		} else {
			proposal, err = api.Projects.ImportNewProject(ctx, changeID, user, "导入作品文件", projection, now)
		}
		if err != nil {
			return importDoneMsg{err: err}
		}
		view, err := api.Workbench.DescribeProposal(ctx, projection.ProjectID, proposal)
		if err != nil {
			return importDoneMsg{err: err}
		}
		return importDoneMsg{projectID: projection.ProjectID, proposal: proposal, view: view}
	}
}

// createProject 创建作品并进入工作台开始创作；填了更多设定时先以完整 Intent 初始化，
// 再走同一条 QuickWrite 路径。
func (m model) createProject() (tea.Model, tea.Cmd) {
	home := &m.home
	premise := strings.TrimSpace(home.premise.Value())
	switch {
	case premise == "":
		home.err = "先写下想写的故事"
		return m, home.setFocus(focusPremise)
	case home.fixed && home.chapters == 0:
		home.err = "输入固定的章数，或切回 AI 决定篇幅"
		return m, home.setFocus(focusLength)
	}
	chapters := 0
	if home.fixed {
		chapters = home.chapters
	}
	projectID := projectdoc.NewID("book", time.Now())
	params := quickParams{
		projectID: projectID, premise: premise,
		chapters: chapters, approval: home.approval, intent: home.intent(premise),
	}
	watch := m.openBench(projectID)
	m.bench.writing = true
	return m, tea.Batch(m.startQuickWriteCmd(params), pollTick(m.bench.gen), m.bench.pulse.start(m.bench.gen), watch)
}

// openProject 打开作品库中的既有作品并恢复落点。
func (m model) openProject(projectID string) (tea.Model, tea.Cmd) {
	watch := m.openBench(projectID)
	return m, tea.Batch(m.refreshBenchCmd(), watch)
}

func (m model) deleteProject(projectID string) (tea.Model, tea.Cmd) {
	api, ctx := m.api, m.ctx
	return m, func() tea.Msg {
		return projectDeletedMsg{projectID: projectID, err: api.Projects.DeleteProject(ctx, projectID)}
	}
}

func splitList(text string) []string {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '，' })
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			items = append(items, part)
		}
	}
	return items
}

func (m model) viewHome() string {
	switch m.home.mode {
	case homeSettings:
		return m.viewHomeSettings()
	case homeImport:
		return m.viewHomeImport()
	}
	return m.homeFrame().text
}

func (m model) viewHomeSettings() string {
	width := m.entryWidth()
	lines := []string{
		benchTheme.Muted.Render(truncate(oneLine(m.home.premise.Value()), width)), "",
		benchTheme.Muted.Render("全部可选。这些是你的原话，会原样作为创作意图保留，AI 不会改写。"), "",
	}
	for i, field := range settingFields {
		input := m.home.settings[i]
		if i == m.home.settingStep {
			input.Width = max(1, width-4)
			input.SetCursor(input.Position())
			lines = append(lines, benchTheme.Accent.Bold(true).Render("▎ "+field.label), "  "+input.View(), "")
			continue
		}
		value := strings.TrimSpace(input.Value())
		if value == "" {
			value = benchTheme.Muted.Render("—")
		}
		label := "  " + field.label + strings.Repeat(" ", 10-lipgloss.Width(field.label))
		lines = append(lines, benchTheme.Muted.Render(label)+truncate(value, width-12), "")
	}
	return m.entryPage("更多设定", lines, m.home.err, "Enter / Tab 下一项 · Shift+Tab 上一项 · 最后一项回车或 Esc 完成")
}

func (m model) viewHomeImport() string {
	input := m.home.importIn
	input.Width = max(1, m.entryWidth()-4)
	input.SetCursor(input.Position())
	lines := []string{benchTheme.Muted.Render("从导出的作品文件继续创作。"), "", input.View(), ""}
	if m.home.importing {
		lines = append(lines, benchTheme.Accent.Render("正在导入…"))
	}
	return m.entryPage("导入作品", lines, m.home.err, "Enter 导入 · Esc 返回")
}
