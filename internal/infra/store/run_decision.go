package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// SettleCreationRun commits a decision only while its complete observation is
// current. User transitions use TransitionCreationRun and need no observation.
func (s *Store) SettleCreationRun(ctx context.Context, observed model.CreationRun, revision model.Revision, to model.CreationRunState, reason string, at time.Time) (model.CreationRun, error) {
	if observed.State != model.RunRunning || revision <= model.InitialRevision ||
		(to != model.RunCompleted && to != model.RunWaitingUser && to != model.RunFailed) {
		return model.CreationRun{}, fmt.Errorf("invalid creation decision: %w", model.ErrInvalid)
	}
	goal, err := json.Marshal(observed.Goal)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("encode observed goal: %w", err)
	}
	strategy, err := json.Marshal(observed.Strategy)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("encode observed strategy: %w", err)
	}
	completedRevision := model.InitialRevision
	if to == model.RunCompleted {
		completedRevision = revision
	}
	settled, err := scanCreationRun(s.db.QueryRowContext(ctx, `
		UPDATE creation_runs SET state = ?, state_reason = ?, completed_revision = ?, updated_at_unix_ms = ?
		WHERE id = ? AND project_id = ? AND state = ? AND goal = ? AND strategy = ?
		AND EXISTS (
			SELECT 1 FROM authority_streams
			WHERE target_kind = ? AND target_id = ? AND target_scope = '' AND current_revision = ?
		)
		RETURNING `+creationRunColumns,
		to, reason, completedRevision, at.UnixMilli(), observed.ID, observed.ProjectID,
		model.RunRunning, goal, strategy, model.AuthorityProject, observed.ProjectID, revision))
	if errors.Is(err, model.ErrNotFound) {
		current, readErr := s.GetCreationRun(ctx, observed.ID)
		if readErr != nil {
			return model.CreationRun{}, readErr
		}
		if current.State != observed.State {
			return model.CreationRun{}, model.ErrStateConflict
		}
		return model.CreationRun{}, model.ErrRevisionConflict
	}
	return settled, err
}
