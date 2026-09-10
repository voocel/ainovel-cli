package operation

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type recoveryAnalyzer struct {
	neverExecutor
	checks      int
	constraints []domain.OwnershipRule
}

func (e *recoveryAnalyzer) AnalyzeSemanticCompliance(_ context.Context, _ domain.Operation, _ domain.Proposal, constraints []domain.OwnershipRule) (domain.SemanticComplianceReport, error) {
	e.checks++
	e.constraints = constraints
	return domain.SemanticComplianceReport{Status: domain.SemanticCompliancePass}, nil
}

func TestRecoveryRechecksComplianceAfterConstraintChange(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "new-guidance"}[changed], func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			s, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "recovery"}
			seedConstrainedProject(t, ctx, s, target, now)
			input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
			op := domain.Operation{ID: "write", Kind: domain.OperationWriteChapter, Target: target, State: domain.OperationQueued, RunID: createEngineTestRun(t, ctx, s, target.ID, now), Snapshot: engineSnapshot(input, 1, domain.ApprovalAuto), Input: input, CreatedAt: now, UpdatedAt: now}
			if _, err = s.CreateOperation(ctx, op); err != nil {
				t.Fatal(err)
			}
			op, err = s.ClaimNextOperation(ctx, "before-crash", time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := (manuscriptExecutor{}).Execute(ctx, op)
			if err != nil {
				t.Fatal(err)
			}
			proposal := *outcome.Proposal
			engine := NewEngine(s)
			constraints, err := engine.semanticConstraints(ctx, op, proposal)
			if err != nil {
				t.Fatal(err)
			}
			proposal.Impact.Compliance, err = engine.analyzeCompliance(ctx, passingManuscriptExecutor{}, "before-crash", time.Minute, op, proposal, constraints)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = change.New(s).PrepareExecution(ctx, proposal, op.Attempt); err != nil {
				t.Fatal(err)
			}
			if changed {
				raw, _ := json.Marshal(domain.OwnershipRule{Target: domain.DocumentRef{Kind: domain.DocumentEntity, ID: "hero"}, Control: domain.ControlGuided, Guidance: []string{"主角不能进入山门"}})
				commitUserChange(t, ctx, s, target, "guide", now.Add(time.Second), domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentOwnership, ID: "entity:hero"}, Operation: domain.PatchPut, Content: raw})
			}
			if _, err = s.RecoverExpiredOperations(ctx, now.Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			executor := &recoveryAnalyzer{}
			result, err := engine.RunNext(ctx, executor, "recovery", time.Minute, now.Add(3*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if changed {
				want = 1
			}
			if result.Operation.State != domain.OperationSucceeded || executor.checks != want || executor.called {
				t.Fatalf("state=%s checks=%d regenerated=%v", result.Operation.State, executor.checks, executor.called)
			}
			if changed && len(executor.constraints) != 2 {
				t.Fatalf("new constraints not analyzed: %+v", executor.constraints)
			}
		})
	}
}
