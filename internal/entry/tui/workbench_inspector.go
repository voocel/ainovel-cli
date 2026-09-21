package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

// Review context follows the proposal, independently of the outline selection.
func (m model) detailSummary(width, rows int) []string {
	return m.detailFrame(width, rows).lines
}

func (m model) detailFrame(width, rows int) inspectorFrame {
	snap := m.bench.snap
	number := m.selectedChapterNumber()
	review := m.bench.content == contentReview && m.bench.decision != nil && m.bench.decision.hasProposal
	var changes []string
	if review {
		chapters, err := m.reviewChapters()
		if err != nil {
			return inspectorFrame{lines: inspectorLimit(contextLines("读取异常", err.Error(), width), rows)}
		}
		number = 0
		if len(chapters) == 1 {
			number = chapters[0].Number
		}
		changes = m.candidateChanges(chapters, width)
	}
	if number == 0 {
		if review {
			return inspectorFrame{lines: inspectorLimit(append([]string{sectionTitle("本次变更", width, false)}, changes...), rows)}
		}
		lines := []string{sectionTitle("作品", width, false), fmt.Sprintf("目标 %d 章 · 已入稿 %d 章", m.currentTarget(), len(snap.Manuscript))}
		if snap.Intent.EndingDirection != "" {
			lines = append(lines, contextLines("结局方向", snap.Intent.EndingDirection, width)...)
		}
		return inspectorFrame{lines: inspectorLimit(lines, rows)}
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
	var requirements, findings, fullFindings []string
	findingCount, requirementCount := 0, 0
	target := domainmodel.DirectiveTarget{ChapterNumber: number, PlanNodeIDs: domainmodel.PlanAncestry(plan, selected.ID)}
	for _, directive := range snap.Directives {
		if directive.Covers(target) {
			requirementCount++
			if requirementCount <= 2 {
				requirements = append(requirements, inspectorExcerpt("["+m.directiveScopeLabel(directive.Scope)+"] "+directive.Text, width-2, 2)...)
			}
		}
	}
	for _, finding := range snap.Findings {
		if chapterID != "" && finding.ChapterID == chapterID {
			findingCount++
			marker := "·"
			if finding.Severity == domainmodel.FindingBlocking {
				marker = "!"
			}
			fullFindings = append(fullFindings, marker+" "+finding.Note)
			if findingCount <= 2 {
				findings = append(findings, inspectorExcerpt(marker+" "+finding.Note, width-2, 2)...)
			}
		}
	}
	lines := []string{benchTheme.Title.Render("本章"), styleTitle.Render(fmt.Sprintf("第 %d 章", number)) + benchTheme.Muted.Render(" · "+state)}
	var hits []inspectorHit
	link := func(label, detail string) {
		label = fitLine(label, width-2)
		hits = append(hits, inspectorHit{start: len(lines), end: len(lines) + 1, x: 2, width: lipgloss.Width(label), detail: detail})
		lines = append(lines, "  "+benchTheme.Accent.Render(label))
	}
	add := func(title string, content []string, limit int) {
		if len(content) > 0 {
			heading := benchTheme.Muted.Render(title)
			if strings.HasPrefix(title, "需要留意") {
				heading = benchTheme.Warning.Render(title)
			}
			lines = append(lines, "", heading)
			for _, line := range inspectorLimit(content, limit) {
				style := benchTheme.Text
				if title == "本章目标" {
					style = benchTheme.Muted
				}
				lines = append(lines, "  "+style.Render(line))
			}
		}
	}
	add("本次变更", changes, 7)
	if findingCount > 0 {
		add(fmt.Sprintf("需要留意 · %d 项", findingCount), findings, 4)
		link("查看完整问题 →", fmt.Sprintf("第 %d 章 · 完整问题\n\n", number)+strings.Join(fullFindings, "\n\n"))
	}
	if requirementCount > 0 {
		add(fmt.Sprintf("你的要求 · %d 项生效", requirementCount), requirements, 4)
	}
	if selected.Summary != "" {
		add("本章目标", inspectorExcerpt(selected.Summary, width-2, 4), 4)
		link("查看完整目标 →", fmt.Sprintf("第 %d 章 · 本章目标\n\n", number)+selected.Summary)
	}
	visible := len(lines)
	if visible > rows {
		visible = max(0, rows-1) // The final row becomes the omission hint, not an action.
	}
	frame := inspectorFrame{lines: inspectorLimit(lines, rows)}
	for _, hit := range hits {
		if hit.start < visible {
			frame.hits = append(frame.hits, hit)
		}
	}
	return frame
}

// Excerpts preserve source wording; deeper reading belongs in the full-screen view.
func inspectorExcerpt(text string, width, limit int) []string {
	lines := readingLines(text, max(1, width))
	if len(lines) > limit {
		lines = lines[:limit]
		lines[limit-1] = strings.TrimSuffix(truncate(lines[limit-1], max(1, width-1)), "…") + "…"
	}
	return lines
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
