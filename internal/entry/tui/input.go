package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
)

// renderInputBox 渲染底部输入区。
// 输入框单独负责输入与提示，不承载启动模式栏。
func renderInputBox(inputView, hints string, snap orchestrator.UISnapshot, outputDir string, width int) string {
	innerW := width - 4 // border + padding
	if innerW < 12 {
		innerW = 12
	}

	// 输入行：提示符 + 输入框
	prompt := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("❯ ")
	inputLine := prompt + inputView

	// 提示行：左快捷键，右进度
	info := buildRightInfo(snap, outputDir)
	line2 := joinInlineSides(hints, info, innerW)

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

	return inputBlock + "\n" + hintBlock + "\n"
}

// buildRightInfo 构建右侧进度和目录信息。
func buildRightInfo(snap orchestrator.UISnapshot, outputDir string) string {
	var parts []string

	if snap.Provider != "" {
		parts = append(parts, snap.Provider)
	}
	if snap.ModelName != "" {
		parts = append(parts, snap.ModelName)
	}
	if snap.TotalChapters > 0 {
		parts = append(parts, fmt.Sprintf("Ch %d/%d", snap.CompletedCount, snap.TotalChapters))
	}
	if snap.TotalWordCount > 0 {
		parts = append(parts, formatNumber(snap.TotalWordCount)+"字")
	}
	runningTasks := 0
	for _, task := range snap.Tasks {
		if task.Status == "running" || task.Status == "queued" {
			runningTasks++
		}
	}
	if runningTasks > 0 {
		parts = append(parts, fmt.Sprintf("Tasks %d", runningTasks))
	}
	if outputDir != "" {
		parts = append(parts, "./"+filepath.Base(outputDir))
	}

	if len(parts) == 0 {
		return lipgloss.NewStyle().Foreground(colorDim).Render("READY")
	}
	return lipgloss.NewStyle().Foreground(colorDim).Render(strings.Join(parts, " · "))
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
