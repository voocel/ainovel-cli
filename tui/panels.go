package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/app"
)

// renderTopBar 渲染顶部状态栏。
func renderTopBar(snap app.UISnapshot, width int, spinnerFrame string) string {
	left := lipgloss.NewStyle().Foreground(colorText).Bold(true).Render(snap.NovelName)
	if snap.Style != "" && snap.Style != "default" {
		left += " " + lipgloss.NewStyle().Foreground(colorDim).Render(snap.Style)
	}
	left += " " + lipgloss.NewStyle().Foreground(colorDim).Render(snap.ModelName)

	// 状态胶囊
	label := snap.StatusLabel
	if label == "" {
		label = "READY"
	}
	color, ok := statusColors[label]
	if !ok {
		color = colorDim
	}
	capsule := statusCapsule.Foreground(lipgloss.Color("#1a1a2e")).Background(color).Render(label)

	// Spinner（运行中显示）
	if snap.IsRunning && spinnerFrame != "" {
		capsule = lipgloss.NewStyle().Foreground(colorAccent).Render(spinnerFrame) + " " + capsule
	}

	// 左右填充
	gap := width - lipgloss.Width(left) - lipgloss.Width(capsule) - 2
	if gap < 1 {
		gap = 1
	}

	return topBarStyle.Width(width).Render(left + strings.Repeat(" ", gap) + capsule)
}

// renderStatePanel 渲染左侧状态面板。
func renderStatePanel(snap app.UISnapshot, width, height int) string {
	var b strings.Builder

	if snap.RecoveryLabel != "" {
		b.WriteString(highlightValueStyle.Render("恢复: " + truncate(snap.RecoveryLabel, width-4)))
		b.WriteString("\n\n")
	}

	b.WriteString(panelTitleStyle.Render("状态"))
	b.WriteString("\n")
	b.WriteString(renderField("Phase", snap.Phase))
	b.WriteString(renderFlowField(snap.Flow))
	b.WriteString(renderField("Chapter", fmt.Sprintf("%d / %d", snap.CompletedCount, snap.TotalChapters)))
	b.WriteString(renderField("Words", formatNumber(snap.TotalWordCount)))

	if snap.InProgressChapter > 0 {
		b.WriteString(renderField("Writing", fmt.Sprintf("第%d章 场景%d", snap.InProgressChapter, snap.CompletedScenes)))
	}

	if len(snap.PendingRewrites) > 0 {
		b.WriteString("\n")
		b.WriteString(panelTitleStyle.Render("返工"))
		b.WriteString("\n")
		b.WriteString(renderHighlightField("Pending", fmt.Sprintf("%v", snap.PendingRewrites)))
		if snap.RewriteReason != "" {
			b.WriteString(renderField("Reason", truncate(snap.RewriteReason, width-12)))
		}
	}

	if snap.PendingSteer != "" {
		b.WriteString("\n")
		b.WriteString(panelTitleStyle.Render("干预"))
		b.WriteString("\n")
		b.WriteString(renderHighlightField("Steer", truncate(snap.PendingSteer, width-12)))
	}

	style := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(baseBorder, false, true, false, false).
		BorderForeground(colorDim).
		Padding(0, 1)

	return style.Render(b.String())
}

// renderEventContent 将事件列表渲染为纯文本（供 viewport 使用）。
func renderEventContent(events []app.UIEvent, width int) string {
	var b strings.Builder
	for i, ev := range events {
		ts := ev.Time.Format("15:04:05")
		cat := ev.Category

		color, ok := categoryColors[cat]
		if !ok {
			color = colorText
		}

		catStyle := lipgloss.NewStyle().Foreground(color).Width(7)
		tsStyle := lipgloss.NewStyle().Foreground(colorDim)
		sumStyle := lipgloss.NewStyle().Foreground(color)

		line := tsStyle.Render(ts) + " " + catStyle.Render(cat) + " " + sumStyle.Render(truncate(ev.Summary, width-20))
		b.WriteString(line)
		if i < len(events)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderEventFlowViewport 用 viewport 包装渲染事件流面板。
func renderEventFlowViewport(vp viewport.Model, width, height int) string {
	style := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Padding(0, 1)

	return style.Render(vp.View())
}

// renderDetailPanel 渲染右侧详情面板。
func renderDetailPanel(snap app.UISnapshot, width, height int) string {
	var b strings.Builder

	if snap.LastCommitSummary != "" {
		b.WriteString(cardTitleStyle.Render("─ 最近提交 ─"))
		b.WriteString("\n")
		b.WriteString(cardContentStyle.Render(snap.LastCommitSummary))
		b.WriteString("\n\n")
	}

	if snap.LastReviewSummary != "" {
		b.WriteString(cardTitleStyle.Render("─ 最近审阅 ─"))
		b.WriteString("\n")
		b.WriteString(cardContentStyle.Render(snap.LastReviewSummary))
		b.WriteString("\n\n")
	}

	if snap.LastCheckpointName != "" {
		b.WriteString(cardTitleStyle.Render("─ 检查点 ─"))
		b.WriteString("\n")
		b.WriteString(cardContentStyle.Render(snap.LastCheckpointName))
		b.WriteString("\n\n")
	}

	if len(snap.RecentSummaries) > 0 {
		b.WriteString(cardTitleStyle.Render("─ 摘要 ─"))
		b.WriteString("\n")
		for _, s := range snap.RecentSummaries {
			b.WriteString(cardContentStyle.Render(s))
			b.WriteString("\n")
		}
	}

	style := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(baseBorder, false, false, false, true).
		BorderForeground(colorDim).
		Padding(0, 1)

	return style.Render(b.String())
}

// renderWelcome 渲染新建态首屏。
func renderWelcome(width, height int, errMsg string) string {
	content := lipgloss.NewStyle().Foreground(colorText).Render("还没有开始创作。") + "\n\n" +
		lipgloss.NewStyle().Foreground(colorDim).Render("请输入你的小说需求，系统会先进入设定与大纲阶段。") + "\n\n" +
		lipgloss.NewStyle().Foreground(colorAccent).Render("示例：写一部 12 章都市悬疑小说，主角是一名女法医")

	if errMsg != "" {
		content += "\n\n" + lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("错误: "+errMsg)
	}

	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		AlignHorizontal(lipgloss.Center).
		AlignVertical(lipgloss.Center).
		Render(content)
}
