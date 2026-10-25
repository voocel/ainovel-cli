package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

// reviewing 表示有稿件等你裁决：y 是通过，不满意跟主编说理由。
func (m model) reviewing() bool {
	d := m.bench.decision
	return d != nil && d.hasProposal && !m.bench.writing
}

func (m model) reviewChapters() ([]domainmodel.ManuscriptChapter, error) {
	var chapters []domainmodel.ManuscriptChapter
	if m.bench.decision == nil {
		return chapters, nil
	}
	for _, patch := range m.bench.decision.proposal.Patches {
		if patch.Document.Kind != domainmodel.DocumentManuscript || patch.Operation != domainmodel.PatchPut {
			continue
		}
		var chapter domainmodel.ManuscriptChapter
		if err := json.Unmarshal(patch.Content, &chapter); err != nil {
			return nil, fmt.Errorf("无法读取待确认章节：%w", err)
		}
		chapters = append(chapters, chapter)
	}
	return chapters, nil
}

func (m model) reviewTarget() string {
	chapters, err := m.reviewChapters()
	if err != nil {
		return "候选稿读取异常"
	}
	if len(chapters) == 1 {
		return fmt.Sprintf("第 %d 章 · %s", chapters[0].Number, m.chapterTitle(chapters[0].Number))
	}
	if len(chapters) > 1 {
		return fmt.Sprintf("%d 章候选稿", len(chapters))
	}
	return "创作方案"
}

// candidateSummary 决定卡上的一行变更摘要：正文比对字数，其余变更（含改标题）给规模。
func (m model) candidateSummary() string {
	d := m.bench.decision
	chapters, err := m.reviewChapters()
	if err != nil {
		return benchTheme.Warning.Render("候选稿读取异常 · " + err.Error())
	}
	var parts []string
	for _, chapter := range chapters {
		previous, ok := chapterByNumber(m.bench.snap.Manuscript, chapter.Number)
		if !ok {
			parts = append(parts, fmt.Sprintf("新稿 %s 字", groupDigits(chapter.Runes())))
			continue
		}
		parts = append(parts, fmt.Sprintf("正文 %s → %s 字", groupDigits(previous.Runes()), groupDigits(chapter.Runes())))
	}
	parts = append(parts, d.view.Summary...)
	if d.view.Compass != nil {
		parts = append(parts, compassSummary(m.bench.snap.Length.Compass, *d.view.Compass))
	}
	if reason := strings.TrimSpace(d.proposal.Reason); reason != "" {
		parts = append(parts, "说明："+oneLine(reason))
	}
	return strings.Join(parts, " · ")
}

// reviewContent 完整审阅文本（/review 全屏）：全部变更用故事语言写出——大纲按卷弧章成树，
// 人物用名称，设定写成主体+谓词+内容，候选正文全文；不露文档 ID 与原始内容。
func (m model) reviewContent() (string, error) {
	d := m.bench.decision
	if d == nil || !d.hasProposal {
		return "暂无待确认稿件\n\n候选稿准备好后，决定卡会出现在正文下方。", nil
	}
	chapters, err := m.reviewChapters()
	if err != nil {
		return "", err
	}
	var body strings.Builder
	section := func(title string) { body.WriteString("\n" + benchTheme.Title.Render(title) + "\n") }
	body.WriteString(benchTheme.Title.Render("审阅 · "+m.reviewTarget()) + "\n\n" + d.reason + "\n")
	if d.stale {
		body.WriteString("\n" + benchTheme.Warning.Render("此稿基于旧版本，只可提出修改意见重写。") + "\n")
	}
	section("概要")
	body.WriteString(m.candidateSummary() + "\n")
	view := d.view
	if len(view.Outline) > 0 {
		section("大纲")
		for _, volume := range view.Outline {
			writeOutlineItem(&body, 0, fmt.Sprintf("第 %d 卷 · %s", volume.Volume, volume.Title), volume.Summary)
			for _, arc := range volume.Arcs {
				writeOutlineItem(&body, 1, fmt.Sprintf("第 %d 个故事弧 · %s", arc.Arc, arc.Title), arc.Summary)
				for _, chapter := range arc.Chapters {
					writeOutlineItem(&body, 2, fmt.Sprintf("第 %d 章 · %s", chapter.Chapter, chapter.Title), chapter.Summary)
				}
			}
		}
	}
	if len(view.Entities) > 0 {
		section("人物与地点")
		for _, entity := range view.Entities {
			line := entity.Name + benchTheme.Muted.Render("（"+entity.Kind.Noun()+"）")
			if len(entity.Aliases) > 0 {
				line += benchTheme.Muted.Render(" 又名 ") + strings.Join(entity.Aliases, "、")
			}
			body.WriteString(benchTheme.Muted.Render("· ") + line + "\n")
		}
	}
	if len(view.Facts) > 0 {
		section("设定")
		for _, fact := range view.Facts {
			body.WriteString(benchTheme.Muted.Render("· ") + factLine(fact) + "\n")
		}
	}
	for _, list := range []struct {
		title string
		items []string
	}{{"删除", view.Removed}, {"其他变更", view.Other}} {
		if len(list.items) == 0 {
			continue
		}
		section(list.title)
		for _, item := range list.items {
			body.WriteString(benchTheme.Muted.Render("· ") + item + "\n")
		}
	}
	if view.Compass != nil {
		section("故事罗盘")
		body.WriteString(compassSummary(m.bench.snap.Length.Compass, *view.Compass) + "\n")
	}
	for _, chapter := range chapters {
		section(fmt.Sprintf("第 %d 章 · %s", chapter.Number, m.chapterTitle(chapter.Number)))
		body.WriteString(benchTheme.Muted.Render("候选稿 · 尚未入稿") + "\n\n" + chapterText(chapter) + "\n")
	}
	body.WriteString("\n" + benchTheme.Muted.Render(m.decisionActions()))
	return body.String(), nil
}

func writeOutlineItem(body *strings.Builder, depth int, title, summary string) {
	indent := strings.Repeat("  ", depth)
	body.WriteString(indent + title + "\n")
	if summary = strings.TrimSpace(summary); summary != "" {
		body.WriteString(benchTheme.Muted.Render(indent+"  "+oneLine(summary)) + "\n")
	}
}

// decisionActions 说清怎么通过、怎么调整：修改意见让 AI 按意见重做这份稿件；规划的
// 意见同时作为后续章节的创作要求留下（§4.9、D71），之后的规划与写作都会遵守。
func (m model) decisionActions() string {
	d := m.bench.decision
	manuscript := func(patch domainmodel.Patch) bool { return patch.Document.Kind == domainmodel.DocumentManuscript }
	switch {
	case d.stale:
		return "回车按现在的内容重写"
	case slices.ContainsFunc(d.proposal.Patches, manuscript):
		return "y 通过 · 不满意跟主编说理由，按意见重写"
	default:
		return "y 通过 · 不满意跟主编说理由，重新规划（意见留作后续章节的要求）"
	}
}

// compassSummary 把罗盘变化说成人话（D63）：AI 把预计篇幅定到护栏以上时，这正是等你
// 裁决的内容（D73）；固定篇幅下罗盘只有终局方向，没有预计篇幅与收官。
func compassSummary(current *domainmodel.Compass, next domainmodel.Compass) string {
	var before domainmodel.Compass
	if current != nil {
		before = *current
	}
	var parts []string
	switch {
	case before.ScaleMax == next.ScaleMax:
	case next.ScaleMax == 0:
		parts = append(parts, fmt.Sprintf("取消预计篇幅（原 %d 章）", before.ScaleMax))
	case before.ScaleMax == 0:
		parts = append(parts, fmt.Sprintf("预计篇幅 %d 章", next.ScaleMax))
	default:
		parts = append(parts, fmt.Sprintf("预计篇幅 %d → %d 章", before.ScaleMax, next.ScaleMax))
	}
	switch {
	case before.Final == next.Final:
	case next.Final == 0:
		parts = append(parts, fmt.Sprintf("撤回收官（原 %d 章）", before.Final))
	default:
		parts = append(parts, fmt.Sprintf("收官 %d 章", next.Final))
	}
	if before.Ending != next.Ending {
		parts = append(parts, "终局："+oneLine(next.Ending))
	}
	var added, answered []string
	for _, thread := range next.Threads {
		old, ok := before.Thread(thread.ID)
		switch {
		case !ok:
			added = append(added, thread.Name)
		case old.Question != thread.Question:
			parts = append(parts, "长线「"+thread.Name+"」改问："+oneLine(thread.Question))
		}
		if thread.Answer != "" && old.Answer != thread.Answer {
			answered = append(answered, thread.Name)
		}
	}
	if len(added) > 0 {
		parts = append(parts, "新立长线："+strings.Join(added, "、"))
	}
	if len(answered) > 0 {
		parts = append(parts, "定下作答章："+strings.Join(answered, "、"))
	}
	if len(parts) == 0 {
		return "故事罗盘未变"
	}
	return strings.Join(parts, " · ")
}

func (m model) openReview() model {
	text, err := m.reviewContent()
	if err != nil {
		m.bench.err = err.Error()
		return m
	}
	return m.openBody(text)
}

// revealCandidate 决定卡点击：单章候选直接在正文视图里读；其余打开完整审阅。
func (m model) revealCandidate() model {
	chapters, err := m.reviewChapters()
	if err == nil && len(chapters) == 1 && m.findChapter(fmt.Sprint(chapters[0].Number), false) {
		return m
	}
	return m.openReview()
}

// detailReport 完整详情（/view 全屏）：选中章的全部依据（与右栏同序，放不下的在这里读全）；
// 选中全书时是篇幅、终局与全部人物；最后都是全书的创作意图与要求。
func (m model) detailReport() string {
	bench := m.bench
	number := m.selectedChapterNumber()
	title := "全书详情"
	if number > 0 {
		title = fmt.Sprintf("第 %d 章详情", number)
	}
	var view strings.Builder
	view.WriteString(benchTheme.Title.Render(title) + "\n")
	section := func(name string) { view.WriteString("\n" + benchTheme.Title.Render(name) + "\n") }
	if node, ok := m.outlineChapter(number); ok && node.Node.Summary != "" {
		section("本章规划")
		view.WriteString(node.Node.Summary + "\n")
		if node.Node.Turn != "" {
			view.WriteString(benchTheme.Muted.Render("转折 ") + node.Node.Turn + "\n")
		}
	}
	brief := bench.snap.Briefs[number]
	if len(brief.Requirements) > 0 {
		section("要求")
		for _, requirement := range brief.Requirements {
			view.WriteString(checkMark(requirement.Status) + " " + requirementText(requirement) + benchTheme.Muted.Render("（"+checkWord(requirement.Status)+"）") + "\n")
		}
	}
	if chapterID := m.selectedChapterID(); chapterID != "" {
		findings := 0
		for _, finding := range bench.snap.Findings {
			if finding.ChapterID != chapterID {
				continue
			}
			if findings == 0 {
				section("审阅意见")
			}
			marker := "· "
			if finding.Severity == domainmodel.FindingBlocking {
				marker = "! "
			}
			view.WriteString(benchTheme.Muted.Render(marker) + finding.Note + "\n")
			findings++
		}
	}
	if len(brief.Facts) > 0 {
		section("本章设定")
		for _, fact := range brief.Facts {
			mark := benchTheme.Muted.Render("· ")
			if fact.Pending {
				mark = benchTheme.Warning.Render("待核验 ")
			}
			view.WriteString(mark + factLine(fact.Fact) + "\n")
		}
	}
	if number == 0 {
		section("篇幅与终局")
		view.WriteString(lengthStatus(bench.snap.Length) + "\n")
		if compass := bench.snap.Length.Compass; compass != nil && compass.Ending != "" {
			view.WriteString(benchTheme.Muted.Render("终局 ") + compass.Ending + "\n")
		}
		if len(bench.snap.Threads) > 0 {
			section(fmt.Sprintf("长线 · %d 条", len(bench.snap.Threads)))
			for _, thread := range bench.snap.Threads {
				view.WriteString(threadLine(thread) + "\n")
			}
		}
		if len(bench.snap.Cast) > 0 {
			section(fmt.Sprintf("人物 · %d 位", len(bench.snap.Cast)))
			for _, member := range bench.snap.Cast {
				view.WriteString(castLine(member) + "\n")
			}
		}
	}
	intent := bench.snap.Intent
	section("创作意图")
	if len(intent.Required) > 0 {
		view.WriteString(benchTheme.Muted.Render("必须 ") + strings.Join(intent.Required, "、") + "\n")
	}
	if len(intent.Forbidden) > 0 {
		view.WriteString(benchTheme.Muted.Render("禁止 ") + strings.Join(intent.Forbidden, "、") + "\n")
	}
	if intent.EndingDirection != "" {
		view.WriteString(benchTheme.Muted.Render("结局 ") + intent.EndingDirection + "\n")
	}
	if len(bench.snap.Ownership) > 0 {
		view.WriteString(benchTheme.Muted.Render(fmt.Sprintf("锁定 %d 处", len(bench.snap.Ownership))) + "\n")
	}
	if len(bench.snap.Directives) > 0 {
		section("创作要求")
		for _, directive := range bench.snap.Directives {
			view.WriteString(benchTheme.Muted.Render("· ") + directive.Text + "（" + directive.Where + "）\n")
		}
	}
	return view.String()
}

// threadLine 一条长线：短名、追问的问题，最近推进的章、停了几章、下一次推进与作答的章。
func threadLine(thread workbench.Thread) string {
	var notes []string
	if thread.Advanced > 0 {
		notes = append(notes, fmt.Sprintf("最近第 %d 章推进", thread.Advanced))
	}
	if thread.Quiet > 0 {
		notes = append(notes, fmt.Sprintf("停了 %d 章", thread.Quiet))
	}
	if thread.Next > 0 && !thread.Answered {
		notes = append(notes, fmt.Sprintf("下次第 %d 章", thread.Next))
	}
	switch {
	case thread.Answered:
		notes = append(notes, fmt.Sprintf("第 %d 章已作答", thread.Answer))
	case thread.Answer > 0:
		notes = append(notes, fmt.Sprintf("第 %d 章作答", thread.Answer))
	default:
		notes = append(notes, "作答章未定")
	}
	return thread.Name + "：" + thread.Question + benchTheme.Muted.Render(" · "+strings.Join(notes, " · "))
}

// castLine 一位人物：名字、别名，涉及的已写章数与最近一章。
func castLine(member workbench.CastMember) string {
	line := member.Name
	if len(member.Aliases) > 0 {
		line += "（又名 " + strings.Join(member.Aliases, "、") + "）"
	}
	return line + benchTheme.Muted.Render(fmt.Sprintf(" · %d 章 · 最近第 %d 章", member.Chapters, member.Latest))
}

// unresolvedFindings 是第 number 章尚未被接受的阻塞意见（D94）。
func (m model) unresolvedFindings(number int) []workbench.WorkbenchFinding {
	chapter, ok := chapterByNumber(m.bench.snap.Manuscript, number)
	if !ok {
		return nil
	}
	var findings []workbench.WorkbenchFinding
	for _, finding := range m.bench.snap.Findings {
		if finding.ChapterID == chapter.ID && finding.Severity == domainmodel.FindingBlocking {
			findings = append(findings, finding)
		}
	}
	return findings
}

// heldFindings 是卡住那一章尚未被接受的阻塞意见。
func (m model) heldFindings() []workbench.WorkbenchFinding {
	return m.unresolvedFindings(m.heldNumber())
}

// heldNumber 是卡住那一章的章号，没有卡住为 0。
func (m model) heldNumber() int {
	if d := m.bench.decision; d != nil && d.held != "" {
		for _, chapter := range m.bench.snap.Manuscript {
			if chapter.ID == d.held {
				return chapter.Number
			}
		}
	}
	return 0
}

// heldSummary 是决定卡上卡住那一章的阻塞意见：第一条原话，其余计数。
func (m model) heldSummary() string {
	findings := m.heldFindings()
	if len(findings) == 0 {
		return ""
	}
	summary := "未解决：" + oneLine(findings[0].Note)
	if len(findings) > 1 {
		summary += fmt.Sprintf("（另有 %d 条）", len(findings)-1)
	}
	return summary
}
