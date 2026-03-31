package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/store"
)

type reportState struct {
	report   diag.Report
	viewport viewport.Model
}

func newReportState(dir string, width, height int) *reportState {
	s := store.NewStore(dir)
	report := diag.Analyze(s)

	boxW, boxH := reportModalSize(width, height)
	contentW := boxW - 6 // padding 2*2 + border 2

	text := renderReportText(report, contentW)

	vp := viewport.New(contentW, boxH-4) // border 2 + padding 2
	vp.SetContent(text)

	return &reportState{
		report:   report,
		viewport: vp,
	}
}

func reportModalSize(termW, termH int) (int, int) {
	w := termW * 80 / 100
	if w > 100 {
		w = 100
	}
	if w < 60 {
		w = termW - 4
	}
	h := termH * 85 / 100
	if h < 20 {
		h = termH - 2
	}
	return w, h
}

func renderReportText(report diag.Report, width int) string {
	var b strings.Builder
	st := report.Stats

	// 概览
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)

	b.WriteString(titleStyle.Render("概览"))
	b.WriteString("\n\n")

	// 第一行：章节 + 字数
	b.WriteString(mutedStyle.Render("章节 "))
	b.WriteString(fmt.Sprintf("%d/%d", st.CompletedChapters, st.TotalChapters))
	b.WriteString(mutedStyle.Render("  字数 "))
	b.WriteString(fmt.Sprintf("%d", st.TotalWords))
	if st.AvgWordsPerCh > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf(" (%d/ch)", st.AvgWordsPerCh)))
	}
	b.WriteString(mutedStyle.Render("  阶段 "))
	b.WriteString(st.Phase)
	if st.Flow != "" && st.Flow != "writing" {
		b.WriteString(mutedStyle.Render("/"))
		b.WriteString(st.Flow)
	}
	b.WriteString("\n")

	// 第二行：评审 + 改写 + 均分
	b.WriteString(mutedStyle.Render("评审 "))
	b.WriteString(fmt.Sprintf("%d次", st.ReviewCount))
	if st.RewriteCount > 0 {
		b.WriteString(mutedStyle.Render("  改写 "))
		b.WriteString(fmt.Sprintf("%d次", st.RewriteCount))
	}
	if st.AvgReviewScore > 0 {
		b.WriteString(mutedStyle.Render("  均分 "))
		b.WriteString(fmt.Sprintf("%.1f", st.AvgReviewScore))
	}
	b.WriteString("\n")

	// 第三行：伏笔 + 规划
	if st.ForeshadowOpen > 0 || st.ForeshadowStale > 0 {
		b.WriteString(mutedStyle.Render("伏笔 "))
		b.WriteString(fmt.Sprintf("打开%d", st.ForeshadowOpen))
		if st.ForeshadowStale > 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorReview).Render(fmt.Sprintf(" 停滞%d", st.ForeshadowStale)))
		}
		b.WriteString("\n")
	}
	if st.PlanningTier != "" {
		b.WriteString(mutedStyle.Render("规划 "))
		b.WriteString(st.PlanningTier)
		b.WriteString("\n")
	}

	// 发现
	b.WriteString("\n")
	findings := report.Findings
	if len(findings) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(colorSuccess).Render("未发现问题"))
		b.WriteString("\n")
		return b.String()
	}

	criticals, warnings, infos := countSeverities(findings)
	b.WriteString(titleStyle.Render("发现"))
	b.WriteString(" ")
	b.WriteString(dimStyle.Render(formatSeverityCounts(criticals, warnings, infos)))
	b.WriteString("\n")

	for _, f := range findings {
		b.WriteString("\n")
		renderFinding(&b, f, width)
	}
	return b.String()
}

func renderFinding(b *strings.Builder, f diag.Finding, width int) {
	var sevStyle lipgloss.Style
	var marker string
	switch f.Severity {
	case diag.SevCritical:
		sevStyle = lipgloss.NewStyle().Foreground(colorError).Bold(true)
		marker = "critical"
	case diag.SevWarning:
		sevStyle = lipgloss.NewStyle().Foreground(colorReview)
		marker = "warning"
	default:
		sevStyle = lipgloss.NewStyle().Foreground(colorDim)
		marker = "info"
	}

	evidenceStyle := lipgloss.NewStyle().Foreground(colorDim)
	suggestionStyle := lipgloss.NewStyle().Foreground(colorAccent2)

	b.WriteString(sevStyle.Render(fmt.Sprintf("[%s]", marker)))
	b.WriteString(" ")
	b.WriteString(f.Title)
	b.WriteString("\n")

	if f.Evidence != "" {
		b.WriteString("  ")
		b.WriteString(evidenceStyle.Render(wrapText(f.Evidence, width-4)))
		b.WriteString("\n")
	}
	if f.Suggestion != "" {
		b.WriteString("  ")
		b.WriteString(suggestionStyle.Render("-> " + wrapText(f.Suggestion, width-7)))
		b.WriteString("\n")
	}
}

func countSeverities(findings []diag.Finding) (c, w, i int) {
	for _, f := range findings {
		switch f.Severity {
		case diag.SevCritical:
			c++
		case diag.SevWarning:
			w++
		case diag.SevInfo:
			i++
		}
	}
	return
}

func formatSeverityCounts(c, w, i int) string {
	parts := make([]string, 0, 3)
	if c > 0 {
		parts = append(parts, fmt.Sprintf("%d critical", c))
	}
	if w > 0 {
		parts = append(parts, fmt.Sprintf("%d warning", w))
	}
	if i > 0 {
		parts = append(parts, fmt.Sprintf("%d info", i))
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, " / ") + ")"
}

// wrapText 对长文本做简单换行。
func wrapText(s string, maxWidth int) string {
	if maxWidth <= 0 || lipgloss.Width(s) <= maxWidth {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	lineW := 0
	for _, r := range runes {
		w := lipgloss.Width(string(r))
		if lineW+w > maxWidth && lineW > 0 {
			b.WriteRune('\n')
			b.WriteString("  ") // indent continuation
			lineW = 2
		}
		b.WriteRune(r)
		lineW += w
	}
	return b.String()
}

func renderReportModal(width, height int, state *reportState) string {
	if state == nil {
		return ""
	}

	boxW, boxH := reportModalSize(width, height)

	lineStyle := lipgloss.NewStyle().Foreground(colorDim)
	titleText := lipgloss.NewStyle().Foreground(colorMuted).Bold(true).Render("诊断报告")
	hint := lineStyle.Render("  ↑↓ 滚动 · Esc 关闭")

	innerW := boxW - 2 // border left + right
	contentW := innerW - 4 // padding 2*2

	// 如果 viewport 尺寸变化了，更新
	if state.viewport.Width != contentW {
		state.viewport.Width = contentW
		state.viewport.Height = boxH - 4
	}

	vpContent := state.viewport.View()

	// 手动绘制边框
	titleLine := lineStyle.Render("┌─ ") + titleText + lineStyle.Render(" " + strings.Repeat("─", max(0, innerW-lipgloss.Width(titleText)-4)) + "┐")
	bottomLine := lineStyle.Render("└") + hint + lineStyle.Render(strings.Repeat("─", max(0, innerW-lipgloss.Width(hint)-1)) + "┘")

	bodyLines := strings.Split(vpContent, "\n")
	var body []string
	for _, line := range bodyLines {
		padding := contentW - lipgloss.Width(line)
		if padding < 0 {
			padding = 0
		}
		body = append(body, lineStyle.Render("│ ")+line+strings.Repeat(" ", padding)+lineStyle.Render(" │"))
	}

	// 补齐高度
	emptyLine := lineStyle.Render("│ ") + strings.Repeat(" ", contentW) + lineStyle.Render(" │")
	for len(body) < boxH-2 {
		body = append(body, emptyLine)
	}

	all := append([]string{titleLine}, body...)
	all = append(all, bottomLine)

	modal := strings.Join(all, "\n")
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func (m Model) handleReportKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.report == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.report = nil
		if m.mode != modeDone {
			return m, m.textarea.Focus()
		}
		return m, nil
	case tea.KeyUp:
		m.report.viewport.ScrollUp(1)
		return m, nil
	case tea.KeyDown:
		m.report.viewport.ScrollDown(1)
		return m, nil
	case tea.KeyPgUp:
		m.report.viewport.HalfPageUp()
		return m, nil
	case tea.KeyPgDown:
		m.report.viewport.HalfPageDown()
		return m, nil
	default:
		return m, nil
	}
}
