package service

import (
	"context"
	"encoding/json"
	"strings"
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
	run := domain.CreationRun{ID: "run-1", ProjectID: "book-1"}
	goal := domain.NovelGoal{Premise: "故事", TargetChapters: 2}

	item := novelItem{
		kind: workRewrite, number: 2, plan: project.Plan[3], chapterID: "chapter-2", revision: 7,
		notes: []string{"节奏太慢"}, basis: chapterBasis(project, 2, project.Plan[3].ID),
	}
	item.directives = coveringDirectives(project, item)
	work, err := item.work(run, goal)
	if err != nil {
		t.Fatalf("rewrite work: %v", err)
	}
	rewrite, ok := work.Input.(domain.RewriteChapterInput)
	if !ok || work.Kind != domain.OperationRewriteChapter || work.ID != "run-1:rewrite:chapter-2:r7" {
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
	reviewInput, ok := work.Input.(domain.ReviewRangeInput)
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

// stubDeriver 按脚本返回步骤，最后一步之后重复返回它；用来验证内核对各类步骤的落点。
type stubDeriver struct {
	steps []step
	calls int
}

func (stubDeriver) ValidateGoal(json.RawMessage) error { return nil }

func (d *stubDeriver) Next(ProjectSnapshot, domain.CreationRun, runEvidence) (step, error) {
	index := min(d.calls, len(d.steps)-1)
	d.calls++
	return d.steps[index], nil
}

func TestDriveCreationRunSettlesByStepKind(t *testing.T) {
	// D49：内核只按步骤种类落点——Done 绑定当前 Revision 完成，Wait 等用户，Fail 失败，
	// Work 驱动 Operation；同一槽位成功后再次出现即卡死判定，绝不无限重试。
	ctx := context.Background()
	planWork := func(runID string) *workItem {
		return &workItem{
			ID: runQuickID(runID, "plan"), Kind: domain.OperationDevelopPlan,
			Input:   domain.DevelopPlanInput{Intent: "故事", TargetChapters: 1, RequestedChapters: 1},
			Reasons: workReasons{Waiting: "等你确认", Failure: "没写完", Stuck: "规划已结束但没有推进"},
		}
	}
	cases := []struct {
		name    string
		steps   func(runID string) []step
		state   domain.CreationRunState
		reason  string
		wantErr bool
		calls   int
	}{
		{"done", func(string) []step { return []step{{Done: "全部完成"}} }, domain.RunCompleted, "全部完成", false, 0},
		{"wait", func(string) []step { return []step{{Wait: "等一下"}} }, domain.RunWaitingUser, "等一下", false, 0},
		{"fail", func(string) []step { return []step{{Fail: "坏了"}} }, domain.RunFailed, "坏了", true, 0},
		{"work then stuck", func(runID string) []step { return []step{{Work: planWork(runID)}} }, domain.RunFailed, "规划已结束但没有推进", true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor := &scriptedQuickExecutor{now: serviceTime()}
			api := newQuickTestService(t, executor)
			projectID := "stub-" + strings.ReplaceAll(tc.name, " ", "-")
			if _, err := api.CreateProject(ctx, CreateProjectCommand{
				ProjectID: projectID, ChangeID: "create", UserID: "user-1", Reason: "建书",
				Draft: ProjectDraft{Intent: domain.Intent{Premise: "故事", TargetChapters: 1}}, CreatedAt: serviceTime(),
			}); err != nil {
				t.Fatalf("create project: %v", err)
			}
			runID := "run:" + projectID + ":1"
			api.derivers["stub"] = &stubDeriver{steps: tc.steps(runID)}
			strategy := domain.CreationRunStrategy{PlanWindowChapters: 1, ReviewCadence: domain.ReviewPerPlanWindow, AutoRepairBudget: 1}
			preset, err := domain.NewCreationRunPreset("test", domain.ApprovalAuto, strategy)
			if err != nil {
				t.Fatalf("preset: %v", err)
			}
			run, err := api.StartCreationRun(ctx, StartCreationRunCommand{
				RunID: runID, ProjectID: projectID, Goal: domain.CreationRunGoal{Kind: "stub", Payload: json.RawMessage(`{}`)},
				Strategy: strategy, Preset: preset, CreatedAt: serviceTime(),
			})
			if err != nil {
				t.Fatalf("start run: %v", err)
			}
			outcome, err := api.driveCreationRun(ctx, run, QuickWriteCommand{
				ProjectID: projectID, WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
			})
			if (err != nil) != tc.wantErr || outcome.run.State != tc.state || outcome.run.StateReason != tc.reason || executor.calls != tc.calls {
				t.Fatalf("outcome = %#v, err = %v, executor calls = %d", outcome.run, err, executor.calls)
			}
			if tc.state == domain.RunCompleted && outcome.run.CompletedRevision != outcome.project.Revision {
				t.Fatalf("completed revision = %d, project revision = %d", outcome.run.CompletedRevision, outcome.project.Revision)
			}
		})
	}
}
