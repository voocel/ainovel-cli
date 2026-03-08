package tui

import "github.com/charmbracelet/lipgloss"

// renderInputBox 渲染底部输入框区域。
func renderInputBox(inputView string, width int) string {
	style := lipgloss.NewStyle().
		Width(width).
		Border(baseBorder, true, false, false, false).
		BorderForeground(colorDim).
		Padding(0, 1)

	return style.Render(inputView)
}
