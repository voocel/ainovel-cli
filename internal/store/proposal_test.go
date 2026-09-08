package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestPendingProposalSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ainovel.db")
	proposal := pendingTestProposal(testChange("persisted-proposal",
		domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}, 0,
		domain.Patch{
			Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
			Operation: domain.PatchPut,
			Content:   []byte(`{"premise":"凡人修仙"}`),
		},
	))

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := s.SaveProposal(ctx, proposal); err != nil {
		t.Fatalf("save proposal: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s.Close()
	stored, err := s.GetProposal(ctx, proposal.ID)
	if err != nil {
		t.Fatalf("get proposal: %v", err)
	}
	if stored.ApprovalState != domain.ApprovalPending || stored.Target != proposal.Target {
		t.Fatalf("stored proposal = %#v", stored)
	}
}

func TestRejectedProposalCannotCommit(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	approved := testChange("rejected-proposal", target, 0, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
		Operation: domain.PatchPut,
		Content:   []byte(`{"premise":"凡人修仙"}`),
	})
	if _, err := s.SaveProposal(ctx, pendingTestProposal(approved)); err != nil {
		t.Fatalf("save proposal: %v", err)
	}
	rejected := approved
	rejected.ApprovalState = domain.ApprovalRejected
	if _, err := s.RejectProposal(ctx, rejected); err != nil {
		t.Fatalf("reject proposal: %v", err)
	}
	if _, err := s.CommitProposal(ctx, approved); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("commit rejected proposal error = %v, want ErrStateConflict", err)
	}
	if _, err := s.CurrentRevision(ctx, target); !errors.Is(err, ErrNotFound) {
		t.Fatalf("current revision error = %v, want ErrNotFound", err)
	}
}

// TestExecutionCommitIsFencedInsideTheTransaction 守护 D42 的提交边界：执行侧提交在同一
// 事务内确认任务仍在运行且 attempt 未被接替；取消先于提交时 ChangeSet 不得落库，
// 用户裁决路径不受围栏限制。
func TestExecutionCommitIsFencedInsideTheTransaction(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	start := operationTime()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}

	if _, err := s.CreateOperation(ctx, testOperation("op", 1, start)); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, start)
	if err != nil || claimed.Attempt != 1 {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	approved := testChange("op-proposal", target, 0, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
		Operation: domain.PatchPut,
		Content:   []byte(`{"premise":"凡人修仙"}`),
	})
	approved.OperationID = claimed.ID
	if _, err := s.SaveProposal(ctx, pendingTestProposal(approved)); err != nil {
		t.Fatalf("save proposal: %v", err)
	}

	if _, err := s.CommitExecutionProposal(ctx, approved, claimed.Attempt+1); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("foreign attempt commit error = %v, want ErrStateConflict", err)
	}
	if _, err := s.TransitionOperation(ctx, claimed.ID, domain.OperationRunning, domain.OperationCancelled, "cancelled by user", start.Add(time.Minute)); err != nil {
		t.Fatalf("user cancel: %v", err)
	}
	if _, err := s.CommitExecutionProposal(ctx, approved, claimed.Attempt); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("commit after cancel error = %v, want ErrStateConflict", err)
	}
	if _, err := s.CurrentRevision(ctx, target); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled execution must not create a revision, got %v", err)
	}

	committed, err := s.CommitProposal(ctx, approved)
	if err != nil || committed.NewRevision != 1 {
		t.Fatalf("user commit = %#v, %v", committed, err)
	}
}

func pendingTestProposal(proposal domain.Proposal) domain.Proposal {
	proposal.ApprovalState = domain.ApprovalPending
	proposal.DecidedBy = nil
	proposal.DecidedAt = nil
	return proposal
}
