package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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

func pendingTestProposal(proposal domain.Proposal) domain.Proposal {
	proposal.ApprovalState = domain.ApprovalPending
	proposal.DecidedBy = nil
	proposal.DecidedAt = nil
	return proposal
}
