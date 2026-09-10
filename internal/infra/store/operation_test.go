package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestOperationQueueWorkspaceAndEvents(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	createdAt := operationTime()
	low := testOperation("low", 1, createdAt)
	high := testOperation("high", 10, createdAt.Add(time.Second))
	if _, err := s.CreateOperation(ctx, low); err != nil {
		t.Fatalf("create low: %v", err)
	}
	if _, err := s.CreateOperation(ctx, high); err != nil {
		t.Fatalf("create high: %v", err)
	}

	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, createdAt.Add(2*time.Second))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != "high" || claimed.State != model.OperationRunning || claimed.LeaseOwner != "worker-1" {
		t.Fatalf("claimed = %#v", claimed)
	}

	artifact, err := s.PutWorkspaceArtifact(ctx, model.WorkspaceArtifact{
		OperationID: claimed.ID,
		Key:         "chapter/chapter-1",
		MediaType:   "text/markdown",
		Content:     []byte("第一版正文"),
		UpdatedAt:   createdAt.Add(3 * time.Second),
	}, 0, claimed.Attempt)
	if err != nil {
		t.Fatalf("put first artifact: %v", err)
	}
	if artifact.Version != 1 || artifact.Digest == "" {
		t.Fatalf("first artifact = %#v", artifact)
	}
	artifact.Content = []byte("第二版正文")
	artifact.UpdatedAt = createdAt.Add(4 * time.Second)
	artifact, err = s.PutWorkspaceArtifact(ctx, artifact, 1, claimed.Attempt)
	if err != nil {
		t.Fatalf("put second artifact: %v", err)
	}
	if artifact.Version != 2 {
		t.Fatalf("artifact version = %d, want 2", artifact.Version)
	}

	stale := artifact
	stale.Content = []byte("过期写入")
	stale.UpdatedAt = createdAt.Add(5 * time.Second)
	if _, err := s.PutWorkspaceArtifact(ctx, stale, 1, claimed.Attempt); !errors.Is(err, model.ErrWorkspaceConflict) {
		t.Fatalf("stale artifact error = %v, want model.ErrWorkspaceConflict", err)
	}
	stored, err := s.GetWorkspaceArtifact(ctx, claimed.ID, artifact.Key)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	if stored.Version != 2 || string(stored.Content) != "第二版正文" {
		t.Fatalf("stored artifact = version %d, content %q", stored.Version, stored.Content)
	}
	artifacts, err := s.ListWorkspaceArtifacts(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Key != artifact.Key || artifacts[0].Version != 2 {
		t.Fatalf("artifacts = %#v", artifacts)
	}

	completed, err := s.TransitionOperation(ctx, claimed.ID, model.OperationRunning, model.OperationSucceeded, "", createdAt.Add(6*time.Second))
	if err != nil {
		t.Fatalf("complete operation: %v", err)
	}
	if completed.State != model.OperationSucceeded || completed.LeaseUntil != nil {
		t.Fatalf("completed = %#v", completed)
	}
	if _, err := s.PutWorkspaceArtifact(ctx, model.WorkspaceArtifact{
		OperationID: claimed.ID, Key: "late", MediaType: "text/plain", Content: []byte("late"), UpdatedAt: createdAt.Add(7 * time.Second),
	}, 0, claimed.Attempt); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("terminal artifact error = %v, want model.ErrStateConflict", err)
	}

	events, err := s.ListOperationEvents(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4: %#v", len(events), events)
	}
	for i, event := range events {
		if event.Sequence != int64(i+1) {
			t.Fatalf("event %d sequence = %d", i, event.Sequence)
		}
	}
}

func TestExpiredLeaseIsExplicitlyRecovered(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	operation := testOperation("write-1", 1, now)
	if _, err := s.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Second)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.RenewOperationLease(ctx, operation.ID, "worker-1", 1, time.Minute, now.Add(2*time.Minute)); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("expired heartbeat error = %v, want model.ErrStateConflict", err)
	}

	recovered, err := s.RecoverExpiredOperations(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(recovered) != 1 || recovered[0] != operation.ID {
		t.Fatalf("recovered = %v", recovered)
	}
	stored, err := s.GetOperation(ctx, operation.ID)
	if err != nil {
		t.Fatalf("get recovered: %v", err)
	}
	if stored.State != model.OperationQueued || stored.Error == "" || stored.LeaseOwner != "" || stored.LeaseUntil != nil {
		t.Fatalf("recovered operation = %#v", stored)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-2", time.Minute, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if claimed.ID != operation.ID || claimed.LeaseOwner != "worker-2" {
		t.Fatalf("reclaimed = %#v", claimed)
	}
}

func TestCreateOperationAndEventAreIdempotent(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	operation := testOperation("write-1", 1, now)
	first, err := s.CreateOperation(ctx, operation)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := s.CreateOperation(ctx, operation)
	if err != nil {
		t.Fatalf("create idempotently: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("operation ids = %q and %q", first.ID, second.ID)
	}
	operation.Priority = 2
	if _, err := s.CreateOperation(ctx, operation); !errors.Is(err, model.ErrIdempotencyConflict) {
		t.Fatalf("changed operation error = %v, want model.ErrIdempotencyConflict", err)
	}

	payload := json.RawMessage(`{"status":"ok"}`)
	event := model.OperationEvent{
		OperationID: first.ID, StepID: "prepare", Attempt: 1, IdempotencyKey: "prepare:1",
		Kind: "step.completed", Payload: payload, CreatedAt: now.Add(time.Second),
	}
	stored, err := s.AppendOperationEvent(ctx, event)
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	repeated, err := s.AppendOperationEvent(ctx, event)
	if err != nil {
		t.Fatalf("append event idempotently: %v", err)
	}
	if stored.Sequence != repeated.Sequence {
		t.Fatalf("event sequences = %d and %d", stored.Sequence, repeated.Sequence)
	}
	event.Kind = "different"
	if _, err := s.AppendOperationEvent(ctx, event); !errors.Is(err, model.ErrIdempotencyConflict) {
		t.Fatalf("changed event error = %v, want model.ErrIdempotencyConflict", err)
	}
}

func TestConcurrentClaimHasSingleWinner(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("write-1", 1, now)); err != nil {
		t.Fatalf("create: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, worker := range []string{"worker-1", "worker-2"} {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.ClaimNextOperation(ctx, worker, time.Minute, now.Add(time.Second))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var succeeded, empty int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, model.ErrNotFound):
			empty++
		default:
			t.Fatalf("claim error: %v", err)
		}
	}
	if succeeded != 1 || empty != 1 {
		t.Fatalf("succeeded=%d empty=%d, want 1/1", succeeded, empty)
	}
}

func TestOperationDependenciesGateQueueClaims(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	parent := testOperation("develop-plan", 1, now)
	child := testOperation("write-chapter", 100, now.Add(time.Second))
	child.DependsOn = []string{parent.ID}
	if _, err := s.CreateOperation(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := s.CreateOperation(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("claim parent: %v", err)
	}
	if claimed.ID != parent.ID {
		t.Fatalf("claimed %q, want dependency %q", claimed.ID, parent.ID)
	}
	if _, err := s.TransitionOperation(ctx, parent.ID, model.OperationRunning, model.OperationSucceeded, "", now.Add(3*time.Second)); err != nil {
		t.Fatalf("complete parent: %v", err)
	}
	claimed, err = s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("claim child: %v", err)
	}
	if claimed.ID != child.ID || len(claimed.DependsOn) != 1 || claimed.DependsOn[0] != parent.ID {
		t.Fatalf("claimed child = %#v", claimed)
	}
}

func TestCreateOperationRejectsMissingDependencyAtomically(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	operation := testOperation("write-chapter", 1, operationTime())
	operation.DependsOn = []string{"missing-plan"}
	if _, err := s.CreateOperation(ctx, operation); err == nil {
		t.Fatal("create operation succeeded with missing dependency")
	}
	if _, err := s.GetOperation(ctx, operation.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("partially created operation error = %v, want model.ErrNotFound", err)
	}
}

func TestClaimNextOperationFiltersByExecutorIdentity(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	matching := testOperation("matching", 1, now)
	other := testOperation("other-executor", 100, now)
	other.Snapshot.Executor = "external.test@1"
	if _, err := s.CreateOperation(ctx, matching); err != nil {
		t.Fatalf("create matching operation: %v", err)
	}
	if _, err := s.CreateOperation(ctx, other); err != nil {
		t.Fatalf("create other operation: %v", err)
	}
	claimed, err := s.ClaimNextOperationForExecutor(ctx, "worker-1", testExecutor, time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim matching operation: %v", err)
	}
	if claimed.ID != matching.ID {
		t.Fatalf("claimed %q, want %q", claimed.ID, matching.ID)
	}
}

func TestClaimOperationByIDAndReprioritize(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	low := testOperation("low", 1, now)
	high := testOperation("high", 10, now.Add(time.Second))
	if _, err := s.CreateOperation(ctx, low); err != nil {
		t.Fatalf("create low: %v", err)
	}
	if _, err := s.CreateOperation(ctx, high); err != nil {
		t.Fatalf("create high: %v", err)
	}
	updated, err := s.SetOperationPriority(ctx, low.ID, 20, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("reprioritize low: %v", err)
	}
	if updated.Priority != 20 {
		t.Fatalf("priority = %d, want 20", updated.Priority)
	}
	claimed, err := s.ClaimOperationForExecutor(ctx, high.ID, "worker-1", testExecutor, time.Minute, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("claim exact operation: %v", err)
	}
	if claimed.ID != high.ID {
		t.Fatalf("claimed %q, want %q", claimed.ID, high.ID)
	}
	if _, err := s.ClaimOperationForExecutor(ctx, high.ID, "worker-2", testExecutor, time.Minute, now.Add(4*time.Second)); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("second exact claim error = %v, want model.ErrStateConflict", err)
	}
}

func TestExpiredLeaseFailsOperationAtAttemptLimit(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	operation := testOperation("write-1", 1, now)
	if _, err := s.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 每一轮都是"领取 → lease 过期 → 回收"，模拟模型调用持续超时。
	for attempt := 1; attempt <= MaxOperationAttempts; attempt++ {
		claimAt := now.Add(time.Duration(attempt) * 10 * time.Minute)
		claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, claimAt)
		if err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
		if claimed.Attempt != attempt {
			t.Fatalf("attempt counter = %d, want %d", claimed.Attempt, attempt)
		}
		if _, err := s.RecoverExpiredOperations(ctx, claimAt.Add(2*time.Minute)); err != nil {
			t.Fatalf("recover attempt %d: %v", attempt, err)
		}
		stored, err := s.GetOperation(ctx, operation.ID)
		if err != nil {
			t.Fatalf("get after attempt %d: %v", attempt, err)
		}
		want := model.OperationQueued
		if attempt >= MaxOperationAttempts {
			want = model.OperationFailed
		}
		if stored.State != want {
			t.Fatalf("state after attempt %d = %s, want %s", attempt, stored.State, want)
		}
	}

	stored, err := s.GetOperation(ctx, operation.ID)
	if err != nil {
		t.Fatalf("get final: %v", err)
	}
	if !strings.Contains(stored.Error, "limit") {
		t.Fatalf("failure reason %q does not explain the attempt limit", stored.Error)
	}
	if _, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Hour)); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("claim after limit = %v, want model.ErrNotFound", err)
	}
}

func TestClaimReportsDependenciesThatCannotSucceed(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	parent := testOperation("develop-plan", 1, now)
	child := testOperation("write-chapter", 100, now.Add(time.Second))
	child.DependsOn = []string{parent.ID}
	if _, err := s.CreateOperation(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := s.CreateOperation(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(2*time.Second)); err != nil {
		t.Fatalf("claim parent: %v", err)
	}
	if _, err := s.TransitionOperation(
		ctx, parent.ID, model.OperationRunning, model.OperationFailed, "模型返回硬错误", now.Add(3*time.Second),
	); err != nil {
		t.Fatalf("fail parent: %v", err)
	}

	// 关键：后继被永久挡住时必须显式报错，不能与"队列已空"共用 model.ErrNotFound——
	// 否则一次失败会让整条后续队列静默停摆，看上去与全部完成没有区别。
	_, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(4*time.Second))
	if !errors.Is(err, model.ErrDependencyBlocked) {
		t.Fatalf("claim next = %v, want model.ErrDependencyBlocked", err)
	}
	if !strings.Contains(err.Error(), child.ID) || !strings.Contains(err.Error(), parent.ID) {
		t.Fatalf("blocked error %q must name both the blocked operation and its dependency", err)
	}
	if _, err := s.ClaimOperationForExecutor(
		ctx, child.ID, "worker-1", testExecutor, time.Minute, now.Add(5*time.Second),
	); !errors.Is(err, model.ErrDependencyBlocked) {
		t.Fatalf("claim child by id = %v, want model.ErrDependencyBlocked", err)
	}

	// 用户重排失败的依赖后不得继续误报。
	if _, err := s.TransitionOperation(
		ctx, parent.ID, model.OperationFailed, model.OperationQueued, "", now.Add(6*time.Second),
	); err != nil {
		t.Fatalf("requeue parent: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(7*time.Second))
	if err != nil {
		t.Fatalf("claim after requeue: %v", err)
	}
	if claimed.ID != parent.ID {
		t.Fatalf("claimed %q, want requeued dependency %q", claimed.ID, parent.ID)
	}
}

const testExecutor = "llm.agent@1/model"

func testOperation(id string, priority int, createdAt time.Time) model.Operation {
	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	return model.Operation{
		ID: id, Kind: model.OperationWriteChapter, RunID: "run:book-1",
		Target:   model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"},
		Priority: priority, State: model.OperationQueued,
		Snapshot: model.ExecutionSnapshot{
			Executor: testExecutor, BaseRevision: 1, InputDigest: model.Digest(input),
			ConfigDigest: "execution-profile", ApprovalPolicy: model.ApprovalManual,
		},
		Input: input, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

// openOperationStore 打开测试库并为 book-1 建好归属 Run：每种 Operation 都必须归属唯一 CreationRun（D27）。
func openOperationStore(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	now := operationTime()
	if _, err := s.CreateCreationRun(context.Background(), model.CreationRun{
		ID: "run:book-1", ProjectID: "book-1",
		Goal:     model.NovelGoal{Premise: "测试创作", TargetChapters: 3}.Goal(),
		Strategy: model.CreationRunStrategy{PlanWindowChapters: 3, ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: 3},
		Preset:   model.CreationRunPreset{Source: "test", Digest: "test-preset", Approval: model.ApprovalAuto},
		State:    model.RunRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create test run: %v", err)
	}
	return s
}

func operationTime() time.Time {
	return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
}

// TestSupersededAttemptCannotWriteOrConclude 守护执行归属不变量：租约过期被接替后，
// 旧执行实例既不能再写工作区，也不能把后继尝试的状态改成自己的结局；
// 用户取消先于收尾时，收尾必须被拒绝而不是覆盖取消。
func TestSupersededAttemptCannotWriteOrConclude(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	start := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("op", 1, start)); err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, start)
	if err != nil || first.Attempt != 1 {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if _, err := s.RecoverExpiredOperations(ctx, start.Add(2*time.Minute)); err != nil {
		t.Fatalf("recover: %v", err)
	}
	second, err := s.ClaimNextOperation(ctx, "worker-2", time.Minute, start.Add(3*time.Minute))
	if err != nil || second.Attempt != 2 || second.LeaseOwner != "worker-2" {
		t.Fatalf("second claim = %#v, %v", second, err)
	}

	late := model.WorkspaceArtifact{
		OperationID: "op", Key: "chapter/late", MediaType: "text/plain",
		Content: []byte("旧实例的迟到写入"), UpdatedAt: start.Add(4 * time.Minute),
	}
	if _, err := s.PutWorkspaceArtifact(ctx, late, 0, first.Attempt); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("stale workspace write error = %v, want model.ErrStateConflict", err)
	}
	if err := s.AssertActiveAttempt(ctx, "op", first.Attempt); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("stale attempt assertion error = %v, want model.ErrStateConflict", err)
	}
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{{
		ID: "op/late", ProjectID: "book-1", Digest: "unused", MediaType: "text/plain",
		OperationID: "op", Attempt: first.Attempt, CreatedAt: start,
	}}, "op", first.Attempt); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("stale artifact save error = %v, want model.ErrStateConflict", err)
	}
	if _, err := s.ConcludeOperation(ctx, "op", first.Attempt, model.OperationFailed, "stale worker", start.Add(4*time.Minute)); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("stale conclude error = %v, want model.ErrStateConflict", err)
	}
	if _, err := s.FailOperation(ctx, "op", first.Attempt, model.FailureResultUnknown, "stale worker", start.Add(4*time.Minute)); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("stale coded failure error = %v, want model.ErrStateConflict", err)
	}
	if err := s.AssertActiveAttempt(ctx, "op", second.Attempt); err != nil {
		t.Fatalf("active attempt must pass: %v", err)
	}
	if _, err := s.PutWorkspaceArtifact(ctx, late, 0, second.Attempt); err != nil {
		t.Fatalf("active workspace write: %v", err)
	}
	concluded, err := s.ConcludeOperation(ctx, "op", second.Attempt, model.OperationSucceeded, "", start.Add(5*time.Minute))
	if err != nil || concluded.State != model.OperationSucceeded {
		t.Fatalf("active conclude = %#v, %v", concluded, err)
	}

	if _, err := s.CreateOperation(ctx, testOperation("cancelled", 1, start)); err != nil {
		t.Fatalf("create cancelled: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-3", time.Minute, start.Add(6*time.Minute))
	if err != nil || claimed.ID != "cancelled" {
		t.Fatalf("claim cancelled = %#v, %v", claimed, err)
	}
	if _, err := s.TransitionOperation(ctx, claimed.ID, model.OperationRunning, model.OperationCancelled, "cancelled by user", start.Add(7*time.Minute)); err != nil {
		t.Fatalf("user cancel: %v", err)
	}
	if _, err := s.ConcludeOperation(ctx, claimed.ID, claimed.Attempt, model.OperationSucceeded, "", start.Add(8*time.Minute)); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("conclude after cancel error = %v, want model.ErrStateConflict", err)
	}
}

// TestFailOperationRecordsCodeUntilNextTransition 守护 D46：失败码随失败落盘，
// 用户重排后清空——结果未知只标记一次，不在后继状态上残留。
func TestFailOperationRecordsCodeUntilNextTransition(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	start := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("op", 1, start)); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, start)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	failed, err := s.FailOperation(ctx, "op", claimed.Attempt, model.FailureResultUnknown, "result unknown", start.Add(time.Minute))
	if err != nil || failed.State != model.OperationFailed || failed.FailureCode != model.FailureResultUnknown || failed.Error != "result unknown" {
		t.Fatalf("failed = %#v, %v", failed, err)
	}
	if stored, err := s.GetOperation(ctx, "op"); err != nil || stored.FailureCode != model.FailureResultUnknown {
		t.Fatalf("stored = %#v, %v", stored, err)
	}
	requeued, err := s.TransitionOperation(ctx, "op", model.OperationFailed, model.OperationQueued, "resumed by user", start.Add(2*time.Minute))
	if err != nil || requeued.State != model.OperationQueued || requeued.FailureCode != "" {
		t.Fatalf("requeued = %#v, %v", requeued, err)
	}
	if _, err := s.FailOperation(ctx, "op", 0, model.FailureResultUnknown, "no attempt", start.Add(3*time.Minute)); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("attempt-less failure err = %v", err)
	}
}

func TestClaimNextOperationAcrossExecutorsPreservesPriority(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	for _, spec := range []struct {
		id, executor string
		priority     int
	}{
		{"llm", "llm@1", 1}, {"external", "external@1", 10}, {"unconfigured", "other@1", 100},
	} {
		operation := testOperation(spec.id, spec.priority, now)
		operation.Snapshot.Executor = spec.executor
		if _, err := s.CreateOperation(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"external", "llm"} {
		operation, err := s.ClaimNextOperationForExecutors(ctx, "worker", []string{"llm@1", "external@1"}, time.Minute, now.Add(time.Second))
		if err != nil || operation.ID != want {
			t.Fatalf("claim=%s err=%v want=%s", operation.ID, err, want)
		}
	}
	if _, err := s.ClaimNextOperationForExecutors(ctx, "worker", []string{"llm@1", "external@1"}, time.Minute, now.Add(time.Second)); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("unconfigured executor must remain unclaimed: %v", err)
	}
}
