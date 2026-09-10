package novel

import (
	"slices"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// CanonGap 是一章的事实缺口（§4.4 D41 / §4.5）：Pending 是正文改动后待核验的来源事实，
// Unrecorded 表示该章没有任何来源事实（未入账）。Revision 是该章正文的最后变化 revision。
type CanonGap struct {
	ChapterID  string
	Number     int
	Revision   model.Revision
	Pending    []string
	Unrecorded bool
}

// CanonGaps 按章号列出有缺口的章。待核验是派生判定、不落字段：事实的最后变化 revision
// 早于来源章的，说明正文在事实入账后被改过（用户编辑、导入或回滚）；AI 重写重申报事实，
// 不产生待核验。
func CanonGaps(project projectdoc.Snapshot) []CanonGap {
	bySource := make(map[string][]model.CanonFact)
	for _, fact := range project.Canon {
		if fact.SourceChapterID != "" {
			bySource[fact.SourceChapterID] = append(bySource[fact.SourceChapterID], fact)
		}
	}
	var gaps []CanonGap
	for _, chapter := range project.Manuscript {
		revision := project.Index[(model.DocumentRef{Kind: model.DocumentManuscript, ID: chapter.ID}).Key()].Revision
		gap := CanonGap{ChapterID: chapter.ID, Number: chapter.Number, Revision: revision}
		facts := bySource[chapter.ID]
		gap.Unrecorded = len(facts) == 0 && project.CanonRecorded[chapter.ID] < revision
		for _, fact := range facts {
			if project.Index[(model.DocumentRef{Kind: model.DocumentCanon, ID: fact.ID}).Key()].Revision < revision {
				gap.Pending = append(gap.Pending, fact.ID)
			}
		}
		if gap.Unrecorded || len(gap.Pending) > 0 {
			slices.Sort(gap.Pending)
			gaps = append(gaps, gap)
		}
	}
	slices.SortFunc(gaps, func(left, right CanonGap) int { return left.Number - right.Number })
	return gaps
}
