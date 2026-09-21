package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

const operationColumns = `
	id, kind, target_kind, target_id, target_scope, priority, state,
	attempt, execution_snapshot, input, error, failure_code, lease_owner, lease_until_unix_ms,
	created_at_unix_ms, updated_at_unix_ms, run_id, run_policy_version`

// MaxLeaseExpiries 是同一 Operation 租约过期后自动重排的次数上限。达到上限转入
// failed 而不是继续排队：无进展的重试必须有明确上界，否则模型调用持续超时会让
// 同一任务被无限重领并持续计费（v1-architecture-plan §6.2）。按过期次数而不是
// attempt 计：attempt 是执行围栏，进程退出主动释放的执行不是无进展。
// 用户显式重排（failed → queued）不受此限，那是有人在场的决定。
const MaxLeaseExpiries = 5

func (s *Store) CreateOperation(ctx context.Context, operation model.Operation) (model.Operation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Operation{}, fmt.Errorf("begin operation creation: %w", err)
	}
	defer tx.Rollback()
	created, err := createOperationTx(ctx, tx, operation)
	if err != nil {
		return model.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Operation{}, fmt.Errorf("commit operation creation: %w", err)
	}
	return created, nil
}

// CreateSuccessorOperation publishes the queued successor only after its inherited
// workspace and lineage are durable. A worker can never claim an unseeded restart.
func (s *Store) CreateSuccessorOperation(ctx context.Context, previousID string, operation model.Operation) (model.Operation, error) {
	if strings.TrimSpace(previousID) == "" || previousID == operation.ID {
		return model.Operation{}, fmt.Errorf("restart requires distinct source and target: %w", model.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Operation{}, fmt.Errorf("begin successor creation: %w", err)
	}
	defer tx.Rollback()
	// Serialize source validation, insertion and seeding against task claims.
	if _, err := tx.ExecContext(ctx, `UPDATE operations SET id = id WHERE id = ?`, previousID); err != nil {
		return model.Operation{}, fmt.Errorf("lock restart source: %w", err)
	}
	previous, err := getOperationTx(ctx, tx, previousID)
	if err != nil {
		return model.Operation{}, err
	}
	if previous.State != model.OperationFailed && previous.State != model.OperationCancelled && previous.State != model.OperationStale {
		return model.Operation{}, fmt.Errorf("restart source is %s: %w", previous.State, model.ErrStateConflict)
	}
	if operation.Target != previous.Target || operation.Kind != previous.Kind ||
		(previous.RunID != "" && operation.RunID != previous.RunID) {
		return model.Operation{}, fmt.Errorf("restart source identity differs: %w", model.ErrInvalid)
	}
	created, err := createOperationTx(ctx, tx, operation)
	if err != nil {
		return model.Operation{}, err
	}
	if err := copyWorkspaceArtifactsTx(ctx, tx, previousID, operation.ID, operation.CreatedAt); err != nil {
		return model.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Operation{}, fmt.Errorf("commit successor creation: %w", err)
	}
	return created, nil
}

func getOperationTx(ctx context.Context, tx *sql.Tx, id string) (model.Operation, error) {
	operation, err := scanOperation(tx.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Operation{}, model.ErrNotFound
	}
	if err != nil {
		return model.Operation{}, fmt.Errorf("read operation: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, tx, id)
	return operation, err
}

func createOperationTx(ctx context.Context, tx *sql.Tx, operation model.Operation) (model.Operation, error) {
	if operation.State != model.OperationQueued {
		return model.Operation{}, fmt.Errorf("new operation must be queued: %w", model.ErrInvalid)
	}
	if spec, err := model.KindSpec(operation.Kind); err != nil {
		return model.Operation{}, err
	} else if spec.RequiresRun && strings.TrimSpace(operation.RunID) == "" {
		return model.Operation{}, fmt.Errorf("%s operation requires a creation run: %w", operation.Kind, model.ErrInvalid)
	}
	if operation.RunPolicyVersion != 0 {
		return model.Operation{}, fmt.Errorf("run policy version is assigned by the store: %w", model.ErrInvalid)
	}
	if err := operation.Validate(); err != nil {
		return model.Operation{}, err
	}
	operation.DependsOn = append([]string(nil), operation.DependsOn...)
	slices.Sort(operation.DependsOn)
	digest, snapshot, err := operationDigest(operation)
	if err != nil {
		return model.Operation{}, err
	}
	var existingDigest string
	if err := tx.QueryRowContext(ctx,
		`SELECT content_digest FROM operations WHERE id = ?`, operation.ID,
	).Scan(&existingDigest); err == nil {
		if existingDigest != digest {
			return model.Operation{}, fmt.Errorf("operation %q: %w", operation.ID, model.ErrIdempotencyConflict)
		}
		return getOperationTx(ctx, tx, operation.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return model.Operation{}, fmt.Errorf("inspect existing operation: %w", err)
	}
	// Run 归属与策略版本在同一事务内校验并落行（§6.3）：策略版本 = 创建时刻
	// 最近一次非 Operation 事件的序号。
	// 策略版本不参与幂等 digest——重入时事件日志可能已推进，身份以调用方输入为准。
	if operation.RunID != "" {
		var runProjectID string
		var runState model.CreationRunState
		if err := tx.QueryRowContext(ctx, `
			SELECT project_id, state FROM creation_runs WHERE id = ?`, operation.RunID,
		).Scan(&runProjectID, &runState); errors.Is(err, sql.ErrNoRows) {
			return model.Operation{}, fmt.Errorf("creation run %q: %w", operation.RunID, model.ErrNotFound)
		} else if err != nil {
			return model.Operation{}, fmt.Errorf("read creation run ownership: %w", err)
		}
		if operation.Target.Kind != model.AuthorityProject || operation.Target.ID != runProjectID {
			return model.Operation{}, fmt.Errorf(
				"operation target does not match creation run project %q: %w", runProjectID, model.ErrInvalid,
			)
		}
		switch runState {
		case model.RunRunning, model.RunWaitingUser, model.RunPaused:
		default:
			return model.Operation{}, fmt.Errorf(
				"creation run %q is %s: %w", operation.RunID, runState, model.ErrStateConflict,
			)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(sequence), 0) FROM creation_run_events
			WHERE run_id = ? AND kind != ?`,
			operation.RunID, model.RunEventOperationCreated).Scan(&operation.RunPolicyVersion); err != nil {
			return model.Operation{}, fmt.Errorf("read run policy version: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO operations (
			id, content_digest, kind, target_kind, target_id, target_scope,
			priority, state, attempt, execution_snapshot, input, error,
			created_at_unix_ms, updated_at_unix_ms, executor, run_id, run_policy_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		operation.ID, digest, operation.Kind, operation.Target.Kind, operation.Target.ID, operation.Target.Scope,
		operation.Priority, operation.State, operation.Attempt, snapshot, []byte(operation.Input), operation.Error,
		operation.CreatedAt.UnixMilli(), operation.UpdatedAt.UnixMilli(), operation.Snapshot.Executor,
		operation.RunID, operation.RunPolicyVersion)
	if err != nil {
		return model.Operation{}, fmt.Errorf("insert operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.Operation{}, fmt.Errorf("inspect operation insert: %w", err)
	}
	if rows == 0 {
		var storedDigest string
		if err := tx.QueryRowContext(ctx, `SELECT content_digest FROM operations WHERE id = ?`, operation.ID).Scan(&storedDigest); err != nil {
			return model.Operation{}, fmt.Errorf("inspect existing operation: %w", err)
		}
		if storedDigest != digest {
			return model.Operation{}, fmt.Errorf("operation %q: %w", operation.ID, model.ErrIdempotencyConflict)
		}
		return getOperationTx(ctx, tx, operation.ID)
	}
	for _, dependency := range operation.DependsOn {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO operation_dependencies (operation_id, dependency_id) VALUES (?, ?)`,
			operation.ID, dependency); err != nil {
			return model.Operation{}, fmt.Errorf("link operation dependency %q: %w", dependency, err)
		}
	}
	if operation.RunID != "" {
		if err := appendRunEventTx(ctx, tx, operation.RunID, model.RunEventOperationCreated, struct {
			OperationID   string `json:"operation_id"`
			PolicyVersion int64  `json:"policy_version"`
		}{operation.ID, operation.RunPolicyVersion}, operation.CreatedAt); err != nil {
			return model.Operation{}, err
		}
	}
	return operation, nil
}

func (s *Store) GetOperation(ctx context.Context, id string) (model.Operation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE id = ?`, id)
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Operation{}, model.ErrNotFound
	}
	if err != nil {
		return model.Operation{}, fmt.Errorf("read operation: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, s.db, operation.ID)
	if err != nil {
		return model.Operation{}, err
	}
	return operation, nil
}

func (s *Store) SetOperationPriority(ctx context.Context, id string, priority int, now time.Time) (model.Operation, error) {
	if strings.TrimSpace(id) == "" || now.IsZero() {
		return model.Operation{}, fmt.Errorf("operation id and time are required: %w", model.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Operation{}, fmt.Errorf("begin operation reprioritization: %w", err)
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `
		UPDATE operations SET priority = ?, updated_at_unix_ms = ?
		WHERE id = ? AND state IN (?, ?)
		RETURNING `+operationColumns,
		priority, now.UnixMilli(), id, model.OperationQueued, model.OperationPaused)
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Operation{}, model.ErrStateConflict
	}
	if err != nil {
		return model.Operation{}, fmt.Errorf("reprioritize operation: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, tx, operation.ID)
	if err != nil {
		return model.Operation{}, err
	}
	payload, err := json.Marshal(map[string]any{"priority": priority})
	if err != nil {
		return model.Operation{}, fmt.Errorf("encode operation priority event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, model.OperationEvent{
		OperationID: id, StepID: "priority", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("priority:%d:%d", priority, now.UnixNano()),
		Kind:           "operation.priority_changed", Payload: payload, CreatedAt: now,
	}); err != nil {
		return model.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Operation{}, fmt.Errorf("commit operation reprioritization: %w", err)
	}
	return operation, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

type operationDependencyQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// blockedDependency 记录一个 queued Operation 被哪个不可能再成功的依赖挡住。
type blockedDependency struct {
	operationID     string
	dependencyID    string
	dependencyState model.OperationState
}

// blockedByFailedDependencies 找出被 failed / cancelled / stale 依赖挡住的 queued
// Operation。领取门控只放行依赖全部 succeeded 的任务，因此这些后继会永久停在队列里，
// 必须显式报告而不是伪装成"无任务可领"。id 为空表示检查整个队列。
func blockedByFailedDependencies(ctx context.Context, query operationDependencyQuery, id string) ([]blockedDependency, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT edge.operation_id, edge.dependency_id, dependency.state
		FROM operation_dependencies edge
		JOIN operations blocked ON blocked.id = edge.operation_id
		JOIN operations dependency ON dependency.id = edge.dependency_id
		WHERE blocked.state = ?
			AND dependency.state IN (?, ?, ?)
			AND (? = '' OR blocked.id = ?)
		ORDER BY edge.operation_id, edge.dependency_id`,
		model.OperationQueued,
		model.OperationFailed, model.OperationCancelled, model.OperationStale,
		id, id)
	if err != nil {
		return nil, fmt.Errorf("read blocked operation dependencies: %w", err)
	}
	defer rows.Close()
	blocked := make([]blockedDependency, 0)
	for rows.Next() {
		var entry blockedDependency
		if err := rows.Scan(&entry.operationID, &entry.dependencyID, &entry.dependencyState); err != nil {
			return nil, fmt.Errorf("scan blocked operation dependency: %w", err)
		}
		blocked = append(blocked, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blocked operation dependencies: %w", err)
	}
	return blocked, nil
}

func describeBlockedDependencies(blocked []blockedDependency) string {
	const shown = 3
	parts := make([]string, 0, shown+1)
	for i, entry := range blocked {
		if i == shown {
			parts = append(parts, fmt.Sprintf("and %d more", len(blocked)-shown))
			break
		}
		parts = append(parts, fmt.Sprintf("%s waits on %s (%s)", entry.operationID, entry.dependencyID, entry.dependencyState))
	}
	return strings.Join(parts, "; ")
}

func loadOperationDependencies(ctx context.Context, query operationDependencyQuery, operationID string) ([]string, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT dependency_id FROM operation_dependencies
		WHERE operation_id = ? ORDER BY dependency_id`, operationID)
	if err != nil {
		return nil, fmt.Errorf("read operation dependencies: %w", err)
	}
	defer rows.Close()
	dependencies := make([]string, 0)
	for rows.Next() {
		var dependency string
		if err := rows.Scan(&dependency); err != nil {
			return nil, fmt.Errorf("scan operation dependency: %w", err)
		}
		dependencies = append(dependencies, dependency)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operation dependencies: %w", err)
	}
	return dependencies, nil
}

func scanOperation(scanner rowScanner) (model.Operation, error) {
	var operation model.Operation
	var snapshot []byte
	var input []byte
	var leaseOwner sql.NullString
	var leaseUntil sql.NullInt64
	var createdAt, updatedAt int64
	err := scanner.Scan(
		&operation.ID, &operation.Kind, &operation.Target.Kind, &operation.Target.ID, &operation.Target.Scope,
		&operation.Priority, &operation.State, &operation.Attempt, &snapshot, &input, &operation.Error, &operation.FailureCode,
		&leaseOwner, &leaseUntil, &createdAt, &updatedAt, &operation.RunID, &operation.RunPolicyVersion,
	)
	if err != nil {
		return model.Operation{}, err
	}
	if err := json.Unmarshal(snapshot, &operation.Snapshot); err != nil {
		return model.Operation{}, fmt.Errorf("decode operation snapshot: %w", err)
	}
	operation.Input = append(json.RawMessage(nil), input...)
	operation.CreatedAt = time.UnixMilli(createdAt).UTC()
	operation.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	if leaseOwner.Valid {
		operation.LeaseOwner = leaseOwner.String
	}
	if leaseUntil.Valid {
		value := time.UnixMilli(leaseUntil.Int64).UTC()
		operation.LeaseUntil = &value
	}
	if err := operation.Validate(); err != nil {
		return model.Operation{}, fmt.Errorf("stored operation is invalid: %w", err)
	}
	return operation, nil
}

func operationDigest(operation model.Operation) (string, []byte, error) {
	snapshot, err := json.Marshal(operation.Snapshot)
	if err != nil {
		return "", nil, fmt.Errorf("marshal execution snapshot: %w", err)
	}
	payload, err := json.Marshal(operation)
	if err != nil {
		return "", nil, fmt.Errorf("marshal operation digest: %w", err)
	}
	return model.Digest(payload), snapshot, nil
}
