package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func paddedModalContentWidth(boxW int) int {
	return max(0, boxW-4)
}

func renderPaddedModalFrame(boxW, boxH int, title, hint string, bodyLines []string) string {
	lineStyle := lipgloss.NewStyle().Foreground(colorDim)
	titleStyle := lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(colorDim)

	innerW := max(0, boxW-2)
	contentW := paddedModalContentWidth(boxW)
	titleView := titleStyle.Render(title)
	hintView := hintStyle.Render(hint)

	titleFill := max(0, innerW-lipgloss.Width(title)-3)
	topLine := lineStyle.Render("┌─ ") + titleView + lineStyle.Render(" "+strings.Repeat("─", titleFill)+"┐")

	var bottomLine string
	if strings.TrimSpace(hint) == "" {
		bottomLine = lineStyle.Render("└" + strings.Repeat("─", innerW) + "┘")
	} else {
		hintFill := max(0, innerW-lipgloss.Width(hint))
		bottomLine = lineStyle.Render("└") + hintView + lineStyle.Render(strings.Repeat("─", hintFill)+"┘")
	}

	body := make([]string, 0, max(len(bodyLines), boxH-2))
	for _, line := range bodyLines {
		padding := contentW - lipgloss.Width(line)
		if padding < 0 {
			padding = 0
		}
		body = append(body, lineStyle.Render("│ ")+line+strings.Repeat(" ", padding)+lineStyle.Render(" │"))
	}

	emptyLine := lineStyle.Render("│ ") + strings.Repeat(" ", contentW) + lineStyle.Render(" │")
	for len(body) < boxH-2 {
		body = append(body, emptyLine)
	}

	return strings.Join(append(append([]string{topLine}, body...), bottomLine), "\n")
}
