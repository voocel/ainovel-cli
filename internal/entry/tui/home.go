package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/app"
	"github.com/voocel/ainovel-cli/internal/entry/headless"
	"github.com/voocel/ainovel-cli/internal/service"
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
	focusImport
	focusConfig
	focusLibrary
)

type homeState struct {
	premise   textinput.Model
	chapters  int
	approval  domain.ApprovalPolicy
	focus     int
	mode      homeMode
	form      []textinput.Model
	formStep  int
	importIn  textinput.Model
	importing bool
	library   []libraryEntry
	cursor    int
	loaded    bool
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
	proposal  domain.Proposal
	err       error
}

var approvalOrder = []domain.ApprovalPolicy{domain.ApprovalAuto, domain.ApprovalMilestone, domain.ApprovalManual}

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
	return homeState{premise: premise, chapters: 3, approval: domain.ApprovalAuto}
}

// loadLibraryCmd 读取作品库：每本书的目标、进度与运行状态。
func (m model) loadLibraryCmd() tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		records, err := api.ListProjects(ctx)
		if err != nil {
			return libraryLoadedMsg{err: err}
		}
		entries := make([]libraryEntry, 0, len(records))
		for _, record := range records {
			entry := libraryEntry{id: record.ID, state: "空闲"}
			snapshot, err := api.Project(ctx, record.ID, domain.InitialRevision)
			if err != nil {
				return libraryLoadedMsg{err: err}
			}
			entry.premise = snapshot.Intent.Premise
			entry.target = snapshot.Intent.TargetChapters
			entry.written = len(snapshot.Manuscript)
			run, hasRun, err := api.LatestCreationRun(ctx, record.ID)
			if err != nil {
				return libraryLoadedMsg{err: err}
			}
			if hasRun {
				entry.state = runStateLabel(run.State)
			}
			entries = append(entries, entry)
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
		m.home.library, m.home.loaded, m.home.err = message.entries, true, ""
		if m.home.cursor >= len(message.entries) {
			m.home.cursor = 0
		}
		// 预选上次打开的作品：回车即回到它的工作台。
		for i, entry := range message.entries {
			if entry.id == m.home.lastOpened {
				m.home.cursor, m.home.focus = i, focusLibrary
				m.home.premise.Blur()
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
			state, err := app.LoadState(m.deps.ConfigDir)
			if err == nil {
				state.LastProjectID = ""
				err = app.SaveState(m.deps.ConfigDir, state)
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
		return m, tea.Batch(m.refreshBenchCmd(), watch)
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
	// 作品库焦点下 d=删除选中作品（危险操作，二次确认）；其他任意键取消确认。
	if home.focus == focusLibrary && len(home.library) > 0 && key.String() == "d" {
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
	case tea.KeyTab:
		home.premise.Blur()
		limit := focusLibrary
		if len(home.library) > 0 {
			limit = focusLibrary + 1
		}
		home.focus = (home.focus + 1) % limit
		if home.focus == focusPremise {
			home.premise.Focus()
		}
		return m, nil
	case tea.KeyEnter:
		switch home.focus {
		case focusLibrary:
			if len(home.library) > 0 {
				return m.openProject(home.library[home.cursor].id)
			}
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
	case tea.KeyUp, tea.KeyDown:
		if home.focus == focusLibrary && len(home.library) > 0 {
			if key.Type == tea.KeyUp && home.cursor > 0 {
				home.cursor--
			}
			if key.Type == tea.KeyDown && home.cursor < len(home.library)-1 {
				home.cursor++
			}
			return m, nil
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
		intent := domain.Intent{
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
// 与 Headless `project import` 调同一 Service 用例（workbench §9）。
func (m model) importProjectCmd(path string) tea.Cmd {
	api, ctx, user := m.api, m.ctx, m.deps.UserID
	known := make(map[string]bool, len(m.home.library))
	for _, entry := range m.home.library {
		known[entry.id] = true
	}
	return func() tea.Msg {
		var projection service.ProjectProjection
		if err := headless.DecodeFile(path, &projection); err != nil {
			return importDoneMsg{err: err}
		}
		now := time.Now().UTC()
		changeID := service.NewID("import", now)
		var proposal domain.Proposal
		var err error
		if known[projection.ProjectID] {
			proposal, err = api.ImportProject(ctx, changeID, user, "导入作品文件", projection, now)
		} else {
			proposal, err = api.ImportNewProject(ctx, changeID, user, "导入作品文件", projection, now)
		}
		if err != nil {
			return importDoneMsg{err: err}
		}
		return importDoneMsg{projectID: projection.ProjectID, proposal: proposal}
	}
}

// createProject 创建作品并进入工作台开始创作；intent 非空时（完善设定入口）
// 先以完整 Intent 初始化，再走同一条 QuickWrite 路径。
func (m model) createProject(intent *domain.Intent) (tea.Model, tea.Cmd) {
	premise := strings.TrimSpace(m.home.premise.Value())
	if premise == "" {
		m.home.err = "先用一句话说想写什么"
		return m, nil
	}
	projectID := service.NewID("book", time.Now())
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
		return projectDeletedMsg{projectID: projectID, err: api.DeleteProject(ctx, projectID)}
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
	home := m.home
	width := min(64, max(40, m.width-8))
	var view strings.Builder
	view.WriteString(splash(width))
	view.WriteString(marker(home.focus == focusPremise, "创作要求") + "\n")
	view.WriteString("  " + home.premise.View() + "\n\n")
	view.WriteString(marker(home.focus == focusChapters, fmt.Sprintf("目标章数    %d", home.chapters)) +
		styleHint.Render("  ←/→ 调整") + "\n")
	view.WriteString(marker(home.focus == focusApproval, "自动化程度  "+approvalLabel(home.approval)) +
		styleHint.Render("  ←/→ 切换，进入工作台后随时可调") + "\n")
	view.WriteString(marker(home.focus == focusRefine, "完善创作设定") +
		styleHint.Render("  受众 · 期待体验 · 必须/禁止 · 结局") + "\n")
	view.WriteString(marker(home.focus == focusImport, "导入作品") + "\n")
	view.WriteString(marker(home.focus == focusConfig, "配置模型") + "\n")
	if len(home.library) > 0 {
		view.WriteString("\n  " + styleTitle.Render("作品库") + "\n")
		for i, entry := range home.library {
			title := entry.premise
			if title == "" {
				title = entry.id
			}
			line := fmt.Sprintf("%s  %s %d/%d 章 · %s",
				truncate(title, max(10, width-28)),
				progressBar(entry.written, entry.target, 8), entry.written, entry.target,
				stateBadge(entry.state))
			view.WriteString(marker(home.focus == focusLibrary && i == home.cursor, line) + "\n")
		}
	} else if home.loaded {
		view.WriteString("\n  " + styleHint.Render("作品库还是空的，写下第一句话开始吧。") + "\n")
	}
	if home.err != "" {
		view.WriteString("\n  " + errLine(home.err))
	} else if home.notice != "" {
		view.WriteString("\n  " + noticeLine(home.notice))
	}
	hints := "回车 开写/打开 · Tab 切换焦点 · Esc 退出"
	if home.focus == focusLibrary {
		hints = "回车 打开 · d 删除 · Tab 切换焦点 · Esc 退出"
	}
	view.WriteString("\n  " + styleHint.Render(hints))
	return centerScreen(m.width, m.height, view.String())
}

func (m model) viewHomeForm() string {
	home := m.home
	var view strings.Builder
	view.WriteString(header("完善创作设定"))
	view.WriteString(styleHint.Render("创作要求  ") +
		truncate(strings.TrimSpace(home.premise.Value()), max(10, m.width-12)) + "\n\n")
	for i, field := range formFields {
		if i == home.formStep {
			view.WriteString(marker(true, field.label) + "\n")
			view.WriteString("  " + home.form[i].View() + "\n")
			continue
		}
		line := marker(false, field.label)
		if value := strings.TrimSpace(home.form[i].Value()); value != "" {
			line += styleHint.Render("  " + value)
		}
		view.WriteString(line + "\n")
	}
	view.WriteString("\n")
	view.WriteString(errLine(home.err))
	view.WriteString(styleHint.Render("回车 下一步（最后一步开写）· Esc 上一步"))
	return centerScreen(m.width, m.height, view.String())
}

func (m model) viewHomeImport() string {
	home := m.home
	var view strings.Builder
	view.WriteString(header("导入作品"))
	view.WriteString("  " + home.importIn.View() + "\n\n")
	if home.importing {
		view.WriteString(styleWarn.Render("正在导入…") + "\n")
	}
	view.WriteString(errLine(home.err))
	view.WriteString(styleHint.Render("回车 导入 · Esc 返回"))
	return centerScreen(m.width, m.height, view.String())
}
