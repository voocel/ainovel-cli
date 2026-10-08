package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

// reviewing 表示有稿件等你裁决：输入框里的文字是修改意见，y 是通过。
func (m model) reviewing() bool {
	d := m.bench.decision
	return d != nil && d.hasProposal && !m.bench.writing
}

// directiveScope 按目录选中行定要求的作用域（§4.9）：章行只管该章，卷/弧行管整个子树，
// 占位行或尚无目录时从下一章起生效。方案里尚未生效的节点不能被引用：章行按章号，
// 卷/弧行从下一章起。target 是给人看的短语。
func (m model) directiveScope() (scope, target string) {
	next := len(m.bench.snap.Manuscript) + 1
	rows := m.outlineRows()
	if cursor := m.bench.cursor; cursor >= 0 && cursor < len(rows) {
		row := rows[cursor]
		switch {
		case row.placeholder:
			return domainmodel.DirectiveScopeFromChapter(row.chapter), fmt.Sprintf("第 %d 章起", row.chapter)
		case row.node.Proposed && row.isChapter():
			return domainmodel.DirectiveScopeChapters(row.chapter, row.chapter), fmt.Sprintf("第 %d 章", row.chapter)
		case row.node.Proposed:
			return domainmodel.DirectiveScopeFromChapter(next), fmt.Sprintf("第 %d 章起", next)
		case row.isChapter():
			return domainmodel.DirectiveScopePlanNode(row.node.Node.ID), fmt.Sprintf("第 %d 章", row.chapter)
		default:
			return domainmodel.DirectiveScopePlanNode(row.node.Node.ID), "「" + row.node.Node.Title + "」"
		}
	}
	return domainmodel.DirectiveScopeFromChapter(next), fmt.Sprintf("第 %d 章起", next)
}

// composerScope 草稿在第一个字时锁定作用域，之后切章不改变它的语义。
func (m model) composerScope() (string, string) {
	if m.bench.inputScope != "" {
		return m.bench.inputScope, m.bench.inputLabel
	}
	return m.directiveScope()
}

func (m model) directiveScopeLabel(scope string) string {
	if scope == domainmodel.DirectiveScopeProject {
		return "全书"
	}
	if chapter, ok := strings.CutPrefix(scope, "from_chapter:"); ok {
		return "第 " + chapter + " 章起"
	}
	if chapters, ok := strings.CutPrefix(scope, "chapter_range:"); ok {
		if from, to, _ := strings.Cut(chapters, "-"); from == to {
			return "第 " + from + " 章"
		}
		return "第 " + chapters + " 章"
	}
	for _, entry := range m.bench.snap.Outline {
		if scope == domainmodel.DirectiveScopePlanNode(entry.Node.ID) {
			return entry.Node.Title
		}
	}
	return "原选定范围（当前大纲中不可见）"
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
		return fmt.Sprintf("第 %d 章 · %s", chapters[0].Number, chapters[0].Title)
	}
	if len(chapters) > 1 {
		return fmt.Sprintf("%d 章候选稿", len(chapters))
	}
	return "创作方案"
}

func chapterWords(chapter domainmodel.ManuscriptChapter) int {
	words := 0
	for _, block := range chapter.Blocks {
		words += utf8.RuneCountInString(block.Text)
	}
	return words
}

// candidateSummary 决定卡上的一行变更摘要：正文比对字数与标题，其余变更给规模。
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
			parts = append(parts, fmt.Sprintf("新稿 %s 字", groupDigits(chapterWords(chapter))))
			continue
		}
		parts = append(parts, fmt.Sprintf("正文 %s → %s 字", groupDigits(chapterWords(previous)), groupDigits(chapterWords(chapter))))
		if previous.Title != chapter.Title {
			parts = append(parts, "标题已调整")
		}
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
	section := func(title string) { body.WriteString("\n" + styleTitle.Render(title) + "\n") }
	body.WriteString(styleTitle.Render("审阅 · "+m.reviewTarget()) + "\n\n" + d.reason + "\n")
	if d.stale {
		body.WriteString("\n" + styleWarn.Render("此稿基于旧版本，只可提出修改意见重写。") + "\n")
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
			line := entity.Name + styleHint.Render("（"+entity.Kind.Noun()+"）")
			if len(entity.Aliases) > 0 {
				line += styleHint.Render(" 又名 ") + strings.Join(entity.Aliases, "、")
			}
			body.WriteString(styleHint.Render("· ") + line + "\n")
		}
	}
	for _, list := range []struct {
		title string
		items []string
	}{{"设定", view.Facts}, {"删除", view.Removed}, {"其他变更", view.Other}} {
		if len(list.items) == 0 {
			continue
		}
		section(list.title)
		for _, item := range list.items {
			body.WriteString(styleHint.Render("· ") + item + "\n")
		}
	}
	if view.Compass != nil {
		section("故事罗盘")
		body.WriteString(compassSummary(m.bench.snap.Length.Compass, *view.Compass) + "\n")
	}
	for _, chapter := range chapters {
		section(fmt.Sprintf("第 %d 章 · %s", chapter.Number, chapter.Title))
		body.WriteString(styleHint.Render("候选稿 · 尚未入稿") + "\n\n" + chapterText(chapter) + "\n")
	}
	body.WriteString("\n" + styleHint.Render(m.decisionActions()))
	return body.String(), nil
}

func writeOutlineItem(body *strings.Builder, depth int, title, summary string) {
	indent := strings.Repeat("  ", depth)
	body.WriteString(indent + title + "\n")
	if summary = strings.TrimSpace(summary); summary != "" {
		body.WriteString(styleHint.Render(indent+"  "+oneLine(summary)) + "\n")
	}
}

// decisionActions 说清怎么通过、怎么调整：修改意见让 AI 按意见重做这份稿件；规划的
// 意见同时作为后续章节的创作要求留下（§4.9、D71），之后的规划与写作都会遵守。
func (m model) decisionActions() string {
	d := m.bench.decision
	manuscript := func(patch domainmodel.Patch) bool { return patch.Document.Kind == domainmodel.DocumentManuscript }
	switch {
	case d.stale:
		return "写下修改意见后回车，让它基于最新内容重写"
	case slices.ContainsFunc(d.proposal.Patches, manuscript):
		return "y 通过 · 写下修改意见后回车重写"
	default:
		return "y 通过 · 写下修改意见后回车重新规划（意见留作后续章节的要求）"
	}
}

// compassSummary 把罗盘变化说成人话（D63）：AI 上调篇幅上限时，这正是等你裁决的内容；
// 固定篇幅下罗盘只有终局方向，没有上限与收官。
func compassSummary(current *domainmodel.Compass, next domainmodel.Compass) string {
	var before domainmodel.Compass
	if current != nil {
		before = *current
	}
	var parts []string
	switch {
	case before.ScaleMax == next.ScaleMax:
	case next.ScaleMax == 0:
		parts = append(parts, fmt.Sprintf("取消篇幅上限（原 %d 章）", before.ScaleMax))
	case before.ScaleMax == 0:
		parts = append(parts, fmt.Sprintf("篇幅上限 %d 章", next.ScaleMax))
	default:
		parts = append(parts, fmt.Sprintf("篇幅上限 %d → %d 章", before.ScaleMax, next.ScaleMax))
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

// firstBlockingFinding 取选中章第一条尚未被接受的阻塞发现（/accept 一次只裁一条）。
func (m model) firstBlockingFinding() (workbench.WorkbenchFinding, bool) {
	chapterID := m.selectedChapterID()
	for _, finding := range m.bench.snap.Findings {
		if chapterID != "" && finding.ChapterID == chapterID && finding.Severity == domainmodel.FindingBlocking {
			return finding, true
		}
	}
	return workbench.WorkbenchFinding{}, false
}

// detailReport 完整详情（/view 全屏）：选中章的全部依据（与右栏同序，放不下的在这里读全），
// 再是全书的创作意图与要求。
func (m model) detailReport() string {
	bench := m.bench
	number := m.selectedChapterNumber()
	title := "详情"
	if number > 0 {
		title = fmt.Sprintf("第 %d 章详情", number)
	}
	var view strings.Builder
	view.WriteString(styleTitle.Render(title) + "\n")
	section := func(name string) { view.WriteString("\n" + styleTitle.Render(name) + "\n") }
	if node, ok := m.outlineChapter(number); ok && node.Node.Summary != "" {
		section("本章规划")
		view.WriteString(node.Node.Summary + "\n")
	}
	brief := bench.snap.Briefs[number]
	if len(brief.Requirements) > 0 {
		section("要求")
		for _, requirement := range brief.Requirements {
			view.WriteString(checkMark(requirement.Status) + " " + requirementText(requirement) + styleHint.Render("（"+checkWord(requirement.Status)+"）") + "\n")
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
			view.WriteString(styleHint.Render(marker) + finding.Note + "\n")
			findings++
		}
	}
	if len(brief.Facts) > 0 {
		section("本章设定")
		for _, fact := range brief.Facts {
			mark := styleHint.Render("· ")
			if fact.Pending {
				mark = styleWarn.Render("待核验 ")
			}
			view.WriteString(mark + fact.Text + "\n")
		}
	}
	intent := bench.snap.Intent
	section("创作意图")
	if len(intent.Required) > 0 {
		view.WriteString(styleHint.Render("必须 ") + strings.Join(intent.Required, "、") + "\n")
	}
	if len(intent.Forbidden) > 0 {
		view.WriteString(styleHint.Render("禁止 ") + strings.Join(intent.Forbidden, "、") + "\n")
	}
	if intent.EndingDirection != "" {
		view.WriteString(styleHint.Render("结局 ") + intent.EndingDirection + "\n")
	}
	if len(bench.snap.Ownership) > 0 {
		view.WriteString(styleHint.Render(fmt.Sprintf("锁定 %d 处", len(bench.snap.Ownership))) + "\n")
	}
	if len(bench.snap.Directives) > 0 {
		section("创作要求")
		for _, directive := range bench.snap.Directives {
			view.WriteString(styleHint.Render("· ") + directive.Text + "（" + m.directiveScopeLabel(directive.Scope) + "）\n")
		}
	}
	return view.String()
}
