package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 主动释放（running → queued）不是无进展：租约过期上限只数过期次数，不数 attempt。
func TestReleasedAttemptsDoNotCountTowardLeaseExpiryLimit(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	operation := testOperation("write-1", 1, now)
	if _, err := s.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 1; i <= MaxLeaseExpiries; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, at)
		if err != nil || claimed.Attempt != i {
			t.Fatalf("claim %d: %+v err=%v", i, claimed, err)
		}
		released, err := s.ConcludeOperation(ctx, operation.ID, claimed.Attempt, model.OperationQueued, "进程退出", at.Add(time.Second))
		if err != nil || released.State != model.OperationQueued || released.LeaseOwner != "" || released.LeaseUntil != nil {
			t.Fatalf("release %d: %+v err=%v", i, released, err)
		}
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Hour))
	if err != nil || claimed.Attempt != MaxLeaseExpiries+1 {
		t.Fatalf("claim after releases: %+v err=%v", claimed, err)
	}
	if _, err := s.RecoverExpiredOperations(ctx, now.Add(time.Hour+2*time.Minute)); err != nil {
		t.Fatalf("recover: %v", err)
	}
	stored, err := s.GetOperation(ctx, operation.ID)
	if err != nil || stored.State != model.OperationQueued || !strings.Contains(stored.Error, "requeued") {
		t.Fatalf("first expiry must requeue regardless of attempt: %+v err=%v", stored, err)
	}
	if failures, err := s.CountOperationFailures(ctx, operation.ID); err != nil || failures != 0 {
		t.Fatalf("failures = %d err=%v, want 0", failures, err)
	}
}

// 失败次数来自事件日志：执行失败与过期判失败各计一次，释放与普通过期不计。
func TestCountOperationFailuresFollowsTheEventLog(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	operation := testOperation("write-1", 1, now)
	if _, err := s.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create: %v", err)
	}
	count := func(want int, step string) {
		t.Helper()
		if got, err := s.CountOperationFailures(ctx, operation.ID); err != nil || got != want {
			t.Fatalf("%s: failures = %d err=%v, want %d", step, got, err, want)
		}
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.FailOperation(ctx, operation.ID, claimed.Attempt, "", "boom", now.Add(time.Second)); err != nil {
		t.Fatalf("fail: %v", err)
	}
	count(1, "after execution failure")
	if _, err := s.TransitionOperation(ctx, operation.ID, model.OperationFailed, model.OperationQueued, "resume", now.Add(2*time.Second)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	claimed, err = s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("claim again: %v", err)
	}
	if _, err := s.ConcludeOperation(ctx, operation.ID, claimed.Attempt, model.OperationQueued, "进程退出", now.Add(4*time.Second)); err != nil {
		t.Fatalf("release: %v", err)
	}
	count(1, "after release")
	// 过期：前几次回收只是重排，第 MaxLeaseExpiries 次判失败才再计一次。
	for i := 1; i <= MaxLeaseExpiries; i++ {
		at := now.Add(time.Duration(i) * 10 * time.Minute)
		if _, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, at); err != nil {
			t.Fatalf("claim before expiry %d: %v", i, err)
		}
		if _, err := s.RecoverExpiredOperations(ctx, at.Add(2*time.Minute)); err != nil {
			t.Fatalf("recover %d: %v", i, err)
		}
		if i < MaxLeaseExpiries {
			count(1, "after plain expiry")
		}
	}
	stored, err := s.GetOperation(ctx, operation.ID)
	if err != nil || stored.State != model.OperationFailed {
		t.Fatalf("expiry limit must fail the operation: %+v err=%v", stored, err)
	}
	count(2, "after expiry limit")
}
