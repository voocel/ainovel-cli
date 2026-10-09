package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 创作团队的用量按尝试加总事件自带的用量：run_ended 载荷里的合计不重复计；进程中断的尝试
// 只到最后一条消息；崩溃后只做合规核对的尝试沿用任务的角色；别的书不混进来。
func TestProjectAttemptsSumPersistedUsage(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	start := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("plan", 1, start)); err != nil {
		t.Fatalf("create: %v", err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		at := start.Add(time.Duration(attempt-1) * 3 * time.Minute)
		if attempt > 1 {
			if _, err := s.RecoverExpiredOperations(ctx, at.Add(-time.Second)); err != nil {
				t.Fatalf("recover: %v", err)
			}
		}
		if claimed, err := s.ClaimNextOperation(ctx, "worker", time.Minute, at); err != nil || claimed.Attempt != attempt {
			t.Fatalf("claim %d = %#v, %v", attempt, claimed, err)
		}
	}
	other := testOperation("elsewhere", 1, start)
	other.RunID, other.Target.ID = "run:book-2", "book-2"
	if _, err := s.CreateCreationRun(ctx, model.CreationRun{
		ID: other.RunID, ProjectID: "book-2", Goal: model.NovelGoal{Premise: "别的书", TargetChapters: 3}.Goal(),
		Strategy: model.CreationRunStrategy{ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: 3},
		Preset:   model.CreationRunPreset{Source: "test", Digest: "test-preset", Approval: model.ApprovalAuto},
		State:    model.RunRunning, CreatedAt: start, UpdatedAt: start,
	}); err != nil {
		t.Fatalf("create other run: %v", err)
	}
	if _, err := s.CreateOperation(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	seq := 0
	event := func(operationID string, attempt int, kind, payload string, usage model.Usage, at time.Time) {
		t.Helper()
		seq++
		if _, err := s.AppendOperationEvent(ctx, model.OperationEvent{
			OperationID: operationID, StepID: kind, Attempt: attempt, IdempotencyKey: fmt.Sprint(kind, seq),
			Kind: kind, Payload: json.RawMessage(payload), Usage: usage, CreatedAt: at,
		}); err != nil {
			t.Fatalf("append %s: %v", kind, err)
		}
	}
	none := model.Usage{}
	event("plan", 1, "agent.run_started", `{"role":"architect","model":"old-model"}`, none, start)
	event("plan", 1, "agent.message_committed", `{}`, model.Usage{Input: 100, Output: 10, CacheRead: 50, Cost: 0.1}, start.Add(20*time.Second))
	event("plan", 1, "agent.message_committed", `{}`, model.Usage{Input: 100, Output: 10, CacheRead: 50, Cost: 0.1}, start.Add(40*time.Second))
	event("plan", 1, "agent.run_ended", `{"usage":{"input_tokens":200}}`, none, start.Add(time.Minute))
	event("plan", 1, "semantic.compliance_failed", `{"model":"judge"}`, model.Usage{Input: 7, Output: 1}, start.Add(61*time.Second))
	event("plan", 2, "agent.run_started", `{"role":"architect","model":"new-model"}`, none, start.Add(3*time.Minute))
	event("plan", 2, "agent.message_committed", `{}`, model.Usage{Input: 30, Output: 5, Cost: 0.03}, start.Add(4*time.Minute))
	event("plan", 3, "semantic.compliance_checked", `{"model":"judge"}`, model.Usage{Input: 9, Output: 2}, start.Add(7*time.Minute))
	event("elsewhere", 1, "agent.run_started", `{"role":"writer","model":"other"}`, none, start)
	event("elsewhere", 1, "agent.message_committed", `{}`, model.Usage{Input: 999}, start.Add(time.Minute))

	attempts, err := s.ProjectAttempts(ctx, "book-1")
	if err != nil || len(attempts) != 3 {
		t.Fatalf("attempts = %+v, %v", attempts, err)
	}
	ended, crashed, judged := attempts[0], attempts[1], attempts[2]
	if ended.Attempt != 1 || ended.Role != "architect" || ended.Model != "old-model" || ended.Current ||
		ended.Usage != (model.Usage{Input: 207, Output: 21, CacheRead: 100, Cost: 0.2}) ||
		!ended.StartedAt.Equal(start) || !ended.LastAt.Equal(start.Add(61*time.Second)) {
		t.Fatalf("ended attempt = %+v", ended)
	}
	if crashed.Attempt != 2 || crashed.Model != "new-model" || crashed.Current ||
		crashed.Usage != (model.Usage{Input: 30, Output: 5, Cost: 0.03}) || !crashed.LastAt.Equal(start.Add(4*time.Minute)) {
		t.Fatalf("crashed attempt = %+v", crashed)
	}
	if judged.Attempt != 3 || judged.Role != "architect" || judged.Model != "" || !judged.Current ||
		judged.State != model.OperationRunning || judged.Usage != (model.Usage{Input: 9, Output: 2}) {
		t.Fatalf("judged attempt = %+v", judged)
	}
}
