package evidence

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/operation"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// Reader selects persisted evidence whose inputs remain valid at a chosen revision.
type Reader struct {
	store      *store.Store
	changes    *change.Engine
	operations *operation.Engine
}

func New(s *store.Store, changes *change.Engine, operations *operation.Engine) *Reader {
	return &Reader{store: s, changes: changes, operations: operations}
}

type Check struct {
	OperationID string
	Kind        model.OperationKind
	Basis       model.EvidenceBasis
	Content     json.RawMessage
}

func (r *Reader) Valid(ctx context.Context, target model.AuthorityTarget, revision model.Revision, basis model.EvidenceBasis) (bool, error) {
	err := r.changes.VerifyBasis(ctx, target, basis, revision)
	if errors.Is(err, change.ErrBasisMismatch) {
		return false, nil
	}
	return err == nil, err
}
func (r *Reader) Artifacts(ctx context.Context, projectID string, revision model.Revision) ([]model.Artifact, error) {
	artifacts, err := r.store.ListArtifacts(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var valid []model.Artifact
	for _, artifact := range artifacts {
		ok, err := r.Valid(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: projectID}, revision, artifact.Basis)
		if err != nil {
			return nil, err
		}
		if ok {
			valid = append(valid, artifact)
		}
	}
	return valid, nil
}

// Checks filters by requested operation kinds before decoding their domain payloads.
func (r *Reader) Checks(ctx context.Context, projectID string, revision model.Revision, kinds ...model.OperationKind) ([]Check, error) {
	selected := make(map[model.OperationKind]bool, len(kinds))
	for _, kind := range kinds {
		selected[kind] = true
	}
	documents, err := r.store.ListDerivedDocumentsByKind(ctx, projectID, model.DerivedVerdictKind)
	if err != nil {
		return nil, err
	}
	var valid []Check
	for _, document := range documents {
		op, err := r.store.GetOperation(ctx, document.Key)
		if err != nil {
			return nil, err
		}
		if op.State != model.OperationSucceeded || !selected[op.Kind] {
			continue
		}
		basis, err := r.operations.ValidateEvidence(ctx, op, document.Content)
		if err != nil {
			return nil, err
		}
		ok, err := r.Valid(ctx, op.Target, revision, basis)
		if err != nil {
			return nil, err
		}
		if ok {
			valid = append(valid, Check{OperationID: op.ID, Kind: op.Kind, Basis: basis, Content: document.Content})
		}
	}
	return valid, nil
}
