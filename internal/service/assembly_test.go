package service

import (
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/derive"
	"github.com/voocel/ainovel-cli/internal/domain"
)

// TestEveryOperationKindIsAssembled 钉住装配完整性：登记一种 Operation 就必须同时
// 给出进行中的文案、LLM 族的能力定义与故事上下文契约。样例输入表必须覆盖每种。
func TestEveryOperationKindIsAssembled(t *testing.T) {
	basis := domain.EvidenceBasis{Documents: []domain.DocumentBasis{{Ref: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-1"}, Revision: 1}}}
	samples := map[domain.OperationKind]domain.TaskInput{
		domain.OperationInitializeProject: domain.InitializeProjectInput{Intent: "凡人修仙"},
		domain.OperationDevelopPlan:       domain.DevelopPlanInput{Intent: "凡人修仙", TargetChapters: 3, RequestedChapters: 3},
		domain.OperationRevisePlan:        domain.RevisePlanInput{Intent: "凡人修仙", TargetChapters: 6, ExistingChapters: 3, RequestedChapters: 6},
		domain.OperationReviseCanon:       domain.ReviseCanonInput{ChapterID: "chapter-1", Reason: "修订设定"},
		domain.OperationWriteChapter:      domain.WriteChapterInput{ChapterPlanID: "chapter-plan-1", ChapterNumber: 1},
		domain.OperationRewriteChapter: domain.RewriteChapterInput{
			ChapterID: "chapter-1", ChapterPlanID: "chapter-plan-1", ChapterNumber: 1, Findings: []string{"结尾仓促"},
		},
		domain.OperationRewriteAffected: domain.RewriteAffectedInput{
			ChapterIDs: []string{"chapter-1"}, BaseRevision: 1, ResolutionProposalID: "resolution-1", Reason: "同步设定",
		},
		domain.OperationReviewRange:   domain.ReviewRangeInput{ChapterIDs: []string{"chapter-1"}, Basis: basis},
		domain.OperationGenerateAsset: domain.GenerateAssetInput{Target: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-1"}, Role: "cover", Basis: basis},
		domain.OperationInspectAsset:  domain.InspectAssetInput{Artifact: domain.ArtifactRef{ID: "op/image", Digest: domain.Digest([]byte("image"))}, Basis: domain.EvidenceBasis{Documents: basis.Documents, Artifacts: []domain.ArtifactRef{{ID: "op/image", Digest: domain.Digest([]byte("image"))}}}},
	}
	content := derive.ProjectContent{
		ID: "book-1", Revision: 1, Intent: domain.Intent{Premise: "凡人修仙"},
		Plan: []domain.PlanNode{
			{ID: "volume-1", Kind: domain.PlanVolume, Title: "入道", Summary: "进入山门"},
			{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "考验"},
			{ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "抵达"},
		},
		Entities: []domain.Entity{{ID: "hero", Kind: domain.EntityCharacter, Name: "主角"}},
		Manuscript: []domain.ManuscriptChapter{{
			ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "第一章", Author: domain.AuthorAI,
			Blocks: []domain.ManuscriptBlock{{ID: "block-1", Text: "他抵达山门。"}},
		}},
	}
	for _, spec := range domain.OperationKinds() {
		sample, ok := samples[spec.Kind]
		if !ok {
			t.Fatalf("kind %s has no sample input in this test", spec.Kind)
		}
		if spec.Label == "" {
			t.Fatalf("kind %s has no progress label", spec.Kind)
		}
		task, err := json.Marshal(sample)
		if err != nil {
			t.Fatalf("encode %s sample: %v", spec.Kind, err)
		}
		if _, err := domain.DecodeTaskInput(spec.Kind, task); err != nil {
			t.Fatalf("kind %s sample input: %v", spec.Kind, err)
		}
		if spec.Executor != domain.ExecutorLLM {
			continue
		}
		if _, err := prompt.BuiltinCapability(spec.Kind); err != nil {
			t.Fatalf("kind %s has no capability: %v", spec.Kind, err)
		}
		if _, err := derive.BuildStoryContext(content, spec.Kind, task); err != nil {
			t.Fatalf("kind %s has no story context contract: %v", spec.Kind, err)
		}
	}
}
