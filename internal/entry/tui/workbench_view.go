package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

const benchHelp = `创作控制台

F1 实时输出：思考、模型说明与正文预览分块显示；上滚暂停跟随，/follow 返回最新。
F2 正文：阅读选中章节，清楚区分候选稿与已入稿正文。
F3 审阅：查看当前待确认方案；输入 y 批准，或写下修改意见后回车重写。

在实时输出与正文视图中，文字始终是创作要求，即使有稿件等待确认。
开始输入时固定要求范围或审阅对象，切换章节和视图不会改变已输入文字的作用对象。
/note 内容 在任意视图独立提出要求，不裁决稿件。

/stream · /body · /review   切换实时输出、正文、审阅
/split <20–50>             设置事件区高度占比（默认 25%）
/follow                    恢复实时输出跟随
/think                     全屏查看当前保留的思考原文
/view                      全屏查看要求、事实、发现与创作设定
/pause · /continue         暂停推进、继续创作
/goal <章数>               调整目标并继续
/budget <次数>             等待时调整自动修订预算
/accept <理由>             接受所选章节第一条阻塞发现
/export <路径>             导出已确认正文（.txt 或 .epub）
/stop · /diag              结束本轮、查看诊断
/<章号或标题> · /next      跳章或搜索、下一个匹配
/current                   定位正在写作／待确认／最近入稿的章节
/help                      本页

命令支持唯一前缀；有歧义时保留输入并列出可选命令。
Tab / Shift+Tab            目录 → 执行事件 → 内容区轮换焦点
↑ / ↓ · PgUp / PgDn        滚动焦点区域；输入为空时 Home/End 定位目录首尾
Enter（空）                折叠卷弧，或全屏阅读当前内容；不会批准稿件
Esc                        清空输入 → 关闭全屏 / 返回直播 → 作品首页

点击章节进入正文；点击标签切换视图；点击待办进入审阅。
点击输入框下方操作填入命令，回车执行；鼠标滚轮只滚动所在区域。
思考仅展示模型服务提供的原文，不是可靠解释；直播内容不会自动成为正式稿。
输出历史有界；前文截断会明确提示。完整作品与待确认稿始终通过正文和审阅查看。
暂停只停止后续推进，当前任务可能继续收尾。终端原生划选复制通常需要按住 Shift。`

// 三栏控制台：目录、执行事件与内容工作区、任务上下文。
// 布局只由终端尺寸决定，与决定卡、报错、输入态无关；渲染与鼠标命中共用同一份，
// 坐标不可能漂移。
const (
	benchHeaderRows   = 3
	benchFooterRows   = 5
	benchPad          = 2 // 正文栏左右留白
	benchOverviewRows = 4 // 概览卡：标题线 + 阶段/字数/全书
)

type benchLayout struct {
	width, height int
	// 列：导航 | 正文与现场 | 上下文；inner 为正文栏去掉留白后的宽度。
	leftWidth, mainX, mainWidth, inner int
	inspectorX, inspectorWidth         int
	// 行：顶栏 3 | 正文区 | 底栏 5。
	bodyY, bodyHeight, footerY int
	// 左栏：概览卡（benchOverviewRows）、空行、大纲（标题线、上截断指示、outlineRows 行、
	// 下截断指示）。指示行恒预留，首个大纲行的 y 是常量。
	outlineTitleY, outlineRowsY, outlineRows int
	// 中栏：执行事件、单行分隔线、内容标签与正文。
	proseRows, activityY, activityRows int
	contentY                           int
	// proseLines 是分页导航使用的正文可见行数。
	proseLines int
}

func (m model) benchLayout() benchLayout {
	l := benchLayout{width: m.width, height: m.height, leftWidth: 32, inspectorWidth: 32}
	if m.width >= 180 {
		l.leftWidth, l.inspectorWidth = 34, 36
	}
	l.mainX = l.leftWidth + 1
	l.inspectorX = m.width - l.inspectorWidth
	l.mainWidth = l.inspectorX - l.mainX - 1
	l.inner = l.mainWidth - 2*benchPad
	l.bodyY = benchHeaderRows
	l.bodyHeight = m.height - benchHeaderRows - benchFooterRows
	l.footerY = l.bodyY + l.bodyHeight
	l.outlineTitleY = l.bodyY + benchOverviewRows + 1
	l.outlineRowsY = l.outlineTitleY + 3
	l.outlineRows = l.footerY - l.outlineRowsY - 1
	percent := m.bench.splitPercent
	if percent == 0 {
		percent = 25
	}
	l.activityRows = max(6, l.bodyHeight*percent/100)
	l.activityY = l.bodyY
	l.contentY = l.bodyY + l.activityRows + 1
	l.proseRows = l.footerY - l.contentY - 1
	l.proseLines = l.proseRows - 3
	return l
}

func (l benchLayout) inBody(y int) bool { return y >= l.bodyY && y < l.footerY }

func (l benchLayout) paneAt(x, y int) benchPane {
	switch {
	case x < l.leftWidth:
		return benchPaneOutline
	case y < l.contentY:
		return benchPaneFeed
	default:
		return benchPaneMain
	}
}

// outlineRowAt 把大纲区内的坐标映射为大纲行下标；窗口与 viewOutlinePane 同一算法。
func (m model) outlineRowAt(l benchLayout, x, y int) (int, bool) {
	if x >= l.leftWidth || y < l.outlineRowsY || y >= l.outlineRowsY+l.outlineRows {
		return 0, false
	}
	rows := m.outlineRows()
	start, end := outlineWindow(len(rows), l.outlineRows, m.bench.cursor)
	if index := start + y - l.outlineRowsY; index < end {
		return index, true
	}
	return 0, false
}

type benchAction struct{ key, label string }

func fitLine(text string, width int) string {
	return ansi.Truncate(text, max(0, width), "…")
}

func textLines(text string, width int) []string {
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}

func fitBlock(text string, width, height int) []string {
	lines := textLines(text, width)
	result := make([]string, max(0, height))
	for i := range result {
		if i < len(lines) {
			result[i] = fitLine(lines[i], width)
		}
		result[i] += strings.Repeat(" ", max(0, width-lipgloss.Width(result[i])))
	}
	return result
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
	lines := m.viewBenchHeader(l)
	left, main := m.leftColumn(l), m.mainColumn(l)
	right := m.inspectorColumn(l)
	separator := benchTheme.Border.Render("│")
	for i := 0; i < l.bodyHeight; i++ {
		leftBorder, rightBorder := separator, separator
		if i == l.activityRows {
			leftBorder, rightBorder = benchTheme.Border.Render("├"), benchTheme.Border.Render("┤")
		}
		lines = append(lines, left[i]+leftBorder+main[i]+rightBorder+right[i])
	}
	lines = append(lines, m.benchFooter(l)...)
	return strings.Join(fitBlock(strings.Join(lines, "\n"), l.width, l.height), "\n")
}

// 顶栏分开呈现作品状态与模型用量，第三行分隔线兼作进度条。
func (m model) viewBenchHeader(l benchLayout) []string {
	title := m.bench.snap.Intent.Premise
	if title == "" {
		title = m.bench.projectID
	}
	done, total := len(m.bench.snap.Manuscript), m.currentTarget()
	right := benchTheme.Muted.Render(fmt.Sprintf("已入稿 %d / %d 章", done, total)) + "   " + m.benchStateBadge()
	brand := benchTheme.Accent.Bold(true).Render("AINOVEL") + "  "
	left := brand + benchTheme.Title.Render(truncate(title, max(4, l.width-lipgloss.Width(right)-lipgloss.Width(brand)-4)))
	gap := max(1, l.width-lipgloss.Width(left)-lipgloss.Width(right)-2)
	telemetry := alignRight(m.statusLine(), benchTheme.Muted.Render("Tab 切换区域   Esc 返回"), l.width-2)
	return []string{fitLine(" "+left+strings.Repeat(" ", gap)+right, l.width), " " + telemetry, progressRule(done, total, l.width)}
}

// progressRule 分隔线兼进度：已入稿比例用强调色实线，其余用边框色。
func progressRule(done, total, width int) string {
	width = max(1, width)
	filled := 0
	if total > 0 {
		filled = min(width, max(0, done)*width/total)
	}
	return benchTheme.Accent.Render(strings.Repeat("━", filled)) + benchTheme.Border.Render(strings.Repeat("─", width-filled))
}

func (m model) leftColumn(l benchLayout) []string {
	width := l.leftWidth - 1
	lines := append(m.overviewCard(width), "")
	lines = append(lines, m.viewOutlinePane(l)...)
	return fitBlock(strings.Join(lines, "\n"), l.leftWidth, l.bodyHeight)
}

// The inspector keeps decision context next to the manuscript, independent of reading focus.
func (m model) inspectorColumn(l benchLayout) []string {
	frame := m.teamFrame(l.inspectorWidth-2, l.bodyHeight)
	return indentBlock(frame.lines, l.inspectorWidth, l.bodyHeight)
}

// mainColumn stacks execution events, a divider, tabs and full-width content.
// Command suggestions overlay the bottom of the content without changing geometry.
func (m model) mainColumn(l benchLayout) []string {
	lines := indentBlock(m.activityStrip(l.mainWidth-2, l.activityRows), l.mainWidth, l.activityRows)
	lines = append(lines, benchRule(l.mainWidth))
	lines = append(lines, fitBlock(" "+m.contentTabs(l.mainWidth-2), l.mainWidth, 1)[0])
	lines = append(lines, indentBlock(m.contentPanel(l.inner, l.proseRows), l.mainWidth, l.proseRows)...)
	if overlay := m.commandOverlay(l.mainWidth - 2); len(overlay) > 0 {
		copy(lines[len(lines)-len(overlay):], indentBlock(overlay, l.mainWidth, len(overlay)))
	}
	return lines
}

func indentBlock(lines []string, width, height int) []string {
	for i := range lines {
		lines[i] = " " + lines[i]
	}
	return fitBlock(strings.Join(lines, "\n"), width, height)
}

// sectionTitle 分区标题线：标题后用边框色横线补满，焦点区标题带 ▎。
func sectionTitle(text string, width int, focused bool) string {
	title := paneTitle(text, focused)
	return title + " " + benchTheme.Border.Render(strings.Repeat("─", max(0, width-lipgloss.Width(title)-1)))
}

// overviewCard 左栏顶部：阶段、字数、全书的要求/事实/待核验计数，一眼看清全局。
func (m model) overviewCard(width int) []string {
	snap := m.bench.snap
	phase := snap.CurrentPhase
	if phase == "" {
		phase = m.benchPhaseLabel()
	}
	field := func(label, value string) string {
		return benchTheme.Muted.Render(label) + strings.Repeat(" ", 4) + fitLine(value, max(1, width-8))
	}
	return []string{
		sectionTitle("概览", width, false),
		field("阶段", phase),
		field("字数", fmt.Sprintf("%s · 已入稿 %d 章", groupDigits(m.wordCount()), len(snap.Manuscript))),
		benchTheme.Muted.Render(fitLine(fmt.Sprintf("要求 %d · 事实 %d · 待核验 %d", len(snap.Directives), len(snap.Canon), len(snap.PendingCanon)), width)),
	}
}

// activityStrip 正文下方的活动条（页面设计 §3）：最近事件带时钟与耗时，
// 末尾一行是等待计时或思考尾部；↑↓/滚轮翻历史时暂停跟随。
func (m model) activityStrip(width, rows int) []string {
	lines := []string{sectionTitle("执行事件", width, m.bench.pane == benchPaneFeed)}
	feed, ok := m.activityFeed()
	if !ok {
		return append(lines, benchTheme.Muted.Render("尚无实时活动 · /diag 查看历史"))
	}
	var tail []string
	if feed.Waiting {
		line := spinnerFrames[m.bench.spin%len(spinnerFrames)] + " 等待模型回应"
		if !feed.WaitingSince.IsZero() {
			line += fmt.Sprintf(" · %d 秒", int(time.Since(feed.WaitingSince).Seconds()))
		}
		tail = append(tail, styleFocus.Render(line))
	}
	if len(tail) == 0 {
		state := m.bench.snap.CurrentPhase
		if state == "" {
			state = m.benchPhaseLabel()
		}
		if feed.Thinking && (m.bench.writing || m.bench.snap.CurrentPhase != "") {
			state += " · 构思中"
		}
		tail = append(tail, benchTheme.Muted.Render(state+" · /split 调整分区"))
	}
	lines = append(lines, m.activityLines(width, max(1, rows-1-len(tail)))...)
	for len(lines) < rows-len(tail) {
		lines = append(lines, "")
	}
	return append(lines, tail...)
}

func contextLines(label, text string, width int) []string {
	lines := readingLines(label+" "+text, width)
	lines[0] = benchTheme.Accent.Render(label) + strings.TrimPrefix(lines[0], label)
	return lines
}

// factLabel 把事实值显示为可读文本：JSON 字符串去引号，其余原样。
func factLabel(value []byte) string {
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	return string(value)
}

func chapterStateLabel(state workbench.ChapterState) string {
	switch state {
	case workbench.ChapterConfirmed:
		return "已入稿"
	case workbench.ChapterPending:
		return "候选待确认"
	case workbench.ChapterInProgress:
		return "进行中"
	default:
		return "已规划"
	}
}

func (m model) chapterProse() (string, string, string) {
	chapter, badge, ok := m.selectedChapter(m.selectedChapterNumber())
	if !ok {
		text := m.bench.snap.NextStep
		if text == "" {
			text = "从一个故事开始。输入 /continue 继续创作，或直接写下要求。"
		}
		if m.bench.writing {
			text = m.bench.snap.CurrentPhase
			if text == "" {
				text = "正在构思与准备正文，可在上方查看执行事件，F1 查看实时输出。"
			}
		}
		return "作品总览", "尚无正文", text
	}
	state := "✓ 已入稿"
	if badge != "" {
		state = "◇ " + badge
	}
	var paragraphs []string
	for _, block := range chapter.Blocks {
		paragraphs = append(paragraphs, block.Text)
	}
	return fmt.Sprintf("第 %d 章 · %s", chapter.Number, chapter.Title), state, strings.Join(paragraphs, "\n\n")
}

// prosePanel 中栏：标题（焦点在正文时带 ▎）、状态、正文；直播时跟随尾部，冻结时停在冻结位。
func (m model) prosePanel(width, height int) []string {
	title, state, text := m.chapterProse()
	if m.bench.writing {
		state += " · 创作继续进行中"
	}
	lines := []string{benchTheme.Title.Render(fitLine(title, width)), benchTheme.Muted.Render(fitLine(state, width)), ""}
	body := m.bench.proseCache.wrap(text, width)
	capacity := max(1, height-len(lines))
	offset := min(m.bench.previewOffset, max(0, len(body)-capacity))
	for _, line := range body[offset:min(len(body), offset+capacity)] {
		lines = append(lines, benchTheme.Text.Render(line))
	}
	return fitBlock(strings.Join(lines, "\n"), width, height)
}

func (m model) scrollProse(delta int) tea.Model {
	l := m.benchLayout()
	_, _, text := m.chapterProse()
	last := max(0, len(m.bench.proseCache.wrap(text, l.inner))-max(1, l.proseRows-3))
	m.bench.previewOffset = min(last, max(0, m.bench.previewOffset+delta))
	return m
}

// The composer sits between two rules; context stays inline and hints share a row with the model.
func (m model) benchFooter(l benchLayout) []string {
	b := m.bench
	inner := l.width - 2
	input := b.input
	context := truncate(m.benchContext(), inner/3)
	input.Width = max(1, inner-lipgloss.Width(context)-5)
	input.Placeholder = m.inputPlaceholder()
	input.TextStyle = benchTheme.Text.Background(benchColors.InputBackground)
	input.PlaceholderStyle = lipgloss.NewStyle().Foreground(benchColors.Placeholder).Background(benchColors.InputBackground)
	input.Cursor.Style = benchTheme.Text
	input.Cursor.TextStyle = input.TextStyle
	if input.Value() != "" {
		// textinput paints padding with TextStyle too; keep the gray surface as short as the text.
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
	lowerRule := benchRule(l.width)
	if strings.TrimSpace(ansi.Strip(feedback)) != "" {
		feedback = truncate(feedback, inner-4)
		lowerRule = benchRule(2) + " " + feedback + " " + benchRule(max(0, l.width-lipgloss.Width(feedback)-4))
	}
	modelLabel := benchTheme.Muted.Render(truncate(m.config.Model, inner/4))
	return []string{
		benchRule(l.width),
		" " + alignRight(input.View(), context, inner),
		lowerRule,
		" " + alignRight(m.benchActionHints(), modelLabel, inner),
		"",
	}
}

func (m model) inputPlaceholder() string {
	d := m.bench.decision
	switch {
	case d == nil || !d.hasProposal || m.bench.writing || !m.composingReview():
		return "写下要求后回车，或输入 / 命令"
	case d.stale:
		return "写下修改意见后回车，让它基于最新内容重写"
	default:
		return "写下修改意见后回车，或输入 y 通过"
	}
}

// statusLine 顶栏运行信息：模型名与本轮累计用量。
func (m model) statusLine() string {
	var parts []string
	feed, _ := m.activityFeed()
	usage := feed.Usage
	parts = append(parts, fmt.Sprintf("本轮 Token · 输入 %s · 输出 %s", formatTokens(usage.Input), formatTokens(usage.Output)))
	cache := "缓存命中 —"
	if usage.Input > 0 {
		cache = fmt.Sprintf("缓存命中 %s / %.1f%%", formatTokens(usage.CacheRead), 100*float64(usage.CacheRead)/float64(usage.Input))
	}
	parts = append(parts, cache)
	if usage.Cost > 0 {
		parts = append(parts, formatCost(usage.Cost))
	}
	return benchTheme.Muted.Render(strings.Join(parts, " · "))
}

// commandOverlay 输入以 / 开头时的命令浮层：按前缀过滤，展示完整命令与用法；
// 无匹配时说明回车会按章号或标题跳转。
func (m model) commandOverlay(width int) []string {
	text := m.bench.input.Value()
	if !strings.HasPrefix(text, "/") {
		return nil
	}
	name, _, _ := strings.Cut(text[1:], " ")
	lines := []string{sectionTitle("命令 · 回车执行，支持唯一前缀", width, false)}
	var matches []benchCommand
	for _, command := range benchCommands {
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
		usage := "/" + command.name
		if command.usage != "" {
			usage += " " + command.usage
		}
		pad := strings.Repeat(" ", max(1, 20-lipgloss.Width(usage)))
		lines = append(lines, benchTheme.Accent.Render(usage)+pad+command.label)
	}
	return lines
}

func (m model) benchContext() string {
	if strings.HasPrefix(m.bench.input.Value(), "/note") {
		_, label := m.composerScope()
		return benchTheme.Accent.Render("创作要求 · " + label + " · 不裁决待确认稿件")
	}
	if d := m.bench.decision; d != nil && m.composingReview() {
		text := "◇ 等你决定 · " + m.reviewTarget()
		if d.hasProposal && d.stale {
			text = "◇ 等你决定 · 不能直接通过 · " + m.reviewTarget()
		}
		if !d.hasProposal {
			text += " · " + d.reason
		}
		return benchTheme.Warning.Render(text)
	}
	_, label := m.composerScope()
	return benchTheme.Accent.Render("创作要求 · " + label)
}

// benchActions 当前可用的操作：待决定时裁决键在前，其余按运行态给出。
func (m model) benchActions() []benchAction {
	b := m.bench
	var actions []benchAction
	// 等你决定时的 y / 修改意见由输入框占位文字承担，不再列为动作。
	switch {
	case b.decision != nil && !b.writing && b.decision.hasProposal:
		actions = append(actions, benchAction{"/review", "审阅稿件"}, benchAction{"/note", "另提要求"})
	case b.decision != nil && !b.writing:
		actions = append(actions, benchAction{"/c", "继续创作"}, benchAction{"/budget", "修订预算"})
	case b.writing:
		if b.hasRun() && (b.run().State == domainmodel.RunRunning || b.run().State == domainmodel.RunWaitingUser) {
			actions = append(actions, benchAction{"/p", "暂停推进"})
		}
	case b.hasRun() && b.run().State == domainmodel.RunCompleted:
		actions = append(actions, benchAction{"/goal", "提高目标续写"})
	default:
		actions = append(actions, benchAction{"/c", "继续创作"})
	}
	if len(b.snap.Manuscript) > 0 && !b.exporting {
		actions = append(actions, benchAction{"/e", "导出"})
	}
	if b.pinned || b.outputFrozen || b.feedOffset > 0 {
		actions = append(actions, benchAction{"/f", "回到最新"})
	}
	// Keep only the primary action here; the command picker exposes the full set.
	return append(actions[:min(1, len(actions))], benchAction{"/?", "更多"})
}

// Rendering and mouse hit testing share the same labels and spacing.
func (m model) benchActionHints() string {
	var hints []string
	for _, action := range m.benchActions() {
		hints = append(hints, benchTheme.Accent.Render(action.key)+" "+benchTheme.Muted.Render(action.label))
	}
	return strings.Join(hints, "   ") + benchTheme.Muted.Render("   · / 命令 · Tab 面板 · ↑↓ 滚动 · Enter 发送")
}

func (m model) benchActionAt(x int) (benchAction, bool) {
	start := 1
	for _, action := range m.benchActions() {
		end := start + lipgloss.Width(action.key+" "+action.label)
		if x >= start && x < end {
			return action, true
		}
		start = end + 3
	}
	return benchAction{}, false
}
