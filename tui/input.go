package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/app"
)

// renderInputBox 渲染底部栏（两行布局）。
// 第一行：❯ + 输入框
// 第二行：左快捷键提示，右进度信息
func renderInputBox(inputView string, snap app.UISnapshot, outputDir string, width int) string {
	innerW := width - 4 // border + padding

	// 第一行：提示符 + 输入框
	prompt := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("❯ ")
	line1 := prompt + inputView

	// 第二行：左快捷键，右进度
	hints := lipgloss.NewStyle().Foreground(colorDim).Render("Tab 切换 · ^L 清屏 · Esc 重置 · Enter 发送")
	info := buildRightInfo(snap, outputDir)

	hintsW := lipgloss.Width(hints)
	infoW := lipgloss.Width(info)
	gap := innerW - hintsW - infoW
	if gap < 1 {
		gap = 1
	}
	line2 := hints + strings.Repeat(" ", gap) + info

	// 输入区（上横线 + 输入行）
	inputStyle := lipgloss.NewStyle().
		Width(width).
		Border(baseBorder, true, false, true, false).
		BorderForeground(colorDim).
		Padding(0, 1)
	inputBlock := inputStyle.Render(line1)

	// 提示行（无边框，紧贴下横线下方）
	hintStyle := lipgloss.NewStyle().
		Width(width).
		Padding(0, 2)
	hintBlock := hintStyle.Render(line2)

	return inputBlock + "\n" + hintBlock + "\n"
}

// buildRightInfo 构建右侧进度和目录信息。
func buildRightInfo(snap app.UISnapshot, outputDir string) string {
	var parts []string

	if snap.ModelName != "" {
		parts = append(parts, snap.ModelName)
	}
	if snap.TotalChapters > 0 {
		parts = append(parts, fmt.Sprintf("Ch %d/%d", snap.CompletedCount, snap.TotalChapters))
	}
	if snap.TotalWordCount > 0 {
		parts = append(parts, formatNumber(snap.TotalWordCount)+"字")
	}
	if outputDir != "" {
		parts = append(parts, "./"+filepath.Base(outputDir))
	}

	if len(parts) == 0 {
		return lipgloss.NewStyle().Foreground(colorDim).Render("READY")
	}
	return lipgloss.NewStyle().Foreground(colorDim).Render(strings.Join(parts, " · "))
}
