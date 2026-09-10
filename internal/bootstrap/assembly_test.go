package bootstrap_test

import (
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain/derive"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

// TestEveryOperationKindIsAssembled 钉住装配完整性：登记一种 Operation 就必须同时
// 给出进行中的文案、LLM 族的能力定义与故事上下文契约。样例输入表必须覆盖每种。
func TestEveryOperationKindIsAssembled(t *testing.T) {
	basis := model.EvidenceBasis{Documents: []model.DocumentBasis{{Ref: model.DocumentRef{Kind: model.DocumentManuscript, ID: "chapter-1"}, Revision: 1}}}
	samples := map[model.OperationKind]model.TaskInput{
		model.OperationInitializeProject: model.InitializeProjectInput{Intent: "凡人修仙"},
		model.OperationDevelopPlan:       model.DevelopPlanInput{Intent: "凡人修仙", TargetChapters: 3, RequestedChapters: 3},
		model.OperationRevisePlan:        model.RevisePlanInput{Intent: "凡人修仙", TargetChapters: 6, ExistingChapters: 3, RequestedChapters: 6},
		model.OperationReviseCanon:       model.ReviseCanonInput{ChapterID: "chapter-1", Reason: "修订设定"},
		model.OperationWriteChapter:      model.WriteChapterInput{ChapterPlanID: "chapter-plan-1", ChapterNumber: 1},
		model.OperationRewriteChapter: model.RewriteChapterInput{
			ChapterID: "chapter-1", ChapterPlanID: "chapter-plan-1", ChapterNumber: 1, Findings: []string{"结尾仓促"},
		},
		model.OperationRewriteAffected: model.RewriteAffectedInput{
			ChapterIDs: []string{"chapter-1"}, BaseRevision: 1, ResolutionProposalID: "resolution-1", Reason: "同步设定",
		},
		model.OperationReviewRange:   model.ReviewRangeInput{ChapterIDs: []string{"chapter-1"}, Basis: basis},
		model.OperationGenerateAsset: model.GenerateAssetInput{Target: model.DocumentRef{Kind: model.DocumentManuscript, ID: "chapter-1"}, Role: "cover", Basis: basis},
		model.OperationInspectAsset:  model.InspectAssetInput{Artifact: model.ArtifactRef{ID: "op/image", Digest: model.Digest([]byte("image"))}, Basis: model.EvidenceBasis{Documents: basis.Documents, Artifacts: []model.ArtifactRef{{ID: "op/image", Digest: model.Digest([]byte("image"))}}}},
	}
	content := derive.ProjectContent{
		ID: "book-1", Revision: 1, Intent: model.Intent{Premise: "凡人修仙"},
		Plan: []model.PlanNode{
			{ID: "volume-1", Kind: model.PlanVolume, Title: "入道", Summary: "进入山门"},
			{ID: "arc-1", Kind: model.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "考验"},
			{ID: "chapter-plan-1", Kind: model.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "抵达"},
		},
		Entities: []model.Entity{{ID: "hero", Kind: model.EntityCharacter, Name: "主角"}},
		Manuscript: []model.ManuscriptChapter{{
			ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "第一章", Author: model.AuthorAI,
			Blocks: []model.ManuscriptBlock{{ID: "block-1", Text: "他抵达山门。"}},
		}},
	}
	for _, spec := range model.OperationKinds() {
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
		if _, err := model.DecodeTaskInput(spec.Kind, task); err != nil {
			t.Fatalf("kind %s sample input: %v", spec.Kind, err)
		}
		if spec.Executor != model.ExecutorLLM {
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
