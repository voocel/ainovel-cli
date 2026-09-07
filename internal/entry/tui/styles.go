package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

// 视觉层唯一定义处。色板沿用 v0 调优过的“暖调书卷气”双档值
// （亮底稳定档 + 暗底提亮档），金色为品牌主色。
var (
	colorAccent  = lipgloss.AdaptiveColor{Light: "#b8860b", Dark: "#e5b449"}
	colorSubtle  = lipgloss.AdaptiveColor{Light: "#8a7e6b", Dark: "#8a8175"}
	colorMuted   = lipgloss.AdaptiveColor{Light: "#7a7060", Dark: "#b8b09c"}
	colorWarn    = lipgloss.AdaptiveColor{Light: "#b07530", Dark: "#e09b5a"}
	colorDanger  = lipgloss.AdaptiveColor{Light: "#b5433a", Dark: "#e07060"}
	colorSuccess = lipgloss.AdaptiveColor{Light: "#3d7a42", Dark: "#7ec488"}

	styleBrand  = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleTitle  = lipgloss.NewStyle().Bold(true)
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
			Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1c1c1c"}).
			Background(colorAccent).Padding(0, 1)
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

// header 是每页顶部的品牌行：◆ ainovel-cli · <上下文>。
func header(context string) string {
	line := styleBrand.Render("◆ ainovel-cli")
	if context != "" {
		line += styleHint.Render(" · ") + styleTitle.Render(context)
	}
	return line + "\n\n"
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
	if width < total {
		width = total
	}
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

// splash 渲染欢迎屏顶部的品牌区（v0 风格）：标题、副标题与波浪分隔线，
// 在给定宽度内逐行居中。
func splash(width int) string {
	center := lipgloss.NewStyle().Width(width).Align(lipgloss.Center)
	divider := strings.Repeat("~", min(44, max(10, width-8)))
	return center.Render(styleBrand.Render("A I N O V E L")) + "\n" +
		center.Render(styleSubtitle.Render("AI 小说创作引擎")) + "\n\n" +
		center.Render(styleHint.Render(divider)) + "\n\n"
}

// centerScreen 把内容块整体放到屏幕正中。先把块内各行补齐到等宽（保持左对齐）
// 再交给 Place——否则 Place 会逐行居中，表单就散架了。
func centerScreen(width, height int, content string) string {
	block := lipgloss.NewStyle().Width(lipgloss.Width(content)).Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}

// pinBottom 把底栏钉在屏幕底部：全屏模式下内容顶部对齐、操作提示始终在最下方。
func pinBottom(content, bottom string, height int) string {
	pad := height - lipgloss.Height(content) - lipgloss.Height(bottom)
	if pad < 1 {
		pad = 1
	}
	return content + strings.Repeat("\n", pad) + bottom
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
