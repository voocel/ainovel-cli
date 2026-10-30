package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// spinnerFrames 是进行中步骤与顶栏的转圈，随创作动画的节拍换帧（pulse.go）。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// newInput 统一文本输入样式：所有页面的输入框长一个样。光标用真实终端光标（caret.go），
// 由 inputView 渲染。
func newInput(placeholder string) textinput.Model {
	input := textinput.New()
	input.SetVirtualCursor(false)
	input.Placeholder = placeholder
	input.Prompt = "❯ "
	input.SetStyles(inputStyles(lipgloss.NewStyle(), benchTheme.Muted))
	return input
}

// inputStyles 是输入框的样式：聚焦与否长一个样。
func inputStyles(text, placeholder lipgloss.Style) textinput.Styles {
	state := textinput.StyleState{Prompt: benchTheme.Focus, Text: text, Placeholder: placeholder}
	return textinput.Styles{Focused: state, Blurred: state}
}

// progressBar 用 ▰▱ 渲染章节进度，写完部分着品牌色。
func progressBar(done, total, width int) string {
	if total <= 0 {
		total = 1
	}
	if done > total {
		done = total
	}
	width = max(0, width)
	done = max(0, done)
	filled := done * width / total
	bar := benchTheme.Focus.Render(strings.Repeat("▰", filled)) +
		benchTheme.Muted.Render(strings.Repeat("▱", width-filled))
	return bar
}
