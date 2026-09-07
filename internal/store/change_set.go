package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func (s *Store) GetChangeSetByRevision(
	ctx context.Context,
	target domain.AuthorityTarget,
	revision domain.Revision,
) (domain.ChangeSet, error) {
	if err := target.Validate(); err != nil {
		return domain.ChangeSet{}, err
	}
	if revision <= domain.InitialRevision {
		return domain.ChangeSet{}, fmt.Errorf("revision must be positive: %w", domain.ErrInvalid)
	}
	var id string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM change_sets
		WHERE target_kind = ? AND target_id = ? AND target_scope = ? AND new_revision = ?`,
		target.Kind, target.ID, target.Scope, revision).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ChangeSet{}, ErrNotFound
	}
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("find change set by revision: %w", err)
	}
	return s.GetChangeSet(ctx, id)
}

func (s *Store) GetChangeSet(ctx context.Context, id string) (domain.ChangeSet, error) {
	var changeSet domain.ChangeSet
	var structural, semantic, compliance []byte
	var decidedByKind, decidedByID, operationID sql.NullString
	var decidedAt sql.NullInt64
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT target_kind, target_id, target_scope, base_revision, new_revision,
			author_kind, author_id, reason, structural_impact, semantic_impact, compliance_impact,
			approval_state, decision_author_kind, decision_author_id,
			decided_at_unix_ms, created_at_unix_ms, operation_id
		FROM change_sets WHERE id = ?`, id).Scan(
		&changeSet.Target.Kind, &changeSet.Target.ID, &changeSet.Target.Scope,
		&changeSet.BaseRevision, &changeSet.NewRevision,
		&changeSet.Author.Kind, &changeSet.Author.ID, &changeSet.Reason,
		&structural, &semantic, &compliance, &changeSet.ApprovalState,
		&decidedByKind, &decidedByID, &decidedAt, &createdAt, &operationID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ChangeSet{}, ErrNotFound
	}
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("read change set: %w", err)
	}
	changeSet.ID = id
	changeSet.OperationID = operationID.String
	changeSet.Impact.Structural = append(json.RawMessage(nil), structural...)
	changeSet.Impact.Semantic = append(json.RawMessage(nil), semantic...)
	changeSet.Impact.Compliance = append(json.RawMessage(nil), compliance...)
	changeSet.CreatedAt = time.UnixMilli(createdAt).UTC()
	if decidedByKind.Valid && decidedByID.Valid {
		changeSet.DecidedBy = &domain.Author{Kind: domain.AuthorKind(decidedByKind.String), ID: decidedByID.String}
	}
	if decidedAt.Valid {
		value := time.UnixMilli(decidedAt.Int64).UTC()
		changeSet.DecidedAt = &value
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT document_kind, document_id, operation, content
		FROM document_versions WHERE change_set_id = ?
		ORDER BY document_kind, document_id`, id)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("read change set patches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var patch domain.Patch
		var content []byte
		if err := rows.Scan(&patch.Document.Kind, &patch.Document.ID, &patch.Operation, &content); err != nil {
			return domain.ChangeSet{}, fmt.Errorf("scan change set patch: %w", err)
		}
		if patch.Operation == domain.PatchPut {
			patch.Content = append(json.RawMessage(nil), content...)
		}
		changeSet.Patches = append(changeSet.Patches, patch)
	}
	if err := rows.Err(); err != nil {
		return domain.ChangeSet{}, fmt.Errorf("iterate change set patches: %w", err)
	}
	if err := changeSet.Validate(); err != nil {
		return domain.ChangeSet{}, fmt.Errorf("stored change set is invalid: %w", err)
	}
	return changeSet, nil
}
