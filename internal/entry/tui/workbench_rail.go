package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// 右栏「写作依据」（页面设计 §3）：主栏正在看的那一章（或卷、弧、全书）依据什么写——
// 规划、覆盖它的要求与审阅结论、审阅意见、这一章记下的设定。全部来自权威快照，重启、
// 空闲时照样完整；底部几行是本轮花费，运行明细在 /diag。

const (
	railSummaryRows = 4 // 规划摘要最多几行
	railItemRows    = 2 // 一条要求、意见或设定最多几行
)

// railColumn 右栏：标题行、依据（放不下时末行指向 /view）、底部本轮花费。
func (m model) railColumn(l benchLayout) []string {
	width := l.railWidth - 2
	lines := append([]string{benchTheme.Muted.Render("写作依据"), ""}, m.briefLines(width)...)
	usage := m.usageLines(width)
	room := l.bodyHeight - len(usage)
	if len(lines) > room {
		lines = append(lines[:room-1], benchTheme.Muted.Render("… /view 查看全部"))
	}
	for len(lines) < room {
		lines = append(lines, "")
	}
	lines = append(lines, usage...)
	for i := range lines {
		lines[i] = " " + fitLine(lines[i], width)
	}
	return fitBlock(strings.Join(lines, "\n"), l.railWidth, l.bodyHeight)
}

// briefLines 跟着目录光标走：章行是这一章的依据，卷/弧头行是这一部分的，没有大纲时是全书的。
func (m model) briefLines(width int) []string {
	rows := m.outlineRows()
	if cursor := m.bench.cursor; cursor >= 0 && cursor < len(rows) {
		switch row := rows[cursor]; {
		case row.isChapter():
			return m.chapterBrief(row.chapter, width)
		case row.header():
			return m.sectionBrief(row, width)
		}
	}
	return m.bookBrief(width)
}

func (m model) chapterBrief(number, width int) []string {
	snap := m.bench.snap
	node, _ := m.outlineChapter(number)
	r := railText{width: width}
	r.head(fmt.Sprintf("第 %d 章 · %s", number, node.Node.Title), m.chapterState(node))
	if node.Node.Summary != "" {
		r.section("规划", "")
		r.paragraph(node.Node.Summary)
	}
	brief := snap.Briefs[number]
	if len(brief.Requirements) > 0 {
		violated := 0
		for _, requirement := range brief.Requirements {
			if requirement.Status == domainmodel.CheckViolated {
				violated++
			}
		}
		note := benchTheme.Muted.Render(fmt.Sprintf("%d 条", len(brief.Requirements)))
		if violated > 0 {
			note = benchTheme.Error.Render(fmt.Sprintf("违反 %d", violated)) + benchTheme.Muted.Render(" · ") + note
		}
		r.section("要求", note)
		for _, requirement := range brief.Requirements {
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
			note := benchTheme.Muted.Render(fmt.Sprintf("%d 条", len(findings)))
			if blocking > 0 {
				note = benchTheme.Warning.Render(fmt.Sprintf("阻塞 %d", blocking)) + benchTheme.Muted.Render(" · ") + note
			}
			r.section("审阅意见", note)
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
		note := benchTheme.Muted.Render(fmt.Sprintf("%d 条", len(brief.Facts)))
		if pending > 0 {
			note = benchTheme.Warning.Render(fmt.Sprintf("待核验 %d", pending)) + benchTheme.Muted.Render(" · ") + note
		}
		r.section("本章设定", note)
		for _, fact := range brief.Facts {
			mark := benchTheme.Muted.Render("·")
			if fact.Pending {
				mark = benchTheme.Warning.Render("◌")
			}
			r.item(mark, fact.Text, benchTheme.Muted)
		}
	}
	return r.lines
}

// chapterState 与目录同一套符号：已入稿附字数。
func (m model) chapterState(node workbench.OutlineNode) string {
	switch {
	case node.State == workbench.ChapterConfirmed:
		chapter, _ := chapterByNumber(m.bench.snap.Manuscript, node.Number)
		return styleNotice.Render(fmt.Sprintf("✓ %s 字", groupDigits(chapterWords(chapter))))
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
		return styleNotice.Render("✓")
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

// sectionBrief 卷/弧：进度、概要，以及挂在这一部分上的要求（输入框此时的作用域就是它）。
func (m model) sectionBrief(row outlineRow, width int) []string {
	r := railText{width: width}
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
		r.section("要求", benchTheme.Muted.Render(fmt.Sprintf("%d 条", len(texts))))
		for _, text := range texts {
			r.item(benchTheme.Muted.Render("·"), text, benchTheme.Text)
		}
	}
	return r.lines
}

// bookBrief 还没有大纲时：篇幅、终局、创作意图与全部要求。
func (m model) bookBrief(width int) []string {
	snap := m.bench.snap
	r := railText{width: width}
	r.head("全书", benchTheme.Muted.Render(lengthStatus(snap.Length)))
	ending := snap.Intent.EndingDirection
	if ending == "" && snap.Length.Compass != nil {
		ending = snap.Length.Compass.Ending
	}
	if ending != "" {
		r.section("终局", "")
		r.paragraph(ending)
	}
	count := len(snap.Intent.Required) + len(snap.Intent.Forbidden) + len(snap.Directives)
	if count == 0 {
		return r.lines
	}
	r.section("要求", benchTheme.Muted.Render(fmt.Sprintf("%d 条", count)))
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
			text += " · " + m.directiveScopeLabel(directive.Scope)
		}
		r.item(mark, text, benchTheme.Text)
	}
	return r.lines
}

// usageLines 本轮花费：合计、用时、用量，每个用过的模型一行；没有实时用量时不占位。
func (m model) usageLines(width int) []string {
	feed, ok := m.activityFeed()
	if !ok || len(feed.Models) == 0 {
		return nil
	}
	note := fmt.Sprintf("%s · 已用 %s", formatCost(feed.Usage.Cost), formatDuration(workedTime(feed)))
	lines := []string{"", railSection("本轮", benchTheme.Muted.Render(note), width), "  " + benchTheme.Muted.Render(tokensLine(feed.Usage))}
	for _, entry := range feed.Models {
		name := entry.Model
		if name == "" {
			name = m.api.Models.Current("").Model
		}
		mark := benchTheme.Muted.Render("·")
		if entry.Model == feed.ActiveModel {
			mark = benchTheme.Accent.Render("●")
		}
		lines = append(lines, alignRight(mark+" "+benchTheme.Text.Render(name), benchTheme.Muted.Render(formatCost(entry.Usage.Cost)), width))
	}
	retries := 0
	for _, task := range feed.Tasks {
		retries += task.Retries
	}
	if retries > 0 {
		lines = append(lines, "  "+benchTheme.Warning.Render(fmt.Sprintf("连接重试 %d 次", retries)))
	}
	return lines
}

// tokensLine 「↑输入 ↓输出 · 缓存 命中率」。
func tokensLine(usage activity.UsageTotals) string {
	line := fmt.Sprintf("↑%s ↓%s", formatTokens(usage.Input), formatTokens(usage.Output))
	if usage.CacheRead > 0 && usage.Input > 0 {
		line += fmt.Sprintf(" · 缓存 %.0f%%", 100*float64(usage.CacheRead)/float64(usage.Input))
	}
	return line
}

// workedTime 本轮实际执行时长：已结束任务之和 + 进行中任务到现在。
func workedTime(feed activity.Snapshot) time.Duration {
	worked := feed.Worked
	for _, task := range feed.Tasks {
		if !task.Done && !task.StartedAt.IsZero() {
			worked += time.Since(task.StartedAt)
		}
	}
	return worked
}

// railSection 分区标题：左边标题，右边是已着色的计数。
func railSection(title, note string, width int) string {
	return alignRight(benchTheme.Muted.Render(title), note, width)
}

// railText 收集右栏的行：分区之间空一行，文字按宽度折行，超出行数的末行以省略号收尾。
type railText struct {
	width int
	lines []string
}

func (r *railText) head(title, state string) {
	r.lines = append(r.lines, alignRight(benchTheme.Title.Render(title), state, r.width))
}

func (r *railText) section(title, note string) {
	r.lines = append(r.lines, "", railSection(title, note, r.width))
}

// paragraph 缩进两格的一段文字。
func (r *railText) paragraph(text string) {
	r.wrap("  ", text, benchTheme.Muted, railSummaryRows)
}

// item 一条带标记的条目：标记在首行行首，折行悬挂缩进两格。
func (r *railText) item(mark, text string, style lipgloss.Style) {
	r.wrap(mark+" ", text, style, railItemRows)
}

func (r *railText) wrap(lead, text string, style lipgloss.Style, rows int) {
	wrapped := readingLines(oneLine(text), r.width-2)
	if len(wrapped) > rows {
		wrapped = wrapped[:rows]
		last := []rune(wrapped[rows-1])
		wrapped[rows-1] = string(last[:len(last)-1]) + "…"
	}
	for i, line := range wrapped {
		if i > 0 {
			lead = "  "
		}
		r.lines = append(r.lines, lead+style.Render(line))
	}
}
