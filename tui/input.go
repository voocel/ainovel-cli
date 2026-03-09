package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/app"
)

// renderInputBox 渲染底部栏：左快捷键 | 中输入框 | 右进度+目录。
func renderInputBox(inputView string, snap app.UISnapshot, outputDir string, width int) string {
	// 左侧：快捷键提示
	keys := lipgloss.NewStyle().Foreground(colorDim).Render("Tab·^L·Esc")

	// 右侧：进度 + 输出目录
	right := buildRightInfo(snap, outputDir)

	// 中间：输入框，自适应宽度
	leftW := lipgloss.Width(keys)
	rightW := lipgloss.Width(right)
	sep := lipgloss.NewStyle().Foreground(colorDim).Render(" │ ")
	sepW := lipgloss.Width(sep)
	inputW := width - leftW - rightW - sepW*2 - 4 // 4 为 padding+border 余量
	if inputW < 20 {
		inputW = 20
	}

	input := lipgloss.NewStyle().Width(inputW).Render(inputView)

	content := keys + sep + input + sep + right

	style := lipgloss.NewStyle().
		Width(width).
		Border(baseBorder, true, false, false, false).
		BorderForeground(colorDim).
		Padding(0, 1)

	return style.Render(content)
}

// buildRightInfo 构建右侧进度和目录信息。
func buildRightInfo(snap app.UISnapshot, outputDir string) string {
	var parts []string

	// 章节进度
	if snap.TotalChapters > 0 {
		parts = append(parts, fmt.Sprintf("Ch %d/%d", snap.CompletedCount, snap.TotalChapters))
	}

	// 字数
	if snap.TotalWordCount > 0 {
		parts = append(parts, formatNumber(snap.TotalWordCount)+"字")
	}

	// 输出目录（缩短为相对路径的最后一段）
	if outputDir != "" {
		dir := filepath.Base(outputDir)
		parts = append(parts, "./"+dir)
	}

	if len(parts) == 0 {
		return lipgloss.NewStyle().Foreground(colorDim).Render("READY")
	}
	return lipgloss.NewStyle().Foreground(colorDim).Render(strings.Join(parts, " · "))
}
