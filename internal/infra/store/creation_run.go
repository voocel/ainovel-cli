package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

const creationRunColumns = `
	id, project_id, goal, strategy, preset, state, state_reason, completed_revision,
	created_at_unix_ms, updated_at_unix_ms`

const activeRunStates = `'running', 'waiting_user', 'paused'`

func (s *Store) CreateCreationRun(ctx context.Context, run model.CreationRun) (model.CreationRun, error) {
	if run.State != model.RunRunning {
		return model.CreationRun{}, fmt.Errorf("new creation run must be running: %w", model.ErrInvalid)
	}
	if err := run.Validate(); err != nil {
		return model.CreationRun{}, err
	}
	goal, err := json.Marshal(run.Goal)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("encode creation run goal: %w", err)
	}
	strategy, err := json.Marshal(run.Strategy)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("encode creation run strategy: %w", err)
	}
	preset, err := json.Marshal(run.Preset)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("encode creation run preset: %w", err)
	}
	digest, err := model.DigestJSON(struct {
		ID        string                    `json:"id"`
		ProjectID string                    `json:"project_id"`
		Goal      model.CreationRunGoal     `json:"goal"`
		Strategy  model.CreationRunStrategy `json:"strategy"`
		Preset    model.CreationRunPreset   `json:"preset"`
	}{run.ID, run.ProjectID, run.Goal, run.Strategy, run.Preset})
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("encode creation run identity: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("begin creation run insert: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO creation_runs (
			id, content_digest, project_id, goal, strategy, preset, state,
			state_reason, completed_revision, created_at_unix_ms, updated_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		run.ID, digest, run.ProjectID, goal, strategy, preset, run.State, run.StateReason,
		run.CompletedRevision, run.CreatedAt.UnixMilli(), run.UpdatedAt.UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "creation_runs.project_id") {
			return model.CreationRun{}, fmt.Errorf(
				"project %q already has an active creation run: %w", run.ProjectID, model.ErrStateConflict)
		}
		return model.CreationRun{}, fmt.Errorf("insert creation run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("inspect creation run insert: %w", err)
	}
	if rows == 0 {
		var storedDigest string
		if err := tx.QueryRowContext(ctx,
			`SELECT content_digest FROM creation_runs WHERE id = ?`, run.ID).Scan(&storedDigest); err != nil {
			return model.CreationRun{}, fmt.Errorf("inspect existing creation run: %w", err)
		}
		if storedDigest != digest {
			stored, err := scanCreationRun(tx.QueryRowContext(ctx,
				`SELECT `+creationRunColumns+` FROM creation_runs WHERE id = ?`, run.ID))
			if err != nil {
				return model.CreationRun{}, err
			}
			if stored.ID != run.ID || stored.ProjectID != run.ProjectID || !stored.Goal.Equal(run.Goal) ||
				stored.Strategy != run.Strategy || stored.Preset != run.Preset {
				return model.CreationRun{}, fmt.Errorf("creation run %q: %w", run.ID, model.ErrIdempotencyConflict)
			}
		}
		if err := tx.Commit(); err != nil {
			return model.CreationRun{}, fmt.Errorf("commit idempotent creation run insert: %w", err)
		}
		return s.GetCreationRun(ctx, run.ID)
	}
	if err := appendRunEventTx(ctx, tx, run.ID, model.RunEventCreated, struct {
		Goal     model.CreationRunGoal     `json:"goal"`
		Strategy model.CreationRunStrategy `json:"strategy"`
		Preset   model.CreationRunPreset   `json:"preset"`
	}{run.Goal, run.Strategy, run.Preset}, run.CreatedAt); err != nil {
		return model.CreationRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.CreationRun{}, fmt.Errorf("commit creation run insert: %w", err)
	}
	return run, nil
}

func (s *Store) GetCreationRun(ctx context.Context, id string) (model.CreationRun, error) {
	return scanCreationRun(s.db.QueryRowContext(ctx,
		`SELECT `+creationRunColumns+` FROM creation_runs WHERE id = ?`, id))
}

func (s *Store) ActiveCreationRun(ctx context.Context, projectID string) (model.CreationRun, error) {
	return scanCreationRun(s.db.QueryRowContext(ctx,
		`SELECT `+creationRunColumns+` FROM creation_runs
		WHERE project_id = ? AND state IN (`+activeRunStates+`)`, projectID))
}

// LatestCreationRun 取项目最近一轮创作（不限状态）：终态落点也要能被界面呈现。
func (s *Store) LatestCreationRun(ctx context.Context, projectID string) (model.CreationRun, error) {
	return scanCreationRun(s.db.QueryRowContext(ctx,
		`SELECT `+creationRunColumns+` FROM creation_runs
		WHERE project_id = ? ORDER BY created_at_unix_ms DESC, id DESC LIMIT 1`, projectID))
}

func (s *Store) CountCreationRuns(ctx context.Context, projectID string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM creation_runs WHERE project_id = ?`, projectID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count creation runs: %w", err)
	}
	return count, nil
}

func (s *Store) TransitionCreationRun(
	ctx context.Context,
	id string,
	from, to model.CreationRunState,
	reason string,
	completedRevision model.Revision,
	now time.Time,
) (model.CreationRun, error) {
	if !model.CanTransitionCreationRun(from, to) {
		return model.CreationRun{}, fmt.Errorf("creation run cannot go %s -> %s: %w", from, to, model.ErrStateConflict)
	}
	if (to == model.RunCompleted) != (completedRevision > model.InitialRevision) {
		return model.CreationRun{}, fmt.Errorf(
			"completed creation run must bind a revision, other states must not: %w", model.ErrInvalid)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE creation_runs
		SET state = ?, state_reason = ?, completed_revision = ?, updated_at_unix_ms = ?
		WHERE id = ? AND state = ?`,
		to, reason, completedRevision, now.UnixMilli(), id, from)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("transition creation run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("inspect creation run transition: %w", err)
	}
	if rows == 0 {
		current, err := s.GetCreationRun(ctx, id)
		if err != nil {
			return model.CreationRun{}, err
		}
		if current.State == to {
			return current, nil
		}
		return model.CreationRun{}, fmt.Errorf(
			"creation run %q is %s, expected %s: %w", id, current.State, from, model.ErrStateConflict)
	}
	return s.GetCreationRun(ctx, id)
}

// UpdateCreationRunGoal 更新进行中创作的目标（§6.3：改目标更新当前 Run，不启动竞争 Run）。
func (s *Store) UpdateCreationRunGoal(
	ctx context.Context,
	id string,
	goal model.CreationRunGoal,
	now time.Time,
) (model.CreationRun, error) {
	if err := goal.Validate(); err != nil {
		return model.CreationRun{}, err
	}
	payload, err := json.Marshal(goal)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("encode creation run goal: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("begin creation run goal update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE creation_runs SET goal = ?, updated_at_unix_ms = ?
		WHERE id = ? AND state IN (`+activeRunStates+`)`,
		payload, now.UnixMilli(), id)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("update creation run goal: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("inspect creation run goal update: %w", err)
	}
	if rows == 0 {
		current, err := s.GetCreationRun(ctx, id)
		if err != nil {
			return model.CreationRun{}, err
		}
		return model.CreationRun{}, fmt.Errorf(
			"creation run %q is %s and cannot change goal: %w", id, current.State, model.ErrStateConflict)
	}
	if err := appendRunEventTx(ctx, tx, id, model.RunEventGoalUpdated, goal, now); err != nil {
		return model.CreationRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.CreationRun{}, fmt.Errorf("commit creation run goal update: %w", err)
	}
	return s.GetCreationRun(ctx, id)
}

// UpdateCreationRunStrategy 更新之后新建 Operation 使用的自动化边界；事件序号
// 就是 Operation 固定的 RunPolicyVersion（§6.3）。
func (s *Store) UpdateCreationRunStrategy(
	ctx context.Context,
	id string,
	strategy model.CreationRunStrategy,
	now time.Time,
) (model.CreationRun, error) {
	if err := strategy.Validate(); err != nil {
		return model.CreationRun{}, err
	}
	payload, err := json.Marshal(strategy)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("encode creation run strategy: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("begin creation run strategy update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE creation_runs SET strategy = ?, updated_at_unix_ms = ?
		WHERE id = ? AND state IN (`+activeRunStates+`)`,
		payload, now.UnixMilli(), id)
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("update creation run strategy: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("inspect creation run strategy update: %w", err)
	}
	if rows == 0 {
		current, err := s.GetCreationRun(ctx, id)
		if err != nil {
			return model.CreationRun{}, err
		}
		return model.CreationRun{}, fmt.Errorf(
			"creation run %q is %s and cannot change strategy: %w", id, current.State, model.ErrStateConflict)
	}
	if err := appendRunEventTx(ctx, tx, id, model.RunEventStrategyUpdated, strategy, now); err != nil {
		return model.CreationRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.CreationRun{}, fmt.Errorf("commit creation run strategy update: %w", err)
	}
	return s.GetCreationRun(ctx, id)
}

func (s *Store) ListCreationRunEvents(ctx context.Context, runID string) ([]model.CreationRunEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, sequence, kind, payload, created_at_unix_ms
		FROM creation_run_events WHERE run_id = ? ORDER BY sequence`, runID)
	if err != nil {
		return nil, fmt.Errorf("list creation run events: %w", err)
	}
	defer rows.Close()
	var events []model.CreationRunEvent
	for rows.Next() {
		var event model.CreationRunEvent
		var payload []byte
		var createdAt int64
		if err := rows.Scan(&event.RunID, &event.Sequence, &event.Kind, &payload, &createdAt); err != nil {
			return nil, fmt.Errorf("scan creation run event: %w", err)
		}
		event.Payload = append(json.RawMessage(nil), payload...)
		event.CreatedAt = time.UnixMilli(createdAt).UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate creation run events: %w", err)
	}
	return events, nil
}

func appendRunEventTx(ctx context.Context, tx *sql.Tx, runID, kind string, payload any, at time.Time) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode run event payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO creation_run_events (run_id, sequence, kind, payload, created_at_unix_ms)
		SELECT ?, COALESCE(MAX(sequence), 0) + 1, ?, ?, ?
		FROM creation_run_events WHERE run_id = ?`,
		runID, kind, content, at.UnixMilli(), runID); err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	return nil
}

func scanCreationRun(row *sql.Row) (model.CreationRun, error) {
	var run model.CreationRun
	var goal, strategy, preset []byte
	var createdAt, updatedAt int64
	err := row.Scan(&run.ID, &run.ProjectID, &goal, &strategy, &preset, &run.State,
		&run.StateReason, &run.CompletedRevision, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CreationRun{}, model.ErrNotFound
	}
	if err != nil {
		return model.CreationRun{}, fmt.Errorf("read creation run: %w", err)
	}
	if err := json.Unmarshal(goal, &run.Goal); err != nil {
		return model.CreationRun{}, fmt.Errorf("decode creation run goal: %w", err)
	}
	if err := json.Unmarshal(strategy, &run.Strategy); err != nil {
		return model.CreationRun{}, fmt.Errorf("decode creation run strategy: %w", err)
	}
	if err := json.Unmarshal(preset, &run.Preset); err != nil {
		return model.CreationRun{}, fmt.Errorf("decode creation run preset: %w", err)
	}
	run.CreatedAt = time.UnixMilli(createdAt).UTC()
	run.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	if err := run.Validate(); err != nil {
		return model.CreationRun{}, fmt.Errorf("stored creation run is invalid: %w", err)
	}
	return run, nil
}
