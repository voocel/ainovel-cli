package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// activityFeed 取当前这轮创作的活动快照，并入用户在这一轮的操作回响；上一轮的活动
// 不冒充当前进展（易失层）。
func (m model) activityFeed() (activity.Snapshot, bool) {
	b := &m.bench
	feed := b.activity
	if feed.Seq == 0 || feed.RunID == "" || b.hasRun() && feed.RunID != b.run().ID {
		feed = activity.Snapshot{}
	}
	if len(b.echoes) > 0 && b.hasRun() && b.echoRun == b.run().ID {
		feed.RunID = b.echoRun
		feed.Entries = append(append([]activity.Entry(nil), feed.Entries...), b.echoes...)
	}
	return feed, feed.RunID != ""
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

// outputKindLabel 输出块的栏目名：结构化产出用发布侧给的栏目（大纲、设定、审阅……）。
func outputKindLabel(block activity.OutputBlock) string {
	switch block.Kind {
	case activity.Thinking:
		return "思考"
	case activity.Prose:
		return "正文"
	case activity.Item:
		return block.Section
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

// stepLine 一步的一行：动作、作用对象（参数收齐后才知道）、进度或耗时。
func (m model) stepLine(entry activity.Entry, width int, clock bool) string {
	prefix := ""
	if clock && !entry.At.IsZero() {
		prefix = benchTheme.Muted.Render(entry.At.Local().Format("15:04:05")) + "  "
	}
	label := toolActivityLabel(entry.Tool)
	detail := ""
	if entry.Detail != "" {
		detail = " · " + entry.Detail
	}
	var body, elapsed string
	switch {
	case entry.Kind == activity.Notice:
		body = noticeLine(entry)
	case entry.Kind == activity.Retry:
		body = benchTheme.Warning.Render(fmt.Sprintf("↻ 连接不稳定，正在第 %d 次重试", entry.Attempt))
	case entry.Kind == activity.ProseStall:
		body = benchTheme.Warning.Render("⚠ 正文直播中断（仅预览受影响），最终以确认稿为准")
	case entry.Err != "":
		body = benchTheme.Warning.Render("! " + label + "遇到问题，模型正在自纠 · " + oneLine(entry.Err))
		elapsed = entryElapsed(entry)
	case entry.Done:
		body = styleNotice.Render("✓ ") + label + benchTheme.Muted.Render(detail)
		elapsed = entryElapsed(entry)
	case !m.bench.writing:
		// 创作已停但这次调用没等到收尾（中途取消）：不能再显示为进行中。
		body = benchTheme.Muted.Render("◦ " + label + detail + " · 未收尾")
	default:
		line := m.bench.pulse.spinner() + " " + label + detail
		if entry.Bytes > 0 {
			line += " · 已接收 " + formatDataSize(entry.Bytes)
		}
		body = styleFocus.Render(line)
		elapsed = entryElapsed(entry)
	}
	return alignRight(prefix+body, benchTheme.Muted.Render(elapsed), width)
}

// toneUser 标记用户自己的操作回响：只活在工作台的交互状态里，发布侧从不产生。
const toneUser activity.Tone = "user"

// noticeLine 一条旁白：符号表明性质，开工一步加粗成为时间线上的段落起点。
func noticeLine(entry activity.Entry) string {
	switch entry.Tone {
	case activity.ToneStep:
		return benchTheme.Accent.Render("→ ") + benchTheme.Title.Render(entry.Text)
	case activity.ToneDone:
		return styleNotice.Render("✓ ") + entry.Text
	case activity.ToneWait:
		return benchTheme.Warning.Render("◇ " + entry.Text)
	case activity.ToneFail:
		return benchTheme.Error.Render("! " + entry.Text)
	case activity.ToneRetry:
		return benchTheme.Warning.Render("↻ ") + entry.Text
	case toneUser:
		return benchTheme.Accent.Render("› " + entry.Text)
	default:
		return benchTheme.Muted.Render("· " + entry.Text)
	}
}

// waitingText 请求已发出、第一个字还没到时的等待计时。
func waitingText(feed activity.Snapshot) string {
	text := "等待模型回应"
	if !feed.WaitingSince.IsZero() {
		text += fmt.Sprintf(" · %d 秒", int(time.Since(feed.WaitingSince).Seconds()))
	}
	return text
}

func (m model) waitingLine(feed activity.Snapshot) string {
	return styleFocus.Render(m.bench.pulse.spinner() + " " + waitingText(feed))
}

// itemLines 结构化产出一条一行，折行时悬挂缩进两格，条与条一眼分得开。
func itemLines(text string, width int) []string {
	var lines []string
	for _, item := range strings.Split(text, "\n") {
		for i, line := range readingLines(item, max(1, width-2)) {
			if i > 0 {
				line = "  " + line
			}
			lines = append(lines, line)
		}
	}
	return lines
}

// blockStyle 输出块文字的样式：思考淡下去，产出用正文色。
func blockStyle(block activity.OutputBlock) lipgloss.Style {
	if block.Kind == activity.Thinking {
		return benchTheme.Thought
	}
	return benchTheme.Text
}

func blockText(block activity.OutputBlock, width int) []string {
	if block.Kind == activity.Item {
		return itemLines(string(block.Text), width)
	}
	return readingLines(string(block.Text), width)
}

// blockLines 一个输出块在活动视图里的排版：标题行（时钟 · 任务 · 栏目）+ 正文 + 空行。
func (m model) blockLines(block activity.OutputBlock, width int) []string {
	return m.bench.outputCache.lines(block, width, func() []string {
		kind := outputKindLabel(block)
		if block.Kind == activity.Prose {
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
		style := blockStyle(block)
		for _, line := range blockText(block, width) {
			lines = append(lines, style.Render(line))
		}
		return append(lines, "")
	})
}

// timelineItem 是时间线上的一项：一步，或一个输出块。
type timelineItem struct {
	at    time.Time
	seq   int
	entry *activity.Entry
	block *activity.OutputBlock
}

// timeline 本轮创作时间线：输出块按开始时刻、收尾的步骤按收尾时刻排（先有产出，后有
// "✓ 提交"）；进行中的步骤单列——它们是此刻正在发生的事，总在最后。活动视图与现场条
// 共用这一个顺序。
func timeline(feed activity.Snapshot) ([]timelineItem, []activity.Entry) {
	items := make([]timelineItem, 0, len(feed.Entries)+len(feed.Output))
	var open []activity.Entry
	for i := range feed.Entries {
		entry := &feed.Entries[i]
		if entry.Kind == activity.ToolStart && !entry.Done {
			open = append(open, *entry)
			continue
		}
		at := entry.DoneAt
		if at.IsZero() {
			at = entry.At
		}
		items = append(items, timelineItem{at: at, seq: i, entry: entry})
	}
	for i := range feed.Output {
		items = append(items, timelineItem{at: feed.Output[i].At, seq: len(feed.Entries) + i, block: &feed.Output[i]})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].at.Equal(items[j].at) {
			return items[i].seq < items[j].seq
		}
		return items[i].at.Before(items[j].at)
	})
	return items, open
}

// timelineLines 活动视图的整条时间线。
func (m model) timelineLines(feed activity.Snapshot, width int) []string {
	m.bench.outputCache.prune(feed.Output)
	items, open := timeline(feed)
	var lines []string
	if feed.OutputDropped > 0 {
		lines = append(lines, benchTheme.Warning.Render(fmt.Sprintf("↑ %d 个早期输出块已超出实时保留范围", feed.OutputDropped)), "")
	}
	for _, item := range items {
		if item.entry != nil {
			lines = append(lines, m.stepLine(*item.entry, width, true))
		} else {
			lines = append(lines, m.blockLines(*item.block, width)...)
		}
	}
	for _, entry := range open {
		lines = append(lines, m.stepLine(entry, width, true))
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

// activityHead 与正文视图同构的单行头：本轮创作 + 当前任务（或阶段、运行状态）。
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
	return []string{viewHead("本轮创作", title, "", width)}
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
		return "全书完成 · /continue 续写"
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

// sceneRow 是现场条正文的一行；step 为真时这一行是 entry 这一步（点击报错行下钻诊断）。
// 输出块的续行记着块的栏目头 head 与本行 body：块首被顶出现场时，栏目名粘到可见的第一行。
type sceneRow struct {
	line  string
	entry activity.Entry
	step  bool
	head  string
	body  string
}

// sceneRows 现场条正文：本轮时间线的最后 height 行，与活动视图同源同序，排得更紧——
// 输出块以栏目名起头、文字悬挂缩进，段间空行不占行；进行中的步骤与停下后的处境钉在
// 最后，不满时从底部长起。只排版看得见的最后几块；正文视图正在显示的那一章，正文不在这里重复。
func (m model) sceneRows(feed activity.Snapshot, width, height int) []sceneRow {
	m.bench.sceneCache.prune(feed.Output)
	items, open := timeline(feed)
	var tail []sceneRow
	for _, entry := range open {
		tail = append(tail, sceneRow{line: m.stepLine(entry, width, false), entry: entry, step: true})
	}
	if !m.bench.writing && m.bench.hasRun() {
		// 创作停下后最后一行是当下处境：这一行永远说的是"此刻"。
		tail = append(tail, sceneRow{line: benchTheme.Muted.Render(m.idleSceneText())})
	}
	shown := m.selectedChapterNumber()
	var rows []sceneRow
	for i := len(items) - 1; i >= 0 && len(rows)+len(tail) < height; i-- {
		var chunk []sceneRow
		if entry := items[i].entry; entry != nil {
			chunk = []sceneRow{{line: m.stepLine(*entry, width, false), entry: *entry, step: true}}
		} else if block := *items[i].block; !m.proseOf(block, shown) {
			label, labelStyle := outputKindLabel(block), benchTheme.Accent
			if block.Kind == activity.Thinking {
				labelStyle = benchTheme.Muted
			}
			head := labelStyle.Render(label) + "  "
			indent := strings.Repeat(" ", lipgloss.Width(label)+2)
			for j, body := range m.sceneBlock(block, width-len(indent)) {
				row := sceneRow{line: head + body, body: body}
				if j > 0 {
					row.line, row.head = indent+body, head
				}
				chunk = append(chunk, row)
			}
		}
		rows = append(chunk, rows...)
	}
	rows = append(rows, tail...)
	if len(rows) > height {
		rows = rows[len(rows)-height:]
		if first := &rows[0]; first.head != "" {
			first.line = first.head + first.body
		}
	}
	return append(make([]sceneRow, height-len(rows)), rows...)
}

// sceneBlock 输出块在现场条里栏目名之后的文字：段间空行不占行。整块从头排版（与活动视图
// 同一锚点）：换行点不随增量漂移，新字只在末行生长，满行后整体上推一行。
func (m model) sceneBlock(block activity.OutputBlock, width int) []string {
	return m.bench.sceneCache.lines(block, width, func() []string {
		style := blockStyle(block)
		var lines []string
		for _, row := range blockText(block, max(1, width)) {
			if strings.TrimSpace(row) != "" {
				lines = append(lines, style.Render(row))
			}
		}
		return lines
	})
}

// sceneTitleLine 现场的标题线说的是「此刻」：创作进行中时行首呼吸着星、光沿整行流过
// （pulse.go），停下即静止；等待模型回应的计时在行尾。都不另占一行。
func (m model) sceneTitleLine(feed activity.Snapshot, ok bool, width int) string {
	title, wait := m.sceneTitle(feed, ok), ""
	if feed.Waiting {
		wait = " " + benchTheme.Muted.Render(waitingText(feed))
		width -= lipgloss.Width(wait)
	}
	if !m.bench.writing {
		return sectionTitle(benchTheme.Muted.Render(title), width) + wait
	}
	return m.bench.pulse.star() + " " + m.bench.pulse.flowLine(title, width-2) + wait
}

// sceneLines 创作现场：空行隔开正文、标题线、时间线尾巴；没有实时活动时最后一行说明处境。
func (m model) sceneLines(width int) []string {
	feed, ok := m.activityFeed()
	lines := []string{"", m.sceneTitleLine(feed, ok, width)}
	if !ok || len(feed.Entries) == 0 && len(feed.Output) == 0 {
		for len(lines) < benchSceneRows-1 {
			lines = append(lines, "")
		}
		return append(lines, benchTheme.Muted.Render(m.idleSceneText()))
	}
	for _, row := range m.sceneRows(feed, width, sceneBodyRows) {
		lines = append(lines, row.line)
	}
	return lines
}

// failed 报告一步是否出了问题：工具报错或任务失败，点击可下钻诊断。
func failed(entry activity.Entry) bool {
	return entry.Err != "" || entry.Kind == activity.Notice && entry.Tone == activity.ToneFail
}

// sceneStepAt 现场条第 row 行（相对现场条顶部，含空行与标题线）上的步骤。
func (m model) sceneStepAt(row, width int) (activity.Entry, bool) {
	feed, ok := m.activityFeed()
	if index := row - 2; ok && index >= 0 && index < sceneBodyRows {
		if r := m.sceneRows(feed, width, sceneBodyRows)[index]; r.step {
			return r.entry, true
		}
	}
	return activity.Entry{}, false
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
