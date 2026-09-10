package bootstrap_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	tasks "github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	runs "github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// stubDeriver 按脚本返回步骤，最后一步之后重复返回它；用来验证内核对各类步骤的落点。
type stubDeriver struct {
	store *store.Store
	steps []runs.Step
	calls int
}

func (stubDeriver) ValidateGoal(json.RawMessage) error { return nil }

func (d *stubDeriver) Next(ctx context.Context, run model.CreationRun) (runs.Decision, error) {
	index := min(d.calls, len(d.steps)-1)
	d.calls++
	revision, err := d.store.CurrentRevision(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: run.ProjectID})
	return runs.Decision{Revision: revision, Step: d.steps[index]}, err
}

func TestDriveCreationRunSettlesByStepKind(t *testing.T) {
	// D49：内核只按步骤种类落点——Done 绑定当前 Revision 完成，Wait 等用户，Fail 失败，
	// Work 驱动 Operation；同一槽位成功后再次出现即卡死判定，绝不无限重试。
	ctx := context.Background()
	planWork := func(runID string) *runs.WorkItem {
		return &runs.WorkItem{
			ID: runQuickID(runID, "plan"), Kind: model.OperationDevelopPlan,
			Input:   model.DevelopPlanInput{Intent: "故事", TargetChapters: 1, RequestedChapters: 1},
			Reasons: runs.WorkReasons{Waiting: "等你确认", Failure: "没写完", Stuck: "规划已结束但没有推进"},
		}
	}
	cases := []struct {
		name    string
		steps   func(runID string) []runs.Step
		state   model.CreationRunState
		reason  string
		wantErr bool
		calls   int
	}{
		{"done", func(string) []runs.Step { return []runs.Step{{Done: "全部完成"}} }, model.RunCompleted, "全部完成", false, 0},
		{"wait", func(string) []runs.Step { return []runs.Step{{Wait: "等一下"}} }, model.RunWaitingUser, "等一下", false, 0},
		{"fail", func(string) []runs.Step { return []runs.Step{{Fail: "坏了"}} }, model.RunFailed, "坏了", true, 0},
		{"work then stuck", func(runID string) []runs.Step { return []runs.Step{{Work: planWork(runID)}} }, model.RunFailed, "规划已结束但没有推进", true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor := &scriptedQuickExecutor{now: testTime()}
			api := newQuickTestApp(t, executor)
			projectID := "stub-" + strings.ReplaceAll(tc.name, " ", "-")
			if _, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
				ProjectID: projectID, ChangeID: "create", UserID: "user-1", Reason: "建书",
				Draft: projectdoc.ProjectDraft{Intent: model.Intent{Premise: "故事", TargetChapters: 1}}, CreatedAt: testTime(),
			}); err != nil {
				t.Fatalf("create project: %v", err)
			}
			runID := "run:" + projectID + ":1"
			api = newTestApp(api.store, bootstrap.Options{Executors: tasks.ExecutorSet{LLM: executor}, Now: testTime, Goals: map[model.GoalKind]runs.Goal{"stub": &stubDeriver{store: api.store, steps: tc.steps(runID)}}})
			strategy := model.CreationRunStrategy{PlanWindowChapters: 1, ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: 1}
			preset, err := model.NewCreationRunPreset("test", model.ApprovalAuto, strategy)
			if err != nil {
				t.Fatalf("preset: %v", err)
			}
			run, err := api.Runs.StartCreationRun(ctx, runs.StartCreationRunCommand{
				RunID: runID, ProjectID: projectID, Goal: model.CreationRunGoal{Kind: "stub", Payload: json.RawMessage(`{}`)},
				Strategy: strategy, Preset: preset, CreatedAt: testTime(),
			})
			if err != nil {
				t.Fatalf("start run: %v", err)
			}
			outcome, err := api.Runs.Drive(ctx, run, api.Tasks.ForCreation(tasks.StartOperationCommand{ProjectID: projectID, CoreProtocolVersion: "core-v1"}), runs.DriveCommand{
				WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
			})
			if (err != nil) != tc.wantErr || outcome.Run.State != tc.state || outcome.Run.StateReason != tc.reason || executor.calls != tc.calls {
				t.Fatalf("outcome = %#v, err = %v, executor calls = %d", outcome.Run, err, executor.calls)
			}
			if tc.state == model.RunCompleted && outcome.Run.CompletedRevision != outcome.Revision {
				t.Fatalf("completed revision = %d, project revision = %d", outcome.Run.CompletedRevision, outcome.Revision)
			}
		})
	}
}
