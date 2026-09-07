package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func (s *Store) SaveProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	if err := proposal.Validate(); err != nil {
		return domain.Proposal{}, err
	}
	if proposal.ApprovalState != domain.ApprovalPending {
		return domain.Proposal{}, fmt.Errorf("only pending proposals can be prepared: %w", ErrStateConflict)
	}
	payload, digest, err := encodeProposal(proposal)
	if err != nil {
		return domain.Proposal{}, err
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO proposals (
			id, content_digest, payload, state, target_kind, target_id, target_scope,
			base_revision, created_at_unix_ms, updated_at_unix_ms, operation_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		proposal.ID, digest, payload, proposal.ApprovalState,
		proposal.Target.Kind, proposal.Target.ID, proposal.Target.Scope,
		proposal.BaseRevision, proposal.CreatedAt.UnixMilli(), proposal.CreatedAt.UnixMilli(), nullableString(proposal.OperationID))
	if err != nil {
		return domain.Proposal{}, fmt.Errorf("save proposal: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Proposal{}, fmt.Errorf("inspect proposal insert: %w", err)
	}
	if rows == 1 {
		return proposal, nil
	}

	stored, storedDigest, err := s.getProposal(ctx, s.db, proposal.ID)
	if err != nil {
		return domain.Proposal{}, err
	}
	if storedDigest != digest {
		return domain.Proposal{}, fmt.Errorf("proposal %q: %w", proposal.ID, ErrIdempotencyConflict)
	}
	if stored.ApprovalState != domain.ApprovalPending {
		return domain.Proposal{}, fmt.Errorf("proposal %q is %s: %w", proposal.ID, stored.ApprovalState, ErrStateConflict)
	}
	return stored, nil
}

func (s *Store) GetProposal(ctx context.Context, id string) (domain.Proposal, error) {
	proposal, _, err := s.getProposal(ctx, s.db, id)
	return proposal, err
}

func (s *Store) GetProposalByOperation(ctx context.Context, operationID string) (domain.Proposal, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM proposals WHERE operation_id = ?`, operationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Proposal{}, ErrNotFound
	}
	if err != nil {
		return domain.Proposal{}, fmt.Errorf("find proposal for operation: %w", err)
	}
	return s.GetProposal(ctx, id)
}

// CommitProposal stores the approval decision and authority revision in one
// transaction. Either both become visible, or neither does.
func (s *Store) CommitProposal(ctx context.Context, proposal domain.Proposal) (domain.ChangeSet, error) {
	if err := proposal.Validate(); err != nil {
		return domain.ChangeSet{}, err
	}
	if proposal.ApprovalState != domain.ApprovalApproved {
		return domain.ChangeSet{}, ErrNotApproved
	}
	preparedDigest, err := preparedProposalDigest(proposal)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	payload, changeDigest, err := encodeProposal(proposal)
	if err != nil {
		return domain.ChangeSet{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("begin proposal commit: %w", err)
	}
	defer tx.Rollback()

	stored, storedDigest, err := s.getProposal(ctx, tx, proposal.ID)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	if storedDigest != preparedDigest {
		return domain.ChangeSet{}, fmt.Errorf("proposal %q changed after preparation: %w", proposal.ID, ErrIdempotencyConflict)
	}
	switch stored.ApprovalState {
	case domain.ApprovalRejected:
		return domain.ChangeSet{}, fmt.Errorf("proposal %q was rejected: %w", proposal.ID, ErrStateConflict)
	case domain.ApprovalApproved:
		_, storedDecisionDigest, err := encodeProposal(stored)
		if err != nil {
			return domain.ChangeSet{}, err
		}
		if storedDecisionDigest != changeDigest {
			return domain.ChangeSet{}, fmt.Errorf("proposal %q has a different approval decision: %w", proposal.ID, ErrIdempotencyConflict)
		}
	case domain.ApprovalPending:
	default:
		return domain.ChangeSet{}, fmt.Errorf("proposal %q has invalid state %q: %w", proposal.ID, stored.ApprovalState, ErrStateConflict)
	}

	committed, err := commitProposalTx(ctx, tx, proposal, changeDigest)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	if stored.ApprovalState == domain.ApprovalPending {
		result, err := tx.ExecContext(ctx, `
			UPDATE proposals
			SET payload = ?, state = ?, updated_at_unix_ms = ?
			WHERE id = ? AND state = ?`,
			payload, proposal.ApprovalState, proposal.DecidedAt.UnixMilli(),
			proposal.ID, domain.ApprovalPending)
		if err != nil {
			return domain.ChangeSet{}, fmt.Errorf("record proposal approval: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return domain.ChangeSet{}, fmt.Errorf("inspect proposal approval: %w", err)
		}
		if rows != 1 {
			return domain.ChangeSet{}, fmt.Errorf("proposal %q approval raced: %w", proposal.ID, ErrStateConflict)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ChangeSet{}, fmt.Errorf("commit approved proposal: %w", err)
	}
	return committed, nil
}

func (s *Store) RejectProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	if err := proposal.Validate(); err != nil {
		return domain.Proposal{}, err
	}
	if proposal.ApprovalState != domain.ApprovalRejected {
		return domain.Proposal{}, fmt.Errorf("rejection requires rejected state: %w", ErrStateConflict)
	}
	preparedDigest, err := preparedProposalDigest(proposal)
	if err != nil {
		return domain.Proposal{}, err
	}
	payload, decisionDigest, err := encodeProposal(proposal)
	if err != nil {
		return domain.Proposal{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Proposal{}, fmt.Errorf("begin proposal rejection: %w", err)
	}
	defer tx.Rollback()
	stored, storedDigest, err := s.getProposal(ctx, tx, proposal.ID)
	if err != nil {
		return domain.Proposal{}, err
	}
	if storedDigest != preparedDigest {
		return domain.Proposal{}, fmt.Errorf("proposal %q changed after preparation: %w", proposal.ID, ErrIdempotencyConflict)
	}
	if stored.ApprovalState == domain.ApprovalApproved {
		return domain.Proposal{}, fmt.Errorf("proposal %q was approved: %w", proposal.ID, ErrStateConflict)
	}
	if stored.ApprovalState == domain.ApprovalRejected {
		_, storedDecisionDigest, err := encodeProposal(stored)
		if err != nil {
			return domain.Proposal{}, err
		}
		if storedDecisionDigest != decisionDigest {
			return domain.Proposal{}, fmt.Errorf("proposal %q has a different rejection decision: %w", proposal.ID, ErrIdempotencyConflict)
		}
		return stored, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE proposals
		SET payload = ?, state = ?, updated_at_unix_ms = ?
		WHERE id = ? AND state = ?`,
		payload, proposal.ApprovalState, proposal.DecidedAt.UnixMilli(),
		proposal.ID, domain.ApprovalPending)
	if err != nil {
		return domain.Proposal{}, fmt.Errorf("record proposal rejection: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Proposal{}, fmt.Errorf("inspect proposal rejection: %w", err)
	}
	if rows != 1 {
		return domain.Proposal{}, fmt.Errorf("proposal %q rejection raced: %w", proposal.ID, ErrStateConflict)
	}
	if err := tx.Commit(); err != nil {
		return domain.Proposal{}, fmt.Errorf("commit proposal rejection: %w", err)
	}
	return proposal, nil
}

type proposalQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) getProposal(ctx context.Context, query proposalQuery, id string) (domain.Proposal, string, error) {
	var payload []byte
	var digest string
	err := query.QueryRowContext(ctx, `SELECT payload, content_digest FROM proposals WHERE id = ?`, id).
		Scan(&payload, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Proposal{}, "", ErrNotFound
	}
	if err != nil {
		return domain.Proposal{}, "", fmt.Errorf("read proposal: %w", err)
	}
	var proposal domain.Proposal
	if err := json.Unmarshal(payload, &proposal); err != nil {
		return domain.Proposal{}, "", fmt.Errorf("decode stored proposal %q: %w", id, err)
	}
	if err := proposal.Validate(); err != nil {
		return domain.Proposal{}, "", fmt.Errorf("validate stored proposal %q: %w", id, err)
	}
	return proposal, digest, nil
}

func preparedProposalDigest(proposal domain.Proposal) (string, error) {
	proposal.ApprovalState = domain.ApprovalPending
	proposal.DecidedBy = nil
	proposal.DecidedAt = nil
	proposal.DecisionReason = ""
	_, digest, err := encodeProposal(proposal)
	return digest, err
}

func encodeProposal(proposal domain.Proposal) ([]byte, string, error) {
	payload, err := json.Marshal(proposal)
	if err != nil {
		return nil, "", fmt.Errorf("marshal proposal: %w", err)
	}
	return payload, domain.Digest(payload), nil
}
