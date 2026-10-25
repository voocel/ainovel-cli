package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
)

const benchHelp = `创作控制台

输入框里的话都交给主编：提要求（“这章主角要输”“以后每章三千字左右”）、问进度、讨论情节、
退回稿件并说明理由、暂停或继续、调整篇幅都行。主编不写正文，它把你的意思落进书里，由创作团队
去写；它做的每件事都在对话里留一行回执。主编回话时再说的话会送进它正在进行的这一轮。

对话视图：你和主编的对话，以及主编做了什么。
正文视图：阅读选中章。正在写的章逐字流出并带光标；候选稿与已入稿正文明确标注。
活动视图：创作的完整过程——每一步、思考、说明与正文预览按时间排列。
创作现场：正文下方常驻，显示上一步、当前步（或等待计时）与最新输出的尾部。
决定卡：等你决定时出现在正文下方。有稿件时回车看全部内容，y 通过，不满意就跟主编说理由；
某章自动修订多次仍没做到你的要求时，y 接受现状，或跟主编说怎么改。只按审阅标准没改好的章
不停下，目录上标“!”、顶栏计数，接着写；选中它按 y 接受现状，或跟主编说怎么改。
确认卡：主编提出的修改要你确认时出现在同一位置，创作随之停下。回车看全部改动，y 确认，不要了
跟主编说；修改与已写章节有出入时，输入序号选怎么处理（重写受影响的章、只改设定后文按新的写、
不改了）。改错了跟主编说，它会查修改记录并撤销。

/view                     全屏查看选中章（或全书）的完整依据
/export <路径>            导出已确认正文（.txt 或 .epub）
/model [角色]             切换连接 / 模型 / 思考强度（角色：architect、writer、editor、chief）
/diag                     查看诊断
/<章号或标题> · /next     跳章或搜索、下一个匹配
/help                     本页

命令支持唯一前缀；有歧义时保留输入并列出可选命令。
Tab                       目录 ⇄ 正文切换焦点
↑ / ↓ · PgUp / PgDn       滚动焦点区域；输入为空时 Home/End 到顶／到底
Enter（空）               正文焦点：有稿件时看全部内容，创作停着时继续创作，否则全屏阅读；目录焦点：折叠卷弧、全屏阅读选中章
Esc                       清空输入 → 打断主编 → 回到最新 → 暂停创作 → 作品首页
F1 / F2 / F3              对话、正文、活动视图（也可点标签）

点击章节选章；点击标签切换视图；点击决定卡阅读候选稿；点击现场报错行查看诊断。
思考仅展示模型服务提供的原文；直播内容不会自动成为正式稿，最终以确认稿为准。
打开作品时，上次没停就退出的创作自动接着写；Esc 暂停立即停下在途的模型调用，对话与草稿保留，
之后回车，或跟主编说完一句话，都从停下的地方接着写。
终端原生划选复制通常需要按住 Shift。`

func (m model) handleBenchKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.bench.diag != nil {
		return m.handleDiagnosticsKey(key)
	}
	if m.bench.modelPanel != nil {
		return m.handleModelPanelKey(key)
	}
	bench := &m.bench
	if bench.reading {
		if key.String() == "esc" {
			bench.reading = false
			return m, nil
		}
		var cmd tea.Cmd
		bench.body, cmd = bench.body.Update(key)
		return m, cmd
	}
	// 输入框常驻聚焦：导航键归面板，其余一律进文本框，没有单键快捷操作。
	switch key.String() {
	case "esc":
		return m.escape()
	case "enter":
		return m.submitInput()
	case "f1":
		m.switchView(viewTalk)
		return m, nil
	case "f2":
		m.switchView(viewProse)
		return m, nil
	case "f3":
		m.switchView(viewActivity)
		return m, nil
	case "tab", "shift+tab":
		bench.pane = 1 - bench.pane
		return m, nil
	case "up":
		return m.scrollPane(bench.pane, -1), nil
	case "down":
		return m.scrollPane(bench.pane, 1), nil
	case "pgup", "pgdown":
		if bench.pane == benchPaneOutline {
			return m.navigateOutline(key.String()), nil
		}
		delta := max(1, m.benchLayout().contentRows-1)
		if key.String() == "pgup" {
			delta = -delta
		}
		return m.scrollPane(benchPaneMain, delta), nil
	case "home", "end":
		if bench.input.Value() != "" {
			break
		}
		if bench.pane == benchPaneOutline {
			return m.navigateOutline(key.String()), nil
		}
		delta := -1 << 20
		if key.String() == "end" {
			delta = 1 << 20
		}
		return m.scrollPane(benchPaneMain, delta), nil
	}
	var cmd tea.Cmd
	bench.input, cmd = bench.input.Update(key)
	return m, cmd
}

// handleBenchPaste 把粘贴交给正在输入的那一栏：诊断页的保存路径或常驻输入框；阅读与模型面板
// 里没有输入框。
func (m model) handleBenchPaste(paste tea.PasteMsg) (tea.Model, tea.Cmd) {
	b := &m.bench
	var cmd tea.Cmd
	switch {
	case b.diag != nil:
		if b.diag.input != nil {
			*b.diag.input, cmd = b.diag.input.Update(paste)
		}
	case b.modelPanel == nil && !b.reading:
		b.input, cmd = b.input.Update(paste)
	}
	return m, cmd
}

// escape 分层返回：清空输入 → 打断主编正在进行的回话 → 回到跟随（当前章、最新内容）→
// 暂停正在进行的创作（打断在途的模型调用）→ 作品首页。
func (m model) escape() (tea.Model, tea.Cmd) {
	b := &m.bench
	if b.input.Value() != "" {
		b.input.SetValue("")
		return m, nil
	}
	if b.session.Interrupt() {
		b.notice = "已打断主编"
		return m, nil
	}
	if b.view == viewActivity || b.pinned || b.proseHold || b.activityHeld != nil || b.talkHeld || b.outlineScrolled {
		m.follow()
		return m, nil
	}
	if b.situation() == situationWriting && b.hasRun() {
		return m, m.pauseRunCmd()
	}
	b.close()
	m.page = pageHome
	m.home = newHomeState(m.width)
	m.home.lastOpened = b.projectID
	return m, m.loadLibraryCmd()
}

func (m *model) switchView(view benchView) {
	m.bench.view, m.bench.pane = view, benchPaneMain
}

// follow 回到最新：对话视图留在对话，其余回到正文视图；目录回到当前章，各视图恢复尾部
// 跟随。离开只取消 UI 订阅，不取消创作。
func (m *model) follow() {
	b := &m.bench
	if b.view != viewTalk {
		b.view = viewProse
	}
	b.pane, b.pinned, b.outlineScrolled = benchPaneMain, false, false
	b.talkHeld, b.talkOffset = false, 0
	b.activityHeld, b.activityOffset = nil, 0
	b.proseOffset, b.proseHold = 0, false
	if number := m.currentChapter(); number > 0 {
		m.revealCurrent(number)
	}
}

// scrollPane 按焦点区分派键盘滚动：目录移动选中章，主区滚正文或活动；滚轮翻目录走 scrollOutline。
func (m model) scrollPane(pane benchPane, delta int) model {
	if pane == benchPaneOutline {
		step := 1
		if delta < 0 {
			step = -1
		}
		if next := nextSelectable(m.outlineRows(), m.bench.cursor, step); next != m.bench.cursor {
			m.selectOutline(next)
		}
		return m
	}
	switch m.bench.view {
	case viewActivity:
		return m.scrollActivity(delta)
	case viewTalk:
		return m.scrollTalk(delta)
	}
	return m.scrollProse(delta)
}

// submitInput 是回车的唯一分派：空回车走面板动作，`/` 是命令，`y` 通过，其余文字交给主编。
// 话没送出去时会话回执把它放回输入框。
func (m model) submitInput() (tea.Model, tea.Cmd) {
	bench := &m.bench
	text := strings.TrimSpace(bench.input.Value())
	bench.err, bench.notice = "", ""
	switch {
	case text == "":
		return m.enterPane()
	case strings.HasPrefix(text, "/"):
		next, cmd := m.runCommand(strings.TrimSpace(text[1:]))
		if updated := next.(model); updated.bench.err == "" {
			updated.bench.input.SetValue("")
			return updated, cmd
		}
		return next, cmd
	case text == "y":
		bench.input.SetValue("")
		return m.approve()
	case m.changeChoice(text) > 0:
		bench.input.SetValue("")
		return m.resolveChange(m.changeChoice(text))
	}
	bench.input.SetValue("")
	return m.sendCmd(text)
}

// enterPane 空回车：正文焦点有稿件等你确认时看全部内容（稿件过时了就按现在的内容重写），创作
// 停着时继续创作，否则全屏阅读当前内容；目录焦点折叠卷弧（尚未展开的空弧无动作）、全屏阅读
// 选中章。批准必须由 y 明确确认。
func (m model) enterPane() (tea.Model, tea.Cmd) {
	b := &m.bench
	if b.pane == benchPaneOutline {
		rows := m.outlineRows()
		if b.cursor >= 0 && b.cursor < len(rows) && rows[b.cursor].header() {
			b.pinned = true
			if rows[b.cursor].foldable() {
				b.notice = m.toggleFold(rows[b.cursor].node.Node.ID)
			}
			return m, nil
		}
	}
	if b.pane == benchPaneMain && b.snap.Change != nil {
		return m.openChange(), nil
	}
	if b.pane == benchPaneMain && m.reviewing() && b.decision.stale {
		return m.continueRun()
	}
	if b.pane == benchPaneMain && m.reviewing() {
		return m.openReview(), nil
	}
	if b.pane == benchPaneMain && b.resumable() {
		return m.continueRun()
	}
	if b.view == viewActivity {
		feed, ok := m.timelineFeed()
		if !ok {
			b.notice = "还没有创作活动"
			return m, nil
		}
		return m.openBody(strings.Join(m.timelineLines(feed, max(1, m.width-4)), "\n")), nil
	}
	if m.selectedChapterNumber() > 0 {
		if next, ok := m.openChapter(); ok {
			return next, nil
		}
	}
	switch {
	case m.reviewing():
		b.notice = "输入 y 通过；不满意就跟主编说理由"
	case m.selectedChapterNumber() == 0:
		b.notice = "还没有可读的章节"
	default:
		b.notice = "这一章还没有正文"
	}
	return m, nil
}

func (m model) approve() (tea.Model, tea.Cmd) {
	bench := &m.bench
	switch {
	case bench.snap.Change != nil:
		return m.confirmChange()
	case bench.decision != nil && bench.decision.held != "":
		return m.acceptHeld()
	case bench.decision == nil || !bench.decision.hasProposal:
		return m.acceptSelected()
	case bench.writing:
		bench.notice = "创作进行中，稿件写完后再裁决"
	case bench.decision.stale:
		// 过时的稿件不能直接通过（会撞版本冲突）：续跑时由后继按现在的内容重写（§5.5 第 4 条）。
		bench.notice = "这份稿件完成后书又有了新变化，不能直接通过；回车按现在的内容重写"
	default:
		return m.approveCmd()
	}
	return m, nil
}

// acceptSelected 接受选中那一章的现状（D94）：标记在案的章随时可以接受；没有未解决意见时
// 没有可决定的事，正在按意见重写的等它写完。
func (m model) acceptSelected() (tea.Model, tea.Cmd) {
	number := m.selectedChapterNumber()
	findings := m.unresolvedFindings(number)
	node, _ := m.outlineChapter(number)
	switch {
	case len(findings) == 0:
		m.bench.notice = "现在没有等你决定的稿件"
	case node.State == workbench.ChapterInProgress:
		m.bench.notice = "这一章正在按意见重写，写完再说"
	default:
		return m, m.acceptCmd(findings, number, false)
	}
	return m, nil
}

// benchCommand 是底栏 `/` 命令：唯一前缀匹配，命令面板与帮助共用同一张表。启停创作、
// 看稿用按键，提要求、退稿、调篇幅跟主编说，这里只留与书的内容无关的事。
// quiet 的不进 `/` 总览。
type benchCommand struct {
	name, usage, label string
	quiet              bool
	run                func(m model, arg string) (tea.Model, tea.Cmd)
}

var benchCommands = []benchCommand{
	{name: "export", usage: "<路径>", label: "导出", run: func(m model, arg string) (tea.Model, tea.Cmd) {
		return m.submitExport(arg)
	}},
	{name: "view", label: "完整详情", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.openBody(m.detailReport()), nil
	}},
	{name: "model", usage: "[角色]", label: "切换模型与思考强度", run: func(m model, arg string) (tea.Model, tea.Cmd) {
		return m.openModelPanel(arg)
	}},
	{name: "diag", label: "诊断", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.openDiagnostics()
	}},
	{name: "next", label: "下一个匹配", quiet: true, run: func(m model, _ string) (tea.Model, tea.Cmd) {
		if m.bench.search == "" || !m.findChapter(m.bench.search, true) {
			m.bench.notice = "先用 /章号 或 /标题 搜索"
		}
		return m, nil
	}},
	{name: "help", label: "帮助", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.openBody(benchHelp), nil
	}},
}

// matchCommand 按完整名称、别名或唯一前缀取命令；有歧义时需继续输入。
func matchCommand(name string) (benchCommand, bool) {
	var found *benchCommand
	for i := range benchCommands {
		command := &benchCommands[i]
		if command.name == name {
			return *command, true
		}
		if strings.HasPrefix(command.name, name) {
			if found != nil {
				return benchCommand{}, false
			}
			found = command
		}
	}
	if found == nil {
		return benchCommand{}, false
	}
	return *found, true
}

// runCommand 分派 `/` 后的文本：命令按表执行，其余按章节号或标题跳转。
func (m model) runCommand(text string) (tea.Model, tea.Cmd) {
	name, arg, _ := strings.Cut(text, " ")
	if name == "" || name == "?" {
		name = "help"
	}
	if command, ok := matchCommand(name); ok {
		return command.run(m, strings.TrimSpace(arg))
	}
	var matches []string
	for _, command := range benchCommands {
		if strings.HasPrefix(command.name, name) {
			matches = append(matches, "/"+command.name)
		}
	}
	if len(matches) > 1 {
		m.bench.err = "请继续输入以选择命令：" + strings.Join(matches, " 或 ")
		return m, nil
	}
	if m.findChapter(text, false) {
		if _, err := strconv.Atoi(text); err != nil {
			m.bench.notice = "/next 下一个匹配"
		}
		return m, nil
	}
	m.bench.err = "没有找到匹配的章节；/? 查看命令"
	return m, nil
}

// handleBenchMouse 滚轮滚动指针所在栏；点击目录选章或折叠、切标签、读候选稿、查报错诊断、填主要操作。
// 命中坐标全部来自同一份布局。阅读态转发给正文视口。
func (m model) handleBenchMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.bench.diag != nil {
		return m.handleDiagnosticsMouse(msg)
	}
	bench := &m.bench
	if bench.reading {
		var cmd tea.Cmd
		bench.body, cmd = bench.body.Update(msg)
		return m, cmd
	}
	mouse := msg.Mouse()
	l := m.benchLayout()
	if l.inRail(mouse.X) {
		return m, nil // 右栏只读，不滚不点
	}
	switch {
	case mouse.Button == tea.MouseWheelUp || mouse.Button == tea.MouseWheelDown:
		if !l.inBody(mouse.Y) {
			return m, nil
		}
		delta := 1
		if mouse.Button == tea.MouseWheelUp {
			delta = -1
		}
		if l.paneAt(mouse.X) == benchPaneOutline {
			return m.scrollOutline(delta), nil
		}
		return m.scrollPane(benchPaneMain, delta), nil
	case !leftClick(msg):
		return m, nil
	}
	if field, ok := m.modelPanelFieldAt(l, mouse.X, mouse.Y); ok {
		bench.modelPanel.focus = field
		return m, nil
	}
	if index, ok := m.outlineRowAt(l, mouse.X, mouse.Y); ok {
		m.clickOutlineRow(index)
		return m, nil
	}
	if mouse.Y == l.tabsY && mouse.X >= l.mainX {
		for _, tab := range m.benchTabs(l) {
			if mouse.X >= tab.x0 && mouse.X < tab.x1 {
				m.switchView(tab.view)
				return m, nil
			}
		}
	}
	if l.cardY >= 0 && mouse.Y >= l.cardY && mouse.Y < l.cardY+benchCardRows && mouse.X >= l.mainX {
		if bench.snap.Change != nil {
			return m.openChange(), nil
		}
		if bench.decision.hasProposal {
			return m.revealCandidate(), nil
		}
		return m, nil
	}
	if l.sceneY >= 0 && mouse.Y > l.sceneY && mouse.Y < l.sceneY+benchSceneRows && mouse.X >= l.mainX {
		if step, ok := m.sceneStepAt(mouse.Y-l.sceneY, l.inner); ok && failed(step) && step.OperationID != "" {
			return m.openOperationDiagnostics(step.OperationID)
		}
		return m, nil
	}
	if mouse.Y == l.footerY+2 {
		if action, ok := m.actionAt(mouse.X); ok {
			if bench.input.Value() != "" {
				bench.notice = "输入框中有未提交内容，请先提交或清空"
				return m, nil
			}
			return action.do(m)
		}
		return m, nil
	}
	if l.inBody(mouse.Y) {
		bench.pane = l.paneAt(mouse.X)
	}
	return m, nil
}
