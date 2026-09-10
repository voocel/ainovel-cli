package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestPreparedProposalCannotBeWrittenBySupersededAttempt(t *testing.T) {
	ctx := context.Background()
	s := openOperationStore(t)
	now := operationTime()
	op := testOperation("op", 1, now)
	if _, err := s.CreateOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimNextOperation(ctx, "old", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AssertActiveAttempt(ctx, first.ID, first.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverExpiredOperations(ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimNextOperation(ctx, "new", time.Minute, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	proposal := pendingTestProposal(testChange("candidate", op.Target, 1, domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, Operation: domain.PatchPut, Content: []byte(`{"premise":"测试"}`)}))
	proposal.OperationID = op.ID
	if _, err := s.SaveExecutionProposal(ctx, proposal, first.Attempt); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("old attempt wrote proposal: %v", err)
	}
	if _, err := s.GetProposal(ctx, proposal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old proposal persisted: %v", err)
	}
	if _, err := s.SaveExecutionProposal(ctx, proposal, second.Attempt); err != nil {
		t.Fatal(err)
	}
}
