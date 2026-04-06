package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// --- 辅助函数 ---

func renderField(label, value string) string {
	if value == "" {
		value = "-"
	}
	return fieldLabelStyle.Render(label) + fieldValueStyle.Render(value) + "\n"
}

func renderFlowField(flow string) string {
	if flow == "" {
		flow = "-"
	}
	label := fieldLabelStyle.Render("Flow")
	if flow != "writing" && flow != "-" && flow != "" {
		return label + highlightValueStyle.Render(flow) + "\n"
	}
	return label + fieldValueStyle.Render(flow) + "\n"
}

func renderHighlightField(label, value string) string {
	return fieldLabelStyle.Render(label) + highlightValueStyle.Render(value) + "\n"
}

// contextPercentColor returns a health-gradient color based on context usage.
// Mirrors Claude Code's calculateTokenWarningState concept:
//   - < 70%: green (healthy headroom)
//   - 70-85%: yellow (approaching compression threshold)
//   - > 85%: red (compression imminent or active)
func contextPercentColor(percent float64) lipgloss.AdaptiveColor {
	switch {
	case percent >= 85:
		return colorError
	case percent >= 70:
		return colorReview
	default:
		return colorSuccess
	}
}

func renderContextUsageField(label string, percent float64, tokens, window int) string {
	if window <= 0 || tokens <= 0 {
		return ""
	}
	percentColor := contextPercentColor(percent)
	usage := lipgloss.NewStyle().Foreground(percentColor).Bold(true).
		Render(fmt.Sprintf("%.0f%%", percent)) +
		contextUsageMetaStyle.Render(" · ") +
		contextUsageMetaStyle.Render(fmt.Sprintf("%s/%s", formatNumber(tokens), formatNumber(window)))
	return fieldLabelStyle.Render(label) + usage + "\n"
}

func formatNumber(n int) string {
	if n == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", n)
	result := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max < 4 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
