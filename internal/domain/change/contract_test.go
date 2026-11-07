package change

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// TestTaskContractHoldsOnEveryPath 钉住 D64：任务提交契约随 ValidateChange 执行，工具边界
// （Validate）、执行收尾（PrepareExecution）与用户批准（Commit）拒绝同一份越界提交——
// 写章任务夹带罗盘。
func TestTaskContractHoldsOnEveryPath(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	now := testTime()
	run, err := s.CreateCreationRun(ctx, model.CreationRun{
		ID: "run:book-1", ProjectID: target.ID,
		Goal:     model.NovelGoal{TargetChapters: 3}.Goal(),
		Strategy: model.CreationRunStrategy{AutoRepairBudget: 3},
		Preset:   model.CreationRunPreset{Source: "test", Digest: "test-preset", Approval: model.ApprovalAuto},
		State:    model.RunRunning, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	const executor = "test.executor@1"
	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`)
	if _, err := s.CreateOperation(ctx, model.Operation{
		ID: "write-1", Kind: model.OperationWriteChapter, Target: target, State: model.OperationQueued, RunID: run.ID,
		Snapshot: model.ExecutionSnapshot{
			Executor: executor, BaseRevision: 1, InputDigest: model.Digest(input),
			ConfigDigest: "profile", ApprovalPolicy: model.ApprovalAuto,
		},
		Input: input, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	claimed, err := s.ClaimNextOperationForExecutors(ctx, "worker-1", []string{executor}, time.Minute, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	smuggled := pendingChange("smuggled", target, 1, model.AuthorAI, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentCompass, ID: model.SingletonDocumentID},
		Operation: model.PatchPut,
		Content:   documentJSON(t, model.Compass{Ending: "送完最后一封信", Final: 8}),
	})
	smuggled.OperationID = claimed.ID
	violates := func(err error) bool {
		return errors.Is(err, model.ErrInvalid) && strings.Contains(err.Error(), "only planning tasks may change the compass")
	}
	engine := New(s)
	if err := engine.Validate(ctx, smuggled); !violates(err) {
		t.Fatalf("tool boundary err = %v", err)
	}
	if _, err := engine.PrepareExecution(ctx, smuggled, claimed.Attempt); !violates(err) {
		t.Fatalf("execution finalize err = %v", err)
	}
	// 绕过准备落一份待裁决稿（例如规则收紧前保存的），用户批准时照样拒绝。
	if _, err := s.SaveProposal(ctx, smuggled); err != nil {
		t.Fatalf("save pending proposal: %v", err)
	}
	approved, err := Decide(smuggled, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if _, err := engine.Commit(ctx, approved); !violates(err) {
		t.Fatalf("user approval err = %v", err)
	}
}
