package creation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// interruptedTasks 在执行途中取消上下文并把任务交回队列，模拟进程退出时引擎的释放。
type interruptedTasks struct {
	creation.Tasks
	cancel context.CancelFunc
}

func (f *interruptedTasks) Start(_ context.Context, _ string, work creation.WorkItem, _ time.Time) (model.Operation, error) {
	return model.Operation{ID: work.ID, State: model.OperationQueued}, nil
}

func (f *interruptedTasks) Run(_ context.Context, id, _ string, _ time.Duration, _ time.Time) (model.Operation, error) {
	f.cancel()
	return model.Operation{ID: id, State: model.OperationQueued, Attempt: 1, Error: "进程退出"}, context.Canceled
}

// 执行被取消中断时 Run 不落 failed，保持 running 等下次进入续跑。
func TestDriverLeavesRunResumableWhenExecutionIsInterrupted(t *testing.T) {
	st, run := fixture(t)
	goal := goalFunc(func(context.Context, model.CreationRun) (creation.Decision, error) {
		return creation.Decision{Revision: 1, Step: creation.Step{Work: &creation.WorkItem{ID: "write"}}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	driver := creation.New(st, map[model.GoalKind]creation.Goal{run.Goal.Kind: goal}, time.Now)
	outcome, err := driver.Drive(ctx, run, &interruptedTasks{cancel: cancel}, creation.DriveCommand{})
	if !errors.Is(err, context.Canceled) || outcome.Run.State != model.RunRunning {
		t.Fatalf("interrupted drive: outcome=%+v err=%v", outcome, err)
	}
	stored, err := st.GetCreationRun(context.Background(), run.ID)
	if err != nil || stored.State != model.RunRunning {
		t.Fatalf("run must stay resumable: %+v err=%v", stored, err)
	}
}
