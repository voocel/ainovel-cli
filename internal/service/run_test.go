package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func directiveTestProject() ProjectSnapshot {
	return ProjectSnapshot{
		Plan: []domain.PlanNode{
			{ID: "volume-1", Kind: domain.PlanVolume, Order: 1, Title: "卷一"},
			{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Order: 1, Title: "弧一"},
			{ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章"},
			{ID: "chapter-plan-2", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 2, Title: "第二章"},
		},
		Manuscript: []domain.ManuscriptChapter{
			{ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1},
			{ID: "chapter-2", PlanNodeID: "chapter-plan-2", Number: 2},
		},
		Directives: []domain.Directive{
			{ID: "d-all", Scope: domain.DirectiveScopeProject, Text: "每章结尾留钩子", Status: domain.DirectiveActive},
			{ID: "d-arc", Scope: "plan_node:arc-1", Text: "弧一压住感情线", Status: domain.DirectiveActive},
			{ID: "d-ch2", Scope: "plan_node:chapter-plan-2", Text: "第二章加一场雨", Status: domain.DirectiveActive},
			{ID: "d-later", Scope: "from_chapter:3", Text: "第三章起换视角", Status: domain.DirectiveActive},
			{ID: "d-old", Scope: domain.DirectiveScopeProject, Text: "已退役", Status: domain.DirectiveRetired},
		},
	}
}

func directiveIDs(directives []domain.Directive) []string {
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
		item workItem
		want []string
	}{
		{"write chapter 1", workItem{kind: workWriteChapter, number: 1, plan: project.Plan[2]}, []string{"d-all", "d-arc"}},
		{"rewrite chapter 2", workItem{kind: workRewrite, number: 2, plan: project.Plan[3], chapterID: "chapter-2"}, []string{"d-all", "d-arc", "d-ch2"}},
		{"review union", workItem{kind: workReview, chapters: []string{"chapter-1", "chapter-2"}}, []string{"d-all", "d-arc", "d-ch2"}},
		{"extend takes all active", workItem{kind: workExtendPlan, covered: 2}, []string{"d-all", "d-arc", "d-ch2", "d-later"}},
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

func TestItemOperationCommandCarriesDirectivesAndRewriteBase(t *testing.T) {
	project := directiveTestProject()
	run := domain.CreationRun{ID: "run-1", ProjectID: "book-1", Goal: domain.CreationRunGoal{Premise: "故事", TargetChapters: 2}}
	base := runBaseCommand(run, QuickWriteCommand{})
	at := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)

	item := workItem{kind: workRewrite, number: 2, plan: project.Plan[3], chapterID: "chapter-2", revision: 7, notes: []string{"节奏太慢"}}
	item.directives = coveringDirectives(project, item)
	command, err := itemOperationCommand(base, run, item, at)
	if err != nil {
		t.Fatalf("rewrite command: %v", err)
	}
	var rewrite struct {
		ChapterID     string             `json:"chapter_id"`
		ChapterPlanID string             `json:"chapter_plan_id"`
		ChapterNumber int                `json:"chapter_number"`
		BaseRevision  domain.Revision    `json:"base_revision"`
		Directives    []domain.Directive `json:"directives"`
	}
	if err := json.Unmarshal(command.Input, &rewrite); err != nil {
		t.Fatalf("decode rewrite input: %v", err)
	}
	if rewrite.ChapterID != "chapter-2" || rewrite.ChapterPlanID != "chapter-plan-2" || rewrite.ChapterNumber != 2 || rewrite.BaseRevision != 7 {
		t.Fatalf("rewrite input = %+v", rewrite)
	}
	if got := directiveIDs(rewrite.Directives); len(got) != 3 || got[2] != "d-ch2" {
		t.Fatalf("rewrite directives = %v", got)
	}

	review := workItem{kind: workReview, chapters: []string{"chapter-1"}, revision: 7}
	review.directives = coveringDirectives(project, review)
	command, err = itemOperationCommand(base, run, review, at)
	if err != nil {
		t.Fatalf("review command: %v", err)
	}
	var reviewInput struct {
		Directives []domain.Directive `json:"directives"`
	}
	if err := json.Unmarshal(command.Input, &reviewInput); err != nil {
		t.Fatalf("decode review input: %v", err)
	}
	if got := directiveIDs(reviewInput.Directives); len(got) != 2 || got[0] != "d-all" || got[1] != "d-arc" {
		t.Fatalf("review directives = %v", got)
	}

	plain := workItem{kind: workWriteChapter, number: 1, plan: project.Plan[2]}
	command, err = itemOperationCommand(base, run, plain, at)
	if err != nil {
		t.Fatalf("write command: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(command.Input, &raw); err != nil {
		t.Fatalf("decode write input: %v", err)
	}
	if _, present := raw["directives"]; present {
		t.Fatalf("write input without directives must omit the field: %s", command.Input)
	}
}
