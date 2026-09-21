package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// 双栏创作台：左栏目录，右栏「正文 / 活动」视图 + 常驻创作现场 + 决定卡；底栏输入。
// 几何只由终端尺寸、当前视图与是否有决定卡决定；渲染与鼠标命中共用同一份。
const (
	benchHeaderRows   = 2   // 标题/状态 + 进度分隔线
	benchFooterRows   = 3   // 分隔线（含反馈）+ 输入 + 提示
	benchTabRows      = 2   // 标签 + 分隔线
	benchSceneRows    = 8   // 创作现场：空行、标题线、输出尾部三行、前两步、当前步
	sceneTailRows     = 3   // 现场条里最新输出块的尾部行数
	sceneStepRows     = 3   // 现场条里的步骤行数（最后一行是当前步或等待计时）
	benchCardRows     = 4   // 决定卡：标题线、原因、变更摘要、操作
	directoryNoteRows = 3   // 目录底部：空行 + 字数/要求数 + 推进方式
	benchPad          = 2   // 主栏左右留白
	proseMaxWidth     = 120 // 正文页最宽（一行 60 个汉字）；更宽的终端把正文页在主栏内居中
	railThreshold     = 180 // 从这个宽度起显示右栏「本轮运行」
	railMinWidth      = 32
	railMaxWidth      = 48
)

type benchLayout struct {
	width, height                      int
	leftWidth, mainX, mainWidth, inner int // 分隔线在 x = leftWidth
	railX, railWidth                   int // 右栏（railWidth = 0 表示本帧没有右栏）
	bodyY, bodyHeight, footerY         int
	outlineY, outlineRows              int // 目录可见行起点与行数（标题行与截断指示行之外）
	tabsY, contentY, contentRows       int
	sceneY, cardY                      int // -1 表示该区本帧不存在
	proseX, proseWidth                 int // 正文页在主栏内的起点与宽度
}

func (m model) benchLayout() benchLayout {
	l := benchLayout{width: m.width, height: m.height, leftWidth: 28, sceneY: -1, cardY: -1}
	if m.width >= 160 {
		l.leftWidth = 32
	}
	l.mainX = l.leftWidth + 1
	l.mainWidth = l.width - l.mainX
	if m.width >= railThreshold {
		// 右栏吸收正文页之外的余量，让正文正好铺满中栏；余量不够时右栏取最小宽度。
		l.railWidth = min(railMaxWidth, max(railMinWidth, l.mainWidth-1-proseMaxWidth-2*benchPad))
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
	if m.bench.decision != nil {
		bottom -= benchCardRows
		l.cardY = bottom
	}
	if m.bench.view == viewProse {
		bottom -= benchSceneRows
		l.sceneY = bottom
	}
	l.contentRows = bottom - l.contentY
	l.proseWidth = min(l.inner, proseMaxWidth)
	l.proseX = (l.inner - l.proseWidth) / 2
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
		body = append(body, benchTheme.Muted.Render(fitLine("↑/↓ 滚动 · Esc 返回", m.width)))
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
		rail = m.runColumn(l)
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
	done, total := len(m.bench.snap.Manuscript), m.bench.snap.TargetChapters
	right := m.benchStateBadge() + benchTheme.Muted.Render(fmt.Sprintf("  ·  已入稿 %d / %d 章", done, total)) + " "
	brand := " " + benchTheme.Accent.Bold(true).Render("AINOVEL") + benchTheme.Muted.Render("  /  ")
	left := brand + benchTheme.Title.Render(truncate(title, max(4, l.width-lipgloss.Width(right)-lipgloss.Width(brand)-2)))
	return []string{alignRight(left, right, l.width), progressRule(done, total, l.width)}
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
		lines = append(lines, padMain(m.decisionCard(l.inner), l, benchCardRows)...)
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
	tabs := []benchTab{{view: viewProse, label: "正文"}, {view: viewActivity, label: "活动"}}
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
	case b.view == viewActivity && b.activityHeld != nil:
		return "已暂停跟随 · /follow 回到最新"
	case b.view == viewActivity:
		return "本轮创作过程 · 跟随最新"
	case b.proseHold:
		return "已暂停跟随 · /follow 回到最新"
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
	if m.bench.view == viewActivity {
		return padMain(m.activityView(l.inner, l.contentRows), l, l.contentRows)
	}
	lines := m.proseView(l)
	if indent := strings.Repeat(" ", l.proseX); indent != "" {
		for i := range lines {
			lines[i] = indent + lines[i]
		}
	}
	return padMain(lines, l, l.contentRows)
}

// decisionCard 只在等待用户决定时出现：琥珀标题线、原因、变更摘要、可做的事。
func (m model) decisionCard(width int) []string {
	d := m.bench.decision
	bar := benchTheme.Warning.Render("▎ ")
	title := "◇ 等你决定"
	summary, actions := "", "/continue 继续 · /budget 调整修订预算 · /stop 结束本轮"
	if d.hasProposal {
		title = "◇ " + m.reviewTarget() + " · 等待你确认"
		summary = m.candidateSummary()
		actions = "y 通过 · 写下修改意见后回车 · /review 查看完整变更"
		if d.stale {
			summary = benchTheme.Warning.Render("基线已过期 · 稿件完成后书又有了新变化，不能直接通过")
			actions = "写下修改意见后回车，让它基于最新内容重写 · /review 查看完整变更"
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
		if command.alias != "" {
			usage += " " + benchTheme.Muted.Render("/"+command.alias)
		}
		if command.usage != "" {
			usage += " " + benchTheme.Accent.Render(command.usage)
		}
		pad := strings.Repeat(" ", max(1, 20-lipgloss.Width(usage)))
		lines = append(lines, usage+pad+command.label)
	}
	return lines
}

// benchFooter：分隔线（反馈嵌在线里）、输入框 + 作用对象、主要操作与提示 + 模型用量。
func (m model) benchFooter(l benchLayout) []string {
	b := m.bench
	inner := l.width - 2
	input := b.input
	context := truncate(m.composerContext(), inner/3)
	input.Width = max(1, inner-lipgloss.Width(context)-5)
	input.Placeholder = m.inputPlaceholder()
	input.TextStyle = benchTheme.Text.Background(benchColors.InputBackground)
	input.PlaceholderStyle = lipgloss.NewStyle().Foreground(benchColors.Placeholder).Background(benchColors.InputBackground)
	input.Cursor.Style = benchTheme.Text
	input.Cursor.TextStyle = input.TextStyle
	if input.Value() != "" {
		// textinput 用 TextStyle 涂满整个宽度；灰底只跟着文字走。
		input.Width = min(input.Width, max(1, lipgloss.Width(input.Value())))
		input.SetCursor(input.Position())
	}
	feedback := ""
	switch {
	case b.err != "":
		feedback = benchTheme.Error.Render("! " + oneLine(b.err))
	case b.notice != "":
		feedback = benchTheme.Accent.Render(oneLine(b.notice))
	}
	rule := benchRule(l.width)
	if strings.TrimSpace(ansi.Strip(feedback)) != "" {
		feedback = truncate(feedback, inner-4)
		rule = benchRule(2) + " " + feedback + " " + benchRule(max(0, l.width-lipgloss.Width(feedback)-4))
	}
	return []string{
		rule,
		" " + alignRight(input.View(), context, inner),
		" " + alignRight(m.actionHints(), m.usageLabel(l), inner),
	}
}

func (m model) inputPlaceholder() string {
	d := m.bench.decision
	switch {
	case d == nil || !d.hasProposal || m.bench.writing:
		_, target := m.composerScope()
		return "给" + target + "提一个要求，或输入 / 命令"
	case d.stale:
		return "写下修改意见后回车，让它基于最新内容重写"
	default:
		return "写下修改意见后回车，或输入 y 通过"
	}
}

// composerContext 输入框右侧：这段文字将作用于谁。
func (m model) composerContext() string {
	if strings.HasPrefix(m.bench.input.Value(), "/note") {
		_, target := m.composerScope()
		return benchTheme.Accent.Render("要求 · " + target + " · 不裁决稿件")
	}
	if m.reviewing() {
		return benchTheme.Warning.Render("◇ 修改意见 · " + m.reviewTarget())
	}
	_, target := m.composerScope()
	return benchTheme.Accent.Render("要求 · " + target)
}

// usageLabel 模型名与本轮累计用量；没有用量、或右栏已在分列展示时只显示模型。
func (m model) usageLabel(l benchLayout) string {
	parts := []string{m.bindingLabel()}
	if feed, ok := m.activityFeed(); ok && feed.Usage.Input > 0 && l.railWidth == 0 {
		parts = append(parts, tokensLine(feed.Usage))
		if feed.Usage.Cost > 0 {
			parts = append(parts, formatCost(feed.Usage.Cost))
		}
	}
	return benchTheme.Muted.Render(truncate(strings.Join(parts, " · "), (l.width-2)/2))
}

type benchAction struct{ key, label string }

// primaryAction 当前最该做的一件事；其余全部在 / 命令面板里。
func (m model) primaryAction() benchAction {
	switch m.bench.situation() {
	case situationWriting, situationPausing, situationCancelling:
		return benchAction{"/pause", "暂停推进"}
	case situationDecidingProposal:
		return benchAction{"/review", "查看完整变更"}
	case situationCompleted:
		return benchAction{"/goal", "提高目标续写"}
	default:
		return benchAction{"/continue", "继续创作"}
	}
}

func (m model) actionHints() string {
	action := m.primaryAction()
	return benchTheme.Accent.Render(action.key) + " " + benchTheme.Muted.Render(action.label) +
		benchTheme.Muted.Render("   · / 命令 · Tab 切换区域 · ↑↓ 滚动 · Esc 返回")
}

// actionAt 提示行上主要操作的点击区间（与 actionHints 同一份文字）。
func (m model) actionAt(x int) (benchAction, bool) {
	action := m.primaryAction()
	if x >= 1 && x < 1+lipgloss.Width(action.key+" "+action.label) {
		return action, true
	}
	return benchAction{}, false
}
