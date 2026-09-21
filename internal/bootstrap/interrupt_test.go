package bootstrap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	novelapp "github.com/voocel/ainovel-cli/internal/app/novel"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// interruptingExecutor 在第一次写章时取消上下文并等它生效，相当于用户在写章途中退出进程。
type interruptingExecutor struct {
	*scriptedQuickExecutor
	cancel      context.CancelFunc
	interrupted string
}

func (e *interruptingExecutor) Execute(ctx context.Context, operation model.Operation) (model.OperationOutcome, error) {
	if operation.Kind == model.OperationWriteChapter && e.interrupted == "" {
		e.interrupted = operation.ID
		e.cancel()
		<-ctx.Done()
		return model.OperationOutcome{}, ctx.Err()
	}
	return e.scriptedQuickExecutor.Execute(ctx, operation)
}

// 进程退出把在途任务放回队列：不是失败、不消耗预算、不留悬空租约；重新进入立刻接着写。
func TestQuickWriteInterruptedByShutdownResumesFromQueue(t *testing.T) {
	authorityStore := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := &interruptingExecutor{
		scriptedQuickExecutor: &scriptedQuickExecutor{now: testTime(), authorityStore: authorityStore},
		cancel:                cancel,
	}
	api := newTestAppWithExecutor(authorityStore, executor)
	api.now = testTime
	command := novelapp.QuickWriteCommand{
		ProjectID: "quick-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 3, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if !errors.Is(err, context.Canceled) || result.RunState != model.RunRunning {
		t.Fatalf("interrupted quick write: state=%s err=%v", result.RunState, err)
	}
	background := context.Background()
	released, err := authorityStore.GetOperation(background, executor.interrupted)
	if err != nil {
		t.Fatalf("read interrupted operation: %v", err)
	}
	if released.State != model.OperationQueued || released.Attempt != 1 || released.LeaseOwner != "" || released.Error == "" {
		t.Fatalf("interrupted operation must be released to the queue: %+v", released)
	}

	// 租约本来还有 1 分钟，重新进入也不该撞"被占用"：接着 attempt 2 写完全书。
	command.CreatedAt = testTime().Add(time.Second)
	result, err = api.Novels.QuickWrite(background, command)
	if err != nil || result.RunState != model.RunCompleted || len(result.Chapters) != 3 {
		t.Fatalf("resume after interruption: %+v err=%v", result, err)
	}
	resumed, err := authorityStore.GetOperation(background, executor.interrupted)
	if err != nil || resumed.State != model.OperationSucceeded || resumed.Attempt != 2 {
		t.Fatalf("resumed operation = %+v err=%v", resumed, err)
	}
	if failures, err := authorityStore.CountOperationFailures(background, executor.interrupted); err != nil || failures != 0 {
		t.Fatalf("an interruption must not count as a failure: failures=%d err=%v", failures, err)
	}
}
