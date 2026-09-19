package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

// Shared widgets use the same semantic palette as the workbench.
var (
	colorAccent  = benchColors.Accent
	colorMuted   = benchColors.Muted
	colorWarn    = benchColors.Warning
	colorDanger  = benchColors.Error
	colorSuccess = benchColors.Success

	styleTitle  = benchTheme.Title
	styleHint   = lipgloss.NewStyle().Foreground(colorMuted)
	styleFocus  = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleErr    = lipgloss.NewStyle().Foreground(colorDanger)
	styleNotice = lipgloss.NewStyle().Foreground(colorSuccess)
	styleWarn   = lipgloss.NewStyle().Bold(true).Foreground(colorWarn)

	styleSubtitle = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
)

// spinnerFrames 复用 800ms 轮询节拍做创作中动画，不引入额外定时器。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// newInput 统一文本输入样式：所有页面的输入框长一个样。
func newInput(placeholder string) textinput.Model {
	input := textinput.New()
	input.Placeholder = placeholder
	input.Prompt = "❯ "
	input.PromptStyle = styleFocus
	input.PlaceholderStyle = styleHint
	return input
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
	bar := styleFocus.Render(strings.Repeat("▰", filled)) +
		styleHint.Render(strings.Repeat("▱", width-filled))
	return bar
}
