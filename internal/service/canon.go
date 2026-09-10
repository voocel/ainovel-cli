package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// canonGap 是一章的事实缺口（§4.4 D41 / §4.5）：Pending 是正文改动后待核验的来源事实，
// Unrecorded 表示该章没有任何来源事实（未入账）。Revision 是该章正文的最后变化 revision。
type canonGap struct {
	ChapterID  string
	Number     int
	Revision   domain.Revision
	Pending    []string
	Unrecorded bool
}

// canonGaps 按章号列出有缺口的章。待核验是派生判定、不落字段：事实的最后变化 revision
// 早于来源章的，说明正文在事实入账后被改过（用户编辑、导入或回滚）；AI 重写重申报事实，
// 不产生待核验。
func canonGaps(project ProjectSnapshot) []canonGap {
	bySource := make(map[string][]domain.CanonFact)
	for _, fact := range project.Canon {
		if fact.SourceChapterID != "" {
			bySource[fact.SourceChapterID] = append(bySource[fact.SourceChapterID], fact)
		}
	}
	var gaps []canonGap
	for _, chapter := range project.Manuscript {
		revision := project.Index[(domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}).Key()].Revision
		gap := canonGap{ChapterID: chapter.ID, Number: chapter.Number, Revision: revision}
		facts := bySource[chapter.ID]
		gap.Unrecorded = len(facts) == 0 && project.CanonRecorded[chapter.ID] < revision
		for _, fact := range facts {
			if project.Index[(domain.DocumentRef{Kind: domain.DocumentCanon, ID: fact.ID}).Key()].Revision < revision {
				gap.Pending = append(gap.Pending, fact.ID)
			}
		}
		if gap.Unrecorded || len(gap.Pending) > 0 {
			slices.Sort(gap.Pending)
			gaps = append(gaps, gap)
		}
	}
	slices.SortFunc(gaps, func(left, right canonGap) int { return left.Number - right.Number })
	return gaps
}

func (s *Service) canonRecorded(ctx context.Context, target domain.AuthorityTarget, at domain.Revision) (map[string]domain.Revision, error) {
	versions, err := s.store.ListDocumentVersions(ctx, target, domain.DocumentCanon, at)
	if err != nil {
		return nil, err
	}
	recorded := make(map[string]domain.Revision)
	for _, version := range versions {
		var fact domain.CanonFact
		if err := json.Unmarshal(version.Content, &fact); err != nil {
			return nil, fmt.Errorf("decode historical Canon %s: %w", version.Document.ID, err)
		}
		if fact.SourceChapterID != "" {
			recorded[fact.SourceChapterID] = max(recorded[fact.SourceChapterID], version.Revision)
		}
	}
	return recorded, nil
}
