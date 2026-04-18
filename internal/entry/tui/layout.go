package tui

import (
	"fmt"
	"math"

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

// formatContextWindow 把 token 数格式化成紧凑窗口标记："128K" / "200K" / "1M" / "2M"。
// Gemini 的 1048576 (2^20) 等技术意义上的 1M 会展示为 "1M" 而非 "1.0M"。
// n<=0 返回空串，调用方应据此决定是否展示。
func formatContextWindow(n int) string {
	if n <= 0 {
		return ""
	}
	if n >= 1_000_000 {
		m := float64(n) / 1_000_000
		rounded := math.Round(m)
		if rounded > 0 && math.Abs(m-rounded)/rounded < 0.05 {
			return fmt.Sprintf("%dM", int(rounded))
		}
		return fmt.Sprintf("%.1fM", m)
	}
	if n >= 1000 {
		return fmt.Sprintf("%dK", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// formatCostUSD 格式化美元成本。<$0.01 用 4 位小数，否则 2 位。0 返回空。
func formatCostUSD(usd float64) string {
	if usd <= 0 {
		return ""
	}
	if usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
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
