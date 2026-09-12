package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

type benchTab int

const (
	benchTabProse benchTab = iota
	benchTabActivity
	benchTabDetail
)

const benchHelp = `工作台

Tab       左右区域切换；窄屏切换目录与主区
1 / 2 / 3 正文 / 活动 / 详情
o         聚焦目录；窄屏展开目录
↑ / ↓     选章或滚动当前视图
Enter     展开卷/弧，或全屏阅读选中章
t / T     展开思考片段 / 全屏查看原文尾部
f         返回实时正文，恢复跟随
i         添加要求（输入框会显示作用范围）
y / n     批准待确认稿件 / 输入修改意见
a         为选中章节的阻塞发现填写接受理由
c / p     继续创作 / 暂停推进
g / b     调整目标 / 等待时调整修订预算
x / d     结束本轮 / 诊断
Esc       关闭输入或阅读 → 返回跟随 → 作品首页

点击标签、章节、思考标题及底部操作均可执行对应动作。
鼠标滚轮作用于指针所在区域。输入时不启用单键快捷操作。
暂停推进后当前任务可能继续收尾，之后不启动下一项。
思考仅展示模型提供的原文尾部，不是对正文的可靠解释。
/ 输入章节号或标题定位；N 下一个匹配。
PgUp/PgDn 翻页，Home/End 定位目录首尾。
e 导出已确认正文，输入 .txt 或 .epub 文件路径。已有文件不会覆盖。
终端原生选择复制通常需要按住 Shift（取决于终端设置）。`

// A frame owns both rendered cells and hit targets. No separate mouse geometry
// can drift when a decision, an input or a narrow terminal changes the layout.
type benchHit struct {
	x, y, width int
	key         string
	row         int
}

type benchFrame struct {
	text            string
	hits            []benchHit
	bodyHeight      int
	proseHeight     int
	detailMaxOffset int
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

func (m model) outlineWidth() int { return min(32, max(24, m.width/5)) }

func (m model) mainWidth() int {
	if m.multiPane() {
		return max(1, m.width-m.outlineWidth()-1)
	}
	return max(1, m.width)
}

// Controls wrap at display-cell boundaries, and each wrapped button keeps its
// own hit target. The action is dispatched through the same keyboard handler.
func actionRows(actions []benchAction, width, x, y int, selected ...string) ([]string, []benchHit) {
	rows := []string{""}
	var hits []benchHit
	for _, action := range actions {
		label := fitLine("["+action.label+"]", width)
		used, size := lipgloss.Width(rows[len(rows)-1]), lipgloss.Width(label)
		if used > 0 && used+size+1 > width {
			rows = append(rows, "")
			used = 0
		}
		if used > 0 {
			rows[len(rows)-1] += " "
			used++
		}
		hits = append(hits, benchHit{x: x + used, y: y + len(rows) - 1, width: size, key: action.key})
		style := benchTheme.Muted
		if len(selected) > 0 && selected[0] == action.key {
			style = benchTheme.Selected
		}
		rows[len(rows)-1] += style.Render(label)
	}
	return rows, hits
}

func (m model) benchDock() ([]string, []benchHit) {
	b := m.bench
	w := max(1, m.width)
	lines := []string{benchRule(w)}
	var hits []benchHit
	appendActions := func(actions []benchAction) {
		rows, targets := actionRows(actions, w-2, 1, len(lines))
		for _, row := range rows {
			lines = append(lines, " "+row)
		}
		hits = append(hits, targets...)
	}
	if b.decision != nil {
		d := b.decision
		lines = append(lines, " "+benchTheme.Warning.Render(fitLine("◇ 等你决定 · "+d.reason, w-2)))
		if d.hasProposal {
			if d.stale {
				lines = append(lines, " "+benchTheme.Warning.Render(fitLine("稿件基于旧版本，不能直接通过", w-2)))
			}
			if !b.writing && !b.input.Focused() {
				actions := []benchAction{{"n", "n 修改意见"}}
				if !d.stale {
					actions = append([]benchAction{{"y", "y 批准并继续"}}, actions...)
				}
				appendActions(actions)
			}
		} else if !b.writing {
			appendActions([]benchAction{{"c", "c 继续创作"}, {"b", "b 修订预算"}})
		}
	}
	if b.err != "" {
		lines = append(lines, " "+benchTheme.Error.Render(fitLine("! "+oneLine(b.err), w-2)))
	} else if b.notice != "" {
		lines = append(lines, " "+benchTheme.Accent.Render(fitLine(oneLine(b.notice), w-2)))
	}
	if b.prompt != nil {
		input := b.prompt.input
		input.Width = max(1, w-5)
		lines = append(lines, " "+benchTheme.Accent.Render(fitLine(b.prompt.label, w-2)), " "+input.View())
	} else if b.input.Focused() {
		input := b.input
		input.Width = max(1, w-5)
		lines = append(lines, " "+input.View())
	} else {
		actions := []benchAction{{"i", "i 提要求"}, {"t", "t 思考"}}
		if len(b.snap.Manuscript) > 0 && !b.exporting {
			actions = append(actions, benchAction{"e", "e 导出"})
		}
		if b.writing {
			if b.hasRun() && (b.run().State == domainmodel.RunRunning || b.run().State == domainmodel.RunWaitingUser) {
				actions = append([]benchAction{{"p", "p 暂停推进"}}, actions...)
			}
		} else if b.decision == nil {
			actions = append([]benchAction{{"c", "c 继续创作"}}, actions...)
		}
		if b.pinned || b.liveHeld || b.feedOffset > 0 {
			actions = append(actions, benchAction{"f", "f 回到最新"})
		}
		appendActions(actions)
		_, label := m.directiveScope()
		hits = append(hits, benchHit{x: 1, y: len(lines), width: w - 2, key: "i"})
		lines = append(lines, " "+benchTheme.Muted.Render(fitLine("› "+label+"…  [i]", w-2)))
	}
	hint := "/ 跳章或搜索 · Tab 切区 · 1 正文 2 活动 3 详情 · ? 更多"
	if w < 75 {
		hint = "Tab 切区 · ? 更多 · Esc 返回"
	}
	if b.prompt != nil || b.input.Focused() {
		hint = "Enter 提交 · Esc 取消输入"
	}
	lines = append(lines, " "+benchTheme.Muted.Render(fitLine(hint, w-2)))
	return lines, hits
}

func (m model) workbenchFrame() benchFrame {
	w, h := max(1, m.width), max(1, m.height)
	if w < 30 || h < 10 {
		return benchFrame{text: strings.Join(fitBlock("请扩大终端以使用工作台\nEsc 返回作品首页", w, h), "\n")}
	}
	dock, dockHits := m.benchDock()
	bodyHeight := max(1, h-2-len(dock))
	frame := benchFrame{bodyHeight: bodyHeight}
	mainWidth, mainX := m.mainWidth(), 0
	if m.multiPane() {
		mainX = m.outlineWidth() + 1
	}
	main, hits, proseHeight, detailMaxOffset := m.mainPanel(mainWidth, bodyHeight)
	frame.proseHeight = proseHeight
	frame.detailMaxOffset = detailMaxOffset
	for _, hit := range hits {
		hit.x += mainX
		hit.y += 2
		frame.hits = append(frame.hits, hit)
	}
	if !m.multiPane() && m.bench.view == 1 {
		main = fitBlock(m.viewOutlinePane(w-2, bodyHeight), w, bodyHeight)
	}
	if m.multiPane() || m.bench.view == 1 {
		rows := m.outlineRows()
		start, end, clipped := outlineWindow(len(rows), max(1, bodyHeight-1), m.bench.cursor)
		y := 3
		if clipped && start > 0 {
			y++
		}
		width := m.outlineWidth()
		if !m.multiPane() {
			width = w
			frame.hits = nil
		}
		frame.hits = append(frame.hits, benchHit{x: 0, y: 2, width: width, key: "/"})
		for i := start; i < end; i++ {
			frame.hits = append(frame.hits, benchHit{x: 0, y: y, width: width, key: "chapter", row: i})
			y++
		}
	}
	visible := frame.hits[:0]
	for _, hit := range frame.hits {
		if hit.y >= 2 && hit.y < 2+bodyHeight && hit.x >= 0 && hit.x+hit.width <= w {
			visible = append(visible, hit)
		}
	}
	frame.hits = visible
	lines := strings.Split(m.viewBenchTopBar(), "\n")
	if m.multiPane() {
		left := fitBlock(m.viewOutlinePane(m.outlineWidth()-2, bodyHeight), m.outlineWidth(), bodyHeight)
		for i := 0; i < bodyHeight; i++ {
			lines = append(lines, left[i]+benchTheme.Border.Render("│")+main[i])
		}
	} else {
		lines = append(lines, main...)
	}
	for _, hit := range dockHits {
		hit.y += 2 + bodyHeight
		frame.hits = append(frame.hits, hit)
	}
	lines = append(lines, dock...)
	frame.text = strings.Join(fitBlock(strings.Join(lines, "\n"), w, h), "\n")
	return frame
}

func (m model) viewWorkbench() string {
	if m.bench.reading {
		body := fitBlock(m.bench.body.View(), max(1, m.width), max(1, m.height-1))
		body = append(body, benchTheme.Muted.Render(fitLine("↑/↓ 滚动 · Esc 返回", m.width)))
		return strings.Join(body, "\n")
	}
	return m.workbenchFrame().text
}

func (m model) benchPaneAt(x int) benchPane {
	if m.multiPane() && x < m.outlineWidth() {
		return benchPaneOutline
	}
	return benchPaneMain
}

func (m model) clickBench(x, y int) (tea.Model, tea.Cmd) {
	frame := m.workbenchFrame()
	for _, hit := range frame.hits {
		if y != hit.y || x < hit.x || x >= hit.x+hit.width {
			continue
		}
		if hit.key != "chapter" {
			return m.handleBenchKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(hit.key)})
		}
		rows := m.outlineRows()
		row := rows[hit.row]
		if row.placeholder {
			return m, nil
		}
		m.bench.pane, m.bench.cursor, m.bench.pinned = benchPaneOutline, hit.row, true
		if row.header() {
			m.bench.notice = m.toggleFold(row.node.Node.ID)
		} else {
			m.bench.pinned, m.bench.liveHeld = true, false
			m.bench.previewOffset, m.bench.detailOffset = 0, 0
			if !m.multiPane() {
				m.bench.view = 0
			}
		}
		return m, nil
	}
	if y >= 2 && y < 2+frame.bodyHeight {
		m.bench.pane = m.benchPaneAt(x)
	}
	return m, nil
}

func (m model) viewBenchTopBar() string {
	w := max(1, m.width)
	title := m.bench.snap.Intent.Premise
	if title == "" {
		title = m.bench.projectID
	}
	right := m.benchStateBadge()
	if w >= 75 {
		right = benchTheme.Muted.Render(fmt.Sprintf("%d/%d 章 · ", len(m.bench.snap.Manuscript), m.currentTarget())) + right
	}
	left := benchTheme.Accent.Render("AINOVEL") + benchTheme.Border.Render(" / ") + benchTheme.Title.Render(truncate(title, max(4, w-lipgloss.Width(right)-14)))
	left = fitLine(left, max(1, w-lipgloss.Width(right)-2))
	gap := max(1, w-lipgloss.Width(left)-lipgloss.Width(right)-1)
	return fitLine(" "+left+strings.Repeat(" ", gap)+right, w) + "\n" + benchRule(w)
}

func (m model) mainPanel(width, height int) ([]string, []benchHit, int, int) {
	padding := 2
	inner := max(1, width-2*padding)
	tabActions := []benchAction{{"1", "1 正文"}, {"2", "2 活动"}, {"3", "3 详情"}}
	if !m.multiPane() {
		tabActions = append([]benchAction{{"o", "o 目录"}}, tabActions...)
	}
	tabs, hits := actionRows(tabActions, inner, padding, 0, fmt.Sprint(int(m.bench.tab)+1))
	lines := append(tabs, "")
	available := max(1, height-len(lines))
	proseHeight := available
	detailMaxOffset := 0
	switch m.bench.tab {
	case benchTabActivity:
		process, processHits := m.processPanel(inner, available, true)
		for _, hit := range processHits {
			hit.x += padding
			hit.y += len(lines)
			hits = append(hits, hit)
		}
		lines = append(lines, process...)
	case benchTabDetail:
		text := m.viewDetailPane(inner)
		all := textLines(text, inner)
		detailMaxOffset = max(0, len(all)-available)
		offset := min(m.bench.detailOffset, detailMaxOffset)
		lines = append(lines, all[offset:]...)
	default:
		processHeight := 0
		if available >= 15 {
			processHeight = 5
			if m.bench.thoughtOpen {
				processHeight += 3
			}
		}
		proseHeight = max(1, available-processHeight)
		lines = append(lines, m.prosePanel(inner, proseHeight)...)
		if processHeight > 0 {
			process, processHits := m.processPanel(inner, processHeight, false)
			for _, hit := range processHits {
				hit.x += padding
				hit.y += len(lines)
				hits = append(hits, hit)
			}
			lines = append(lines, process...)
		}
	}
	for i := range lines {
		lines[i] = strings.Repeat(" ", padding) + fitLine(lines[i], inner)
	}
	return fitBlock(strings.Join(lines, "\n"), width, height), hits, proseHeight, detailMaxOffset
}

func (m model) processPanel(width, height int, full bool) ([]string, []benchHit) {
	feed, ok := m.activityFeed()
	lines := []string{benchTheme.Border.Render("─ AI 创作现场 " + strings.Repeat("─", max(0, width-14)))}
	label := "▸ 思考片段"
	if m.bench.thoughtOpen {
		label = "▾ 思考片段"
	}
	if feed.Thinking {
		label += " · 构思中"
	}
	lines = append(lines, benchTheme.Muted.Render(label+"  [t]"))
	hits := []benchHit{{x: 0, y: 1, width: min(width, lipgloss.Width(label)+5), key: "t"}}
	if m.bench.thoughtOpen {
		thought := "模型未提供思考文本"
		if feed.ThinkingNote != "" {
			thought = feed.ThinkingNote
		}
		limit := min(3, max(1, height-5))
		if full {
			limit = min(8, max(1, height/3))
		}
		wrapped := m.bench.thoughtCache.wrap(thought, max(1, width-2))
		if len(wrapped) > limit {
			wrapped = wrapped[len(wrapped)-limit:]
		}
		for _, line := range wrapped {
			lines = append(lines, benchTheme.Muted.Render("│ "+line))
		}
		if full && feed.ThinkingNote != "" {
			hits = append(hits, benchHit{x: 0, y: len(lines), width: min(width, 28), key: "T"})
			lines = append(lines, benchTheme.Accent.Render("[T 全屏查看原文尾部]"))
		}
	}
	if ok {
		remaining := max(0, height-len(lines))
		events := textLines(strings.TrimSuffix(m.viewActivity(width, max(1, remaining)), "\n"), width)
		if len(events) > remaining {
			events = events[len(events)-remaining:]
		}
		lines = append(lines, events...)
	} else {
		lines = append(lines, benchTheme.Muted.Render("尚无实时活动 · 历史记录按 d 查看"))
	}
	return fitBlock(strings.Join(lines, "\n"), width, height), hits
}

func (m model) liveProse() (string, bool) {
	if m.bench.liveHeld {
		return m.bench.liveText, true
	}
	if !m.bench.pinned && (m.bench.writing || m.bench.snap.CurrentPhase != "") {
		if feed, ok := m.activityFeed(); ok && len(feed.Prose) > 0 {
			return string(feed.Prose), true
		}
	}
	return "", false
}

func (m model) chapterProse() (string, string, string) {
	chapter, badge, ok := m.selectedChapter(m.selectedChapterNumber())
	if !ok {
		text := m.bench.snap.NextStep
		if text == "" {
			text = "从一个故事开始。按 c 继续创作，按 i 添加要求。"
		}
		if m.bench.writing {
			text = m.bench.snap.CurrentPhase
			if text == "" {
				text = "正在构思与准备正文，可按 2 查看实时活动。"
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

func (m model) prosePanel(width, height int) []string {
	var title, state, text string
	liveText, live := m.liveProse()
	if live {
		title, state, text = "正在落笔", "正文预览 · 最终以确认稿为准", liveText
		for _, row := range m.bench.snap.Outline {
			if row.State == workbench.ChapterInProgress && !m.bench.liveHeld {
				title = fmt.Sprintf("第 %d 章 · %s", row.Number, row.Node.Title)
				break
			}
		}
	}
	if !live {
		title, state, text = m.chapterProse()
	}
	if m.bench.pinned && m.bench.writing {
		state += " · 创作继续进行中"
	}
	if m.bench.liveHeld {
		state = "预览 · 已暂停跟随 · f 回到最新"
	}
	lines := []string{benchTheme.Muted.Render(fitLine(state, width)), benchTheme.Title.Render(fitLine(title, width)), ""}
	body := m.bench.proseCache.wrap(text, min(width, 88))
	capacity := max(1, height-len(lines))
	offset := min(m.bench.previewOffset, max(0, len(body)-capacity))
	if live {
		offset = max(0, len(body)-capacity)
		if m.bench.liveHeld {
			offset = min(m.bench.liveOffset, offset)
		}
	}
	for _, line := range body[offset:min(len(body), offset+capacity)] {
		lines = append(lines, benchTheme.Text.Render(line))
	}
	return fitBlock(strings.Join(lines, "\n"), width, height)
}

func (m model) scrollProse(delta int) tea.Model {
	frame := m.workbenchFrame()
	width, height := min(88, max(1, m.mainWidth()-4)), max(1, frame.proseHeight-3)
	if text, live := m.liveProse(); live {
		last := max(0, len(m.bench.proseCache.wrap(text, width))-height)
		if !m.bench.liveHeld {
			if delta > 0 {
				return m
			}
			m.bench.liveHeld, m.bench.liveText, m.bench.liveOffset = true, text, last
		}
		m.bench.liveOffset = min(last, max(0, m.bench.liveOffset+delta))
		if delta > 0 && m.bench.liveOffset == last {
			m.bench.liveHeld = false
		}
		return m
	}
	_, _, text := m.chapterProse()
	last := max(0, len(m.bench.proseCache.wrap(text, width))-height)
	m.bench.previewOffset = min(last, max(0, m.bench.previewOffset+delta))
	return m
}
