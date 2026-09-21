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
// 占位行或尚无目录时从下一章起生效。target 是给人看的短语。
func (m model) directiveScope() (scope, target string) {
	rows := m.outlineRows()
	if cursor := m.bench.cursor; cursor >= 0 && cursor < len(rows) {
		row := rows[cursor]
		switch {
		case row.placeholder:
			return fmt.Sprintf("from_chapter:%d", row.chapter), fmt.Sprintf("第 %d 章起", row.chapter)
		case row.isChapter():
			return domainmodel.DirectiveScopePlanNode(row.node.Node.ID), fmt.Sprintf("第 %d 章", row.chapter)
		default:
			return domainmodel.DirectiveScopePlanNode(row.node.Node.ID), "「" + row.node.Node.Title + "」"
		}
	}
	next := len(m.bench.snap.Manuscript) + 1
	return fmt.Sprintf("from_chapter:%d", next), fmt.Sprintf("第 %d 章起", next)
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
	return "当前创作方案"
}

func chapterWords(chapter domainmodel.ManuscriptChapter) int {
	words := 0
	for _, block := range chapter.Blocks {
		words += utf8.RuneCountInString(block.Text)
	}
	return words
}

// candidateSummary 决定卡上的一行变更摘要：只比对具体正文，不推测语义差异。
func (m model) candidateSummary() string {
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
	if attached := len(m.bench.decision.proposal.Patches) - len(chapters); attached > 0 {
		parts = append(parts, fmt.Sprintf("另有 %d 项附带变更", attached))
	}
	if reason := strings.TrimSpace(m.bench.decision.proposal.Reason); reason != "" {
		parts = append(parts, "说明："+oneLine(reason))
	}
	return strings.Join(parts, " · ")
}

// reviewContent 完整审阅文本（/review 全屏）：原因、变更、候选正文与全部附带变更。
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
	body.WriteString("审阅 · " + m.reviewTarget() + "\n\n" + d.reason + "\n")
	if d.stale {
		body.WriteString("\n此稿基于旧版本，只可提出修改意见重写。\n")
	}
	body.WriteString("\n本次变更（对比当前已入稿正文）\n" + m.candidateSummary() + "\n")
	for _, chapter := range chapters {
		body.WriteString(fmt.Sprintf("\n第 %d 章 · %s\n候选稿 · 尚未入稿\n\n%s\n", chapter.Number, chapter.Title, chapterText(chapter)))
	}
	for _, patch := range d.proposal.Patches {
		if patch.Document.Kind == domainmodel.DocumentManuscript && patch.Operation == domainmodel.PatchPut {
			continue
		}
		if patch.Document.Kind == domainmodel.DocumentPlan && patch.Operation == domainmodel.PatchPut {
			var node domainmodel.PlanNode
			if err := json.Unmarshal(patch.Content, &node); err != nil {
				return "", fmt.Errorf("无法读取待确认大纲：%w", err)
			}
			body.WriteString("\n大纲 · " + node.Title + "\n" + node.Summary + "\n")
			continue
		}
		// 少见的文档类型也原样保留，审阅不能漏掉任何附带变更。
		body.WriteString(fmt.Sprintf("\n附带变更（原始内容） · %s · %s\n%s\n", patch.Document.ID, patch.Operation, patch.Content))
	}
	if d.stale {
		body.WriteString("\n写下修改意见后回车，让它基于最新内容重写。")
	} else {
		body.WriteString("\n输入 y 通过，或写下修改意见。/note 内容 可独立提出创作要求。")
	}
	return body.String(), nil
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

func factLabel(value []byte) string {
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	return string(value)
}

// detailReport 完整详情（/view 全屏）：本章规划、已确认事实、审阅发现、创作意图与要求。
func (m model) detailReport() string {
	bench := m.bench
	number := m.selectedChapterNumber()
	title := "详情"
	if number > 0 {
		title = fmt.Sprintf("第 %d 章详情", number)
	}
	var view strings.Builder
	view.WriteString(styleTitle.Render(title) + "\n")
	if node, ok := m.outlineChapter(number); ok && node.Node.Summary != "" {
		view.WriteString("\n" + styleTitle.Render("本章规划") + "\n" + node.Node.Summary + "\n")
	}
	chapterID := m.selectedChapterID()
	var facts []string
	for _, fact := range bench.snap.Canon {
		if chapterID == "" || fact.SourceChapterID != chapterID {
			continue
		}
		label := factLabel(fact.Value)
		if slices.Contains(bench.snap.PendingCanon, fact.ID) {
			label = styleWarn.Render("待核验 ") + label
		}
		facts = append(facts, label)
	}
	if len(facts) > 0 {
		view.WriteString("\n" + styleTitle.Render("已确认事实") + "\n")
		for _, fact := range facts {
			view.WriteString(styleHint.Render("· ") + fact + "\n")
		}
	}
	findings := 0
	for _, finding := range bench.snap.Findings {
		if chapterID == "" || finding.ChapterID != chapterID {
			continue
		}
		if findings == 0 {
			view.WriteString("\n" + styleTitle.Render("审阅发现") + "\n")
		}
		marker := "· "
		if finding.Severity == domainmodel.FindingBlocking {
			marker = "! "
		}
		view.WriteString(styleHint.Render(marker) + finding.Note + "\n")
		findings++
	}
	intent := bench.snap.Intent
	view.WriteString("\n" + styleTitle.Render("创作意图") + "\n")
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
		view.WriteString("\n" + styleTitle.Render("创作要求") + "\n")
		for _, directive := range bench.snap.Directives {
			view.WriteString(styleHint.Render("· ") + directive.Text + "（" + m.directiveScopeLabel(directive.Scope) + "）\n")
		}
	}
	return view.String()
}
