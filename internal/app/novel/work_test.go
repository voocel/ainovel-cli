package novel

import (
	"encoding/json"
	"testing"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func directiveTestProject() projectdoc.Snapshot {
	return projectdoc.Snapshot{
		Plan: []model.PlanNode{
			{ID: "volume-1", Kind: model.PlanVolume, Order: 1, Title: "卷一"},
			{ID: "arc-1", Kind: model.PlanArc, ParentID: "volume-1", Order: 1, Title: "弧一"},
			{ID: "chapter-plan-1", Kind: model.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章"},
			{ID: "chapter-plan-2", Kind: model.PlanChapter, ParentID: "arc-1", Order: 2, Title: "第二章"},
		},
		Manuscript: []model.ManuscriptChapter{
			{ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1},
			{ID: "chapter-2", PlanNodeID: "chapter-plan-2", Number: 2},
		},
		Directives: []model.Directive{
			{ID: "d-all", Scope: model.DirectiveScopeProject, Text: "每章结尾留钩子", Status: model.DirectiveActive},
			{ID: "d-arc", Scope: "plan_node:arc-1", Text: "弧一压住感情线", Status: model.DirectiveActive},
			{ID: "d-ch2", Scope: "plan_node:chapter-plan-2", Text: "第二章加一场雨", Status: model.DirectiveActive},
			{ID: "d-later", Scope: "from_chapter:3", Text: "第三章起换视角", Status: model.DirectiveActive},
			{ID: "d-old", Scope: model.DirectiveScopeProject, Text: "已退役", Status: model.DirectiveRetired},
		},
	}
}

func directiveIDs(directives []model.Directive) []string {
	ids := make([]string, 0, len(directives))
	for _, directive := range directives {
		ids = append(ids, directive.ID)
	}
	return ids
}

func TestCoveringDirectivesByWorkItem(t *testing.T) {
	project := directiveTestProject()
	cases := []struct {
		name string
		item novelItem
		want []string
	}{
		{"write chapter 1", novelItem{kind: workWriteChapter, number: 1, plan: project.Plan[2]}, []string{"d-all", "d-arc"}},
		{"rewrite chapter 2", novelItem{kind: workRewrite, number: 2, plan: project.Plan[3], chapterID: "chapter-2"}, []string{"d-all", "d-arc", "d-ch2"}},
		{"review union", novelItem{kind: workReview, chapters: []string{"chapter-1", "chapter-2"}}, []string{"d-all", "d-arc", "d-ch2"}},
		{"extend takes all active", novelItem{kind: workExtendPlan, covered: 2}, []string{"d-all", "d-arc", "d-ch2", "d-later"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := directiveIDs(coveringDirectives(project, tc.item))
			if len(got) != len(tc.want) {
				t.Fatalf("directives = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("directives = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestNovelItemWorkCarriesDirectivesAndRewriteBase(t *testing.T) {
	project := directiveTestProject()
	run := model.CreationRun{ID: "run-1", ProjectID: "book-1"}
	goal := model.NovelGoal{Premise: "故事", TargetChapters: 2}

	item := novelItem{
		kind: workRewrite, number: 2, plan: project.Plan[3], chapterID: "chapter-2", revision: 7,
		notes: []string{"节奏太慢"}, basis: chapterBasis(project, 2, project.Plan[3].ID),
	}
	item.directives = coveringDirectives(project, item)
	work, err := item.work(run, goal)
	if err != nil {
		t.Fatalf("rewrite work: %v", err)
	}
	rewrite, ok := work.Input.(model.RewriteChapterInput)
	if !ok || work.Kind != model.OperationRewriteChapter || work.ID != "run-1:rewrite:chapter-2:r7" {
		t.Fatalf("rewrite work = %#v", work)
	}
	if rewrite.ChapterID != "chapter-2" || rewrite.ChapterPlanID != "chapter-plan-2" || rewrite.ChapterNumber != 2 {
		t.Fatalf("rewrite input = %+v", rewrite)
	}
	// 章节任务基线只钉本章的要求作用域（D51）：相干要求变化才使任务失效。
	if len(rewrite.Basis.Documents) != 0 || len(rewrite.Basis.Scopes) != 1 || rewrite.Basis.Scopes[0].Target.ChapterNumber != 2 {
		t.Fatalf("rewrite basis = %+v", rewrite.Basis)
	}
	if got := directiveIDs(rewrite.Directives); len(got) != 3 || got[2] != "d-ch2" {
		t.Fatalf("rewrite directives = %v", got)
	}
	if work.Reasons.Waiting == "" || work.Reasons.Failure == "" || work.Reasons.Stuck == "" {
		t.Fatalf("rewrite reasons = %#v", work.Reasons)
	}

	review := novelItem{kind: workReview, chapters: []string{"chapter-1"}, revision: 7}
	review.directives = coveringDirectives(project, review)
	work, err = review.work(run, goal)
	if err != nil {
		t.Fatalf("review work: %v", err)
	}
	reviewInput, ok := work.Input.(model.ReviewRangeInput)
	if !ok || work.ID != "run-1:review:r7" {
		t.Fatalf("review work = %#v", work)
	}
	if got := directiveIDs(reviewInput.Directives); len(got) != 2 || got[0] != "d-all" || got[1] != "d-arc" {
		t.Fatalf("review directives = %v", got)
	}

	plain := novelItem{kind: workWriteChapter, number: 1, plan: project.Plan[2]}
	work, err = plain.work(run, goal)
	if err != nil {
		t.Fatalf("write work: %v", err)
	}
	encoded, err := json.Marshal(work.Input)
	if err != nil {
		t.Fatalf("encode write input: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("decode write input: %v", err)
	}
	if _, present := raw["directives"]; present {
		t.Fatalf("write input without directives must omit the field: %s", encoded)
	}
}
