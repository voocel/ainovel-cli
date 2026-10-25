package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/voocel/ainovel-cli/internal/app/novel"
)

// 双栏创作台：左栏目录，右栏「正文 / 活动」视图 + 常驻创作现场 + 决定卡；底栏输入。
// 几何只由终端尺寸、当前视图与是否有决定卡决定；渲染与鼠标命中共用同一份。
const (
	benchHeaderRows   = 2   // 标题/状态 + 进度分隔线
	benchFooterRows   = 3   // 分隔线（含反馈）+ 输入 + 提示
	benchTabRows      = 2   // 标签 + 分隔线
	sceneBodyRows     = 8   // 现场条正文：创作时间线的最后几行，进行中的步骤钉在最后
	benchCardRows     = 4   // 决定卡：标题线、原因、变更摘要、操作
	directoryNoteRows = 3   // 目录底部：空行 + 字数/要求数 + 推进方式
	benchPad          = 2   // 主栏左右留白
	mainTargetWidth   = 120 // 有右栏时主栏内宽先保到这里，余量给右栏
	railThreshold     = 170 // 从这个宽度起显示右栏「写作依据」
	railMinWidth      = 32
	railMaxWidth      = 48

	// 创作现场：空行、标题线、时间线尾巴。
	benchSceneRows = 2 + sceneBodyRows
)

type benchLayout struct {
	width, height                      int
	leftWidth, mainX, mainWidth, inner int // 分隔线在 x = leftWidth
	railX, railWidth                   int // 右栏（railWidth = 0 表示本帧没有右栏）
	bodyY, bodyHeight, footerY         int
	outlineY, outlineRows              int // 目录可见行起点与行数（标题行与截断指示行之外）
	tabsY, contentY, contentRows       int
	sceneY, cardY                      int // -1 表示该区本帧不存在
}

func (m model) benchLayout() benchLayout {
	l := benchLayout{width: m.width, height: m.height, leftWidth: 28, sceneY: -1, cardY: -1}
	if m.width >= 160 {
		l.leftWidth = 32
	}
	l.mainX = l.leftWidth + 1
	l.mainWidth = l.width - l.mainX
	if m.width >= railThreshold {
		// 右栏吸收主栏目标内宽之外的余量；余量不够时右栏取最小宽度。
		l.railWidth = min(railMaxWidth, max(railMinWidth, l.mainWidth-1-mainTargetWidth-2*benchPad))
		l.mainWidth -= l.railWidth + 1
		l.railX = l.mainX + l.mainWidth + 1
	}
	l.inner = l.mainWidth - 2*benchPad
	l.bodyY = benchHeaderRows
	l.bodyHeight = l.height - benchHeaderRows - benchFooterRows
	l.footerY = l.bodyY + l.bodyHeight
	l.outlineY = l.bodyY + 2
	l.outlineRows = l.bodyHeight - 3 - directoryNoteRows
	l.tabsY = l.bodyY
	l.contentY = l.bodyY + benchTabRows
	bottom := l.footerY
	if m.bench.decision != nil || m.bench.snap.Change != nil {
		bottom -= benchCardRows
		l.cardY = bottom
	}
	if m.bench.view == viewProse {
		bottom -= benchSceneRows
		l.sceneY = bottom
	}
	l.contentRows = bottom - l.contentY
	return l
}

func (l benchLayout) inBody(y int) bool { return y >= l.bodyY && y < l.footerY }
func (l benchLayout) inRail(x int) bool { return l.railWidth > 0 && x >= l.railX-1 }

func (l benchLayout) paneAt(x int) benchPane {
	if x < l.leftWidth {
		return benchPaneOutline
	}
	return benchPaneMain
}

func (m model) viewWorkbench() string {
	if m.bench.diag != nil {
		return m.viewDiagnostics()
	}
	if m.bench.reading {
		body := fitBlock(m.bench.body.View(), max(1, m.width), max(1, m.height-1))
		body = append(body, fitLine(m.keyHint(benchTheme.Muted.Render("↑/↓ 滚动 · Esc 返回")), m.width))
		return strings.Join(body, "\n")
	}
	return m.viewBench()
}

func (m model) viewBench() string {
	l := m.benchLayout()
	lines := m.benchHeader(l)
	left, main := m.directoryColumn(l), m.mainColumn(l)
	var rail []string
	if l.railWidth > 0 {
		rail = m.railColumn(l)
	}
	separator := benchTheme.Border.Render("│")
	for i := 0; i < l.bodyHeight; i++ {
		line := left[i] + separator + main[i]
		if rail != nil {
			line += separator + rail[i]
		}
		lines = append(lines, line)
	}
	lines = append(lines, m.benchFooter(l)...)
	return strings.Join(fitBlock(strings.Join(lines, "\n"), l.width, l.height), "\n")
}

// benchHeader：品牌 / 作品名 …… 运行状态 · 入稿进度；第二行分隔线兼进度条。
func (m model) benchHeader(l benchLayout) []string {
	title := m.bench.snap.Intent.Premise
	if title == "" {
		title = m.bench.projectID
	}
	done, length := len(m.bench.snap.Manuscript), m.bench.snap.Length
	right := m.benchStateBadge() + benchTheme.Muted.Render("  ·  "+progressLabel(done, chapterCount(m.bench.snap), length))
	if unresolved := len(unresolvedChapters(m.bench.snap)); unresolved > 0 {
		right += benchTheme.Muted.Render("  ·  ") + benchTheme.Warning.Render(fmt.Sprintf("! %d 章未解决", unresolved))
	}
	right += " "
	brand := " " + benchTheme.Accent.Bold(true).Render("AINOVEL") + benchTheme.Muted.Render("  /  ")
	left := brand + benchTheme.Title.Render(truncate(title, max(4, l.width-lipgloss.Width(right)-lipgloss.Width(brand)-2)))
	return []string{alignRight(left, right, l.width), progressRule(done, length.Final, l.width)}
}

// progressLabel 是头部的入稿进度（D63 三态）：全书章数已知时给分母，AI 收官后注明；
// 开放期还有已规划到第几章与预计篇幅（滚动规划，D73）。
func progressLabel(done, planned int, length novel.Length) string {
	switch {
	case length.Final > 0 && length.Fixed == 0:
		return fmt.Sprintf("已入稿 %d / %d 章 · 收官", done, length.Final)
	case length.Final > 0:
		return fmt.Sprintf("已入稿 %d / %d 章", done, length.Final)
	case length.Compass != nil && length.Compass.ScaleMax > 0:
		return fmt.Sprintf("已入稿 %d 章 · 已规划 %d · 预计 %d", done, planned, length.Compass.ScaleMax)
	default:
		return fmt.Sprintf("已入稿 %d 章 · 篇幅待定", done)
	}
}

// lengthStatus 是篇幅设定的一句话描述，供命令提示使用。
func lengthStatus(length novel.Length) string {
	switch {
	case length.Fixed > 0:
		return fmt.Sprintf("固定 %d 章", length.Fixed)
	case length.Final > 0:
		return fmt.Sprintf("AI 已承诺 %d 章收官", length.Final)
	case length.Compass != nil && length.Compass.ScaleMax > 0:
		return fmt.Sprintf("AI 决定，预计 %d 章", length.Compass.ScaleMax)
	default:
		return "AI 决定"
	}
}

// padMain 给主栏行加左留白并裁到主栏宽度。
func padMain(lines []string, l benchLayout, height int) []string {
	pad := strings.Repeat(" ", benchPad)
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return fitBlock(strings.Join(lines, "\n"), l.mainWidth, height)
}

// mainColumn 自上而下：标签、内容、创作现场、决定卡；命令浮层或模型面板覆盖内容区底部。
func (m model) mainColumn(l benchLayout) []string {
	lines := m.tabRows(l)
	lines = append(lines, m.contentLines(l)...)
	if l.sceneY >= 0 {
		lines = append(lines, padMain(m.sceneLines(l.inner), l, benchSceneRows)...)
	}
	if l.cardY >= 0 {
		card := m.decisionCard
		if m.bench.snap.Change != nil {
			card = m.changeCard
		}
		lines = append(lines, padMain(card(l.inner), l, benchCardRows)...)
	}
	overlay := m.commandOverlay(l.inner)
	if m.bench.modelPanel != nil {
		overlay = m.modelPanelLines(l.inner)
	}
	if len(overlay) > 0 {
		end := benchTabRows + l.contentRows
		start := max(benchTabRows, end-len(overlay))
		copy(lines[start:end], padMain(overlay[len(overlay)-(end-start):], l, end-start))
	}
	return fitBlock(strings.Join(lines, "\n"), l.mainWidth, l.bodyHeight)
}

type benchTab struct {
	view   benchView
	label  string
	x0, x1 int // 标签在整屏上的列区间 [x0, x1)
}

func (m model) benchTabs(l benchLayout) []benchTab {
	tabs := []benchTab{{view: viewTalk, label: "对话"}, {view: viewProse, label: "正文"}, {view: viewActivity, label: "活动"}}
	x := l.mainX + benchPad
	for i := range tabs {
		tabs[i].x0, tabs[i].x1 = x, x+lipgloss.Width(tabs[i].label)
		x = tabs[i].x1 + 3
	}
	return tabs
}

// tabRows：标签行（当前标签强调色，右侧是视图状态说明）+ 分隔线（当前标签下方实线）。
func (m model) tabRows(l benchLayout) []string {
	var labels []string
	rule := []rune(strings.Repeat("─", l.mainWidth))
	for _, tab := range m.benchTabs(l) {
		if tab.view == m.bench.view {
			labels = append(labels, benchTheme.Accent.Bold(true).Render(tab.label))
			for x := tab.x0; x < tab.x1; x++ {
				rule[x-l.mainX] = '━'
			}
		} else {
			labels = append(labels, benchTheme.Muted.Render(tab.label))
		}
	}
	note := benchTheme.Muted.Render(m.viewNote())
	row := alignRight(strings.Repeat(" ", benchPad)+strings.Join(labels, "   "), note+" ", l.mainWidth)
	return []string{row, benchTheme.Border.Render(string(rule))}
}

func (m model) viewNote() string {
	b := m.bench
	switch {
	case b.view == viewTalk && b.talkHeld:
		return "已暂停跟随 · Esc 回到最新"
	case b.view == viewTalk:
		return ""
	case b.view == viewActivity && b.activityHeld != nil:
		return "已暂停跟随 · Esc 回到最新"
	case b.view == viewActivity:
		return "创作过程 · 跟随最新"
	case b.proseHold:
		return "已暂停跟随 · Esc 回到最新"
	case m.proseSource().live:
		return "实时预览 · 最终以入稿版本为准"
	case b.writing && b.pinned:
		if number := m.currentChapter(); number > 0 {
			return fmt.Sprintf("正在写第 %d 章 · Esc 回到当前章", number)
		}
	}
	return ""
}

func (m model) contentLines(l benchLayout) []string {
	switch m.bench.view {
	case viewActivity:
		return padMain(m.activityView(l.inner, l.contentRows), l, l.contentRows)
	case viewTalk:
		return padMain(m.talkView(l.inner, l.contentRows), l, l.contentRows)
	}
	return padMain(m.proseView(l), l, l.contentRows)
}

// decisionCard 只在等待用户决定时出现：琥珀标题线、原因、变更摘要、可做的事。
func (m model) decisionCard(width int) []string {
	d := m.bench.decision
	bar := benchTheme.Warning.Render("▎ ")
	title := "◇ 等你决定"
	summary, actions := "", "回车继续"
	if d.held != "" {
		summary = m.heldSummary()
		actions = "y 接受现状 · 回车阅读这一章 · 或跟主编说怎么改"
	}
	if d.hasProposal {
		title = "◇ " + m.reviewTarget() + " · 等待你确认"
		summary = m.candidateSummary()
		actions = m.decisionActions() + " · 回车查看全部"
		if d.stale {
			actions = m.decisionActions()
			summary = benchTheme.Warning.Render("已过时 · 稿件完成后书又有了新变化，不能直接通过")
		}
	}
	return []string{
		sectionTitle(benchTheme.Warning.Bold(true).Render(title), width),
		bar + fitLine(oneLine(d.reason), width-2),
		bar + fitLine(summary, width-2),
		bar + benchTheme.Muted.Render(fitLine(actions, width-2)),
	}
}

// commandOverlay 输入以 / 开头时的命令面板：按前缀过滤；无匹配时说明回车会按章号或标题跳转。
func (m model) commandOverlay(width int) []string {
	text := m.bench.input.Value()
	if !strings.HasPrefix(text, "/") {
		return nil
	}
	name, _, _ := strings.Cut(text[1:], " ")
	lines := []string{sectionTitle(benchTheme.Muted.Render("命令 · 回车执行，支持唯一前缀"), width)}
	var matches []benchCommand
	for _, command := range benchCommands {
		if command.quiet && name == "" {
			continue
		}
		if strings.HasPrefix(command.name, name) {
			matches = append(matches, command)
		}
	}
	if len(matches) == 0 {
		if _, err := strconv.Atoi(name); err == nil {
			return append(lines, "回车跳到第 "+name+" 章")
		}
		return append(lines, "回车搜索标题「"+name+"」")
	}
	const shown = 7
	for i, command := range matches {
		if i == shown {
			lines = append(lines, benchTheme.Muted.Render(fmt.Sprintf("… 还有 %d 个，继续输入首字母筛选", len(matches)-shown)))
			break
		}
		usage := benchTheme.Accent.Render("/" + command.name)
		if command.usage != "" {
			usage += " " + benchTheme.Accent.Render(command.usage)
		}
		pad := strings.Repeat(" ", max(1, 20-lipgloss.Width(usage)))
		lines = append(lines, usage+pad+command.label)
	}
	return lines
}

// benchFooter：分隔线（反馈嵌在线里）、输入框、主要操作与提示 + 模型用量。
func (m model) benchFooter(l benchLayout) []string {
	b := m.bench
	inner := l.width - 2
	input := b.input
	input.SetWidth(max(1, inner-5))
	input.Placeholder = m.inputPlaceholder()
	if input.Value() != "" {
		// textinput 用正文样式涂满整个宽度；灰底只跟着文字走。
		input.SetWidth(min(input.Width(), max(1, lipgloss.Width(input.Value()))))
		input.SetCursor(input.Position())
	}
	feedback := ""
	switch {
	case b.err != "":
		feedback = benchTheme.Error.Render("! " + oneLine(b.err))
	case b.notice != "":
		feedback = benchTheme.Accent.Render(oneLine(b.notice))
	}
	hints := m.keyHint(m.actionHints())
	return []string{
		feedbackRule(feedback, l.width),
		" " + input.View(),
		" " + alignRight(hints, m.usageLabel(l, inner-lipgloss.Width(hints)-2), inner),
	}
}

func (m model) inputPlaceholder() string {
	switch {
	case m.bench.thread.Busy:
		return "接着说，主编会在这一轮里读到"
	case m.bench.decision != nil && m.bench.decision.held != "":
		return "y 接受现状，或跟主编说这一章怎么改"
	case m.bench.resumable():
		return "回车继续创作，或跟主编说说想法"
	case m.reviewing():
		return "y 通过，或跟主编说哪里不满意"
	case len(m.unresolvedFindings(m.selectedChapterNumber())) > 0:
		return "y 接受这一章的现状，或跟主编说这一章怎么改"
	default:
		return "跟主编说：提要求、问进度、讨论情节…（/ 命令）"
	}
}

// usageLabel 模型摘要与全书累计用量，占提示之外的宽度，放不下时截掉尾部；没有用量、
// 或右栏已在分列展示时只显示模型摘要。
func (m model) usageLabel(l benchLayout, room int) string {
	parts := []string{m.bindingLabel()}
	if total := m.bench.snap.Usage.Total; total.Input > 0 && l.railWidth == 0 {
		parts = append(parts, spendLine(total))
	}
	return benchTheme.Muted.Render(truncate(strings.Join(parts, " · "), room))
}

// benchAction 是提示行上的主要操作：点击即执行 do。
type benchAction struct {
	key, label string
	do         func(model) (tea.Model, tea.Cmd)
}

// primaryAction 当前最该做的一件事；其余全部在 / 命令面板里。
func (m model) primaryAction() benchAction {
	if m.bench.thread.Busy {
		return benchAction{key: "Esc", label: "打断主编", do: model.escape}
	}
	if m.bench.snap.Change != nil && !m.bench.writing {
		return benchAction{key: "Enter", label: "查看修改", do: func(m model) (tea.Model, tea.Cmd) { return m.openChange(), nil }}
	}
	switch m.bench.situation() {
	case situationWriting:
		return benchAction{key: "Esc", label: "暂停", do: func(m model) (tea.Model, tea.Cmd) { return m, m.pauseRunCmd() }}
	case situationPausing, situationCancelling:
		return benchAction{key: "Esc", label: "返回", do: model.escape}
	case situationDecidingProposal:
		return benchAction{key: "Enter", label: "查看全部内容", do: func(m model) (tea.Model, tea.Cmd) { return m.openReview(), nil }}
	case situationCompleted:
		return benchAction{key: "Enter", label: "阅读选中章", do: model.enterPane}
	}
	switch {
	case !m.bench.hasRun():
		return benchAction{key: "Enter", label: "开始创作", do: model.continueRun}
	case m.bench.decision != nil && m.bench.decision.held != "":
		return benchAction{key: "Enter", label: "阅读这一章", do: model.enterPane}
	}
	return benchAction{key: "Enter", label: "继续创作", do: model.continueRun}
}

func (m model) actionHints() string {
	action := m.primaryAction()
	rest := "   · / 命令 · Tab 切换区域 · ↑↓ 滚动"
	if action.key != "Esc" {
		rest += " · Esc 返回"
	}
	return benchTheme.Accent.Render(action.key) + " " + benchTheme.Muted.Render(action.label) + benchTheme.Muted.Render(rest)
}

// actionAt 提示行上主要操作的点击区间（与 actionHints 同一份文字）；提示行换成退出确认
// 时没有可点的操作。
func (m model) actionAt(x int) (benchAction, bool) {
	if m.quitArmed {
		return benchAction{}, false
	}
	action := m.primaryAction()
	if x >= 1 && x < 1+lipgloss.Width(action.key+" "+action.label) {
		return action, true
	}
	return benchAction{}, false
}
