package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/utils"
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
		infoParts = append(infoParts, snap.ModelName)
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
	capsule := statusCapsule.Foreground(lipgloss.Color("#1c1a14")).Background(color).Render(label)

	if snap.IsRunning && spinnerFrame != "" {
		capsule = lipgloss.NewStyle().Foreground(colorAccent).Render(spinnerFrame) + " " + capsule
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
		Foreground(colorText).
		Bold(true).
		Render(titleText)
	rightCell := lipgloss.NewStyle().
		Width(rightW).
		AlignHorizontal(lipgloss.Right).
		Render(capsule)

	content := leftCell + centerCell + rightCell
	return topBarStyle.Width(width).
		Border(baseBorder, false, false, true, false).
		BorderForeground(colorDim).
		Render(content)
}

// renderStatePanel 渲染左侧状态面板。
func renderStatePanel(snap host.UISnapshot, width, height int) string {
	contentW := max(12, width-4)
	agents := sidebarAgents(snap.Agents)
	tasks := sidebarTasks(snap.Tasks)
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
		if snap.TotalChapters > 0 {
			overview.WriteString(renderField("已规划", fmt.Sprintf("%d 章", snap.TotalChapters)))
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
	if snap.InProgressChapter > 0 {
		overview.WriteString(renderField("写作中", fmt.Sprintf("第 %d 章", snap.InProgressChapter)))
	}
	if headline := snapshotHeadline(tasks, snap); headline != "" {
		label := "当前"
		if !snap.IsRunning {
			label = "待恢复"
		}
		overview.WriteString(renderHighlightField(label, truncate(headline, contentW-10)))
	}
	sections = append(sections, renderSidebarSection("概览", overview.String(), contentW))

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

	if body := renderContextSidebar(snap, contentW); body != "" {
		sections = append(sections, renderSidebarSection("上下文", body, contentW))
	}

	if len(tasks) > 0 {
		var taskBody strings.Builder
		limit := 4
		if len(tasks) < limit {
			limit = len(tasks)
		}
		for i := 0; i < limit; i++ {
			taskBody.WriteString(renderTaskLine(tasks[i], contentW))
			taskBody.WriteString("\n")
		}
		sections = append(sections, renderSidebarSection("任务队列", taskBody.String(), contentW))
	}

	style := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(baseBorder, false, true, false, false).
		BorderForeground(colorDim).
		Padding(1, 1)

	return style.Render(strings.Join(sections, "\n\n"))
}

func renderAgentLine(agent host.AgentSnapshot, width int) string {
	stateColor := taskStatusColor(agent.State)
	icon := lipgloss.NewStyle().Foreground(stateColor).Render(agentStateIcon(agent.State))
	badge := lipgloss.NewStyle().Foreground(stateColor).Render(agentStateLabel(agent.State))
	name := lipgloss.NewStyle().Foreground(colorText).Bold(true).Render(agentDisplayName(agent.Name))
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

func renderTaskLine(task host.TaskSnapshot, width int) string {
	color := taskStatusColor(task.Status)
	icon := lipgloss.NewStyle().Foreground(color).Render(taskStateIcon(task.Status))
	head := lipgloss.NewStyle().Foreground(color).Render(taskStatusLabel(task.Status))
	title := truncate(taskListTitle(task), max(8, width-lipgloss.Width(head)-3))
	line := icon + " " + head + " " + title
	var meta []string
	if task.Chapter > 0 {
		meta = append(meta, fmt.Sprintf("第%d章", task.Chapter))
	}
	if task.Volume > 0 && task.Arc > 0 {
		meta = append(meta, fmt.Sprintf("第%d卷·第%d弧", task.Volume, task.Arc))
	}
	if owner := taskOwnerLabel(task.Owner); owner != "" {
		meta = append(meta, owner)
	}
	if len(meta) > 0 {
		line += "\n" + lipgloss.NewStyle().Foreground(colorDim).Render("  "+strings.Join(meta, " · "))
	}
	detail := task.Summary
	if detail == "" {
		detail = task.Tool
	}
	if detail != "" {
		line += "\n" + lipgloss.NewStyle().Foreground(colorMuted).Render("  "+truncate(detail, max(8, width-2)))
	}
	if task.OutputRef != "" {
		line += "\n" + lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render("  out: "+truncate(task.OutputRef, max(8, width-7)))
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

func sidebarTasks(tasks []host.TaskSnapshot) []host.TaskSnapshot {
	var active []host.TaskSnapshot
	for _, task := range tasks {
		if task.Status != "running" && task.Status != "queued" {
			continue
		}
		active = append(active, task)
	}
	if len(active) <= 1 {
		return active
	}

	var concrete []host.TaskSnapshot
	for _, task := range active {
		if task.Kind == "coordinator_decision" {
			continue
		}
		concrete = append(concrete, task)
	}
	if len(concrete) > 0 {
		return concrete
	}
	return active
}

func snapshotHeadline(tasks []host.TaskSnapshot, snap host.UISnapshot) string {
	if len(tasks) > 0 {
		title := taskListTitle(tasks[0])
		if !snap.IsRunning && tasks[0].Status == "queued" {
			return "待恢复：" + title
		}
		return title
	}
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

func renderContextSidebar(snap host.UISnapshot, width int) string {
	if snap.ContextWindow <= 0 && snap.ProjectionWindow <= 0 && snap.ContextStrategy == "" && snap.ContextScope == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(renderContextUsageField("主上下文", snap.ContextPercent, snap.ContextTokens, snap.ContextWindow))
	showProjection := snap.ProjectionTokens > 0 &&
		snap.ProjectionWindow > 0 &&
		(snap.ProjectionTokens != snap.ContextTokens || snap.ProjectionWindow != snap.ContextWindow)
	if showProjection {
		b.WriteString(renderContextUsageField("最近投影", snap.ProjectionPercent, snap.ProjectionTokens, snap.ProjectionWindow))
	}
	if showProjection {
		if strategy := contextStrategyLabel(snap.ProjectionStrategy); strategy != "" {
			b.WriteString(renderField("投影策略", truncate(strategy, max(8, width-12))))
		}
	} else if strategy := contextStrategyLabel(snap.ContextStrategy); strategy != "" {
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
	compactedCount := snap.ContextCompactedCount
	keptCount := snap.ContextKeptCount
	if showProjection && (snap.ProjectionCompacted > 0 || snap.ProjectionKept > 0) {
		compactedCount = snap.ProjectionCompacted
		keptCount = snap.ProjectionKept
	}
	if compactedCount > 0 || keptCount > 0 {
		b.WriteString(renderField("最近重写", fmt.Sprintf("%d → %d", compactedCount, keptCount)))
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

func taskStatusLabel(status string) string {
	switch status {
	case "running":
		return "运行中"
	case "queued":
		return "排队中"
	case "failed":
		return "失败"
	case "canceled":
		return "取消"
	case "succeeded":
		return "完成"
	default:
		return status
	}
}

func taskStateIcon(status string) string {
	switch status {
	case "running":
		return "●"
	case "queued":
		return "○"
	case "failed":
		return "×"
	case "canceled":
		return "·"
	case "succeeded":
		return "✓"
	default:
		return "·"
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

func taskOwnerLabel(owner string) string {
	switch owner {
	case "architect":
		return "规划师"
	case "editor":
		return "评审者"
	case "writer":
		return "写作者"
	case "coordinator":
		return "协调器"
	case "runtime":
		return "系统"
	default:
		return owner
	}
}

func taskListTitle(task host.TaskSnapshot) string {
	switch task.Kind {
	case "foundation_plan", "coordinator_decision", "steer_apply", "volume_append", "arc_expand":
		return taskKindLabel(task.Kind)
	case "chapter_write", "chapter_review", "chapter_rewrite", "chapter_polish":
		if task.Chapter > 0 {
			return fmt.Sprintf("%s · 第 %d 章", taskKindLabel(task.Kind), task.Chapter)
		}
	}
	if task.Title != "" {
		return task.Title
	}
	return taskKindLabel(task.Kind)
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

	case ev.Category == "CONTEXT" || ev.Category == "COMPACT":
		icon := lipgloss.NewStyle().Foreground(colorContext).Render("⚙")
		sumColor := colorContext
		if ev.Level == "debug" {
			sumColor = colorMuted
		}
		sum := lipgloss.NewStyle().Foreground(sumColor).Render(truncate(ev.Summary, maxSumW))
		return tsStr + " " + indent + icon + " " + sum

	default:
		color, ok := categoryColors[ev.Category]
		if !ok {
			color = colorText
		}
		icon := lipgloss.NewStyle().Foreground(color).Render("·")
		sum := lipgloss.NewStyle().Foreground(color).Render(truncate(ev.Summary, maxSumW))
		return tsStr + " " + indent + icon + " " + sum
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
	pattern := []rune(sparkleFrames[frame%len(sparkleFrames)])

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
	// 分隔标题栏（始终醒目）
	title := lipgloss.NewStyle().Foreground(colorAccent).Bold(focused).Render(":: 实时输出")
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
	major := lipgloss.NewStyle().Foreground(lipgloss.Color("#d4a21a")).Bold(true).Render(pair[0])
	minor := lipgloss.NewStyle().Foreground(colorAccent2).Render(pair[1])
	return major + " " + minor
}

// renderStreamContent 将流式输出按轮次渲染为语义分块。
// Agent 调度块（以 ▸ 开头）用 accent 标题 + dim 指令；正文块用标准文本色。
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
		if strings.HasPrefix(text, "▸") {
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
	parts := strings.Split(text, utils.ThinkingSep)

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
		for _, e := range snap.Outline {
			ch := fmt.Sprintf("%2d", e.Chapter)
			var marker, chStyle string
			titleStyle := cardContentStyle
			if snap.CompletedCount >= e.Chapter {
				// 已完成：绿点 + 柔色章节号
				marker = lipgloss.NewStyle().Foreground(colorSuccess).Render("●")
				chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
			} else if snap.InProgressChapter == e.Chapter {
				// 进行中：金色箭头 + 高亮章节号
				marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸")
				chStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(ch)
				titleStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
			} else {
				// 未开始：暗圆 + 暗章节号
				marker = lipgloss.NewStyle().Foreground(colorDim).Render("○")
				chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
				titleStyle = lipgloss.NewStyle().Foreground(colorMuted)
			}
			title := truncate(e.Title, contentW-6)
			line := marker + chStyle + " " + titleStyle.Render(title)
			if snap.InProgressChapter == e.Chapter {
				badge := lipgloss.NewStyle().Foreground(colorAccent).Italic(true).Render(" 进行中")
				line += badge
			}
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
