package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
)

// renderTopBar 渲染顶部状态栏（两行布局）。
// 第一行：小说名居中
// 第二行：左侧模型/风格信息，右侧状态胶囊
func renderTopBar(snap orchestrator.UISnapshot, width int, spinnerFrame string) string {
	// 第一行：小说名居中
	novelName := snap.NovelName
	if novelName == "" {
		novelName = "AiNovel"
	}
	line1 := lipgloss.NewStyle().
		Width(width - 2).
		AlignHorizontal(lipgloss.Center).
		Foreground(colorText).
		Bold(true).
		Render("~ " + novelName + " ~")

	// 第二行左侧：模型 + 风格
	var infoParts []string
	if snap.Provider != "" {
		infoParts = append(infoParts, snap.Provider)
	}
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
	capsule := statusCapsule.Foreground(lipgloss.Color("#1c1a14")).Background(color).Render(label)

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
func renderStatePanel(snap orchestrator.UISnapshot, width, height int) string {
	var b strings.Builder

	if snap.RecoveryLabel != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Italic(true).
			Render(truncate(snap.RecoveryLabel, width-4)))
		b.WriteString("\n\n")
	}

	b.WriteString(panelTitleStyle.Render(":: 状态"))
	b.WriteString("\n")
	b.WriteString(renderField("Phase", snap.Phase))
	b.WriteString(renderFlowField(snap.Flow))
	b.WriteString(renderField("Chapter", fmt.Sprintf("%d / %d", snap.CompletedCount, snap.TotalChapters)))
	b.WriteString(renderField("Words", formatNumber(snap.TotalWordCount)))

	if snap.InProgressChapter > 0 {
		b.WriteString(renderField("Writing", fmt.Sprintf("第%d章", snap.InProgressChapter)))
	}

	if len(snap.PendingRewrites) > 0 {
		b.WriteString("\n")
		b.WriteString(panelTitleStyle.Render(":: 返工"))
		b.WriteString("\n")
		b.WriteString(renderHighlightField("Pending", fmt.Sprintf("%v", snap.PendingRewrites)))
		if snap.RewriteReason != "" {
			b.WriteString(renderField("Reason", truncate(snap.RewriteReason, width-12)))
		}
	}

	if snap.PendingSteer != "" {
		b.WriteString("\n")
		b.WriteString(panelTitleStyle.Render(":: 干预"))
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
			b.WriteString(lipgloss.NewStyle().Foreground(colorAccent2).Render("✧"))
		case '·':
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render("·"))
		default:
			b.WriteRune(ch)
		}
	}
	label := lipgloss.NewStyle().Foreground(colorMuted).Render("  AI 生成中...")
	return "\n" + b.String() + "\n" + label
}

// renderEventContent 将事件列表渲染为纯文本（供 viewport 使用）。
func renderEventContent(events []orchestrator.UIEvent, width int) string {
	var b strings.Builder
	for i, ev := range events {
		ts := ev.Time.Format("15:04:05")
		cat := ev.Category

		color, ok := categoryColors[cat]
		if !ok {
			color = colorText
		}

		catStyle := lipgloss.NewStyle().Foreground(color).Bold(true).Width(7)
		tsStyle := lipgloss.NewStyle().Foreground(colorDim)
		sumStyle := lipgloss.NewStyle().Foreground(colorText)
		// SYSTEM 和 ERROR 的摘要用自身颜色高亮
		if cat == "SYSTEM" || cat == "ERROR" || cat == "REVIEW" {
			sumStyle = lipgloss.NewStyle().Foreground(color)
		}

		maxSumW := width - 20
		summary := ev.Summary
		if cat == "ERROR" {
			// 错误信息不截断，自动换行
			lines := wrapStreamText(summary, maxSumW)
			first := tsStyle.Render(ts) + " " + catStyle.Render(cat) + " " + sumStyle.Render(lines[0])
			b.WriteString(first)
			indent := strings.Repeat(" ", 16) // 对齐到摘要起始位置
			for _, l := range lines[1:] {
				b.WriteString("\n" + indent + sumStyle.Render(l))
			}
		} else {
			line := tsStyle.Render(ts) + " " + catStyle.Render(cat) + " " + sumStyle.Render(truncate(summary, maxSumW))
			b.WriteString(line)
		}
		if i < len(events)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderEventFlowViewport 用 viewport 包装渲染事件流面板。
func renderEventFlowViewport(vp viewport.Model, width, height int, focused bool) string {
	// 标题栏
	titleColor := colorDim
	if focused {
		titleColor = colorAccent
	}
	title := lipgloss.NewStyle().Foreground(titleColor).Render(":: 事件流")
	lineW := width - lipgloss.Width(title) - 4
	if lineW < 0 {
		lineW = 0
	}
	separator := lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", lineW))
	header := " " + title + " " + separator

	vpH := height - 1
	if vpH < 1 {
		vpH = 1
	}
	style := lipgloss.NewStyle().
		Width(width).
		Height(vpH).
		Padding(0, 1)

	return header + "\n" + style.Render(vp.View())
}

// renderStreamPanel 渲染流式输出面板（中间列下半部分）。
func renderStreamPanel(vp viewport.Model, width, height int, focused bool) string {
	// 分隔标题栏（始终醒目）
	title := lipgloss.NewStyle().Foreground(colorAccent).Bold(focused).Render(":: 实时输出")
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

// renderStreamContent 将流式输出按轮次渲染为语义分块。
// Agent 调度块（以 ▸ 开头）用 accent 标题 + dim 指令；正文块用标准文本色。
func renderStreamContent(rounds []string, width int) string {
	if width < 24 {
		width = 24
	}

	var blocks []string
	for _, round := range rounds {
		text := strings.TrimSpace(round)
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "▸") {
			blocks = append(blocks, renderAgentBlock(text, width))
		} else {
			blocks = append(blocks, renderChapterBlock(text, width))
		}
	}
	return strings.Join(blocks, "\n\n")
}

// renderAgentBlock 渲染 Agent 调度块：标题 + 分隔线 + 任务指令。
func renderAgentBlock(text string, width int) string {
	headerLine, body, _ := strings.Cut(text, "\n")

	// 标题行 + 分隔线
	titleW := lipgloss.Width(headerLine)
	lineW := max(0, width-titleW-1)
	header := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(headerLine) +
		" " + lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", lineW))

	var b strings.Builder
	b.WriteString(header)

	// 任务指令：dim 色，缩进 2 格
	body = strings.TrimSpace(body)
	if body != "" {
		taskStyle := lipgloss.NewStyle().Foreground(colorMuted)
		lines := wrapStreamText(body, max(16, width-6))
		b.WriteString("\n")
		for i, line := range lines {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(taskStyle.Render("  " + line))
		}
	}
	return b.String()
}

// renderChapterBlock 渲染正文块，自动区分思考内容和章节正文。
// 思考内容（ThinkingSep 标记的段落）用淡色斜体，正文用标准文本色。
func renderChapterBlock(text string, width int) string {
	contentStyle := lipgloss.NewStyle().Foreground(colorText)
	thinkStyle := lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	wrapW := max(16, width-4)

	// 按 ThinkingSep 分割：奇数段是思考，偶数段是正文
	// 格式：[正文] \x02 [思考] [正文] \x02 [思考] ...
	parts := strings.Split(text, orchestrator.ThinkingSep)

	var b strings.Builder
	for i, part := range parts {
		part = strings.TrimRight(part, " ")
		if part == "" {
			continue
		}
		isThinking := i > 0 && i%2 != 0 // ThinkingSep 之后的奇数段是思考
		// 如果整段都是思考标记开头（第一个 part 之前无正文），调整判断
		if i == 0 && part == "" {
			continue
		}

		style := contentStyle
		if isThinking {
			style = thinkStyle
		}

		lines := wrapStreamText(part, wrapW)
		for j, line := range lines {
			if b.Len() > 0 && j == 0 {
				b.WriteString("\n")
			} else if j > 0 {
				b.WriteString("\n")
			}
			b.WriteString(style.Render(line))
		}
	}
	return b.String()
}

func wrapStreamText(text string, width int) []string {
	if width < 8 {
		return []string{text}
	}

	var out []string
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(raw) == "" {
			out = append(out, "")
			continue
		}
		if compact, ok := compactJSONLine(raw, width); ok {
			out = append(out, compact)
			continue
		}
		prefix, rest, nextPrefix := parseWrapPrefix(raw)
		wrapped := wrapRunes(rest, max(4, width-lipgloss.Width(prefix)))
		for i, line := range wrapped {
			if i == 0 {
				out = append(out, prefix+line)
				continue
			}
			out = append(out, nextPrefix+line)
		}
	}
	return out
}

func compactJSONLine(line string, width int) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", false
	}
	if !(strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		return "", false
	}

	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return "", false
	}

	compact, err := json.Marshal(value)
	if err != nil {
		return "", false
	}

	text := string(compact)
	limit := max(24, width-2)
	if lipgloss.Width(text) > limit {
		text = truncate(text, limit-1)
	}
	return lipgloss.NewStyle().Foreground(colorDim).Render("JSON: ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#8fb7c9")).Render(text), true
}

func parseWrapPrefix(line string) (prefix, content, nextPrefix string) {
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	trimmed := strings.TrimSpace(line)

	switch {
	case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "), strings.HasPrefix(trimmed, "• "):
		prefix = indent + trimmed[:2]
		content = strings.TrimSpace(trimmed[2:])
		nextPrefix = indent + "  "
		return prefix, content, nextPrefix
	case orderedListPrefix(trimmed) != "":
		marker := orderedListPrefix(trimmed)
		prefix = indent + marker
		content = strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
		nextPrefix = indent + strings.Repeat(" ", lipgloss.Width(marker))
		return prefix, content, nextPrefix
	case strings.HasPrefix(trimmed, "```"):
		return indent, trimmed, indent
	default:
		return indent, trimmed, indent
	}
}

func orderedListPrefix(line string) string {
	end := strings.Index(line, ". ")
	if end <= 0 {
		return ""
	}
	for _, r := range line[:end] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return line[:end+2]
}

func wrapRunes(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	if width < 2 {
		return []string{text}
	}

	var lines []string
	var current strings.Builder
	currentWidth := 0

	for _, r := range text {
		rw := lipgloss.Width(string(r))
		if currentWidth > 0 && currentWidth+rw > width {
			lines = append(lines, strings.TrimRight(current.String(), " "))
			current.Reset()
			currentWidth = 0
			if r == ' ' {
				continue
			}
		}
		current.WriteRune(r)
		currentWidth += rw
	}
	if current.Len() > 0 {
		lines = append(lines, strings.TrimRight(current.String(), " "))
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// renderDetailContent 构建右侧详情面板内容。
// 优先展示基础设定（大纲、角色），然后是运行时信息（提交、审阅等）。
func renderDetailContent(snap orchestrator.UISnapshot, contentW int) string {
	var b strings.Builder

	// 大纲
	if len(snap.Outline) > 0 {
		outlineHeader := ":: 大纲"
		if snap.Layered {
			outlineHeader = fmt.Sprintf(":: 大纲（%s · 动态续写）", snap.CurrentVolumeArc)
		}
		b.WriteString(panelTitleStyle.Render(outlineHeader))
		b.WriteString("\n")
		for _, e := range snap.Outline {
			ch := fmt.Sprintf("%2d", e.Chapter)
			var marker, chStyle string
			if snap.CompletedCount >= e.Chapter {
				// 已完成：绿点 + 柔色章节号
				marker = lipgloss.NewStyle().Foreground(colorSuccess).Render("●")
				chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
			} else if snap.InProgressChapter == e.Chapter {
				// 进行中：金色箭头 + 高亮章节号
				marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸")
				chStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(ch)
			} else {
				// 未开始：暗圆 + 暗章节号
				marker = lipgloss.NewStyle().Foreground(colorDim).Render("○")
				chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
			}
			title := truncate(e.Title, contentW-6)
			titleStyle := cardContentStyle
			if snap.CompletedCount < e.Chapter && snap.InProgressChapter != e.Chapter {
				titleStyle = lipgloss.NewStyle().Foreground(colorMuted)
			}
			line := marker + chStyle + " " + titleStyle.Render(title)
			b.WriteString(line)
			b.WriteString("\n")
		}
		// 滚动规划提示
		compassStyle := lipgloss.NewStyle().Foreground(colorDim).Italic(true)
		if snap.Layered {
			if snap.NextVolumeTitle != "" {
				b.WriteString(compassStyle.Render("  ┄ 下一卷：" + snap.NextVolumeTitle))
				b.WriteString("\n")
			}
			b.WriteString(compassStyle.Render("  ··· 后续章节随创作推进自动生成"))
			b.WriteString("\n")
			if snap.CompassDirection != "" {
				direction := fmt.Sprintf("  → 终局：%s", snap.CompassDirection)
				if snap.CompassScale != "" {
					direction += "（" + snap.CompassScale + "）"
				}
				b.WriteString(compassStyle.Render(truncate(direction, contentW)))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	// 角色
	if len(snap.Characters) > 0 {
		b.WriteString(panelTitleStyle.Render(":: 角色"))
		b.WriteString("\n")
		for _, c := range snap.Characters {
			b.WriteString(cardContentStyle.Render("· " + truncate(c, contentW-2)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// 前提
	if snap.Premise != "" {
		b.WriteString(panelTitleStyle.Render(":: 前提"))
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render(truncate(snap.Premise, contentW*3)))
		b.WriteString("\n\n")
	}

	// 运行时信息
	if snap.LastCommitSummary != "" {
		b.WriteString(cardTitleStyle.Render("~ 最近提交 ~"))
		b.WriteString("\n")
		b.WriteString(cardContentStyle.Render(snap.LastCommitSummary))
		b.WriteString("\n\n")
	}

	if snap.LastReviewSummary != "" {
		b.WriteString(cardTitleStyle.Render("~ 最近审阅 ~"))
		b.WriteString("\n")
		b.WriteString(cardContentStyle.Render(snap.LastReviewSummary))
		b.WriteString("\n\n")
	}

	if len(snap.RecentSummaries) > 0 {
		b.WriteString(cardTitleStyle.Render("~ 摘要 ~"))
		b.WriteString("\n")
		for _, s := range snap.RecentSummaries {
			b.WriteString(cardContentStyle.Render(truncate(s, contentW)))
			b.WriteString("\n")
		}
	}

	return b.String()
}

// renderDetailPanel 渲染右侧可滚动详情面板。
func renderDetailPanel(vp viewport.Model, width, height int, focused bool) string {
	borderColor := colorDim
	if focused {
		borderColor = colorAccent
	}
	style := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(baseBorder, false, false, false, true).
		BorderForeground(borderColor).
		Padding(0, 1)

	return style.Render(vp.View())
}

// renderWelcome 渲染新建态首屏。
func renderWelcome(width, height int, errMsg string, mode startupMode) string {
	// 简洁标题
	title := lipgloss.NewStyle().
		Foreground(colorAccent).
		Bold(true).
		Render("A I N O V E L")

	// 副标题
	subtitle := lipgloss.NewStyle().
		Foreground(colorMuted).
		Italic(true).
		Render("AI-Powered Novel Creation Engine")

	// 分隔线
	divW := 44
	if divW > width-8 {
		divW = width - 8
	}
	divider := lipgloss.NewStyle().Foreground(colorDim).
		Render(strings.Repeat("~", divW))

	// 功能亮点
	features := []struct{ icon, label, desc string }{
		{">>", "多模型协作", "Architect 规划 / Writer 创作 / Editor 审阅"},
		{"::", "断点恢复", "崩溃或中断后从上次进度自动续写"},
		{"<>", "实时干预", "创作过程中随时调整剧情走向"},
		{"##", "分层长篇", "支持卷-弧-章分层结构的长篇创作"},
	}
	iconStyle := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	featLabelStyle := lipgloss.NewStyle().Foreground(colorText)
	descStyle := lipgloss.NewStyle().Foreground(colorDim)
	var featLines []string
	for _, f := range features {
		line := iconStyle.Render(f.icon) + " " +
			featLabelStyle.Render(f.label) + "  " +
			descStyle.Render(f.desc)
		featLines = append(featLines, line)
	}
	feats := strings.Join(featLines, "\n")

	// 输入提示
	prompt := lipgloss.NewStyle().Foreground(colorText).
		Render("在下方输入你的小说需求开始创作")

	modeLine := lipgloss.NewStyle().
		Foreground(colorMuted).
		Render("当前模式：" + mode.label() + " · " + mode.subtitle())

	// 示例
	examples := []string{
		"写一部 12 章都市悬疑小说，主角是一名女法医",
		"创作一部仙侠长篇，主角从凡人修炼至飞升",
		"写一个科幻短篇，讲述 AI 觉醒后的伦理困境",
	}
	exStyle := lipgloss.NewStyle().Foreground(colorAccent)
	dotStyle := lipgloss.NewStyle().Foreground(colorDim)
	var exLines []string
	for _, ex := range examples {
		exLines = append(exLines, dotStyle.Render("  . ")+exStyle.Render(ex))
	}
	exBlock := strings.Join(exLines, "\n")

	// 组装
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(subtitle)
	b.WriteString("\n\n")
	b.WriteString(divider)
	b.WriteString("\n\n")
	b.WriteString(feats)
	b.WriteString("\n\n")
	b.WriteString(divider)
	b.WriteString("\n\n")
	b.WriteString(modeLine)
	b.WriteString("\n\n")
	b.WriteString(prompt)
	b.WriteString("\n\n")
	b.WriteString(exBlock)
	b.WriteString("\n\n")
	b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Italic(true).
		Render("Tab 切换模式 · 快速开始下 Enter 直接创作 · 共创规划下 Enter 进入对话"))

	if errMsg != "" {
		b.WriteString("\n\n")
		b.WriteString(lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("! " + errMsg))
	}

	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		AlignHorizontal(lipgloss.Center).
		AlignVertical(lipgloss.Center).
		Render(b.String())
}
