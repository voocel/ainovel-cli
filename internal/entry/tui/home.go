package tui

import (
	"fmt"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/jsonc"
)

// 首页（workbench §4）：一句话输入为主体；其他入口只有完善创作设定、
// 导入作品、进入配置和作品库。三个创建入口只产生不同的初始化输入，
// 最终创建相同的 Project 与 CreationRun。
type homeMode int

const (
	homeMain homeMode = iota
	homeForm
	homeImport
)

const (
	focusPremise = iota
	focusChapters
	focusApproval
	focusRefine
	focusStart
	focusImport
	focusConfig
	focusSearch
	focusLibrary
)

type homeState struct {
	search          textinput.Model
	searching       bool
	chapterInput    textinput.Model
	editingChapters bool
	premise         textinput.Model
	chapters        int
	approval        domainmodel.ApprovalPolicy
	focus           int
	mode            homeMode
	form            []textinput.Model
	formStep        int
	importIn        textinput.Model
	importing       bool
	library         []libraryEntry
	cursor          int
	loaded          bool
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
	state   string
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
	err       error
}

var approvalOrder = []domainmodel.ApprovalPolicy{domainmodel.ApprovalAuto, domainmodel.ApprovalMilestone, domainmodel.ApprovalManual}

// 完善创作设定（workbench §4）：只是更完整的初始化输入，全部可空。
var formFields = []struct{ label, placeholder string }{
	{"受众", "写给谁看，例如：都市悬疑读者（可空，回车跳过）"},
	{"期待体验", "逗号分隔，例如：紧张,反转,治愈（可空）"},
	{"必须出现", "逗号分隔的元素或情节（可空）"},
	{"禁止出现", "逗号分隔（可空）"},
	{"结局方向", "例如：主角赢但付出代价（可空）"},
}

func newHomeState() homeState {
	premise := newInput("一句话说想写什么，回车开写")
	premise.Focus()
	return homeState{premise: premise, chapters: 3, approval: domainmodel.ApprovalAuto, search: newInput("搜索作品标题或 ID"), chapterInput: newInput("目标章数")}
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
			state := "空闲"
			if record.State != "" {
				state = runStateLabel(record.State)
			}
			entries = append(entries, libraryEntry{id: record.ID, premise: record.Premise, target: record.Target, written: record.Written, state: state})
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
			reason: "导入的草案等你批准", proposal: message.proposal, hasProposal: true,
		})
		m.switchContent(contentReview)
		return m, tea.Batch(m.refreshBenchCmd(), watch)
	case tea.MouseMsg:
		return m.handleHomeMouse(message)
	case tea.KeyMsg:
		switch m.home.mode {
		case homeForm:
			return m.handleFormKey(message)
		case homeImport:
			return m.handleImportKey(message)
		}
		return m.handleHomeKey(message)
	}
	return m, nil
}

func (m model) handleHomeKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	home := &m.home
	if home.searching || home.editingChapters {
		return m.handleHomeInline(key)
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
		title := entry.premise
		if title == "" {
			title = entry.id
		}
		home.confirmDelete = entry.id
		home.err, home.notice = "", fmt.Sprintf("再按一次 d 永久删除《%s》，按其他键取消", truncate(title, 20))
		return m, nil
	}
	if home.confirmDelete != "" {
		home.confirmDelete, home.notice = "", ""
	}
	switch key.Type {
	case tea.KeyEsc:
		return m, tea.Quit
	case tea.KeyTab, tea.KeyShiftTab:
		home.premise.Blur()
		limit := focusLibrary
		if len(home.library) > 0 {
			limit = focusLibrary + 1
		}
		delta := 1
		if key.Type == tea.KeyShiftTab {
			delta = -1
		}
		home.focus = (home.focus + delta + limit) % limit
		if home.focus == focusPremise {
			home.premise.Focus()
		}
		return m, nil
	case tea.KeyEnter:
		switch home.focus {
		case focusLibrary:
			if len(m.libraryIndices()) > 0 {
				return m.openProject(home.library[home.cursor].id)
			}
			return m, nil
		case focusSearch:
			return m.beginLibrarySearch()
		case focusChapters:
			home.editingChapters = true
			home.chapterInput.SetValue(strconv.Itoa(home.chapters))
			home.chapterInput.CursorEnd()
			home.premise.Blur()
			return m, home.chapterInput.Focus()
		case focusApproval:
			return m.handleHomeKey(tea.KeyMsg{Type: tea.KeyRight})
		case focusRefine:
			if strings.TrimSpace(home.premise.Value()) == "" {
				home.err = "先用一句话说想写什么，再完善设定"
				return m, nil
			}
			home.mode, home.formStep, home.err = homeForm, 0, ""
			home.form = make([]textinput.Model, len(formFields))
			for i, field := range formFields {
				home.form[i] = newInput(field.placeholder)
			}
			home.form[0].Focus()
			return m, nil
		case focusImport:
			home.mode, home.err = homeImport, ""
			home.importIn = newInput("作品文件路径（Projection JSONC，例如 export 产物）")
			home.importIn.Focus()
			return m, nil
		case focusConfig:
			m.page = pageWizard
			m.wizard = newWizardState(m.config, "", true)
			return m, nil
		}
		return m.createProject(nil)
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
		case focusChapters:
			home.chapters = max(1, home.chapters+delta)
			return m, nil
		case focusApproval:
			index := 0
			for i, policy := range approvalOrder {
				if policy == home.approval {
					index = i
				}
			}
			home.approval = approvalOrder[(index+delta+len(approvalOrder))%len(approvalOrder)]
			return m, nil
		}
	}
	// 焦点在别处时直接打字＝想写新书：自动聚焦一句话输入框并录入，
	// 免得作品库预选把首次输入吞掉。
	if home.focus != focusPremise && key.Type == tea.KeyRunes {
		home.focus = focusPremise
		home.premise.Focus()
	}
	if home.focus == focusPremise {
		var cmd tea.Cmd
		home.premise, cmd = home.premise.Update(key)
		return m, cmd
	}
	return m, nil
}

func (m model) handleFormKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	home := &m.home
	switch key.Type {
	case tea.KeyEsc:
		if home.formStep == 0 {
			home.mode = homeMain
			return m, nil
		}
		home.form[home.formStep].Blur()
		home.formStep--
		home.form[home.formStep].Focus()
		return m, nil
	case tea.KeyEnter:
		if home.formStep < len(home.form)-1 {
			home.form[home.formStep].Blur()
			home.formStep++
			home.form[home.formStep].Focus()
			return m, nil
		}
		intent := domainmodel.Intent{
			Premise:           strings.TrimSpace(home.premise.Value()),
			Audience:          strings.TrimSpace(home.form[0].Value()),
			DesiredExperience: splitList(home.form[1].Value()),
			Required:          splitList(home.form[2].Value()),
			Forbidden:         splitList(home.form[3].Value()),
			EndingDirection:   strings.TrimSpace(home.form[4].Value()),
			TargetChapters:    home.chapters,
		}
		return m.createProject(&intent)
	}
	var cmd tea.Cmd
	home.form[home.formStep], cmd = home.form[home.formStep].Update(key)
	return m, cmd
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
		return importDoneMsg{projectID: projection.ProjectID, proposal: proposal}
	}
}

// createProject 创建作品并进入工作台开始创作；intent 非空时（完善设定入口）
// 先以完整 Intent 初始化，再走同一条 QuickWrite 路径。
func (m model) createProject(intent *domainmodel.Intent) (tea.Model, tea.Cmd) {
	premise := strings.TrimSpace(m.home.premise.Value())
	if premise == "" {
		m.home.err = "先用一句话说想写什么"
		return m, nil
	}
	projectID := projectdoc.NewID("book", time.Now())
	params := quickParams{
		projectID: projectID, premise: premise,
		chapters: m.home.chapters, approval: m.home.approval, intent: intent,
	}
	watch := m.openBench(projectID)
	m.bench.writing = true
	return m, tea.Batch(m.startQuickWriteCmd(params), pollTick(m.bench.gen), watch)
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
	case homeForm:
		return m.viewHomeForm()
	case homeImport:
		return m.viewHomeImport()
	}
	return m.homeFrame().text
}

func (m model) viewHomeForm() string {
	width := m.entryWidth()
	lines := []string{benchTheme.Muted.Render(truncate(m.home.premise.Value(), width)), ""}
	for i, field := range formFields {
		if i == m.home.formStep {
			input := m.home.form[i]
			input.Width = max(1, width-4)
			input.SetCursor(input.Position())
			lines = append(lines, benchTheme.Accent.Render("▎ "+field.label), "  "+input.View(), "")
		} else {
			value := strings.TrimSpace(m.home.form[i].Value())
			if value == "" {
				value = "可选"
			}
			lines = append(lines, benchTheme.Muted.Render(truncate("  "+field.label+" · "+value, width)))
		}
	}
	return m.entryPage("完善创作设定", lines, m.home.err, "Enter 下一项 / 开始创作 · Esc 上一步")
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
