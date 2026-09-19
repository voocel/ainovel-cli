package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

// Review context follows the proposal, independently of the outline selection.
func (m model) detailSummary(width, rows int) []string {
	snap := m.bench.snap
	number := m.selectedChapterNumber()
	review := m.bench.content == contentReview && m.bench.decision != nil && m.bench.decision.hasProposal
	var changes []string
	if review {
		chapters, err := m.reviewChapters()
		if err != nil {
			return contextLines("读取异常", err.Error(), width)
		}
		number = 0
		if len(chapters) == 1 {
			number = chapters[0].Number
		}
		changes = m.candidateChanges(chapters, width)
	}
	if number == 0 {
		if review {
			return inspectorLimit(append([]string{sectionTitle("本次变更", width, false)}, changes...), rows)
		}
		lines := []string{sectionTitle("作品", width, false), fmt.Sprintf("目标 %d 章 · 已入稿 %d 章", m.currentTarget(), len(snap.Manuscript))}
		if snap.Intent.EndingDirection != "" {
			lines = append(lines, contextLines("结局方向", snap.Intent.EndingDirection, width)...)
		}
		return inspectorLimit(lines, rows)
	}
	state, chapterID := "已规划", ""
	var selected domainmodel.PlanNode
	var plan []domainmodel.PlanNode
	for _, entry := range snap.Outline {
		plan = append(plan, entry.Node)
		if entry.Node.Kind == domainmodel.PlanChapter && entry.Number == number {
			selected, state = entry.Node, chapterStateLabel(entry.State)
		}
	}
	for _, chapter := range snap.Manuscript {
		if chapter.Number == number {
			chapterID = chapter.ID
		}
	}
	if review {
		state = "候选待确认"
	}
	var requirements, findings, facts []string
	target := domainmodel.DirectiveTarget{ChapterNumber: number, PlanNodeIDs: domainmodel.PlanAncestry(plan, selected.ID)}
	for _, directive := range snap.Directives {
		if directive.Covers(target) {
			requirements = append(requirements, contextLines("["+m.directiveScopeLabel(directive.Scope)+"]", directive.Text, width)...)
		}
	}
	findingCount, factCount := 0, 0
	for _, finding := range snap.Findings {
		if chapterID != "" && finding.ChapterID == chapterID {
			findingCount++
			marker := "·"
			if finding.Severity == domainmodel.FindingBlocking {
				marker = "!"
			}
			findings = append(findings, contextLines(marker, finding.Note, width)...)
		}
	}
	for _, fact := range snap.Canon {
		if chapterID != "" && fact.SourceChapterID == chapterID {
			factCount++
			label := "·"
			if slices.Contains(snap.PendingCanon, fact.ID) {
				label = "待核验"
			}
			facts = append(facts, contextLines(label, factLabel(fact.Value), width)...)
		}
	}
	lines := []string{sectionTitle("本章", width, false), styleTitle.Render(fmt.Sprintf("第 %d 章", number)) + benchTheme.Muted.Render(" · "+state), benchTheme.Muted.Render(fmt.Sprintf("事实 %d · 发现 %d", factCount, findingCount))}
	add := func(title string, content []string, limit int) {
		if len(content) > 0 {
			lines = append(lines, sectionTitle(title, width, false))
			lines = append(lines, inspectorLimit(content, limit)...)
		}
	}
	add("本次变更", changes, 5)
	add("待解决问题", findings, 4)
	add("生效要求", requirements, 6)
	if selected.Summary != "" {
		add("本章目标", contextLines("", selected.Summary, width), 4)
	}
	add("本章事实", facts, 4)
	return inspectorLimit(lines, rows)
}

func inspectorLimit(lines []string, limit int) []string {
	if len(lines) <= limit {
		return lines
	}
	if limit <= 0 {
		return nil
	}
	return append(lines[:limit-1], benchTheme.Muted.Render("… 尚有内容未展开"))
}

// Compare concrete manuscript content; do not infer a semantic change summary.
func (m model) candidateChanges(chapters []domainmodel.ManuscriptChapter, width int) []string {
	d := m.bench.decision
	lines := []string{truncate("候选 "+d.proposal.ID, width)}
	if d.stale {
		lines = append(lines, benchTheme.Warning.Render("基线已过期 · 需重新生成"))
	}
	for _, chapter := range chapters {
		var previous *domainmodel.ManuscriptChapter
		for i := range m.bench.snap.Manuscript {
			old := &m.bench.snap.Manuscript[i]
			if old.Number == chapter.Number {
				previous = old
				break
			}
		}
		if previous == nil {
			lines = append(lines, fmt.Sprintf("第 %d 章 · 新稿 %d 字", chapter.Number, inspectorWords(chapter)))
			continue
		}
		lines = append(lines, fmt.Sprintf("第 %d 章 · 正文 %d → %d 字", chapter.Number, inspectorWords(*previous), inspectorWords(chapter)))
		if previous.Title != chapter.Title {
			lines = append(lines, "标题已调整")
		}
	}
	attached := len(d.proposal.Patches) - len(chapters)
	if attached > 0 {
		lines = append(lines, fmt.Sprintf("另有 %d 项附带变更 · F3 查看", attached))
	}
	if strings.TrimSpace(d.proposal.Reason) != "" {
		lines = append(lines, contextLines("提交说明", d.proposal.Reason, width)...)
	}
	return lines
}

func inspectorWords(chapter domainmodel.ManuscriptChapter) int {
	words := 0
	for _, block := range chapter.Blocks {
		words += utf8.RuneCountInString(block.Text)
	}
	return words
}
