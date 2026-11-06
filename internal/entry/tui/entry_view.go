package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// 首页与它的表单页（更多设定、导入作品）共用工作台的外框：顶栏两行（品牌与右侧信息、分隔线），
// 底栏两行（嵌着反馈的分隔线、按键），进出工作台时外框不跳。
const (
	pageHeader = 2
	pageFooter = 2
)

// pageTop 是顶栏：品牌（可带页面名）靠左，right 靠右。
func pageTop(title, right string, width int) []string {
	left := " " + benchTheme.Accent.Bold(true).Render("AINOVEL")
	if title != "" {
		left += benchTheme.Muted.Render("  /  " + title)
	}
	return []string{alignRight(left, right+" ", width), benchRule(width)}
}

// pageBottom 是底栏：反馈嵌在分隔线里，下一行是按键与靠右的 aside。clickable 的页面在按键
// 前用强调色说明可以用鼠标点：很多人不知道终端界面也能像网页一样点。
func (m model) pageBottom(feedback, hint, aside string, clickable bool, width int) []string {
	keys := benchTheme.Muted.Render(hint)
	if clickable {
		keys = benchTheme.Accent.Render("可用鼠标点选") + benchTheme.Muted.Render(" · ") + keys
	}
	return []string{feedbackRule(feedback, width), " " + alignRight(m.keyHint(keys), aside, width-2)}
}

func problemFeedback(problem string) string {
	if problem == "" {
		return ""
	}
	return benchTheme.Error.Render("! " + oneLine(problem))
}

func (m model) entryWidth() int { return min(76, max(1, m.width-4)) }

// entryPage 是表单页：内容在居中的窄栏里自上而下排，字段变化时不重新居中。
func (m model) entryPage(title string, content []string, problem, hint string) string {
	w, h := max(1, m.width), max(1, m.height)
	inner := m.entryWidth()
	pad := strings.Repeat(" ", (w-inner)/2)
	lines := pageTop(title, "", w)
	for _, line := range fitBlock(strings.Join(append([]string{""}, content...), "\n"), inner, h-pageHeader-pageFooter) {
		lines = append(lines, pad+line)
	}
	lines = append(lines, m.pageBottom(problemFeedback(problem), hint, "", false, w)...)
	return strings.Join(fitBlock(strings.Join(lines, "\n"), w, h), "\n")
}

// pageGutter 是内容栏两侧的留白：焦点竖线落在左侧，选中行的底色带铺过两侧。
const pageGutter = 2

// pageHit 是一块可点的区域：focus 是它对应的控件，row 是列表下标、option 是选项下标，
// 不是列表行或选项时为 -1。
type pageHit struct {
	x, y, width, focus int
	row, option        int
}

// pageColumn 是内容栏的行缓冲：每行连同两侧留白，热区按内容左缘记录，拼页时再平移。
type pageColumn struct {
	width int
	lines []string
	hits  []pageHit
}

func (c *pageColumn) add(text string) { c.mark(false, text) }

// mark 加一行；focused 时左侧留白画强调色竖线，标出焦点所在的控件。
func (c *pageColumn) mark(focused bool, text string) {
	gutter := strings.Repeat(" ", pageGutter)
	if focused {
		gutter = benchTheme.Accent.Render("▎") + " "
	}
	c.lines = append(c.lines, gutter+padLine(text, c.width)+strings.Repeat(" ", pageGutter))
}

// hit 登记下一行（即将加入的那一行）上的热区。
func (c *pageColumn) hit(x, width, focus, row, option int) {
	c.hits = append(c.hits, pageHit{x: x, y: len(c.lines), width: width, focus: focus, row: row, option: option})
}

// item 加一行列表项并登记整行（连同两侧留白）为热区。
func (c *pageColumn) item(row string, focus, index int) {
	c.hit(-pageGutter, c.width+2*pageGutter, focus, index, -1)
	c.lines = append(c.lines, row)
}

// rowLabel 是表单行的标签列：焦点所在行用强调色。
func rowLabel(focused bool, label string, width int) string {
	style := benchTheme.Muted
	if focused {
		style = benchTheme.Accent.Bold(true)
	}
	return padLine(style.Render(label), width)
}

type styled struct {
	text  string
	style lipgloss.Style
}

// rowLook 是列表行的样子：选中整行铺选中底色、左侧竖线，片段各自的颜色让位给底色；
// 指针悬停整行铺输入面底色，片段保留各自颜色。
type rowLook int

const (
	rowPlain rowLook = iota
	rowHovered
	rowSelected
)

func lookOf(selected bool) rowLook {
	if selected {
		return rowSelected
	}
	return rowPlain
}

// listRow 是列表的一行（连同两侧留白）：left 靠左、right 靠右。
func listRow(left, right []styled, width int, look rowLook) string {
	var plainLeft, plainRight, styledLeft, styledRight strings.Builder
	fill := lipgloss.NewStyle()
	if look == rowHovered {
		fill = fill.Background(benchColors.InputBackground)
	}
	for _, cell := range left {
		plainLeft.WriteString(cell.text)
		styledLeft.WriteString(cell.style.Inherit(fill).Render(cell.text))
	}
	for _, cell := range right {
		plainRight.WriteString(cell.text)
		styledRight.WriteString(cell.style.Inherit(fill).Render(cell.text))
	}
	room := width - lipgloss.Width(plainRight.String())
	if look == rowSelected {
		return benchTheme.Selected.Render("▎ " + padLine(plainLeft.String(), room) + plainRight.String() + strings.Repeat(" ", pageGutter))
	}
	gutter := fill.Render(strings.Repeat(" ", pageGutter))
	gap := fill.Render(strings.Repeat(" ", max(0, room-lipgloss.Width(plainLeft.String()))))
	return gutter + styledLeft.String() + gap + styledRight.String() + gutter
}

// listWindow 是列表的可见区间：装不下时让选中项留在窗口中部。
func listWindow(count, selected, capacity int) (int, int) {
	start := min(max(0, selected-capacity/2), max(0, count-capacity))
	return start, min(count, start+capacity)
}

// sectionHead 是分区标题行：标题后一道细线伸到靠右的控件前。
func sectionHead(title, controls string, width int) string {
	if controls != "" {
		controls = "  " + controls
	}
	return title + " " + benchRule(width-lipgloss.Width(title)-1-lipgloss.Width(controls)) + controls
}
