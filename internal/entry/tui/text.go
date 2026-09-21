package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func fitLine(text string, width int) string {
	return ansi.Truncate(text, max(0, width), "…")
}

// fitBlock 把已带样式的界面块裁成恰好 height 行、每行恰好 width 列（ANSI 感知折行）。
// 正文与中文长文的折行走 readingLines，这里不承担阅读排版。
func fitBlock(text string, width, height int) []string {
	lines := strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
	result := make([]string, max(0, height))
	for i := range result {
		if i < len(lines) {
			result[i] = fitLine(lines[i], width)
		}
		result[i] += strings.Repeat(" ", max(0, width-lipgloss.Width(result[i])))
	}
	return result
}

func benchRule(width int) string {
	return benchTheme.Border.Render(strings.Repeat("─", max(0, width)))
}

// sectionTitle 分区标题线：标题后用边框色横线补满。
func sectionTitle(title string, width int) string {
	return title + " " + benchRule(width-lipgloss.Width(title)-1)
}

// progressRule 分隔线兼进度条：已入稿比例用强调色实线。
func progressRule(done, total, width int) string {
	width = max(1, width)
	filled := 0
	if total > 0 {
		filled = min(width, max(0, done)*width/total)
	}
	return benchTheme.Accent.Render(strings.Repeat("━", filled)) + benchTheme.Border.Render(strings.Repeat("─", width-filled))
}

// alignRight 把 right 靠右放在同一行；左边放不下时截断左边，两者至少隔一格。
func alignRight(left, right string, width int) string {
	if right == "" {
		return fitLine(left, width)
	}
	room := width - lipgloss.Width(right) - 1
	left = fitLine(left, max(0, room))
	return left + strings.Repeat(" ", max(1, room-lipgloss.Width(left)+1)) + right
}

// oneLine 把多行文本压成一行（摘要行不允许撑破布局）。
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func formatDuration(d time.Duration) string {
	switch {
	case d < 0:
		return ""
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dK", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatCost(usd float64) string {
	if usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

func formatDataSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d 字节", n)
	}
	return fmt.Sprintf("%.1fK", float64(n)/1024)
}

// groupDigits 千分位分组："9800" → "9,800"。
func groupDigits(n int) string {
	digits := fmt.Sprintf("%d", n)
	var out []byte
	for i, c := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
