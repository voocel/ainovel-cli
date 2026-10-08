package workbench

import (
	"slices"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// ChapterBrief 是一章的依据（页面设计 §3 右栏）：覆盖它的要求与审阅对每项的结论，以及
// 这一章记下的设定。规划摘要在大纲里，审阅发现在 Findings 里。
type ChapterBrief struct {
	Requirements []BriefRequirement `json:"requirements,omitempty"`
	Facts        []BriefFact        `json:"facts,omitempty"`
}

// BriefRequirement 是覆盖这一章的一项要求；Status 是包含这一章的当前裁定对它的结论
// （model.CheckSatisfied / CheckViolated / CheckPending），还没有有效裁定时为空。
type BriefRequirement struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Scope     string `json:"scope,omitempty"` // 用户要求的作用域（故事语言）；全书为空
	Forbidden bool   `json:"forbidden,omitempty"`
	Status    string `json:"status,omitempty"`
}

// BriefFact 是这一章记下的一条设定；Pending 表示正文改动后待核验（D41）。
type BriefFact struct {
	Text    string `json:"text"` // 「主体」谓词：内容
	Pending bool   `json:"pending,omitempty"`
}

// chapterBriefs 按章号组装各已规划章节的依据：要求与审阅闸门同一份清单、同一覆盖规则，
// 结论取该章的当前裁定（含用户接受的发现，D43）。
func chapterBriefs(project projectdoc.Snapshot, final int, current map[string]*novel.StoredVerdict) map[int]ChapterBrief {
	plans := model.ChapterPlansInOrder(project.Plan)
	written := make(map[string]model.ManuscriptChapter, len(project.Manuscript)) // plan node id → 正文章节
	numbers := make(map[string]int, len(project.Manuscript))                     // 正文章节 id → 章号
	for _, chapter := range project.Manuscript {
		written[chapter.PlanNodeID], numbers[chapter.ID] = chapter, chapter.Number
	}
	briefs := make(map[int]ChapterBrief, len(plans))
	effective := make(map[string]model.ReviewVerdict)
	for number, requirements := range novel.RequirementsByChapter(project, final) {
		var verdict model.ReviewVerdict
		if stored := current[written[plans[number-1].ID].ID]; stored != nil {
			if _, ok := effective[stored.Key]; !ok {
				effective[stored.Key] = stored.Effective()
			}
			verdict = effective[stored.Key]
		}
		brief := briefs[number]
		for _, requirement := range requirements {
			brief.Requirements = append(brief.Requirements, BriefRequirement{
				ID: requirement.ID, Text: requirement.Text, Scope: requirement.Scope,
				Forbidden: requirement.Forbidden, Status: verdict.CheckStatus(requirement.ID),
			})
		}
		briefs[number] = brief
	}
	var pending []string
	for _, gap := range novel.CanonGaps(project) {
		pending = append(pending, gap.Pending...)
	}
	story := storyOf(project)
	for _, fact := range project.Canon {
		number := numbers[fact.SourceChapterID]
		if number == 0 {
			continue
		}
		brief := briefs[number]
		brief.Facts = append(brief.Facts, BriefFact{Text: story.FactSummary(fact), Pending: slices.Contains(pending, fact.ID)})
		briefs[number] = brief
	}
	return briefs
}
