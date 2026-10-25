package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

// 右栏（页面设计 §3）上下两块：上块「写作依据」跟着目录光标走——正在看的那一章（或卷、
// 弧、全书）依据什么写：规划、覆盖它的要求与审阅结论、审阅意见、这一章记下的设定；下块
// 「创作团队」钉在底部——谁在干活、用哪个模型、这本书上各自用了多少与多久，最后是合计。
// 两块都来自落盘数据，重启、空闲时照样完整。运行明细在 /diag。

// railLimits 是一段文字最多占几行、人物最多列几位：先按宽松的排，放不下再收紧，仍放不下
// 才指向 /view。
type railLimits struct{ summary, item, cast int }

var (
	railRoomy   = railLimits{summary: 12, item: 6, cast: 10}
	railCompact = railLimits{summary: 4, item: 2, cast: 5}
)

// railColumn 右栏：上块依据从顶部排，下块创作团队钉在底部，中间留白。
func (m model) railColumn(l benchLayout) []string {
	width := l.railWidth - 2
	crew := m.crewCard(width)
	room := l.bodyHeight - len(crew)
	lines := m.briefCard(width, railRoomy)
	if len(lines) > room {
		lines = m.briefCard(width, railCompact)
	}
	if len(lines) > room {
		lines = append(lines[:room-1], benchTheme.Muted.Render("… /view 查看全部"))
	}
	for len(lines) < room {
		lines = append(lines, "")
	}
	lines = append(lines, crew...)
	for i := range lines {
		lines[i] = " " + fitLine(lines[i], width)
	}
	return fitBlock(strings.Join(lines, "\n"), l.railWidth, l.bodyHeight)
}

// briefCard 写作依据：标题线下是光标所在处的依据——章行是这一章的，卷/弧头行是这一部分的，
// 全书行与还没有大纲时是全书的。
func (m model) briefCard(width int, limits railLimits) []string {
	r := railText{width: width, limits: limits, lines: []string{railRule(benchTheme.Muted.Render("写作依据"), "", width)}}
	rows := m.outlineRows()
	if cursor := m.bench.cursor; cursor >= 0 && cursor < len(rows) {
		switch row := rows[cursor]; {
		case row.isChapter():
			m.chapterBrief(&r, row.chapter)
			return r.lines
		case row.header():
			m.sectionBrief(&r, row)
			return r.lines
		}
	}
	m.bookBrief(&r)
	return r.lines
}

func (m model) chapterBrief(r *railText, number int) {
	snap := m.bench.snap
	node, _ := m.outlineChapter(number)
	r.head(fmt.Sprintf("第 %d 章 · %s", number, node.Node.Title), m.chapterState(node))
	if node.Node.Summary != "" {
		r.section("规划", "")
		r.paragraph(node.Node.Summary)
	}
	if node.Node.Turn != "" {
		r.section("转折", "")
		r.paragraph(node.Node.Turn)
	}
	brief := snap.Briefs[number]
	if len(brief.Threads) > 0 {
		r.section("长线", "")
		for _, thread := range brief.Threads {
			r.item(benchTheme.Muted.Render("·"), briefThreadText(thread), benchTheme.Text)
		}
	}
	if len(brief.Requirements) > 0 {
		violated := 0
		for _, requirement := range brief.Requirements {
			if requirement.Status == domainmodel.CheckViolated {
				violated++
			}
		}
		r.section("要求", railCount(len(brief.Requirements), violated, benchTheme.Error, "违反"))
		for _, requirement := range brief.Requirements {
			if requirement.Rework {
				r.item(benchTheme.Warning.Render("↻"), requirementText(requirement)+" · 待按它重写", benchTheme.Text)
				continue
			}
			r.item(checkMark(requirement.Status), requirementText(requirement), benchTheme.Text)
		}
	}
	if chapter, ok := chapterByNumber(snap.Manuscript, number); ok {
		var findings []workbench.WorkbenchFinding
		blocking := 0
		for _, finding := range snap.Findings {
			if finding.ChapterID == chapter.ID {
				findings = append(findings, finding)
				if finding.Severity == domainmodel.FindingBlocking {
					blocking++
				}
			}
		}
		if len(findings) > 0 {
			r.section("审阅意见", railCount(len(findings), blocking, benchTheme.Warning, "未解决"))
			for _, finding := range findings {
				if finding.Severity == domainmodel.FindingBlocking {
					r.item(benchTheme.Warning.Render("!"), finding.Note, benchTheme.Text)
				} else {
					r.item(benchTheme.Muted.Render("·"), finding.Note, benchTheme.Muted)
				}
			}
		}
	}
	if len(brief.Facts) > 0 {
		pending := 0
		for _, fact := range brief.Facts {
			if fact.Pending {
				pending++
			}
		}
		r.section("本章设定", railCount(len(brief.Facts), pending, benchTheme.Warning, "待核验"))
		for _, fact := range brief.Facts {
			r.fact(fact)
		}
	}
}

// railCount 分区右侧的计数；有要紧的（违反、阻塞、待核验）时先用醒目色标出。
func railCount(total, urgent int, style lipgloss.Style, word string) string {
	note := benchTheme.Muted.Render(fmt.Sprintf("%d 条", total))
	if urgent > 0 {
		note = style.Render(fmt.Sprintf("%s %d", word, urgent)) + benchTheme.Muted.Render(" · ") + note
	}
	return note
}

// chapterState 与目录同一套符号：已入稿附字数，带未解决意见的附条数（D94）。
func (m model) chapterState(node workbench.OutlineNode) string {
	unresolved := len(m.unresolvedFindings(node.Number))
	switch {
	case node.State == workbench.ChapterConfirmed && unresolved > 0:
		return benchTheme.Warning.Render(fmt.Sprintf("! 未解决 %d 条", unresolved))
	case node.State == workbench.ChapterConfirmed:
		chapter, _ := chapterByNumber(m.bench.snap.Manuscript, node.Number)
		return benchTheme.Success.Render(fmt.Sprintf("✓ %s 字", groupDigits(chapter.Runes())))
	case node.State == workbench.ChapterPending || node.Proposed:
		return benchTheme.Warning.Render("◇ 待确认")
	case node.State == workbench.ChapterInProgress && m.bench.writing:
		return benchTheme.Accent.Render("◉ 写作中")
	case node.State == workbench.ChapterInProgress:
		return benchTheme.Muted.Render("◌ 未写完")
	default:
		return benchTheme.Muted.Render("○ 已规划")
	}
}

// checkMark 审阅对一项要求的结论：兑现、违反、待定（作用域还没写完），没审过是一个点。
func checkMark(status string) string {
	switch status {
	case domainmodel.CheckSatisfied:
		return benchTheme.Success.Render("✓")
	case domainmodel.CheckViolated:
		return benchTheme.Error.Render("!")
	case domainmodel.CheckPending:
		return benchTheme.Muted.Render("◌")
	default:
		return benchTheme.Muted.Render("·")
	}
}

func checkWord(status string) string {
	switch status {
	case domainmodel.CheckSatisfied:
		return "已兑现"
	case domainmodel.CheckViolated:
		return "被违反"
	case domainmodel.CheckPending:
		return "待定，作用域还没写完"
	default:
		return "还没审"
	}
}

func requirementText(requirement workbench.BriefRequirement) string {
	text := requirement.Text
	if requirement.Forbidden {
		text = "禁止 " + text
	}
	if requirement.Scope != "" {
		text += " · " + requirement.Scope
	}
	return text
}

// factKinds 是设定种类的读者用语：两字等宽，作行首标签时天然对齐。
var factKinds = map[domainmodel.CanonFactKind]string{
	domainmodel.CanonEvent: "事件", domainmodel.CanonState: "状态", domainmodel.CanonRelationship: "关系",
	domainmodel.CanonWorldRule: "规则", domainmodel.CanonForeshadow: "伏笔",
}

// factLine 一条设定的整行写法（/view、/review）：种类标签、主体、内容。
func factLine(fact workbench.Fact) string {
	return benchTheme.Muted.Render(factKinds[fact.Kind]+" ") + fact.Subject + "：" + fact.Text
}

// sectionBrief 卷/弧：进度、概要，以及挂在这一部分上的要求（输入框此时的作用域就是它）。
func (m model) sectionBrief(r *railText, row outlineRow) {
	state := benchTheme.Muted.Render(fmt.Sprintf("%d/%d 章", row.confirmed, row.chapters))
	if row.node.Proposed {
		state = benchTheme.Warning.Render("◇ 待确认")
	}
	r.head(row.node.Node.Title, state)
	if row.node.Node.Summary != "" {
		r.section("概要", "")
		r.paragraph(row.node.Node.Summary)
	}
	scope := domainmodel.DirectiveScopePlanNode(row.node.Node.ID)
	var texts []string
	for _, directive := range m.bench.snap.Directives {
		if directive.Scope == scope {
			texts = append(texts, directive.Text)
		}
	}
	if len(texts) > 0 {
		r.section("要求", railCount(len(texts), 0, benchTheme.Muted, ""))
		for _, text := range texts {
			r.item(benchTheme.Muted.Render("·"), text, benchTheme.Text)
		}
	}
}

// bookBrief 全书：篇幅、终局、长线的进度、出过场的人物（涉及章数多的在前），创作意图与
// 全部要求。
func (m model) bookBrief(r *railText) {
	snap := m.bench.snap
	r.head("全书", benchTheme.Muted.Render(lengthStatus(snap.Length)))
	ending := snap.Intent.EndingDirection
	if ending == "" && snap.Length.Compass != nil {
		ending = snap.Length.Compass.Ending
	}
	if ending != "" {
		r.section("终局", "")
		r.paragraph(ending)
	}
	if len(snap.Threads) > 0 {
		r.section("长线", benchTheme.Muted.Render(fmt.Sprintf("%d 条", len(snap.Threads))))
		for _, thread := range snap.Threads {
			r.body(alignRight(benchTheme.Text.Render(thread.Name), benchTheme.Muted.Render(threadState(thread)), r.inner()))
			r.item(" ", thread.Question, benchTheme.Muted)
		}
	}
	if len(snap.Cast) > 0 {
		r.section("人物", benchTheme.Muted.Render(fmt.Sprintf("%d 位", len(snap.Cast))))
		for _, member := range snap.Cast[:min(len(snap.Cast), r.limits.cast)] {
			r.body(alignRight(benchTheme.Text.Render(member.Name), benchTheme.Muted.Render(fmt.Sprintf("%d 章", member.Chapters)), r.inner()))
		}
		if more := len(snap.Cast) - r.limits.cast; more > 0 {
			r.body(benchTheme.Muted.Render(fmt.Sprintf("… 另 %d 位 · /view 查看全部", more)))
		}
	}
	count := len(snap.Intent.Required) + len(snap.Intent.Forbidden) + len(snap.Directives)
	if count == 0 {
		return
	}
	r.section("要求", railCount(count, 0, benchTheme.Muted, ""))
	mark := benchTheme.Muted.Render("·")
	for _, text := range snap.Intent.Required {
		r.item(mark, text, benchTheme.Text)
	}
	for _, text := range snap.Intent.Forbidden {
		r.item(mark, "禁止 "+text, benchTheme.Text)
	}
	for _, directive := range snap.Directives {
		text := directive.Text
		if directive.Scope != domainmodel.DirectiveScopeProject {
			text += " · " + directive.Where
		}
		r.item(mark, text, benchTheme.Text)
	}
}

// threadState 一条长线此刻的进度：作答了写作答章；写过的章里推进过、之后没再推进的写停了
// 几章；刚推进的写那一章；还没写到的写第一次推进的章。
func threadState(thread workbench.Thread) string {
	switch {
	case thread.Answered:
		return fmt.Sprintf("第 %d 章作答", thread.Answer)
	case thread.Quiet > 0:
		return fmt.Sprintf("停了 %d 章", thread.Quiet)
	case thread.Advanced > 0:
		return fmt.Sprintf("第 %d 章推进", thread.Advanced)
	case thread.Next > 0:
		return fmt.Sprintf("第 %d 章起", thread.Next)
	default:
		return ""
	}
}

func briefThreadText(thread workbench.BriefThread) string {
	if thread.Answer {
		return thread.Name + " · 本章作答"
	}
	return thread.Name
}

// crewCard 创作团队在这本书上的累计：标题线右端是花费（有定价时）与用时；其下每个角色
// 两行；最后是合计。数字来自落盘的执行记录与主编对话，重启后照样完整。协调器按缺口派活，
// Architect、Writer、Editor 一次一个接力；Chief 是主编，回话时亮起。还没有记录时整块不出现。
func (m model) crewCard(width int) []string {
	usage := m.bench.snap.Usage
	if len(usage.Roles) == 0 {
		return nil
	}
	note := formatDuration(usage.Worked)
	if usage.Total.Cost > 0 {
		note = formatCost(usage.Total.Cost) + " · " + note
	}
	lines := []string{"", railRule(benchTheme.Muted.Render("创作团队"), benchTheme.Muted.Render(note), width)}
	for _, role := range m.api.Models.Roles()[1:] {
		lines = append(lines, m.crewLines(role, width)...)
	}
	return append(lines, benchTheme.Muted.Render("合计 "+tokensLine(usage.Total)))
}

// crewLines 一个角色两行：首行是状态、名字、模型与累计用时，正在干活的角色名字亮起；
// 次行是它名下的用量与缓存命中，有定价时右侧是花费。出过场的角色写它最近一次实际用的
// 模型，没出场的写它此刻的绑定。
func (m model) crewLines(role string, width int) []string {
	stats, appeared := m.bench.snap.Usage.Roles[role]
	if !appeared {
		return []string{
			benchTheme.Muted.Render("○ ") + benchTheme.Text.Render(roleNames[role]) + " " + benchTheme.Muted.Render(m.api.Models.Current(role).Model),
			"  " + benchTheme.Muted.Render("还没出场"),
		}
	}
	name := benchTheme.Text.Render(roleNames[role])
	mark, worked := m.stateMark(stats.State), formatDuration(stats.Worked)
	switch {
	case role == domainmodel.ChiefRole:
		// 主编不经任务执行：亮不亮看它是否正在回话，没有用时。
		mark, worked = benchTheme.Success.Render("✓"), ""
		if m.bench.thread.Busy {
			mark, name = benchTheme.Accent.Render("◉"), benchTheme.Focus.Render(roleNames[role])
		}
	case stats.State == domainmodel.OperationRunning && m.bench.writing:
		name = benchTheme.Focus.Render(roleNames[role])
	}
	price := ""
	if stats.Usage.Cost > 0 {
		price = benchTheme.Muted.Render(formatCost(stats.Usage.Cost))
	}
	return []string{
		alignRight(mark+" "+name+" "+benchTheme.Muted.Render(stats.Model), benchTheme.Muted.Render(worked), width),
		alignRight("  "+benchTheme.Muted.Render(tokensLine(stats.Usage)), price, width),
	}
}

// stateMark 角色最近一项任务的状态：进行中、失败、停下没收尾，其余（完成、等你确认、
// 被后继取代）都算做完了这一步。
func (m model) stateMark(state domainmodel.OperationState) string {
	switch state {
	case domainmodel.OperationRunning:
		if m.bench.writing {
			return benchTheme.Accent.Render("◉")
		}
		return benchTheme.Muted.Render("◦")
	case domainmodel.OperationFailed:
		return benchTheme.Error.Render("!")
	case domainmodel.OperationCancelled, domainmodel.OperationPaused:
		return benchTheme.Muted.Render("◦")
	default:
		return benchTheme.Success.Render("✓")
	}
}

// tokensLine 「↑输入 ↓输出 · 缓存 命中率」。
func tokensLine(usage domainmodel.Usage) string {
	line := fmt.Sprintf("↑%s ↓%s", formatTokens(usage.Input), formatTokens(usage.Output))
	if usage.CacheRead > 0 && usage.Input > 0 {
		line += fmt.Sprintf(" · 缓存 %.0f%%", 100*float64(usage.CacheRead)/float64(usage.Input))
	}
	return line
}

// spendLine 是 tokensLine 加上已知花费；没定价的模型不显示 $0。
func spendLine(usage domainmodel.Usage) string {
	if usage.Cost > 0 {
		return tokensLine(usage) + " · " + formatCost(usage.Cost)
	}
	return tokensLine(usage)
}

// railRule 右栏的标题线：与「AI 创作现场」同一写法，右端可带说明。两块的标题淡色，
// 依据里的分区名用强调色，由调用方上色。
func railRule(title, note string, width int) string {
	if note != "" {
		note = " " + note
	}
	return sectionTitle(title, width-lipgloss.Width(note)) + note
}

// railText 收集依据的行：分区名用强调色，其后横线补满；分区里的行左侧一道竖线，与横线
// 合成一个半框，标出这一段到哪为止；分区之间空一行。正文用正文色，次要的淡下去。
type railText struct {
	width  int
	limits railLimits
	lines  []string
}

func (r *railText) head(title, state string) {
	r.lines = append(r.lines, alignRight(benchTheme.Title.Render(title), state, r.width))
}

func (r *railText) section(title, note string) {
	r.lines = append(r.lines, "", railRule(benchTheme.Accent.Render(title), note, r.width))
}

// body 分区里的一行，前面是竖线。
func (r *railText) body(line string) {
	r.lines = append(r.lines, benchTheme.Border.Render("│")+" "+line)
}

// inner 是分区里竖线之后的宽度。
func (r *railText) inner() int { return r.width - 2 }

func (r *railText) paragraph(text string) {
	for _, line := range r.wrap(text, 0, r.limits.summary) {
		r.body(benchTheme.Text.Render(line))
	}
}

// item 一条带标记的条目：标记在首行行首，折行悬挂缩进两格。
func (r *railText) item(mark, text string, style lipgloss.Style) {
	for i, line := range r.wrap(text, 2, r.limits.item) {
		lead := "  "
		if i == 0 {
			lead = mark + " "
		}
		r.body(lead + style.Render(line))
	}
}

// fact 一条设定：种类标签在行首（待核验的用警示色），主体加粗，折行与标签后的文字对齐。
func (r *railText) fact(fact workbench.BriefFact) {
	tag := benchTheme.Muted
	if fact.Pending {
		tag = benchTheme.Warning
	}
	label := factKinds[fact.Kind]
	indent := lipgloss.Width(label) + 1
	for i, line := range r.wrap(fact.Subject+"："+fact.Text, indent, r.limits.item) {
		if i > 0 {
			r.body(strings.Repeat(" ", indent) + benchTheme.Text.Render(line))
			continue
		}
		body := benchTheme.Text.Render(line)
		if rest, ok := strings.CutPrefix(line, fact.Subject); ok {
			body = benchTheme.Title.Render(fact.Subject) + benchTheme.Text.Render(rest)
		}
		r.body(tag.Render(label) + " " + body)
	}
}

// wrap 把 text 折成分区里宽 inner-indent 的行，超出 rows 行的末行以省略号收尾。
func (r *railText) wrap(text string, indent, rows int) []string {
	lines := readingLines(oneLine(text), r.inner()-indent)
	if len(lines) > rows {
		lines = lines[:rows]
		last := []rune(lines[rows-1])
		lines[rows-1] = string(last[:len(last)-1]) + "…"
	}
	return lines
}
