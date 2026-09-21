package operation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// blockingExecutor 取消上下文后等它生效再返回，模拟执行途中进程退出。
type blockingExecutor struct{ cancel context.CancelFunc }

func (blockingExecutor) Identity() string { return testExecutor }

func (e blockingExecutor) Execute(ctx context.Context, _ model.Operation) (model.OperationOutcome, error) {
	e.cancel()
	<-ctx.Done()
	return model.OperationOutcome{}, ctx.Err()
}

// 取消不是失败：任务放回队列、租约释放、attempt 保留为围栏，立刻可再领；不计失败次数。
func TestCancelledExecutionReleasesOperationForResume(t *testing.T) {
	background := context.Background()
	authorityStore, err := store.Open(background, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	now := time.Now().UTC()
	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	operation := model.Operation{
		ID: "interrupted", Kind: model.OperationWriteChapter,
		Target:   model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"},
		State:    model.OperationQueued,
		RunID:    createEngineTestRun(t, background, authorityStore, "book-1", now),
		Snapshot: engineSnapshot(input, 1, model.ApprovalManual),
		Input:    input, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(background, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	claimed, err := authorityStore.ClaimNextOperationForExecutor(background, "worker-1", testExecutor, time.Minute, now)
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	ctx, cancel := context.WithCancel(background)
	defer cancel()
	_, err = NewEngine(authorityStore, change.New(authorityStore)).runClaimed(ctx, blockingExecutor{cancel: cancel}, claimed, "worker-1", time.Minute, now)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run error = %v, want context.Canceled", err)
	}
	stored, err := authorityStore.GetOperation(background, operation.ID)
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if stored.State != model.OperationQueued || stored.Attempt != 1 || stored.LeaseOwner != "" || stored.LeaseUntil != nil || stored.Error != releasedMessage {
		t.Fatalf("operation was not released: %+v", stored)
	}
	again, err := authorityStore.ClaimNextOperationForExecutor(background, "worker-1", testExecutor, time.Minute, now.Add(time.Second))
	if err != nil || again.Attempt != 2 {
		t.Fatalf("re-claim after release: %+v err=%v", again, err)
	}
	if failures, err := authorityStore.CountOperationFailures(background, operation.ID); err != nil || failures != 0 {
		t.Fatalf("release counted as failure: failures=%d err=%v", failures, err)
	}
}
