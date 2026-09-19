package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

// A draft keeps its target while the reader navigates or live progress advances.
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

func (m model) reviewContent() (string, error) {
	d := m.bench.decision
	if d == nil || !d.hasProposal {
		return "暂无待确认稿件\n\n候选稿准备好后，会在右侧待办中提示。你可以继续阅读正文或观察实时输出。", nil
	}
	var body strings.Builder
	body.WriteString("审阅 · " + m.reviewTarget() + "\n\n" + d.reason + "\n")
	if d.stale {
		body.WriteString("\n此稿基于旧版本，只可提出修改意见重写。\n")
	}
	chapters, err := m.reviewChapters()
	if err != nil {
		return "", err
	}
	body.WriteString("\n本次变更（对比当前已入稿正文）\n" + strings.Join(m.candidateChanges(chapters, 90), "\n") + "\n")
	for _, chapter := range chapters {
		body.WriteString("\n" + renderChapterBody(chapter, "候选稿 · 尚未入稿") + "\n")
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
		// Preserve every attached change for review, including less common document types.
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
	return m.openBody(text).(model)
}
