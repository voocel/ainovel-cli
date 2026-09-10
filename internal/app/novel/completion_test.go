package novel

import (
	"strings"
	"testing"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestCompletionContractRejectsExtraPlanChapters(t *testing.T) {
	project := projectdoc.Snapshot{
		Plan: []model.PlanNode{
			{ID: "chapter-1", Kind: model.PlanChapter, Order: 1, Title: "第一章", Summary: "开端"},
			{ID: "chapter-2", Kind: model.PlanChapter, Order: 2, Title: "第二章", Summary: "推进"},
		},
		Manuscript: []model.ManuscriptChapter{
			{ID: "manuscript-1", PlanNodeID: "chapter-1", Number: 1, Title: "第一章", Blocks: []model.ManuscriptBlock{{ID: "block-1", Text: "正文"}}},
			{ID: "manuscript-2", PlanNodeID: "chapter-2", Number: 2, Title: "第二章", Blocks: []model.ManuscriptBlock{{ID: "block-2", Text: "正文"}}},
		},
	}
	goal := model.NovelGoal{Premise: "一章故事", TargetChapters: 1}
	if unmet := completionUnmet(project, goal); !strings.Contains(unmet, "蓝图有 2 章") {
		t.Fatalf("completion mismatch = %q", unmet)
	}
}
