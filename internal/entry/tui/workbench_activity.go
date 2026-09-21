package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// activityFeed 取当前这轮创作的活动快照；上一轮的活动不冒充当前进展（易失层）。
func (m model) activityFeed() (activity.Snapshot, bool) {
	feed := m.bench.activity
	if feed.Seq == 0 || feed.RunID == "" {
		return activity.Snapshot{}, false
	}
	if m.bench.hasRun() && feed.RunID != m.bench.run().ID {
		return activity.Snapshot{}, false
	}
	return feed, true
}

// timelineFeed 活动视图的数据：上滚后持有当时的快照，新增量不改变读者所在处。
func (m model) timelineFeed() (activity.Snapshot, bool) {
	if m.bench.activityHeld != nil {
		return *m.bench.activityHeld, true
	}
	return m.activityFeed()
}

// toolActivityLabels 是工具名到创作语言的唯一翻译表。
var toolActivityLabels = map[string]string{
	"authority_read":          "查阅设定与前情",
	"workspace_list":          "清点工作区",
	"workspace_read":          "翻看工作稿",
	"workspace_put_chapter":   "落笔章节工作稿",
	"workspace_replace_block": "修改段落",
	"workspace_put_candidate": "整理结构候选",
	"workspace_put_review":    "记录审阅意见",
	"proposal_submit":         "提交候选稿",
	"verdict_submit":          "给出审阅结论",
	"semantic_compliance":     "核对语义合规",
}

func toolActivityLabel(tool string) string {
	if label, ok := toolActivityLabels[tool]; ok {
		return label
	}
	return "推进创作步骤"
}

func outputKindLabel(kind activity.Kind) string {
	switch kind {
	case activity.Thinking:
		return "思考"
	case activity.Prose:
		return "正文"
	default:
		return "说明"
	}
}

func entryElapsed(entry activity.Entry) string {
	switch {
	case entry.At.IsZero():
		return ""
	case entry.Done && entry.DoneAt.IsZero():
		return ""
	case entry.Done:
		return formatDuration(entry.DoneAt.Sub(entry.At))
	default:
		return formatDuration(time.Since(entry.At))
	}
}

// stepLine 一条生命周期条目：[时钟] 图标 标签 [· 已接收 N]，右端耗时。
func (m model) stepLine(entry activity.Entry, width int, clock bool) string {
	prefix := ""
	if clock && !entry.At.IsZero() {
		prefix = benchTheme.Muted.Render(entry.At.Local().Format("15:04:05")) + "  "
	}
	label := toolActivityLabel(entry.Tool)
	var body, elapsed string
	switch {
	case entry.Kind == activity.Retry:
		body = benchTheme.Warning.Render(fmt.Sprintf("↻ 连接不稳定，正在第 %d 次重试", entry.Attempt))
	case entry.Kind == activity.ProseStall:
		body = benchTheme.Warning.Render("⚠ 正文直播中断（仅预览受影响），最终以确认稿为准")
	case entry.Err != "":
		body = benchTheme.Warning.Render("! " + label + "遇到问题，模型正在自纠 · " + oneLine(entry.Err))
		elapsed = entryElapsed(entry)
	case entry.Done:
		body = styleNotice.Render("✓ ") + label
		elapsed = entryElapsed(entry)
	case !m.bench.writing:
		// 创作已停但这次调用没等到收尾（中途取消）：不能再显示为进行中。
		body = benchTheme.Muted.Render("◦ " + label + " · 未收尾")
	default:
		line := spinnerFrames[m.bench.spin%len(spinnerFrames)] + " " + label
		if entry.Bytes > 0 {
			line += " · 已接收 " + formatDataSize(entry.Bytes)
		}
		body = styleFocus.Render(line)
		elapsed = entryElapsed(entry)
	}
	return alignRight(prefix+body, benchTheme.Muted.Render(elapsed), width)
}

func (m model) waitingLine(feed activity.Snapshot) string {
	line := spinnerFrames[m.bench.spin%len(spinnerFrames)] + " 等待模型回应"
	if !feed.WaitingSince.IsZero() {
		line += fmt.Sprintf(" · %d 秒", int(time.Since(feed.WaitingSince).Seconds()))
	}
	return styleFocus.Render(line)
}

// blockLines 一个输出块的排版：标题行（时钟 · 任务 · 类型）+ 正文 + 空行；按 ID/版本/宽度缓存。
func (m model) blockLines(block activity.OutputBlock, width int) []string {
	cache := m.bench.outputCache
	if cache != nil {
		if entry, ok := cache.blocks[block.ID]; ok && entry.version == block.Version && entry.width == width {
			return entry.lines
		}
	}
	style := benchTheme.Text
	kind := outputKindLabel(block.Kind)
	switch block.Kind {
	case activity.Thinking:
		style = benchTheme.Muted
	case activity.Prose:
		kind = "正文预览 · 未入稿"
	}
	task := block.TaskLabel
	if task == "" {
		task = "创作任务"
		if block.Scope.ChapterNumber > 0 {
			task = fmt.Sprintf("第 %d 章", block.Scope.ChapterNumber)
		}
	}
	if len(block.Scope.ChapterIDs) > 1 {
		task += fmt.Sprintf(" · 跨章任务（%d 章）", len(block.Scope.ChapterIDs))
	}
	header := task + " · " + kind
	if !block.At.IsZero() {
		header = block.At.Local().Format("15:04:05") + "  " + header
	}
	lines := []string{benchTheme.Accent.Render(fitLine(header, width))}
	if block.Truncated {
		lines = append(lines, benchTheme.Warning.Render("… 本段前文已截断，仅保留最近输出"))
	}
	for _, line := range readingLines(string(block.Text), width) {
		lines = append(lines, style.Render(line))
	}
	lines = append(lines, "")
	if cache != nil {
		if cache.blocks == nil {
			cache.blocks = make(map[uint64]outputWrappedBlock)
		}
		cache.blocks[block.ID] = outputWrappedBlock{version: block.Version, width: width, lines: lines}
	}
	return lines
}

// timelineLines 本轮创作时间线：生命周期条目与输出块按时间合并。
func (m model) timelineLines(feed activity.Snapshot, width int) []string {
	type item struct {
		at    time.Time
		seq   int
		lines []string
	}
	items := make([]item, 0, len(feed.Entries)+len(feed.Output))
	for i, entry := range feed.Entries {
		items = append(items, item{entry.At, i, []string{m.stepLine(entry, width, true)}})
	}
	for i, block := range feed.Output {
		items = append(items, item{block.At, len(feed.Entries) + i, m.blockLines(block, width)})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].at.Equal(items[j].at) {
			return items[i].seq < items[j].seq
		}
		return items[i].at.Before(items[j].at)
	})
	var lines []string
	if feed.OutputDropped > 0 {
		lines = append(lines, benchTheme.Warning.Render(fmt.Sprintf("↑ %d 个早期输出块已超出实时保留范围", feed.OutputDropped)), "")
	}
	for _, entry := range items {
		lines = append(lines, entry.lines...)
	}
	if feed.Waiting {
		lines = append(lines, m.waitingLine(feed))
	}
	return lines
}

// currentTaskLabel 正在执行的任务名；创作停下后是运行状态。
func (m model) currentTaskLabel(feed activity.Snapshot, ok bool) string {
	if ok {
		for i := len(feed.Tasks) - 1; i >= 0; i-- {
			if !feed.Tasks[i].Done {
				return feed.Tasks[i].Label
			}
		}
	}
	if m.bench.hasRun() && !m.bench.writing {
		return runStateLabel(m.bench.run().State)
	}
	return ""
}

// activityHead 与正文视图同构的三行头：本轮创作 / 当前任务（或阶段、运行状态）。
func (m model) activityHead(feed activity.Snapshot, ok bool, width int) []string {
	title := m.currentTaskLabel(feed, ok)
	switch {
	case title != "":
	case m.bench.snap.CurrentPhase != "":
		title = m.bench.snap.CurrentPhase
	case m.bench.hasRun():
		title = runStateLabel(m.bench.run().State)
	default:
		title = "还没有开始创作"
	}
	return []string{benchTheme.Muted.Render("本轮创作"), benchTheme.Title.Render(fitLine(title, width)), ""}
}

// activityView 活动视图：跟随最新；上滚后持有快照，↓ 到底或 /follow 恢复。
func (m model) activityView(width, height int) []string {
	feed, ok := m.timelineFeed()
	lines := m.activityHead(feed, ok, width)
	if !ok || (len(feed.Entries) == 0 && len(feed.Output) == 0 && !feed.Waiting) {
		return append(lines,
			benchTheme.Muted.Render("模型的每一步、思考、说明与正文预览会按时间显示在这里。"),
			benchTheme.Muted.Render("离开后重新打开不会回放已丢失的直播内容；完整正文与待确认稿以正文视图为准。"),
		)
	}
	body := m.timelineLines(feed, width)
	capacity := max(1, height-viewHeadRows)
	last := max(0, len(body)-capacity)
	offset := last
	if m.bench.activityHeld != nil {
		offset = min(m.bench.activityOffset, last)
	}
	return append(lines, body[offset:min(len(body), offset+capacity)]...)
}

func (m model) scrollActivity(delta int) model {
	l := m.benchLayout()
	feed, ok := m.timelineFeed()
	if !ok {
		return m
	}
	last := max(0, len(m.timelineLines(feed, l.inner))-max(1, l.contentRows-viewHeadRows))
	if m.bench.activityHeld == nil {
		if delta >= 0 || last == 0 {
			return m
		}
		held := feed
		m.bench.activityHeld, m.bench.activityOffset = &held, last
	}
	m.bench.activityOffset = min(last, max(0, m.bench.activityOffset+delta))
	if delta > 0 && m.bench.activityOffset == last {
		m.bench.activityHeld = nil
	}
	return m
}

// sceneSteps 创作现场末几行的步骤：等待模型时末行是等待计时，步骤少留一条。
func sceneSteps(feed activity.Snapshot) []activity.Entry {
	count := sceneStepRows
	if feed.Waiting {
		count--
	}
	steps := make([]activity.Entry, 0, count)
	for i := max(0, len(feed.Entries)-count); i < len(feed.Entries); i++ {
		steps = append(steps, feed.Entries[i])
	}
	for len(steps) < count {
		steps = append([]activity.Entry{{}}, steps...)
	}
	return steps
}

// sceneStepAt 现场条第 row 行（相对现场条顶部）上的步骤，供点击报错行下钻诊断。
// 步骤行紧跟在空行、标题线与输出尾部之后。
func sceneStepAt(feed activity.Snapshot, row int) (activity.Entry, bool) {
	steps := sceneSteps(feed)
	if index := row - 2 - sceneTailRows; index >= 0 && index < len(steps) {
		return steps[index], true
	}
	return activity.Entry{}, false
}

func (m model) sceneTitle(feed activity.Snapshot, ok bool) string {
	if label := m.currentTaskLabel(feed, ok); label != "" {
		return "AI 创作现场 · " + label
	}
	return "AI 创作现场"
}

// idleSceneText 没有实时活动时，现场条说明当下处境与下一步。
func (m model) idleSceneText() string {
	b := m.bench
	switch b.situation() {
	case situationWriting, situationPausing, situationCancelling:
		if b.snap.CurrentPhase != "" {
			return b.snap.CurrentPhase + " · 准备中"
		}
		return "准备中"
	case situationDecidingProposal, situationDeciding:
		return "等你决定 · 见下方决定卡"
	case situationNoRun:
		return "还没有开始创作 · /continue 开始"
	case situationCompleted:
		return "全书完成 · /goal 提高目标续写"
	case situationPaused:
		return "已暂停 · /continue 继续"
	case situationFailed:
		return "需要处理 · /continue 重试，/diag 查看诊断"
	case situationCancelled:
		return "本轮已取消 · /continue 开启新一轮"
	default:
		return runStateLabel(b.run().State)
	}
}

// sceneLines 创作现场：空行隔开正文、标题线、最新输出块尾部、前几步、当前步（或等待计时）。
// 尾部随每次增量刷新，让人始终看得见模型在输出；当前步固定在最后一行。
func (m model) sceneLines(width int) []string {
	feed, ok := m.activityFeed()
	lines := []string{"", sectionTitle(benchTheme.Muted.Render(m.sceneTitle(feed, ok)), width)}
	if ok {
		lines = append(lines, m.outputTail(feed, width)...)
	}
	if !ok || len(feed.Entries) == 0 && !feed.Waiting {
		for len(lines) < benchSceneRows-1 {
			lines = append(lines, "")
		}
		return append(lines, benchTheme.Muted.Render(m.idleSceneText()))
	}
	for _, step := range sceneSteps(feed) {
		if step.At.IsZero() {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, m.stepLine(step, width, false))
	}
	if feed.Waiting {
		lines = append(lines, m.waitingLine(feed))
	}
	return lines
}

// outputTail 最新输出块的最后几行，带类型标签与竖线。
// 正文视图正在直播的那块不重复，改看它之前的最新输出（通常是思考）。
// 整块从头排版再取尾部（与活动视图同一锚点）：换行点不随增量漂移，
// 新字只在末行生长，满行后整体上推一行；段落间的空行不占尾部。
func (m model) outputTail(feed activity.Snapshot, width int) []string {
	var shown uint64
	if m.bench.view == viewProse {
		shown = m.proseSource().blockID
	}
	index := len(feed.Output) - 1
	for index >= 0 && feed.Output[index].ID == shown {
		index--
	}
	if index < 0 {
		return make([]string, sceneTailRows)
	}
	block := feed.Output[index]
	label := outputKindLabel(block.Kind)
	indent := lipgloss.Width(label) + 1
	all := m.bench.sceneCache.wrap(string(block.Text), max(1, width-indent-2))
	wrapped := make([]string, 0, sceneTailRows)
	for i := len(all) - 1; i >= 0 && len(wrapped) < sceneTailRows; i-- {
		if strings.TrimSpace(all[i]) != "" {
			wrapped = append([]string{all[i]}, wrapped...)
		}
	}
	for len(wrapped) < sceneTailRows {
		wrapped = append(wrapped, "")
	}
	bar := benchTheme.Border.Render("▏")
	style := benchTheme.Text
	if block.Kind == activity.Thinking {
		style = benchTheme.Muted
	}
	lines := make([]string, sceneTailRows)
	for i, row := range wrapped {
		head := strings.Repeat(" ", indent)
		if i == 0 {
			head = benchTheme.Accent.Render(label) + " "
		}
		lines[i] = head + bar + " " + style.Render(row)
	}
	return lines
}

// thinkingContent 本轮全部思考原文（/think 全屏）。
func (m model) thinkingContent() string {
	feed, ok := m.activityFeed()
	if !ok {
		return ""
	}
	var parts []string
	for _, block := range feed.Output {
		if block.Kind != activity.Thinking {
			continue
		}
		label := block.TaskLabel
		if label == "" {
			label = "创作任务"
		}
		if block.Truncated {
			label += " · 前文已截断"
		}
		parts = append(parts, label+"\n"+string(block.Text))
	}
	if len(parts) == 0 {
		return ""
	}
	return "思考原文 · 模型提供的实时内容\n\n" + strings.Join(parts, "\n\n")
}
