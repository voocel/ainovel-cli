package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/app"
)

// renderTopBar 渲染顶部状态栏（两行布局）。
// 第一行：小说名居中
// 第二行：左侧模型/风格信息，右侧状态胶囊
func renderTopBar(snap app.UISnapshot, width int, spinnerFrame string) string {
	// 第一行：小说名居中
	novelName := snap.NovelName
	if novelName == "" {
		novelName = "Novel Agent"
	}
	line1 := lipgloss.NewStyle().
		Width(width - 2).
		AlignHorizontal(lipgloss.Center).
		Foreground(colorText).
		Bold(true).
		Render("✦ " + novelName + " ✦")

	// 第二行左侧：模型 + 风格
	var infoParts []string
	if snap.ModelName != "" {
		infoParts = append(infoParts, snap.ModelName)
	}
	if snap.Style != "" && snap.Style != "default" {
		infoParts = append(infoParts, snap.Style)
	}
	left := lipgloss.NewStyle().Foreground(colorDim).Render(strings.Join(infoParts, " · "))

	// 第二行右侧：状态胶囊
	label := snap.StatusLabel
	if label == "" {
		label = "READY"
	}
	color, ok := statusColors[label]
	if !ok {
		color = colorDim
	}
	capsule := statusCapsule.Foreground(lipgloss.Color("#1a1a2e")).Background(color).Render(label)

	if snap.IsRunning && spinnerFrame != "" {
		capsule = lipgloss.NewStyle().Foreground(colorAccent).Render(spinnerFrame) + " " + capsule
	}

	gap := width - lipgloss.Width(left) - lipgloss.Width(capsule) - 2
	if gap < 1 {
		gap = 1
	}
	line2 := left + strings.Repeat(" ", gap) + capsule

	content := line1 + "\n" + line2
	return topBarStyle.Width(width).
		Border(baseBorder, false, false, true, false).
		BorderForeground(colorDim).
		Render(content)
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

// 星光动画帧
var sparklePatterns = []string{
	"  ✦       ·     ✧          ·  ",
	"     ·  ✧     ✦    ·          ",
	"  ·        ✦       ·    ✧     ",
	" ✧    ·        ✧       ✦     ·",
	"      ✦    ·       ✧    ·     ",
	"  ·       ✧    ✦       ·      ",
	"    ✧  ·       ·    ✦    ✧    ",
	"  ✦        ·    ✧        ✦  · ",
}

// renderSparkle 渲染事件流底部的星光加载动画。
func renderSparkle(frame int) string {
	idx := frame % len(sparklePatterns)
	// 亮星用琥珀色，暗星用灰色
	line := sparklePatterns[idx]
	var b strings.Builder
	for _, ch := range line {
		switch ch {
		case '✦':
			b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Render("✦"))
		case '✧':
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#887730")).Render("✧"))
		case '·':
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render("·"))
		default:
			b.WriteRune(ch)
		}
	}
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("#887730")).Render("  AI 生成中…")
	return "\n" + b.String() + "\n" + label
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

// renderStreamPanel 渲染流式输出面板（中间列下半部分）。
func renderStreamPanel(vp viewport.Model, width, height int, focused bool) string {
	// 分隔标题栏
	titleColor := colorDim
	if focused {
		titleColor = colorAccent
	}
	title := lipgloss.NewStyle().Foreground(titleColor).Render("✦ 生成内容")
	lineW := width - lipgloss.Width(title) - 4
	if lineW < 0 {
		lineW = 0
	}
	separator := lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", lineW))
	header := " " + title + " " + separator

	// viewport 内容（height 包含 header 行，viewport 实际高度需减 1）
	vpH := height - 1
	if vpH < 1 {
		vpH = 1
	}
	vpStyle := lipgloss.NewStyle().
		Width(width).
		Height(vpH).
		Padding(0, 1).
		Foreground(colorText)

	return header + "\n" + vpStyle.Render(vp.View())
}

// renderDetailPanel 渲染右侧详情面板。
// 优先展示基础设定（大纲、角色），然后是运行时信息（提交、审阅等）。
func renderDetailPanel(snap app.UISnapshot, width, height int) string {
	contentW := width - 4 // 边框 + padding
	var b strings.Builder

	// 大纲
	if len(snap.Outline) > 0 {
		b.WriteString(panelTitleStyle.Render("大纲"))
		b.WriteString("\n")
		for _, e := range snap.Outline {
			ch := fmt.Sprintf("%2d", e.Chapter)
			// 已完成的章节用绿色标记
			marker := lipgloss.NewStyle().Foreground(colorDim).Render("○")
			if snap.CompletedCount >= e.Chapter {
				marker = lipgloss.NewStyle().Foreground(colorSuccess).Render("●")
			} else if snap.InProgressChapter == e.Chapter {
				marker = lipgloss.NewStyle().Foreground(colorAccent).Render("◐")
			}
			title := truncate(e.Title, contentW-6)
			line := marker + lipgloss.NewStyle().Foreground(colorDim).Render(ch) + " " +
				cardContentStyle.Render(title)
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// 角色
	if len(snap.Characters) > 0 {
		b.WriteString(panelTitleStyle.Render("角色"))
		b.WriteString("\n")
		for _, c := range snap.Characters {
			b.WriteString(cardContentStyle.Render("· " + truncate(c, contentW-2)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// 前提
	if snap.Premise != "" {
		b.WriteString(panelTitleStyle.Render("前提"))
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render(truncate(snap.Premise, contentW*3)))
		b.WriteString("\n\n")
	}

	// 运行时信息
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

	if len(snap.RecentSummaries) > 0 {
		b.WriteString(cardTitleStyle.Render("─ 摘要 ─"))
		b.WriteString("\n")
		for _, s := range snap.RecentSummaries {
			b.WriteString(cardContentStyle.Render(truncate(s, contentW)))
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
