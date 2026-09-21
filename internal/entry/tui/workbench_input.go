package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

const benchHelp = `创作控制台

正文视图：阅读选中章。正在写的章逐字流出并带光标；候选稿与已入稿正文明确标注。
活动视图：本轮创作的完整过程——每一步、思考、说明与正文预览按时间排列。
创作现场：正文下方常驻，显示上一步、当前步（或等待计时）与最新输出的尾部。
决定卡：有稿件等你确认时出现在正文下方；输入 y 通过，或写下修改意见后回车重写。

输入框里的文字：等你决定时是修改意见，其余时候是对选中章（或卷、后续章节）的创作要求。
开始输入时锁定作用对象，之后切章不改变已输入文字的语义。/note 内容（或 /n 内容）在任何时候独立提出要求。

/prose · /activity        切换正文、活动视图
/current                  定位正在写作／待确认／最近入稿的章节
/follow                   恢复跟随最新内容
/review                   全屏查看待确认稿件的完整变更
/think · /view            全屏查看思考原文、完整详情
/pause · /continue        暂停推进、继续创作（也可 /p · /c）
/goal <章数>              调整目标并继续
/budget <次数>            等待时调整自动修订预算
/accept <理由>            接受选中章第一条阻塞发现
/export <路径>            导出已确认正文（.txt 或 .epub）
/model [角色]             切换连接 / 模型 / 思考强度（角色：architect、writer、editor）
/stop · /diag             结束本轮、查看诊断
/<章号或标题> · /next     跳章或搜索、下一个匹配
/help                     本页

命令支持唯一前缀；有歧义时保留输入并列出可选命令。常用动作有单字母别名：/c 继续、/p 暂停、/n 要求。
Tab                       目录 ⇄ 正文切换焦点
↑ / ↓ · PgUp / PgDn       滚动焦点区域；输入为空时 Home/End 到顶／到底
Enter（空）               目录焦点：折叠卷弧；正文焦点：全屏阅读当前内容
Esc                       清空输入 → 关闭全屏 → 回到跟随 → 作品首页
F1 / F2                   正文、活动视图

点击章节选章；点击标签切换视图；点击决定卡阅读候选稿；点击现场报错行查看诊断。
思考仅展示模型服务提供的原文；直播内容不会自动成为正式稿，最终以确认稿为准。
暂停只停止后续推进，当前任务可能继续收尾。终端原生划选复制通常需要按住 Shift。`

func (m model) handleBenchKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.bench.diag != nil {
		return m.handleDiagnosticsKey(key)
	}
	if m.bench.modelPanel != nil {
		return m.handleModelPanelKey(key)
	}
	bench := &m.bench
	if bench.reading {
		if key.Type == tea.KeyEsc {
			bench.reading = false
			return m, nil
		}
		var cmd tea.Cmd
		bench.body, cmd = bench.body.Update(key)
		return m, cmd
	}
	// 输入框常驻聚焦：导航键归面板，其余一律进文本框，没有单键快捷操作。
	switch key.Type {
	case tea.KeyEsc:
		return m.escape()
	case tea.KeyEnter:
		return m.submitInput()
	case tea.KeyF1:
		m.switchView(viewProse)
		return m, nil
	case tea.KeyF2:
		m.switchView(viewActivity)
		return m, nil
	case tea.KeyTab, tea.KeyShiftTab:
		bench.pane = 1 - bench.pane
		return m, nil
	case tea.KeyUp:
		return m.scrollPane(bench.pane, -1), nil
	case tea.KeyDown:
		return m.scrollPane(bench.pane, 1), nil
	case tea.KeyPgUp, tea.KeyPgDown:
		if bench.pane == benchPaneOutline {
			return m.navigateOutline(key.Type), nil
		}
		delta := max(1, m.benchLayout().contentRows-1)
		if key.Type == tea.KeyPgUp {
			delta = -delta
		}
		return m.scrollPane(benchPaneMain, delta), nil
	case tea.KeyHome, tea.KeyEnd:
		if bench.input.Value() != "" {
			break
		}
		if bench.pane == benchPaneOutline {
			return m.navigateOutline(key.Type), nil
		}
		delta := -1 << 20
		if key.Type == tea.KeyEnd {
			delta = 1 << 20
		}
		return m.scrollPane(benchPaneMain, delta), nil
	}
	var cmd tea.Cmd
	wasEmpty := bench.input.Value() == ""
	bench.input, cmd = bench.input.Update(key)
	if wasEmpty && bench.input.Value() != "" {
		// 第一个字锁定作用对象：要求的范围，或正在裁决的稿件。
		bench.inputScope, bench.inputLabel = m.directiveScope()
		bench.inputDecision = ""
		if m.reviewing() {
			bench.inputDecision = bench.decision.proposal.ID
		}
	}
	if bench.input.Value() == "" {
		bench.inputScope, bench.inputLabel, bench.inputDecision = "", "", ""
	}
	return m, cmd
}

// escape 分层返回：清空输入 → 回到跟随（正文视图、当前章、最新内容）→ 作品首页。
func (m model) escape() (tea.Model, tea.Cmd) {
	b := &m.bench
	if b.input.Value() != "" {
		b.input.SetValue("")
		b.inputScope, b.inputLabel, b.inputDecision = "", "", ""
		return m, nil
	}
	if b.view == viewActivity || b.pinned || b.proseHold || b.activityHeld != nil {
		m.follow()
		return m, nil
	}
	b.close()
	m.page = pageHome
	m.home = newHomeState()
	m.home.lastOpened = b.projectID
	return m, m.loadLibraryCmd()
}

func (m *model) switchView(view benchView) {
	m.bench.view, m.bench.pane = view, benchPaneMain
}

// follow 回到创作进度：正文视图、当前章、尾部跟随；离开只取消 UI 订阅，不取消创作。
func (m *model) follow() {
	b := &m.bench
	b.view, b.pane, b.pinned = viewProse, benchPaneMain, false
	b.activityHeld, b.activityOffset = nil, 0
	b.proseOffset, b.proseHold = 0, false
	if number := m.currentChapter(); number > 0 {
		m.revealCurrent(number)
	}
}

// scrollPane 按焦点区分派滚动：目录移动选中章，主区滚正文或活动。
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
	if m.bench.view == viewActivity {
		return m.scrollActivity(delta)
	}
	return m.scrollProse(delta)
}

// submitInput 是回车的唯一分派：空回车走面板动作，`/` 是命令，`y` 通过，
// 其余文字在等你决定时是修改意见、否则是带作用域的要求。校验失败保留输入并显示 err。
func (m model) submitInput() (tea.Model, tea.Cmd) {
	bench := &m.bench
	text := strings.TrimSpace(bench.input.Value())
	bench.err, bench.notice = "", ""
	var (
		next tea.Model
		cmd  tea.Cmd
	)
	switch {
	case text == "":
		return m.enterPane()
	case strings.HasPrefix(text, "/"):
		next, cmd = m.runCommand(strings.TrimSpace(text[1:]))
	case text == "y":
		next, cmd = m.approve()
	case text == "n":
		bench.notice = "写下修改意见后回车"
		if !m.reviewing() {
			bench.notice = "现在没有等你决定的稿件"
		}
		next = m
	case m.reviewing():
		if bench.inputDecision != bench.decision.proposal.ID {
			bench.err = "新的稿件正在等待审阅；这段文字仍是创作要求，请用 /note 提交，或 Esc 清空后再写修改意见"
			return m, nil
		}
		next, cmd = m.decideCmd(false, text)
	default:
		if bench.inputDecision != "" {
			bench.err = "这份稿件已不再等待审阅；修改意见已保留，请 Esc 清空后重新选择操作"
			return m, nil
		}
		scope, _ := m.composerScope()
		next, cmd = m, m.addDirectiveCmd(scope, text)
	}
	updated := next.(model)
	if updated.bench.err == "" {
		updated.bench.input.SetValue("")
		updated.bench.inputScope, updated.bench.inputLabel, updated.bench.inputDecision = "", "", ""
	}
	return updated, cmd
}

// enterPane 空回车：目录焦点折叠卷弧，主区全屏阅读当前内容；批准必须由 y 明确确认。
func (m model) enterPane() (tea.Model, tea.Cmd) {
	b := &m.bench
	if b.pane == benchPaneOutline {
		rows := m.outlineRows()
		if b.cursor >= 0 && b.cursor < len(rows) && rows[b.cursor].header() {
			b.pinned = true
			b.notice = m.toggleFold(rows[b.cursor].node.Node.ID)
			return m, nil
		}
	}
	if b.view == viewActivity {
		feed, ok := m.timelineFeed()
		if !ok {
			b.notice = "本轮还没有活动"
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
		b.notice = "输入 y 通过，或写下修改意见后回车"
	case m.selectedChapterNumber() == 0:
		b.notice = "还没有可读的章节；写下要求，或 /continue 开始创作"
	default:
		b.notice = "这一章还没有正文"
	}
	return m, nil
}

func (m model) approve() (tea.Model, tea.Cmd) {
	bench := &m.bench
	switch {
	case bench.decision == nil || !bench.decision.hasProposal:
		bench.notice = "现在没有等你决定的稿件"
	case bench.writing:
		bench.notice = "创作进行中，稿件写完后再裁决"
	case bench.inputDecision != bench.decision.proposal.ID:
		bench.err = "待审稿件已变化，请 Esc 清空后重新确认"
	case bench.decision.stale:
		// 过期稿件不能直接通过（会撞版本冲突）：只留重写路径。
		bench.notice = "这份稿件完成后书又有了新变化，不能直接通过；写下修改意见后回车，让它基于最新内容重写"
	default:
		return m.decideCmd(true, "")
	}
	return m, nil
}

// benchCommand 是底栏 `/` 命令：唯一前缀匹配，命令面板与帮助共用同一张表。
// alias 给首字母撞车的常用动作一个单字母写法（/c 继续、/p 暂停、/n 要求）。
// idleOnly 的命令在创作进行中屏蔽，以免并发驱动；quiet 的不进 `/` 总览。
type benchCommand struct {
	name, alias, usage, label string
	idleOnly, quiet           bool
	run                       func(m model, arg string) (tea.Model, tea.Cmd)
}

var benchCommands = []benchCommand{
	{name: "current", label: "定位当前章节", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		m.returnToCurrentChapter()
		return m, nil
	}},
	{name: "prose", label: "正文视图", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		m.switchView(viewProse)
		return m, nil
	}},
	{name: "activity", label: "活动视图", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		m.switchView(viewActivity)
		return m, nil
	}},
	{name: "review", label: "查看待确认稿件的完整变更", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.openReview(), nil
	}},
	{name: "follow", label: "回到最新", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		m.follow()
		return m, nil
	}},
	{name: "note", alias: "n", usage: "<要求>", label: "提出创作要求（不裁决稿件）", run: func(m model, arg string) (tea.Model, tea.Cmd) {
		if arg == "" {
			m.bench.err = "用法：/note 要求内容；作用范围见输入框右侧"
			return m, nil
		}
		scope, _ := m.composerScope()
		return m, m.addDirectiveCmd(scope, arg)
	}},
	{name: "pause", alias: "p", label: "暂停推进", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		if b := &m.bench; b.hasRun() && (b.run().State == domainmodel.RunRunning || b.run().State == domainmodel.RunWaitingUser) {
			return m, m.pauseRunCmd()
		}
		m.bench.notice = "现在没有在推进的创作"
		return m, nil
	}},
	{name: "continue", alias: "c", label: "继续创作", idleOnly: true, run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.continueRun()
	}},
	{name: "goal", usage: "<章数>", label: "调整总章数并继续", idleOnly: true, run: func(m model, arg string) (tea.Model, tea.Cmd) {
		chapters, err := strconv.Atoi(arg)
		if err != nil || chapters <= 0 {
			m.bench.err = fmt.Sprintf("用法：/goal 章数（当前目标 %d 章）", m.currentTarget())
			return m, nil
		}
		return m.continueRunWith(chapters)
	}},
	{name: "budget", usage: "<次数>", label: "修订预算", idleOnly: true, run: func(m model, arg string) (tea.Model, tea.Cmd) {
		b := &m.bench
		if b.decision == nil || b.decision.hasProposal || !b.hasRun() {
			b.notice = "只有等待修订预算时才能调整"
			return m, nil
		}
		budget, err := strconv.Atoi(arg)
		if err != nil || budget <= 0 {
			b.err = fmt.Sprintf("用法：/budget 次数（当前 %d 次）", b.run().Strategy.AutoRepairBudget)
			return m, nil
		}
		return m, m.applyBudgetCmd(budget)
	}},
	{name: "accept", usage: "<理由>", label: "接受发现", idleOnly: true, run: func(m model, arg string) (tea.Model, tea.Cmd) {
		finding, ok := m.firstBlockingFinding()
		if !ok {
			m.bench.notice = "选中的章节没有待处理的阻塞发现"
			return m, nil
		}
		if arg == "" {
			m.bench.err = "用法：/accept 理由 · 接受发现「" + truncate(finding.Note, 40) + "」"
			return m, nil
		}
		return m, m.adjudicateCmd(finding.ID, arg)
	}},
	{name: "export", usage: "<路径>", label: "导出", run: func(m model, arg string) (tea.Model, tea.Cmd) {
		return m.submitExport(arg)
	}},
	{name: "think", label: "思考原文", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		if text := m.thinkingContent(); text != "" {
			return m.openBody(text), nil
		}
		m.bench.notice = "模型未提供思考文本"
		return m, nil
	}},
	{name: "view", label: "完整详情", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		return m.openBody(m.detailReport()), nil
	}},
	{name: "model", usage: "[角色]", label: "切换模型与思考强度", run: func(m model, arg string) (tea.Model, tea.Cmd) {
		return m.openModelPanel(arg)
	}},
	{name: "stop", label: "结束本轮", run: func(m model, _ string) (tea.Model, tea.Cmd) {
		if b := &m.bench; b.hasRun() && b.run().State != domainmodel.RunCompleted &&
			b.run().State != domainmodel.RunFailed && b.run().State != domainmodel.RunCancelled {
			return m, m.cancelRunCmd()
		}
		m.bench.notice = "没有可以结束的创作"
		return m, nil
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
		if command.name == name || command.alias == name {
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
		if command.idleOnly && m.bench.writing {
			m.bench.notice = "创作进行中，先 /pause 或等它写完"
			return m, nil
		}
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
func (m model) handleBenchMouse(mouse tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.bench.diag != nil {
		return m.handleDiagnosticsMouse(mouse)
	}
	bench := &m.bench
	if bench.reading {
		var cmd tea.Cmd
		bench.body, cmd = bench.body.Update(mouse)
		return m, cmd
	}
	l := m.benchLayout()
	if l.inRail(mouse.X) {
		return m, nil // 右栏只读，不滚不点
	}
	switch {
	case mouse.Button == tea.MouseButtonWheelUp || mouse.Button == tea.MouseButtonWheelDown:
		if !l.inBody(mouse.Y) {
			return m, nil
		}
		delta := 1
		if mouse.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		return m.scrollPane(l.paneAt(mouse.X), delta), nil
	case mouse.Action != tea.MouseActionPress || mouse.Button != tea.MouseButtonLeft:
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
		if bench.decision.hasProposal {
			return m.revealCandidate(), nil
		}
		return m, nil
	}
	if l.sceneY >= 0 && mouse.Y > l.sceneY && mouse.Y < l.sceneY+benchSceneRows && mouse.X >= l.mainX {
		if feed, ok := m.activityFeed(); ok {
			if step, ok := sceneStepAt(feed, mouse.Y-l.sceneY); ok && step.Err != "" && step.OperationID != "" {
				return m.openOperationDiagnostics(step.OperationID)
			}
		}
		return m, nil
	}
	if mouse.Y == l.footerY+2 {
		if action, ok := m.actionAt(mouse.X); ok {
			if bench.input.Value() != "" {
				bench.notice = "输入框中有未提交内容，请先提交或清空"
				return m, nil
			}
			command := action.key
			if action.key == "/goal" {
				command += " "
			}
			bench.input.SetValue(command)
			bench.inputScope, bench.inputLabel = m.directiveScope()
		}
		return m, nil
	}
	if l.inBody(mouse.Y) {
		bench.pane = l.paneAt(mouse.X)
	}
	return m, nil
}
