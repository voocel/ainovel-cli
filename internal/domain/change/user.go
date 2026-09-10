package change

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func (s *Engine) CommitUser(
	ctx context.Context,
	requested model.Proposal,
	decidedAt time.Time,
) (model.ChangeSet, error) {
	if requested.Author.Kind != model.AuthorUser || requested.ApprovalState != model.ApprovalPending {
		return model.ChangeSet{}, fmt.Errorf("direct user commit requires a pending user proposal: %w", model.ErrInvalid)
	}
	prepared, err := s.store.GetProposal(ctx, requested.ID)
	if errors.Is(err, model.ErrNotFound) {
		prepared, err = s.Prepare(ctx, requested)
	} else if err == nil {
		same, compareErr := sameProposalRequest(requested, prepared)
		if compareErr != nil {
			return model.ChangeSet{}, compareErr
		}
		if !same {
			return model.ChangeSet{}, fmt.Errorf("proposal %q: %w", requested.ID, model.ErrIdempotencyConflict)
		}
	} else {
		return model.ChangeSet{}, err
	}
	if err != nil {
		return model.ChangeSet{}, err
	}
	switch prepared.ApprovalState {
	case model.ApprovalApproved:
		return s.store.GetChangeSet(ctx, prepared.ID)
	case model.ApprovalRejected:
		return model.ChangeSet{}, fmt.Errorf("proposal %q was rejected: %w", prepared.ID, model.ErrStateConflict)
	case model.ApprovalPending:
		approved, err := Decide(prepared, model.ApprovalApproved, requested.Author, decidedAt)
		if err != nil {
			return model.ChangeSet{}, err
		}
		return s.Commit(ctx, approved)
	default:
		return model.ChangeSet{}, fmt.Errorf("proposal %q has invalid state %q: %w", prepared.ID, prepared.ApprovalState, model.ErrStateConflict)
	}
}

func sameProposalRequest(left, right model.Proposal) (bool, error) {
	normalize := func(proposal model.Proposal) ([]byte, error) {
		proposal.Impact = model.ImpactReport{}
		proposal.ApprovalState = model.ApprovalPending
		proposal.DecidedBy = nil
		proposal.DecidedAt = nil
		proposal.CreatedAt = time.Time{}
		return json.Marshal(proposal)
	}
	leftPayload, err := normalize(left)
	if err != nil {
		return false, fmt.Errorf("encode requested proposal identity: %w", err)
	}
	rightPayload, err := normalize(right)
	if err != nil {
		return false, fmt.Errorf("encode stored proposal identity: %w", err)
	}
	return bytes.Equal(leftPayload, rightPayload), nil
}
