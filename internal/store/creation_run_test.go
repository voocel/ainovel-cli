package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestCreationRunSingleActivePerProject(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	first := domain.CreationRun{
		ID: "run:book:1", ProjectID: "book",
		Goal:     domain.CreationRunGoal{Premise: "一句话", TargetChapters: 3},
		Strategy: testRunStrategy(), Preset: testRunPreset(), State: domain.RunRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.CreateCreationRun(ctx, first); err != nil {
		t.Fatalf("create first run: %v", err)
	}

	second := first
	second.ID = "run:book:2"
	if _, err := s.CreateCreationRun(ctx, second); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("second active run must hit the storage invariant, got %v", err)
	}

	if replayed, err := s.CreateCreationRun(ctx, first); err != nil || replayed.ID != first.ID {
		t.Fatalf("idempotent replay = %#v, %v", replayed, err)
	}
	changed := first
	changed.Goal.TargetChapters = 9
	if _, err := s.CreateCreationRun(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same id with different goal must conflict, got %v", err)
	}

	waiting, err := s.TransitionCreationRun(ctx, first.ID, domain.RunRunning, domain.RunWaitingUser, "等待确认", 0, now.Add(time.Minute))
	if err != nil || waiting.State != domain.RunWaitingUser {
		t.Fatalf("transition to waiting = %#v, %v", waiting, err)
	}
	if active, err := s.ActiveCreationRun(ctx, "book"); err != nil || active.ID != first.ID {
		t.Fatalf("waiting run must stay active = %#v, %v", active, err)
	}
	if _, err := s.TransitionCreationRun(ctx, first.ID, domain.RunWaitingUser, domain.RunCompleted, "完成", 5, now.Add(2*time.Minute)); err == nil {
		t.Fatal("waiting cannot complete directly")
	}
	if _, err := s.TransitionCreationRun(ctx, first.ID, domain.RunWaitingUser, domain.RunRunning, "继续", 0, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if _, err := s.TransitionCreationRun(ctx, first.ID, domain.RunRunning, domain.RunCompleted, "完成", 0, now.Add(3*time.Minute)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("completion must bind a revision, got %v", err)
	}
	completed, err := s.TransitionCreationRun(ctx, first.ID, domain.RunRunning, domain.RunCompleted, "完成", 5, now.Add(3*time.Minute))
	if err != nil || completed.CompletedRevision != 5 {
		t.Fatalf("complete run = %#v, %v", completed, err)
	}
	if _, err := s.TransitionCreationRun(ctx, first.ID, domain.RunCompleted, domain.RunRunning, "重启", 0, now.Add(4*time.Minute)); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("terminal run must not transition, got %v", err)
	}
	if _, err := s.UpdateCreationRunStrategy(ctx, first.ID, domain.CreationRunStrategy{
		PlanWindowChapters: 5, ReviewCadence: domain.ReviewPerPlanWindow, AutoRepairBudget: 3,
	}, now.Add(4*time.Minute)); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("terminal run must not change strategy, got %v", err)
	}

	if _, err := s.ActiveCreationRun(ctx, "book"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no active run expected, got %v", err)
	}
	if _, err := s.CreateCreationRun(ctx, second); err != nil {
		t.Fatalf("new run after terminal state: %v", err)
	}
	if count, err := s.CountCreationRuns(ctx, "book"); err != nil || count != 2 {
		t.Fatalf("count = %d, %v", count, err)
	}
}

func TestCreateOperationBindsRunLineageTransactionally(t *testing.T) {
	// §6.3：内容类 Operation 原生归属 CreationRun——run_id 与创建时的策略版本
	// 落在 Operation 行上，血统事件与创建同事务，重入不重复计入。
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	run := domain.CreationRun{
		ID: "run:lineage:1", ProjectID: "lineage",
		Goal:     domain.CreationRunGoal{Premise: "一句话", TargetChapters: 3},
		Strategy: testRunStrategy(), Preset: testRunPreset(), State: domain.RunRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.CreateCreationRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	// 目标更新推进策略版本：created(1) + goal_updated(2)。
	if _, err := s.UpdateCreationRunGoal(ctx, run.ID,
		domain.CreationRunGoal{Premise: "一句话", TargetChapters: 5}, now.Add(time.Minute)); err != nil {
		t.Fatalf("update goal: %v", err)
	}
	strategy := run.Strategy
	strategy.PlanWindowChapters = 5
	if _, err := s.UpdateCreationRunStrategy(ctx, run.ID, strategy, now.Add(90*time.Second)); err != nil {
		t.Fatalf("update strategy: %v", err)
	}
	operation := domain.Operation{
		ID: "op:lineage:plan", Kind: domain.OperationDevelopPlan,
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "lineage"},
		State:  domain.OperationQueued, RunID: run.ID,
		Snapshot: domain.ExecutionSnapshot{
			ExecutionProfileDigest: "profile", BaseRevision: 1, CoreProtocolVersion: "core-v1",
			WorkerProfileVersion: "architect.design@1", ToolSchemaDigest: "tools", PromptDigest: "prompt",
			ModelConfigDigest: "model", ApprovalPolicy: domain.ApprovalAuto, ApprovalPolicyDigest: "auto",
		},
		Input: []byte(`{"intent":"一句话"}`), CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Minute),
	}
	created, err := s.CreateOperation(ctx, operation)
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if created.RunID != run.ID || created.RunPolicyVersion != 3 {
		t.Fatalf("created binding = %q@%d, want %q@3", created.RunID, created.RunPolicyVersion, run.ID)
	}
	stored, err := s.GetOperation(ctx, operation.ID)
	if err != nil || stored.RunID != run.ID || stored.RunPolicyVersion != 3 {
		t.Fatalf("stored binding = %#v, %v", stored, err)
	}
	// 幂等重入：不重复创建、不重复追加血统事件。
	if _, err := s.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("idempotent create: %v", err)
	}
	events, err := s.ListCreationRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	operationEvents := 0
	for _, event := range events {
		if event.Kind == domain.RunEventOperationCreated {
			operationEvents++
		}
	}
	if len(events) != 4 || operationEvents != 1 {
		t.Fatalf("events = %#v, want created+goal_updated+strategy_updated+operation_created", events)
	}

	missingRun := operation
	missingRun.ID, missingRun.RunID = "op:missing-run", ""
	if _, err := s.CreateOperation(ctx, missingRun); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("content operation without run = %v, want domain.ErrInvalid", err)
	}
	mismatched := operation
	mismatched.ID = "op:wrong-project"
	mismatched.Target.ID = "other-project"
	if _, err := s.CreateOperation(ctx, mismatched); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("operation with mismatched run project = %v, want domain.ErrInvalid", err)
	}
	if _, err := s.TransitionCreationRun(
		ctx, run.ID, domain.RunRunning, domain.RunCancelled, "取消", 0, now.Add(3*time.Minute),
	); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if replayed, err := s.CreateOperation(ctx, operation); err != nil || replayed.ID != operation.ID {
		t.Fatalf("terminal run must still allow idempotent operation replay: %#v, %v", replayed, err)
	}
	terminal := operation
	terminal.ID = "op:terminal-run"
	if _, err := s.CreateOperation(ctx, terminal); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("operation on terminal run = %v, want ErrStateConflict", err)
	}
}

func testRunStrategy() domain.CreationRunStrategy {
	return domain.CreationRunStrategy{
		PlanWindowChapters: 3, ReviewCadence: domain.ReviewPerPlanWindow, AutoRepairBudget: 3,
	}
}

func testRunPreset() domain.CreationRunPreset {
	return domain.CreationRunPreset{Source: "test", Digest: "test-preset", Approval: domain.ApprovalAuto}
}
