package tui

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Accelerator-mzq/ainovel-cli/internal/host"
	"github.com/Accelerator-mzq/ainovel-cli/internal/utils"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// renderTopBar 渲染顶部状态栏。
// 左侧：provider/model，中间：书名，右侧：状态胶囊。
func renderTopBar(snap host.UISnapshot, width int, spinnerFrame string) string {
	novelName := snap.NovelName
	if novelName == "" {
		novelName = "未定书名"
	}

	var infoParts []string
	if snap.Provider != "" {
		infoParts = append(infoParts, snap.Provider)
	}
	if snap.ModelName != "" {
		if w := formatContextWindow(snap.ModelContextWindow); w != "" {
			infoParts = append(infoParts, snap.ModelName+"("+w+")")
		} else {
			infoParts = append(infoParts, snap.ModelName)
		}
	}
	if snap.Style != "" && snap.Style != "default" {
		infoParts = append(infoParts, snap.Style)
	}
	leftText := strings.Join(infoParts, " · ")

	label := snap.StatusLabel
	if label == "" {
		label = "READY"
	}
	color, ok := statusColors[label]
	if !ok {
		color = colorDim
	}
	disp, ok := statusDisplay[label]
	if !ok {
		disp = struct {
			icon  string
			label string
		}{"○", strings.ToLower(label)}
	}
	icon := disp.icon
	if snap.IsRunning && spinnerFrame != "" {
		icon = spinnerFrame
	}
	var status string
	if icon != "" {
		status = statusIconStyle.Foreground(color).Render(icon) + " " + statusLabelStyle.Render(disp.label)
	} else {
		status = statusLabelStyle.Render(disp.label)
	}

	innerW := max(12, width-2)
	titleText := truncate(novelName, max(8, innerW/3))
	centerW := max(16, lipgloss.Width(titleText)+6)
	if centerW > innerW-24 {
		centerW = max(8, innerW-24)
	}
	sideTotal := innerW - centerW
	if sideTotal < 0 {
		sideTotal = 0
		centerW = innerW
	}
	leftW := sideTotal / 2
	rightW := innerW - centerW - leftW

	leftCell := lipgloss.NewStyle().
		Width(leftW).
		AlignHorizontal(lipgloss.Left).
		Foreground(colorDim).
		Render(truncate(leftText, leftW))
	centerCell := lipgloss.NewStyle().
		Width(centerW).
		AlignHorizontal(lipgloss.Center).
		Bold(true).
		Foreground(bodyTextColor).
		Render(titleText)
	rightCell := lipgloss.NewStyle().
		Width(rightW).
		AlignHorizontal(lipgloss.Right).
		Render(status)

	content := leftCell + centerCell + rightCell
	return topBarStyle.Width(width).
		Border(baseBorder, false, false, true, false).
		BorderForeground(colorDim).
		Render(content)
}

// renderStatePanel 把状态侧栏内容(已在 stateVP 中)包进左侧带右边框的盒子。
// 与 renderDetailPanel 对称：内容由 renderStateContent 生成并喂进 viewport，这里只负责框。
// MaxHeight 钳高，防止窗口缩小时溢出比右栏高（见 panels_test.go 的高度契约）。
func renderStatePanel(vp viewport.Model, width, height int, focused bool) string {
	borderColor := colorDim
	if focused {
		borderColor = colorAccent
	}
	style := lipgloss.NewStyle().
		Width(width).
		Height(height).
		MaxHeight(height).
		Border(baseBorder, false, true, false, false).
		BorderForeground(borderColor).
		Padding(1, 1)
	return style.Render(vp.View())
}

// renderStateContent 生成状态侧栏的纯内容(不含边框/外框)，供 stateVP.SetContent 使用。
func renderStateContent(snap host.UISnapshot, contentW int) string {
	contentW = max(12, contentW)
	agents := sidebarAgents(snap.Agents)
	idleAgents := sidebarIdleAgents(snap.Agents)
	var sections []string

	if snap.RecoveryLabel != "" {
		sections = append(sections, lipgloss.NewStyle().Foreground(colorMuted).Italic(true).
			Render(truncate(snap.RecoveryLabel, contentW)))
	}

	var overview strings.Builder
	overview.WriteString(renderField("运行态", snapshotRuntimeStateLabel(snap.RuntimeState)))
	overview.WriteString(renderField("阶段", snapshotPhaseLabel(snap.Phase)))
	overview.WriteString(renderField("流程", snapshotFlowLabel(snap.Flow)))
	if snap.Layered {
		overview.WriteString(renderField("已完成", fmt.Sprintf("%d 章", snap.CompletedCount)))
		// 分层动态规划：右栏只展示当前弧已展开的章节，"已规划"也用同一个口径，
		// 否则会把骨架弧 EstimatedChapters 的粗估算（如 92）混进来，与可见大纲对不上。
		// progress.TotalChapters 那个值仅用于内部 ContextProfile 决策，不要泄漏到 UI。
		if planned := len(snap.Outline); planned > 0 {
			overview.WriteString(renderField("已规划", fmt.Sprintf("%d 章", planned)))
		}
	} else {
		switch {
		case snap.TotalChapters > 0:
			overview.WriteString(renderField("进度", fmt.Sprintf("%d / %d 章", snap.CompletedCount, snap.TotalChapters)))
		default:
			overview.WriteString(renderField("已完成", fmt.Sprintf("%d 章", snap.CompletedCount)))
		}
	}
	overview.WriteString(renderField("字数", formatNumber(snap.TotalWordCount)))
	if label, ch := inProgressDisplay(snap); label != "" {
		overview.WriteString(renderField(label, fmt.Sprintf("第 %d 章", ch)))
	}
	if headline := snapshotHeadline(snap); headline != "" {
		label := "当前"
		if !snap.IsRunning {
			label = "待恢复"
		}
		overview.WriteString(renderHighlightField(label, truncate(headline, contentW-10)))
	}
	sections = append(sections, renderSidebarSection("概览", overview.String(), contentW))

	if len(agents) > 0 {
		var agentBody strings.Builder
		for _, agent := range agents {
			agentBody.WriteString(renderAgentLine(agent, contentW))
			agentBody.WriteString("\n")
		}
		if len(idleAgents) > 0 {
			agentBody.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render("待命: " + truncate(strings.Join(idleAgents, " · "), max(8, contentW-2))))
			agentBody.WriteString("\n")
		}
		sections = append(sections, renderSidebarSection("运行角色", agentBody.String(), contentW))
	}

	if len(snap.PendingRewrites) > 0 {
		var rewrite strings.Builder
		rewrite.WriteString(renderHighlightField("队列", fmt.Sprintf("%v", snap.PendingRewrites)))
		if snap.RewriteReason != "" {
			rewrite.WriteString(renderField("原因", truncate(snap.RewriteReason, contentW-10)))
		}
		sections = append(sections, renderSidebarSection("返工", rewrite.String(), contentW))
	}

	if snap.PendingSteer != "" {
		sections = append(sections, renderSidebarSection("干预",
			renderHighlightField("待处理", truncate(snap.PendingSteer, contentW-10)), contentW))
	}

	if body := renderUsageSidebar(snap, contentW); body != "" {
		sections = append(sections, renderSidebarSection("用量", body, contentW))
	}

	if body := renderCacheSidebar(snap, contentW); body != "" {
		sections = append(sections, renderSidebarSection("缓存", body, contentW))
	}

	if body := renderContextSidebar(snap, contentW); body != "" {
		sections = append(sections, renderSidebarSection("上下文", body, contentW))
	}

	return strings.Join(sections, "\n\n")
}

func renderAgentLine(agent host.AgentSnapshot, width int) string {
	stateColor := taskStatusColor(agent.State)
	icon := lipgloss.NewStyle().Foreground(stateColor).Render(agentStateIcon(agent.State))
	badge := lipgloss.NewStyle().Foreground(stateColor).Render(agentStateLabel(agent.State))
	name := lipgloss.NewStyle().Bold(true).Foreground(bodyTextColor).Render(agentDisplayName(agent.Name))
	line := icon + " " + name + " " + badge

	taskLine := agentTaskLine(agent)
	if taskLine != "" {
		line += "\n" + lipgloss.NewStyle().Foreground(colorDim).Render("  "+truncate(taskLine, max(8, width-2)))
	}

	detail := agent.Summary
	if agent.Tool != "" {
		detail = agent.Tool
	}
	if agent.State == "idle" && detail == "待命" {
		detail = ""
	}
	if detail != "" && detail != taskLine {
		line += "\n" + lipgloss.NewStyle().Foreground(colorMuted).Render("  "+truncate(detail, max(8, width-2)))
	}
	if ctx := agentContextLine(agent); ctx != "" {
		line += "\n" + lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render("  "+truncate(ctx, max(8, width-2)))
	}
	return line
}

func renderSidebarSection(title, body string, width int) string {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return ""
	}
	lineW := max(0, width-lipgloss.Width(title)-1)
	header := panelTitleStyle.Render(title) + " " +
		lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", lineW))
	card := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colorDim).
		PaddingLeft(1).
		Render(body)
	return header + "\n" + card
}

func sidebarAgents(agents []host.AgentSnapshot) []host.AgentSnapshot {
	var out []host.AgentSnapshot
	for _, agent := range agents {
		if agent.State == "idle" {
			continue
		}
		out = append(out, agent)
	}
	if len(out) == 0 {
		out = append(out, agents...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := out[i], out[j]
		if agentStateRank(li.State) != agentStateRank(lj.State) {
			return agentStateRank(li.State) < agentStateRank(lj.State)
		}
		return agentOrder(li.Name) < agentOrder(lj.Name)
	})
	return out
}

func sidebarIdleAgents(agents []host.AgentSnapshot) []string {
	var names []string
	hasActive := false
	for _, agent := range agents {
		if agent.State != "idle" {
			hasActive = true
			continue
		}
		names = append(names, agentDisplayName(agent.Name))
	}
	if !hasActive {
		return nil
	}
	sort.Strings(names)
	return names
}

// inProgressDisplay 计算"进行中"字段的标签和章节号。
// 根据 flow 选择动词（打磨/重写/写作）；in_progress_chapter 与 flow 不匹配时视为 stale：
//   - polishing/rewriting 模式下章节不在 pending_rewrites 中 → 回退到队列首章
//   - 字段为 0 时不渲染
func inProgressDisplay(snap host.UISnapshot) (label string, chapter int) {
	ch := snap.InProgressChapter
	switch snap.Flow {
	case "polishing":
		if ch <= 0 || !slices.Contains(snap.PendingRewrites, ch) {
			if len(snap.PendingRewrites) == 0 {
				return "", 0
			}
			ch = snap.PendingRewrites[0]
		}
		return "打磨中", ch
	case "rewriting":
		if ch <= 0 || !slices.Contains(snap.PendingRewrites, ch) {
			if len(snap.PendingRewrites) == 0 {
				return "", 0
			}
			ch = snap.PendingRewrites[0]
		}
		return "重写中", ch
	default:
		if ch <= 0 {
			return "", 0
		}
		return "写作中", ch
	}
}

func snapshotHeadline(snap host.UISnapshot) string {
	if snap.PendingSteer != "" {
		if !snap.IsRunning {
			return "待恢复：处理用户干预"
		}
		return "等待处理用户干预"
	}
	if len(snap.PendingRewrites) > 0 {
		if !snap.IsRunning {
			return "待恢复：返工处理"
		}
		return "等待返工处理"
	}
	return ""
}

func snapshotPhaseLabel(phase string) string {
	switch phase {
	case "premise":
		return "前提"
	case "outline":
		return "大纲"
	case "writing":
		return "写作"
	case "complete":
		return "完成"
	case "init":
		return "初始化"
	default:
		if phase == "" {
			return "-"
		}
		return phase
	}
}

func snapshotRuntimeStateLabel(state string) string {
	switch state {
	case "running":
		return "运行中"
	case "pausing":
		return "暂停中"
	case "paused":
		return "已暂停"
	case "completed":
		return "已完成"
	default:
		return "空闲"
	}
}

func snapshotFlowLabel(flow string) string {
	switch flow {
	case "":
		return "-"
	case "writing":
		return "写作"
	case "reviewing":
		return "评审"
	case "rewriting":
		return "重写"
	case "polishing":
		return "打磨"
	case "steering":
		return "干预"
	default:
		return flow
	}
}

func renderUsageSidebar(snap host.UISnapshot, width int) string {
	if snap.TotalInputTokens <= 0 && snap.TotalOutputTokens <= 0 && snap.TotalCostUSD <= 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(renderField("输入", formatTokensCompact(snap.TotalInputTokens)))
	b.WriteString(renderField("输出", formatTokensCompact(snap.TotalOutputTokens)))
	if cost := formatCostUSD(snap.TotalCostUSD); cost != "" {
		b.WriteString(renderField("费用", cost))
	}
	if saved := formatCostUSD(snap.TotalSavedUSD); saved != "" {
		b.WriteString(renderField("节省", saved))
	}

	agentStats := usageStatsByCost(snap.CachePerAgent)
	if len(agentStats) > 0 {
		b.WriteString(renderUsageGroupHeader("角色", width))
		limit := min(len(agentStats), 4)
		for i := 0; i < limit; i++ {
			a := agentStats[i]
			b.WriteString(renderUsageLine(agentDisplayName(a.Role), eventAgentColor(a.Role), a.Input, a.Output, a.Cost, width))
			b.WriteString("\n")
		}
	}
	modelStats := usageStatsByCost(snap.CachePerModel)
	if len(modelStats) > 0 {
		b.WriteString(renderUsageGroupHeader("模型", width))
		limit := min(len(modelStats), 4)
		for i := 0; i < limit; i++ {
			a := modelStats[i]
			b.WriteString(renderUsageLine(modelDisplayName(a.Model), bodyTextColor, a.Input, a.Output, a.Cost, width))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func usageStatsByCost(in []host.AgentCacheStat) []host.AgentCacheStat {
	out := append([]host.AgentCacheStat(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Input+out[i].Output > out[j].Input+out[j].Output
	})
	return out
}

func renderUsageGroupHeader(label string, width int) string {
	line := lipgloss.NewStyle().Foreground(colorDim).
		Render(strings.Repeat("·", max(8, width-lipgloss.Width(label)-3)))
	return lipgloss.NewStyle().Foreground(colorMuted).Render(label+" ") + line + "\n"
}

func renderUsageLine(name string, color lipgloss.TerminalColor, input, output int, cost float64, width int) string {
	nameW := 11
	if width < 24 {
		nameW = 8
	}
	nameCell := lipgloss.NewStyle().Foreground(color).Width(nameW).
		Render(truncate(name, nameW))
	tokens := formatTokensCompact(input + output)
	right := tokens
	if costStr := formatCostUSD(cost); costStr != "" {
		right += " · " + costStr
	}
	return fitInlineLine(nameCell+lipgloss.NewStyle().Foreground(colorDim).Render(right), width)
}

func modelDisplayName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	parts := strings.Split(model, "/")
	if len(parts) >= 3 {
		return strings.Join(parts[1:], "/")
	}
	if len(parts) == 2 {
		return parts[1]
	}
	return model
}

// renderCacheSidebar 渲染左栏"缓存"区块。
//
// 三种态：
//  1. 完全没消费 token：返回空，section 不渲染
//  2. 当前会话所有 role 都跑的是不支持 prompt cache 的模型：仅渲染一行"未启用"提示
//  3. 已启用：顶部"命中率累计/近10 · 节省 · 读/写"+ 分隔 + per-role 行
//
// per-role 行 capable 时显示"累计/近10%"双数字；不 capable 时显示"未启用"。
// 通过累计 vs 近 N 次的对比可以识别"前期拖累"vs"稳态低命中"。
func renderCacheSidebar(snap host.UISnapshot, width int) string {
	// 上游 streaming 没发 OpenAI 的 final usage chunk —— 累计数据全为 0，
	// 但这不是"没启用 cache"也不是"用量太低被门控藏起来"，必须显式提示，
	// 否则用户会一直以为左栏写了缓存代码却显示不出来。优先级最高。
	if snap.MissingAssistantUsage > 0 && snap.TotalInputTokens <= 0 {
		warn := lipgloss.NewStyle().Foreground(colorError).Bold(true).
			Render(fmt.Sprintf("⚠ 上游未返 usage（%d 次）", snap.MissingAssistantUsage))
		hint := lipgloss.NewStyle().Foreground(colorDim).Italic(true).
			Render(truncate("检查 provider stream_options.include_usage", max(8, width-2)))
		return warn + "\n" + hint + "\n"
	}

	if snap.TotalInputTokens <= 0 && snap.TotalCacheWriteTokens <= 0 {
		return ""
	}

	// 全程未启用 → 显示一行解释，避免用户误判为"0% 命中需要排查"
	if !snap.OverallCacheCapable && snap.TotalCacheReadTokens == 0 && snap.TotalCacheWriteTokens == 0 {
		return lipgloss.NewStyle().Foreground(colorDim).Italic(true).
			Render(truncate("当前模型未启用 prompt cache", max(8, width-2))) + "\n"
	}

	var b strings.Builder

	// 顶部综合指标：累计 + 近 N 各占一行，标签明示，避免 "X% · 近N Y%" 这种
	// 三种分隔符（百分号 / 中点 / 文字）混杂导致语义不清。
	overallHit := cacheHitRate(snap.TotalCacheReadTokens, snap.TotalInputTokens)
	b.WriteString(renderField("累计命中", colorPercent(overallHit)))
	if snap.OverallRecentSamples > 0 && snap.OverallRecentInput > 0 {
		recent := cacheHitRate(snap.OverallRecentCacheRead, snap.OverallRecentInput)
		b.WriteString(renderField(fmt.Sprintf("近%d命中", snap.OverallRecentSamples), colorPercent(recent)))
	}

	if savedStr := formatCostUSD(snap.TotalSavedUSD); savedStr != "" {
		b.WriteString(renderField("节省", savedStr))
	}

	// 读/写量分两行。写量为 0 在 OpenAI / Gemini 系协议是常态——
	// 这两家是自动透明 caching，cache 写入完全免费（首次未命中按正常输入价，
	// 建立 cache 不收任何溢价），所以协议本身不暴露 cache_creation 字段，没必要。
	// 只有 Anthropic / Bedrock 系才报写量，因为他们写要加价（5m +25%/1h +100%），
	// 必须把这个量给用户用于计费。
	b.WriteString(renderField("缓存读量", formatTokensCompact(snap.TotalCacheReadTokens)))
	if snap.TotalCacheWriteTokens > 0 {
		b.WriteString(renderField("缓存写量", formatTokensCompact(snap.TotalCacheWriteTokens)))
	} else if snap.TotalCacheReadTokens > 0 {
		hint := lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render("(自动缓存无溢价)")
		b.WriteString(renderField("缓存写量", "0 "+hint))
	}

	if len(snap.CachePerAgent) > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(colorDim).
			Render(strings.Repeat("·", max(8, width-12))))
		b.WriteString("\n")
		for _, a := range snap.CachePerAgent {
			b.WriteString(renderCacheAgentLine(a, width))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// colorPercent 把百分比按命中率分档着色后转字符串，仅用于值列。
func colorPercent(p float64) string {
	return lipgloss.NewStyle().Foreground(cacheHitColor(p)).Bold(true).
		Render(formatPercent(p))
}

// renderCacheAgentLine 渲染单个 role 行：role + 命中率 + 缓存读 / 总输入。
//
// 把分子分母都摆出来（cacheRead / input）让用户一眼就能验算命中率的来源，
// 也能识别"高百分比但小样本"的侥幸数据（比如 100% / 1k 的可信度低于 80% / 300k）。
//
// 百分比优先用滑动窗稳态值；窗内无样本时回落到累计。整个左栏只有这一处用 "/"，
// 语义专一（数学除号：cache 命中量 / 总输入量），不会与其它分隔符混淆。
//
// 三种态：
//
//	未启用     "WRITER        未启用"
//	已启用     "WRITER        85%  · 323k / 394k"
//	无 cache  显式"未启用"，不混进 0/0 干扰判读
func renderCacheAgentLine(a host.AgentCacheStat, width int) string {
	// role 名与"运行角色"区保持完全一致；Width 取 12 让最长的 COORDINATOR
	// 仍能保留 1 列尾随空格做分隔，其它 role 自动右侧填充。
	roleStyle := lipgloss.NewStyle().Foreground(eventAgentColor(a.Role)).Width(12)
	role := roleStyle.Render(agentDisplayName(a.Role))

	if !a.CacheCapable {
		dim := lipgloss.NewStyle().Foreground(colorDim).Italic(true)
		_ = width
		return role + dim.Render("未启用")
	}

	// 稳态命中率优先；窗内无样本时回落到累计。
	hit := cacheHitRate(a.RecentCacheRead, a.RecentInput)
	if a.RecentSamples == 0 || a.RecentInput == 0 {
		hit = cacheHitRate(a.CacheRead, a.Input)
	}
	// 百分比固定 4 列宽（"100%"），避免读量列在 "5%" 与 "85%" 之间左右跳。
	pctCell := lipgloss.NewStyle().Width(4).
		Render(colorPercent(hit))

	// 累计读 / 累计输入 — 即便上方百分比是滑动窗值，分子分母都用累计，因为
	// "看出规模"才是这一列的主诉求；百分比单独提供稳态信号即可。
	tokens := lipgloss.NewStyle().Foreground(colorDim).Render(
		" · " + formatTokensCompact(a.CacheRead) + " / " + formatTokensCompact(a.Input))
	_ = width
	return role + pctCell + tokens
}

// cacheHitRate 在 input 已含 cacheRead 的语义下直接除得百分比。
// input == 0 时返回 0，避免出现假命中。
func cacheHitRate(cacheRead, input int) float64 {
	if input <= 0 {
		return 0
	}
	return float64(cacheRead) / float64(input) * 100
}

// cacheHitColor 命中率染色：≥50% 绿 / 20–50% 黄 / <20% 红。
// 用与上下文使用率相反的方向：缓存命中率越高越健康。
func cacheHitColor(percent float64) lipgloss.AdaptiveColor {
	switch {
	case percent >= 50:
		return colorSuccess
	case percent >= 20:
		return colorReview
	default:
		return colorError
	}
}

func formatPercent(p float64) string {
	if p <= 0 {
		return "0%"
	}
	if p < 10 {
		return fmt.Sprintf("%.1f%%", p)
	}
	return fmt.Sprintf("%.0f%%", p)
}

// formatTokensCompact 把 token 数渲染成 "8.2k" / "1.4M" 这种紧凑形式。
// 用于狭窄的 per-role 行，避免和 formatNumber 的逗号风格挤出去。
func formatTokensCompact(n int) string {
	if n <= 0 {
		return "0"
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func renderContextSidebar(snap host.UISnapshot, width int) string {
	if snap.ContextWindow <= 0 && snap.ContextStrategy == "" && snap.ContextScope == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(renderContextUsageField("主上下文", snap.ContextPercent, snap.ContextTokens, snap.ContextWindow))
	if strategy := contextStrategyLabel(snap.ContextStrategy); strategy != "" {
		b.WriteString(renderField("最近策略", truncate(strategy, max(8, width-12))))
	}
	if scope := contextScopeLabel(snap.ContextScope); scope != "" {
		b.WriteString(renderField("当前视图", scope))
	}
	if snap.ContextSummaryCount > 0 {
		b.WriteString(renderField("摘要", fmt.Sprintf("%d 条", snap.ContextSummaryCount)))
	}
	if snap.ContextActiveMessages > 0 {
		b.WriteString(renderField("消息数", fmt.Sprintf("%d", snap.ContextActiveMessages)))
	}
	if snap.ContextCompactedCount > 0 || snap.ContextKeptCount > 0 {
		b.WriteString(renderField("最近重写", fmt.Sprintf("%d → %d", snap.ContextCompactedCount, snap.ContextKeptCount)))
	}
	return b.String()
}

func contextScopeLabel(scope string) string {
	switch scope {
	case "baseline":
		return "基线"
	case "projected":
		return "投影"
	case "recovered":
		return "恢复"
	case "committed":
		return "已提交"
	case "skipped":
		return "熔断跳过"
	default:
		return scope
	}
}

func contextStrategyLabel(strategy string) string {
	switch strategy {
	case "":
		return ""
	case "tool_result_microcompact":
		return "工具结果微压缩"
	case "light_trim":
		return "轻裁剪"
	case "full_summary":
		return "完整摘要"
	default:
		return strategy
	}
}

func agentDisplayName(name string) string {
	return strings.ToUpper(name)
}

func agentTaskLine(agent host.AgentSnapshot) string {
	if agent.TaskKind != "" {
		return taskKindLabel(agent.TaskKind)
	}
	if agent.Summary != "" {
		return agent.Summary
	}
	return ""
}

func agentContextLine(agent host.AgentSnapshot) string {
	ctx := agent.Context
	if ctx.ContextWindow <= 0 || ctx.Tokens <= 0 {
		return ""
	}
	percentColor := contextPercentColor(ctx.Percent)
	percentStr := lipgloss.NewStyle().Foreground(percentColor).Render(fmt.Sprintf("ctx %.0f%%", ctx.Percent))
	parts := []string{percentStr}
	if scope := contextScopeLabel(ctx.Scope); scope != "" {
		parts = append(parts, scope)
	}
	if strategy := contextStrategyLabel(ctx.Strategy); strategy != "" {
		parts = append(parts, strategy)
	}
	return strings.Join(parts, " · ")
}

func agentStateRank(state string) int {
	switch state {
	case "running":
		return 0
	case "failed":
		return 1
	default:
		return 2
	}
}

func agentOrder(name string) int {
	switch {
	case strings.HasPrefix(name, "architect"):
		return 0
	case name == "coordinator":
		return 1
	case name == "editor":
		return 2
	case name == "writer":
		return 3
	default:
		return 9
	}
}

func agentStateLabel(state string) string {
	switch state {
	case "running":
		return "运行中"
	case "failed":
		return "异常"
	case "idle":
		return "待命"
	default:
		return state
	}
}

func agentStateIcon(state string) string {
	switch state {
	case "running":
		return "●"
	case "failed":
		return "×"
	default:
		return "·"
	}
}

func taskStatusColor(status string) lipgloss.AdaptiveColor {
	switch status {
	case "running":
		return colorSuccess
	case "queued":
		return colorMuted
	case "failed", "canceled":
		return colorError
	case "succeeded":
		return colorSuccess
	default:
		return colorDim
	}
}

func taskKindLabel(kind string) string {
	switch kind {
	case "foundation_plan":
		return "基础规划"
	case "chapter_write":
		return "章节写作"
	case "chapter_review":
		return "章节评审"
	case "chapter_rewrite":
		return "章节重写"
	case "chapter_polish":
		return "章节打磨"
	case "arc_expand":
		return "弧展开"
	case "volume_append":
		return "下一卷规划"
	case "steer_apply":
		return "处理干预"
	case "coordinator_decision":
		return "协调推进"
	default:
		return kind
	}
}

// renderEventContent 将事件列表渲染为层次化事件流。
// DISPATCH 作为顶级标题，子代理工具缩进显示，形成清晰的调度树。
// spinnerFrame 用于给"进行中"的行渲染动态图标（跟 topbar spinner 同步）。
func renderEventContent(events []host.Event, width, spinnerFrame int) string {
	var b strings.Builder
	for i, ev := range events {
		b.WriteString(renderEventLine(ev, width, spinnerFrame))
		if i < len(events)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// 进行中的调用类事件使用的 spinner 帧（bubbles.Spinner.Dot，独立于顶栏 MiniDot）。
var eventRunningFrames = toolSpinnerFrames

func runningSpinner(frame int) string {
	return eventRunningFrames[frame%len(eventRunningFrames)]
}

func renderEventLine(ev host.Event, width, spinnerFrame int) string {
	tsStr := lipgloss.NewStyle().Foreground(colorDim).Render(ev.Time.Format("15:04:05"))
	indent := ""
	if ev.Depth > 0 {
		indent = "  "
	}
	maxSumW := max(20, width-12-ev.Depth*2)

	running := ev.Running()
	durStr := renderEventDuration(ev.Duration)

	switch {
	case ev.Category == "DISPATCH":
		// 三态：进行中（accent spinner + 加粗）/ 失败（红 ✕）/ 完成（绿 ✓）
		var icon string
		switch {
		case running:
			icon = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(runningSpinner(spinnerFrame))
		case ev.Failed:
			icon = lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("✕")
		default:
			icon = lipgloss.NewStyle().Foreground(colorSuccess).Render("✓")
		}
		sum := renderDispatchSummary(ev.Summary, maxSumW)
		if running {
			// 进行中保持原样但加粗
			sum = lipgloss.NewStyle().Bold(true).Render(sum)
		}
		line := tsStr + " " + icon + " " + sum
		if !running {
			line += durStr
		}
		return line

	case ev.Category == "DONE":
		// 兼容旧 replay 数据；新流程不再产生 DONE 独立事件
		icon := lipgloss.NewStyle().Foreground(colorSuccess).Render("✓")
		color := eventAgentColor(ev.Agent)
		name := lipgloss.NewStyle().Foreground(color).Render(agentDisplayName(ev.Agent))
		return tsStr + " " + icon + " " + name + durStr

	case ev.Category == "TOOL" && ev.Depth == 0:
		// coordinator 自身工具
		var icon, sum string
		switch {
		case running:
			icon = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(runningSpinner(spinnerFrame))
			sum = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(truncate(ev.Summary, maxSumW))
		case ev.Failed:
			icon = lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("✕")
			sum = lipgloss.NewStyle().Foreground(colorError).Render(truncate(ev.Summary, maxSumW))
		default:
			icon = lipgloss.NewStyle().Foreground(colorTool).Render("◇")
			sum = lipgloss.NewStyle().Foreground(colorTool).Render(truncate(ev.Summary, maxSumW))
		}
		line := tsStr + " " + icon + " " + sum
		if !running {
			line += durStr
		}
		return line

	case ev.Category == "TOOL":
		// subagent 内部工具（Depth=1）
		var icon, sum string
		switch {
		case running:
			icon = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(runningSpinner(spinnerFrame))
			sum = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(truncate(ev.Summary, maxSumW))
		case ev.Failed:
			icon = lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("✕")
			sum = lipgloss.NewStyle().Foreground(colorError).Render(truncate(ev.Summary, maxSumW))
		default:
			icon = lipgloss.NewStyle().Foreground(colorDim).Render("├")
			sum = lipgloss.NewStyle().Foreground(colorMuted).Render(truncate(ev.Summary, maxSumW))
		}
		line := tsStr + " " + indent + icon + " " + sum
		if !running {
			line += durStr
		}
		return line

	case ev.Category == "ERROR":
		icon := lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("✕")
		errStyle := lipgloss.NewStyle().Foreground(colorError)
		lines := wrapStreamText(ev.Summary, maxSumW)
		first := tsStr + " " + indent + icon + " " + errStyle.Render(lines[0])
		pad := strings.Repeat(" ", 10+len(indent))
		for _, l := range lines[1:] {
			first += "\n" + pad + errStyle.Render(l)
		}
		if durStr != "" {
			first += durStr
		}
		return first

	case ev.Category == "SYSTEM":
		icon := lipgloss.NewStyle().Foreground(colorAccent).Render("⚙")
		sumColor := colorMuted
		if ev.Level == "warn" {
			sumColor = colorAccent
		}
		sum := lipgloss.NewStyle().Foreground(sumColor).Render(truncate(ev.Summary, maxSumW))
		return tsStr + " " + indent + icon + " " + sum

	case ev.Category == "USER":
		// 用户在输入框发送的 Steer / Continue 文本回显；与 SYSTEM 的 ⚙ 拉开形态，用 ✎ 暗示"输入"。
		// 颜色用 colorAccent2（青绿）与 SYSTEM 的金色拉开，避免误读为系统消息。
		icon := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true).Render("✎")
		sum := lipgloss.NewStyle().Foreground(colorAccent2).Render(truncate(ev.Summary, maxSumW))
		return tsStr + " " + indent + icon + " " + sum

	case ev.Category == "CONTEXT" || ev.Category == "COMPACT":
		icon := lipgloss.NewStyle().Foreground(colorContext).Render("⚙")
		sumColor := colorContext
		if ev.Level == "debug" {
			sumColor = colorMuted
		}
		sum := lipgloss.NewStyle().Foreground(sumColor).Render(truncate(ev.Summary, maxSumW))
		return tsStr + " " + indent + icon + " " + sum

	default:
		// 已知 category 走映射色；未知 category 跟随终端默认前景，避免硬塞 colorText。
		if color, ok := categoryColors[ev.Category]; ok {
			icon := lipgloss.NewStyle().Foreground(color).Render("·")
			sum := lipgloss.NewStyle().Foreground(color).Render(truncate(ev.Summary, maxSumW))
			return tsStr + " " + indent + icon + " " + sum
		}
		icon := lipgloss.NewStyle().Foreground(colorDim).Render("·")
		return tsStr + " " + indent + icon + " " + truncate(ev.Summary, maxSumW)
	}
}

// renderDispatchSummary 渲染 DISPATCH 摘要：Agent 名用角色色，任务用淡色。
func renderDispatchSummary(summary string, maxW int) string {
	agentName := summary
	taskPart := ""
	if idx := strings.Index(summary, "（"); idx > 0 {
		agentName = summary[:idx]
		taskPart = summary[idx:]
	}
	displayName := agentDisplayName(agentName)
	color := eventAgentColor(agentName)
	nameW := lipgloss.Width(displayName)
	if nameW >= maxW {
		return lipgloss.NewStyle().Foreground(color).Bold(true).Render(truncate(displayName, maxW))
	}
	result := lipgloss.NewStyle().Foreground(color).Bold(true).Render(displayName)
	if taskPart != "" {
		remaining := maxW - nameW
		if remaining > 2 {
			result += lipgloss.NewStyle().Foreground(colorMuted).Render(truncate(taskPart, remaining))
		}
	}
	return result
}

// eventAgentColor 返回 Agent 角色对应的主题色。
func eventAgentColor(agent string) lipgloss.AdaptiveColor {
	switch {
	case strings.HasPrefix(agent, "architect"):
		return colorAccent2
	case agent == "writer":
		return colorTool
	case agent == "editor":
		return colorReview
	default:
		return colorAccent
	}
}

// renderEventDuration 将 Duration 渲染为淡色括号标注，零值返回空。
func renderEventDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return " " + lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render("("+formatDuration(d)+")")
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%ds", m, s)
}

func renderEventActivity(snap host.UISnapshot, frame, width int) string {
	if !snap.IsRunning {
		return ""
	}
	return renderEventSparkle(frame, width)
}

var sparkleFrames = []string{
	"✦  ·   ✧   ·  ✦",
	"·  ✧   ·  ✦   ·",
	"  ✧   ·  ✦   · ",
	"   ·  ✦   ·  ✧ ",
	"✧   ·  ✦  ·   ✧",
	" ·  ✧   ·  ✦  ·",
	"✦   ·  ✧   ·  ✦",
	" ·  ✦   ·  ✧   ",
}

func renderEventSparkle(frame, width int) string {
	pattern := sparkleFrames[frame%len(sparkleFrames)]

	var b strings.Builder
	for _, ch := range pattern {
		switch ch {
		case '✦':
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#d4a21a")).Bold(true).Render("✦"))
		case '✧':
			b.WriteString(lipgloss.NewStyle().Foreground(colorAccent2).Render("✧"))
		case '·':
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render("·"))
		default:
			b.WriteRune(ch)
		}
	}
	_ = width
	return " " + b.String()
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
func renderStreamPanel(vp viewport.Model, width, height int, focused, running bool, frame int) string {
	// 分隔标题栏（始终醒目）：粗竖条前缀 + 永远 Bold + 强调色，避免与思考的淡灰斜体撞色
	// focused 时额外下划线，区分焦点态。
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Underline(focused)
	title := titleStyle.Render("▍实时输出")
	if running {
		status := renderStreamActivity(frame)
		title += " " + status
	}
	lineW := width - lipgloss.Width(title) - 4
	if lineW < 0 {
		lineW = 0
	}
	separator := lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", lineW))
	header := " " + title + " " + separator

	// viewport 内容（height 包含 header 行，viewport 实际高度需减 1）。
	// 外层 vpStyle 不设 Foreground —— 章节正文颜色由 renderChapterBlock 内部的
	// contentStyle 管（亮底深棕 / 暗底终端默认）。如果外层加 Foreground，亮底
	// 主题下 agent 调度块（✻ 金色 + 青色 label）会被深棕"压"成普通正文色。
	vpH := height - 1
	if vpH < 1 {
		vpH = 1
	}
	vpStyle := lipgloss.NewStyle().
		Width(width).
		Height(vpH).
		Padding(0, 1)

	return header + "\n" + vpStyle.Render(vp.View())
}

var streamCursorFrames = []string{"·", "✢", "✳", "✶", "✻", "✽"}

func renderStreamCursor(frame int) string {
	f := frame % len(streamCursorFrames)
	var dots [3]string
	for i := range 3 {
		dots[i] = streamCursorFrames[(f+i)%len(streamCursorFrames)]
	}
	trail := dots[0] + " " + dots[1] + " " + dots[2]
	return "\n" + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(trail)
}

var streamActivityFrames = [][2]string{
	{"✦", "✧"},
	{"✦", "✧"},
	{"✧", "✦"},
	{"✧", "✦"},
	{"✦", "✧"},
	{"✦", "✧"},
	{"✧", "✦"},
	{"✧", "✦"},
}

func renderStreamActivity(frame int) string {
	pair := streamActivityFrames[frame%len(streamActivityFrames)]
	major := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(pair[0])
	minor := lipgloss.NewStyle().Foreground(colorAccent2).Render(pair[1])
	return major + " " + minor
}

// renderStreamContent 将流式输出按轮次渲染为语义分块。
// Agent 调度块（以 ▸ 或 ✻ 开头）用 accent 标题 + dim 指令；正文块跟随终端默认色。
// cursor 非空时追加在末尾，表示 AI 正在输出。
func renderStreamContent(rounds []string, width int, cursor string) string {
	if width < 24 {
		width = 24
	}

	var blocks []string
	for _, round := range rounds {
		text := strings.TrimSpace(round)
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "▸") || strings.HasPrefix(text, "✻") {
			blocks = append(blocks, renderAgentBlock(text, width))
		} else {
			blocks = append(blocks, renderChapterBlock(text, width))
		}
	}
	result := strings.Join(blocks, "\n\n")
	if cursor != "" {
		result += cursor
	}
	return result
}

// renderAgentBlock 渲染 Agent 调度块：图标 + 标题 + 分隔线 + 任务指令。
//
// label 用 colorAccent2 青绿 + Bold + Underline 三重强调 —— 之前 colorAccent
// 金色 + Bold 在暗底跟 colorDim 灰的思考行视觉太接近，分不出主次。青绿是冷色，
// 跟思考行用的暖灰在色相上完全拉开；Underline 在所有终端都稳定生效，比 Bold
// 更可靠的视觉锚。图标 ✻ 反过来用金色作锚点，跟 label 形成双色对比。
func renderAgentBlock(text string, width int) string {
	headerLine, body, _ := strings.Cut(text, "\n")

	iconStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true).Underline(true)

	// 拆分前缀图标（✻ 或 ▸）和正文 label，分别染色；无图标的旧格式保持单色。
	var headerStyled string
	if first, rest, ok := strings.Cut(headerLine, " "); ok && (first == "✻" || first == "▸") {
		headerStyled = iconStyle.Render(first) + " " + labelStyle.Render(rest)
	} else {
		headerStyled = labelStyle.Render(headerLine)
	}

	// 标题行 + 分隔线（lineW 用 headerLine 的视觉宽度而非渲染后的字节宽度）
	titleW := lipgloss.Width(headerLine)
	lineW := max(0, width-titleW-1)
	header := headerStyled +
		" " + lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", lineW))

	var b strings.Builder
	b.WriteString(header)

	// 任务指令：dim 色，缩进 2 格；与 header 之间留一行空行，防止视觉贴一起。
	body = strings.TrimSpace(body)
	if body != "" {
		taskStyle := lipgloss.NewStyle().Foreground(colorMuted)
		lines := wrapStreamText(body, max(16, width-6))
		b.WriteString("\n\n")
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
// 思考内容（ThinkingSep 标记的段落）用 colorDim 斜体；章节正文走 bodyTextColor：
// 暗底继承终端默认前景，亮底用深棕保留暖调。
func renderChapterBlock(text string, width int) string {
	contentStyle := lipgloss.NewStyle().Foreground(bodyTextColor)
	thinkStyle := lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	wrapW := max(16, width-4)

	// 按 ThinkingSep 分割：奇数段是思考，偶数段是正文
	// 格式：[正文] \x02 [思考] [正文] \x02 [思考] ...
	parts := strings.Split(text, utils.ThinkingSep)

	var b strings.Builder
	for i, part := range parts {
		part = strings.TrimRight(part, " \n")
		if part == "" {
			continue
		}
		isThinking := i > 0 && i%2 != 0 // ThinkingSep 之后的奇数段是思考

		style := contentStyle
		if isThinking {
			style = thinkStyle
		}

		lines := wrapStreamText(part, wrapW)
		for j, line := range lines {
			if b.Len() > 0 && j == 0 {
				b.WriteString("\n\n") // 段间空行：思考与正文之间留出视觉间隔
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

// outlineGridThreshold 大纲切换多列的章节阈值。
// short tier 上限 25 章，20 以下单列一屏装得下、且能保留"进行中"徽标；
// 长篇 layered 模式滚动展开后 n 自然会突破 20，平滑切到多列。
const outlineGridThreshold = 20

// renderOutlineSection 按章节数选布局：少则单列（含"进行中"徽标），多则多列网格。
func renderOutlineSection(snap host.UISnapshot, contentW int) string {
	if len(snap.Outline) < outlineGridThreshold {
		return renderOutlineList(snap, contentW)
	}
	return renderOutlineGrid(snap, contentW)
}

// renderOutlineList 单列章节列表（短篇用）。每行尾部带"进行中"徽标，垂直阅读节奏更接近目录。
func renderOutlineList(snap host.UISnapshot, contentW int) string {
	var b strings.Builder
	for _, e := range snap.Outline {
		ch := fmt.Sprintf("%2d", e.Chapter)
		var marker, chStyle string
		titleStyle := cardContentStyle
		switch {
		case snap.CompletedCount >= e.Chapter:
			marker = lipgloss.NewStyle().Foreground(colorSuccess).Render("●")
			chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
		case snap.InProgressChapter == e.Chapter:
			marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸")
			chStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(ch)
			titleStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
		default:
			marker = lipgloss.NewStyle().Foreground(colorDim).Render("○")
			chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
			titleStyle = lipgloss.NewStyle().Foreground(colorMuted)
		}
		title := truncate(e.Title, contentW-6)
		line := marker + chStyle + " " + titleStyle.Render(title)
		if snap.InProgressChapter == e.Chapter {
			line += lipgloss.NewStyle().Foreground(colorAccent).Italic(true).Render(" 进行中")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// renderOutlineGrid 把大纲章节按"列优先"填充为多列网格，避免宽屏单列大量留白。
// 列数按 contentW 自适应（1-4），列内章节连续递增（"读完一列再读下一列"）。
// 与单列布局的取舍：放弃尾部" 进行中"徽标——多列下徽标会破坏列对齐，
// 且 ▸ 标记 + 金色 + 左侧概览栏的"写作中 第 N 章"已经把进行中信息说清楚。
func renderOutlineGrid(snap host.UISnapshot, contentW int) string {
	n := len(snap.Outline)
	if n == 0 {
		return ""
	}
	chNumW := 2
	titleW := 0
	for _, e := range snap.Outline {
		if w := len(strconv.Itoa(e.Chapter)); w > chNumW {
			chNumW = w
		}
		if w := lipgloss.Width(e.Title); w > titleW {
			titleW = w
		}
	}
	// 标题宽度上限 14（约 7 个汉字）；偶尔出现的长标题截断，避免一两个长标题撑大全体 cell
	if titleW > 14 {
		titleW = 14
	} else if titleW < 4 {
		titleW = 4
	}
	cellW := 3 + chNumW + titleW // marker(1) + 空格(1) + 章号 + 空格(1) + 标题
	gutter := 4
	cols := (contentW + gutter) / (cellW + gutter)
	if cols < 1 {
		cols = 1
	} else if cols > 4 {
		cols = 4
	}
	rows := (n + cols - 1) / cols

	var b strings.Builder
	cellStyle := lipgloss.NewStyle().Width(cellW)
	gutterStr := strings.Repeat(" ", gutter)
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			idx := c*rows + r
			if idx >= n {
				break
			}
			cell := renderOutlineCell(snap.Outline[idx], snap, chNumW, titleW)
			// 后续列还有 cell 时按 cellW 补齐 + gutter；否则当前 cell 是行尾不补
			if c < cols-1 && (c+1)*rows+r < n {
				b.WriteString(cellStyle.Render(cell))
				b.WriteString(gutterStr)
			} else {
				b.WriteString(cell)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderOutlineCell 渲染单个章节 cell：完成（绿●）/ 进行中（金▸）/ 未开始（暗○）。
func renderOutlineCell(e host.OutlineSnapshot, snap host.UISnapshot, chNumW, titleW int) string {
	chStr := fmt.Sprintf("%*d", chNumW, e.Chapter)
	title := truncateWidth(e.Title, titleW)
	var marker, chRendered, titleRendered string
	switch {
	case snap.CompletedCount >= e.Chapter:
		marker = lipgloss.NewStyle().Foreground(colorSuccess).Render("●")
		chRendered = lipgloss.NewStyle().Foreground(colorDim).Render(chStr)
		titleRendered = cardContentStyle.Render(title)
	case snap.InProgressChapter == e.Chapter:
		marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸")
		chRendered = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(chStr)
		titleRendered = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(title)
	default:
		marker = lipgloss.NewStyle().Foreground(colorDim).Render("○")
		chRendered = lipgloss.NewStyle().Foreground(colorDim).Render(chStr)
		titleRendered = lipgloss.NewStyle().Foreground(colorMuted).Render(title)
	}
	return marker + " " + chRendered + " " + titleRendered
}

// truncateWidth 按"视觉宽度"截断（中文字符算 2 列），与 lipgloss.Width 同源。
// 普通 truncate 按 rune 数算，对中文会截到双倍宽度，这里需要列对齐时不能用。
func truncateWidth(s string, maxW int) string {
	if lipgloss.Width(s) <= maxW {
		return s
	}
	var b strings.Builder
	cur := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if cur+rw > maxW {
			break
		}
		b.WriteRune(r)
		cur += rw
	}
	return b.String()
}

// renderDetailContent 构建右侧详情面板内容。
// 优先展示基础设定（大纲、角色），然后是运行时信息（提交、审阅等）。
func renderDetailContent(snap host.UISnapshot, contentW int) string {
	var b strings.Builder

	// 大纲
	if len(snap.Outline) > 0 {
		outlineHeader := ":: 大纲"
		if snap.Layered {
			outlineHeader = fmt.Sprintf(":: 大纲（%s · 动态规划大纲）", snap.CurrentVolumeArc)
		}
		b.WriteString(panelTitleStyle.Render(outlineHeader))
		b.WriteString("\n")
		b.WriteString(renderOutlineSection(snap, contentW))
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

	// 配角生态：累计已出场的次要角色总数 + 最近活跃前 5 名
	if snap.SupportingCount > 0 {
		b.WriteString(panelTitleStyle.Render(":: 配角生态"))
		b.WriteString("\n")
		b.WriteString(cardContentStyle.Render(truncate(fmt.Sprintf("已出场：%d 位", snap.SupportingCount), contentW)))
		b.WriteString("\n")
		for _, name := range snap.RecentSupporting {
			b.WriteString(cardContentStyle.Render("· " + truncate(name, contentW-2)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// 前提
	if snap.Premise != "" {
		b.WriteString(panelTitleStyle.Render(":: 前提"))
		b.WriteString("\n")
		for _, line := range wrapStreamText(snap.Premise, contentW) {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render(line))
			b.WriteString("\n")
		}
		b.WriteString("\n\n")
	}

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
		MaxHeight(height).
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
	featLabelStyle := lipgloss.NewStyle().Foreground(bodyTextColor)
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
	prompt := lipgloss.NewStyle().Foreground(bodyTextColor).Render("在下方输入你的小说需求开始创作")

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
