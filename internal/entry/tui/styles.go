package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

// Shared widgets use the same semantic palette as the workbench.
var (
	colorAccent  = benchColors.Accent
	colorSubtle  = benchColors.Muted
	colorMuted   = benchColors.Muted
	colorWarn    = benchColors.Warning
	colorDanger  = benchColors.Error
	colorSuccess = benchColors.Success

	styleTitle  = benchTheme.Title
	styleHint   = lipgloss.NewStyle().Foreground(colorSubtle)
	styleFocus  = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleErr    = lipgloss.NewStyle().Foreground(colorDanger)
	styleNotice = lipgloss.NewStyle().Foreground(colorSuccess)
	styleWarn   = lipgloss.NewStyle().Bold(true).Foreground(colorWarn)

	// 决定卡是工作台的第一交互：暖色圆角边框，视觉上先于一切内容。
	styleCard = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorWarn).
			Padding(0, 1)

	styleTabActive = lipgloss.NewStyle().Bold(true).
			Foreground(benchColors.SelectedText).
			Background(benchColors.SelectedBackground).Padding(0, 1)
	styleTabIdle = lipgloss.NewStyle().Foreground(colorSubtle).Padding(0, 1)

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

// marker 渲染行首焦点指示：聚焦行整体高亮。
func marker(focused bool, text string) string {
	if focused {
		return styleFocus.Render("❯ " + text)
	}
	return "  " + text
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

// tabs 渲染视图切换：当前视图为实心胶囊。
func tabs(names []string, active int) string {
	parts := make([]string, len(names))
	for i, name := range names {
		if i == active {
			parts[i] = styleTabActive.Render(name)
		} else {
			parts[i] = styleTabIdle.Render(name)
		}
	}
	return strings.Join(parts, "")
}

// tabIndexAt 用与 tabs() 相同的渲染宽度计算 x 命中的标签下标；未命中返回 -1。
func tabIndexAt(names []string, active, x int) int {
	edge := 0
	for i, name := range names {
		style := styleTabIdle
		if i == active {
			style = styleTabActive
		}
		edge += lipgloss.Width(style.Render(name))
		if x < edge {
			return i
		}
	}
	return -1
}

// stateBadge 给运行状态着色：等待暖色、进行品牌色、完成绿色、异常红色。
func stateBadge(label string) string {
	switch label {
	case "等你决定":
		return styleWarn.Render(label)
	case "创作中":
		return styleFocus.Render(label)
	case "已完成":
		return styleNotice.Render(label)
	case "需要处理":
		return styleErr.Render(label)
	default:
		return styleHint.Render(label)
	}
}

func errLine(text string) string {
	if text == "" {
		return ""
	}
	return styleErr.Render("✗ "+text) + "\n"
}

func noticeLine(text string) string {
	if text == "" {
		return ""
	}
	return styleNotice.Render("✓ "+text) + "\n"
}
