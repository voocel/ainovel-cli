package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// 真实终端光标跟着输入框的插入点走：输入法把拼写中的文字画在终端光标处，光标若留在
// 渲染器收尾的右下角，拼写会顶出屏幕、把整屏滚上去一行，渲染器不知情，留下错位的残影。
//
// 聚焦的输入框在插入点放 caretMark（零宽的 APC 串，排版函数原样保留、不占列），各页面
// 照常排版；View 在整屏里找到它，把光标放过去并去掉标记。
const caretMark = "\x1b_caret\x1b\\"

// placeCaret 取出整屏里的插入点标记：返回去掉标记的画面与光标；没有聚焦的输入框时光标为 nil（隐藏）。
func placeCaret(screen string) (string, *tea.Cursor) {
	i := strings.Index(screen, caretMark)
	if i < 0 {
		return screen, nil
	}
	before := screen[:i]
	row := before[strings.LastIndex(before, "\n")+1:]
	cursor := tea.NewCursor(ansi.StringWidth(row), strings.Count(before, "\n"))
	cursor.Color = benchColors.Text
	return strings.ReplaceAll(screen, caretMark, ""), cursor
}

// inputView 渲染单行输入框，聚焦时在插入点放标记；width 含提示符。不用 textinput 自带的
// View：它按字符数定光标列、横向滚动不认中文宽度。这里按显示宽度截出含插入点的一段，
// 插入点之前放不下时向左滚，末尾留一列给光标。
func inputView(input textinput.Model, width int) string {
	styles, mark := input.Styles().Blurred, ""
	if input.Focused() {
		styles, mark = input.Styles().Focused, caretMark
	}
	prompt := styles.Prompt.Render(input.Prompt)
	room := max(1, width-lipgloss.Width(input.Prompt))
	if input.Value() == "" {
		return prompt + mark + styles.Placeholder.Render(fitLine(input.Placeholder, room))
	}
	value := []rune(input.Value())
	switch input.EchoMode {
	case textinput.EchoPassword:
		value = []rune(strings.Repeat(string(input.EchoCharacter), len(value)))
	case textinput.EchoNone:
		value = nil
	}
	split := min(input.Position(), len(value))
	before, after := string(value[:split]), string(value[split:])
	before = ansi.TruncateLeft(before, max(0, lipgloss.Width(before)-room+1), "")
	after = fitLine(after, room-lipgloss.Width(before))
	return prompt + styles.Text.Render(before) + mark + styles.Text.Render(after)
}

// areaView 渲染多行输入框的各行，聚焦时在插入点放标记。textarea 的 Cursor 按显示宽度
// 给出插入点在自身视图里的位置。
func areaView(input textarea.Model) []string {
	rows := strings.Split(strings.TrimSuffix(input.View(), "\n"), "\n")
	if cursor := input.Cursor(); cursor != nil && cursor.Y < len(rows) {
		row := rows[cursor.Y]
		rows[cursor.Y] = ansi.Truncate(row, cursor.X, "") + caretMark + ansi.TruncateLeft(row, cursor.X, "")
	}
	return rows
}
