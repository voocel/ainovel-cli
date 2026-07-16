package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/host"
)

// renderInputBox 渲染底部输入区：输入框、快捷键提示行、最底部用量状态栏。
// 输入框单独负责输入与提示，不承载启动模式栏。
func renderInputBox(inputView, hints string, snap host.UISnapshot, outputDir string, width int) string {
	innerW := width - 4 // border + padding
	if innerW < 12 {
		innerW = 12
	}

	// 输入行：提示符 + 输入框
	prompt := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("❯ ")
	inputLine := prompt + inputView

	// 提示行：快捷键独占整行——模型/花费等运行信息移入底部状态栏，不再挤在右侧互相截断。
	line2 := fitInlineLine(hints, innerW)

	// 输入区（单一盒子，避免视觉上出现双输入框）
	inputStyle := lipgloss.NewStyle().
		Width(width).
		Border(baseBorder, true, false, true, false).
		BorderForeground(colorDim).
		Padding(0, 1)
	inputBlock := inputStyle.Render(inputLine)

	// 提示行（无边框，紧贴下横线下方）
	hintStyle := lipgloss.NewStyle().
		Width(width).
		Padding(0, 2)
	hintBlock := hintStyle.Render(line2)

	// 状态栏占用输入区原有的末尾空行：整块高度不变，layoutHeights 无需调整。
	statusBlock := hintStyle.Render(renderStatusBar(snap, outputDir, innerW))

	return inputBlock + "\n" + hintBlock + "\n" + statusBlock
}

func joinInlineSides(left, right string, width int) string {
	if width <= 0 {
		return left + right
	}
	if strings.TrimSpace(right) == "" {
		return fitInlineLine(left, width)
	}

	right = fitInlineLine(right, width)
	rightW := ansi.StringWidth(right)
	if rightW >= width {
		return right
	}

	leftMax := width - rightW - 1
	if leftMax < 0 {
		leftMax = 0
	}
	left = fitInlineLine(left, leftMax)
	gap := width - ansi.StringWidth(left) - rightW
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func fitInlineLine(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(text) <= width {
		return text
	}
	return ansi.Truncate(text, width, "...")
}
