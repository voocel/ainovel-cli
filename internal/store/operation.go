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

	"github.com/voocel/ainovel-cli/internal/domain"
)

const operationColumns = `
	id, kind, target_kind, target_id, target_scope, priority, state,
	attempt, execution_snapshot, input, error, lease_owner, lease_until_unix_ms,
	created_at_unix_ms, updated_at_unix_ms, run_id, run_policy_version`

// MaxOperationAttempts 是 lease 过期后自动重排的尝试上限。达到上限的 Operation
// 转入 failed 而不是继续排队：无进展的重试必须有明确上界，否则模型调用持续超时
// 会让同一任务被无限重领并持续计费（v1-architecture-plan §6.2）。
// 用户显式重排（failed → queued）不受此限，那是有人在场的决定。
const MaxOperationAttempts = 5

func (s *Store) CreateOperation(ctx context.Context, operation domain.Operation) (domain.Operation, error) {
	if operation.State != domain.OperationQueued {
		return domain.Operation{}, fmt.Errorf("new operation must be queued: %w", domain.ErrInvalid)
	}
	if operation.Kind.RequiresCreationRun() && strings.TrimSpace(operation.RunID) == "" {
		return domain.Operation{}, fmt.Errorf("%s operation requires a creation run: %w", operation.Kind, domain.ErrInvalid)
	}
	if operation.RunPolicyVersion != 0 {
		return domain.Operation{}, fmt.Errorf("run policy version is assigned by the store: %w", domain.ErrInvalid)
	}
	if err := operation.Validate(); err != nil {
		return domain.Operation{}, err
	}
	operation.DependsOn = append([]string(nil), operation.DependsOn...)
	slices.Sort(operation.DependsOn)
	digest, snapshot, err := operationDigest(operation)
	if err != nil {
		return domain.Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, fmt.Errorf("begin operation creation: %w", err)
	}
	defer tx.Rollback()
	var existingDigest string
	if err := tx.QueryRowContext(ctx,
		`SELECT content_digest FROM operations WHERE id = ?`, operation.ID,
	).Scan(&existingDigest); err == nil {
		if existingDigest != digest {
			return domain.Operation{}, fmt.Errorf("operation %q: %w", operation.ID, ErrIdempotencyConflict)
		}
		if err := tx.Commit(); err != nil {
			return domain.Operation{}, fmt.Errorf("commit idempotent operation creation: %w", err)
		}
		return s.GetOperation(ctx, operation.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.Operation{}, fmt.Errorf("inspect existing operation: %w", err)
	}
	// Run 归属与策略版本在同一事务内校验并落行（§6.3）：策略版本 = 创建时刻
	// 最近一次非 Operation 事件的序号。
	// 策略版本不参与幂等 digest——重入时事件日志可能已推进，身份以调用方输入为准。
	if operation.RunID != "" {
		var runProjectID string
		var runState domain.CreationRunState
		if err := tx.QueryRowContext(ctx, `
			SELECT project_id, state FROM creation_runs WHERE id = ?`, operation.RunID,
		).Scan(&runProjectID, &runState); errors.Is(err, sql.ErrNoRows) {
			return domain.Operation{}, fmt.Errorf("creation run %q: %w", operation.RunID, ErrNotFound)
		} else if err != nil {
			return domain.Operation{}, fmt.Errorf("read creation run ownership: %w", err)
		}
		if operation.Target.Kind != domain.AuthorityProject || operation.Target.ID != runProjectID {
			return domain.Operation{}, fmt.Errorf(
				"operation target does not match creation run project %q: %w", runProjectID, domain.ErrInvalid,
			)
		}
		switch runState {
		case domain.RunRunning, domain.RunWaitingUser, domain.RunPaused:
		default:
			return domain.Operation{}, fmt.Errorf(
				"creation run %q is %s: %w", operation.RunID, runState, ErrStateConflict,
			)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(sequence), 0) FROM creation_run_events
			WHERE run_id = ? AND kind != ?`,
			operation.RunID, domain.RunEventOperationCreated).Scan(&operation.RunPolicyVersion); err != nil {
			return domain.Operation{}, fmt.Errorf("read run policy version: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO operations (
			id, content_digest, kind, target_kind, target_id, target_scope,
			priority, state, attempt, execution_snapshot, input, error,
			created_at_unix_ms, updated_at_unix_ms, model_config_digest, run_id, run_policy_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		operation.ID, digest, operation.Kind, operation.Target.Kind, operation.Target.ID, operation.Target.Scope,
		operation.Priority, operation.State, operation.Attempt, snapshot, []byte(operation.Input), operation.Error,
		operation.CreatedAt.UnixMilli(), operation.UpdatedAt.UnixMilli(), operation.Snapshot.ModelConfigDigest,
		operation.RunID, operation.RunPolicyVersion)
	if err != nil {
		return domain.Operation{}, fmt.Errorf("insert operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Operation{}, fmt.Errorf("inspect operation insert: %w", err)
	}
	if rows == 0 {
		var storedDigest string
		if err := tx.QueryRowContext(ctx, `SELECT content_digest FROM operations WHERE id = ?`, operation.ID).Scan(&storedDigest); err != nil {
			return domain.Operation{}, fmt.Errorf("inspect existing operation: %w", err)
		}
		if storedDigest != digest {
			return domain.Operation{}, fmt.Errorf("operation %q: %w", operation.ID, ErrIdempotencyConflict)
		}
		if err := tx.Commit(); err != nil {
			return domain.Operation{}, fmt.Errorf("commit idempotent operation creation: %w", err)
		}
		return s.GetOperation(ctx, operation.ID)
	}
	for _, dependency := range operation.DependsOn {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO operation_dependencies (operation_id, dependency_id) VALUES (?, ?)`,
			operation.ID, dependency); err != nil {
			return domain.Operation{}, fmt.Errorf("link operation dependency %q: %w", dependency, err)
		}
	}
	if operation.RunID != "" {
		if err := appendRunEventTx(ctx, tx, operation.RunID, domain.RunEventOperationCreated, struct {
			OperationID   string `json:"operation_id"`
			PolicyVersion int64  `json:"policy_version"`
		}{operation.ID, operation.RunPolicyVersion}, operation.CreatedAt); err != nil {
			return domain.Operation{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Operation{}, fmt.Errorf("commit operation creation: %w", err)
	}
	return operation, nil
}

func (s *Store) GetOperation(ctx context.Context, id string) (domain.Operation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE id = ?`, id)
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Operation{}, ErrNotFound
	}
	if err != nil {
		return domain.Operation{}, fmt.Errorf("read operation: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, s.db, operation.ID)
	if err != nil {
		return domain.Operation{}, err
	}
	return operation, nil
}

func (s *Store) ClaimNextOperation(ctx context.Context, workerID string, leaseDuration time.Duration, now time.Time) (domain.Operation, error) {
	return s.claimOperation(ctx, "", workerID, "", leaseDuration, now)
}

func (s *Store) ClaimNextOperationForModel(
	ctx context.Context,
	workerID, modelConfigDigest string,
	leaseDuration time.Duration,
	now time.Time,
) (domain.Operation, error) {
	if strings.TrimSpace(modelConfigDigest) == "" {
		return domain.Operation{}, fmt.Errorf("model config digest is required: %w", domain.ErrInvalid)
	}
	return s.claimOperation(ctx, "", workerID, modelConfigDigest, leaseDuration, now)
}

func (s *Store) ClaimOperationForModel(
	ctx context.Context,
	id, workerID, modelConfigDigest string,
	leaseDuration time.Duration,
	now time.Time,
) (domain.Operation, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(modelConfigDigest) == "" {
		return domain.Operation{}, fmt.Errorf("operation and model config digest are required: %w", domain.ErrInvalid)
	}
	return s.claimOperation(ctx, id, workerID, modelConfigDigest, leaseDuration, now)
}

func (s *Store) claimOperation(
	ctx context.Context,
	id, workerID, modelConfigDigest string,
	leaseDuration time.Duration,
	now time.Time,
) (domain.Operation, error) {
	if strings.TrimSpace(workerID) == "" || leaseDuration <= 0 || now.IsZero() {
		return domain.Operation{}, fmt.Errorf("worker, positive lease duration and time are required: %w", domain.ErrInvalid)
	}
	leaseUntil := now.Add(leaseDuration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, fmt.Errorf("begin claim operation: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		WITH next AS (
			SELECT candidate.id FROM operations candidate
			WHERE candidate.state = ?
				AND (? = '' OR candidate.id = ?)
				AND (? = '' OR candidate.model_config_digest = ?)
				AND NOT EXISTS (
					SELECT 1
					FROM operation_dependencies edge
					JOIN operations dependency ON dependency.id = edge.dependency_id
					WHERE edge.operation_id = candidate.id AND dependency.state <> ?
				)
			ORDER BY candidate.priority DESC, candidate.created_at_unix_ms, candidate.id
			LIMIT 1
		)
		UPDATE operations
		SET state = ?, attempt = attempt + 1, lease_owner = ?, lease_until_unix_ms = ?, updated_at_unix_ms = ?, error = ''
		WHERE id = (SELECT id FROM next) AND state = ?
		RETURNING `+operationColumns,
		domain.OperationQueued, id, id, modelConfigDigest, modelConfigDigest, domain.OperationSucceeded,
		domain.OperationRunning, workerID, leaseUntil.UnixMilli(), now.UnixMilli(), domain.OperationQueued)
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		// 领不到任务有两种成因："队列已空"和"后继全被不可能成功的依赖挡住"。
		// 二者都表现为无任务可领，后者却是必须暴露的故障——否则一次失败会让
		// 整条后续队列静默停摆，看上去与"全部完成"无法区分。
		blocked, blockedErr := blockedByFailedDependencies(ctx, tx, id)
		if blockedErr != nil {
			return domain.Operation{}, blockedErr
		}
		if len(blocked) > 0 {
			return domain.Operation{}, fmt.Errorf("%w: %s", ErrDependencyBlocked, describeBlockedDependencies(blocked))
		}
		if id != "" {
			if _, lookupErr := s.GetOperation(ctx, id); lookupErr != nil {
				return domain.Operation{}, lookupErr
			}
			return domain.Operation{}, fmt.Errorf("operation %q is not runnable: %w", id, ErrStateConflict)
		}
		return domain.Operation{}, ErrNotFound
	}
	if err != nil {
		return domain.Operation{}, fmt.Errorf("claim operation: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, tx, operation.ID)
	if err != nil {
		return domain.Operation{}, err
	}
	payload, err := json.Marshal(map[string]any{"worker": workerID, "lease_until": leaseUntil})
	if err != nil {
		return domain.Operation{}, fmt.Errorf("encode operation claim event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, domain.OperationEvent{
		OperationID: operation.ID, StepID: "claim", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("claim:%d", leaseUntil.UnixNano()),
		Kind:           "operation.claimed", Payload: payload, CreatedAt: now,
	}); err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Operation{}, fmt.Errorf("commit operation claim: %w", err)
	}
	return operation, nil
}

func (s *Store) SetOperationPriority(ctx context.Context, id string, priority int, now time.Time) (domain.Operation, error) {
	if strings.TrimSpace(id) == "" || now.IsZero() {
		return domain.Operation{}, fmt.Errorf("operation id and time are required: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, fmt.Errorf("begin operation reprioritization: %w", err)
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `
		UPDATE operations SET priority = ?, updated_at_unix_ms = ?
		WHERE id = ? AND state IN (?, ?)
		RETURNING `+operationColumns,
		priority, now.UnixMilli(), id, domain.OperationQueued, domain.OperationPaused)
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Operation{}, ErrStateConflict
	}
	if err != nil {
		return domain.Operation{}, fmt.Errorf("reprioritize operation: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, tx, operation.ID)
	if err != nil {
		return domain.Operation{}, err
	}
	payload, err := json.Marshal(map[string]any{"priority": priority})
	if err != nil {
		return domain.Operation{}, fmt.Errorf("encode operation priority event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, domain.OperationEvent{
		OperationID: id, StepID: "priority", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("priority:%d:%d", priority, now.UnixNano()),
		Kind:           "operation.priority_changed", Payload: payload, CreatedAt: now,
	}); err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Operation{}, fmt.Errorf("commit operation reprioritization: %w", err)
	}
	return operation, nil
}

func (s *Store) RenewOperationLease(ctx context.Context, id, workerID string, leaseDuration time.Duration, now time.Time) (domain.Operation, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(workerID) == "" || leaseDuration <= 0 || now.IsZero() {
		return domain.Operation{}, fmt.Errorf("operation, worker, positive lease duration and time are required: %w", domain.ErrInvalid)
	}
	leaseUntil := now.Add(leaseDuration)
	row := s.db.QueryRowContext(ctx, `
		UPDATE operations
		SET lease_until_unix_ms = ?, updated_at_unix_ms = ?
		WHERE id = ? AND state = ? AND lease_owner = ? AND lease_until_unix_ms >= ?
		RETURNING `+operationColumns,
		leaseUntil.UnixMilli(), now.UnixMilli(), id, domain.OperationRunning, workerID, now.UnixMilli())
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Operation{}, ErrStateConflict
	}
	if err != nil {
		return domain.Operation{}, fmt.Errorf("renew operation lease: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, s.db, operation.ID)
	if err != nil {
		return domain.Operation{}, err
	}
	return operation, nil
}

// TransitionOperation 是用户控制入口（暂停、恢复、取消、裁决）：只按状态机校验，
// 不受执行归属限制——取消永远赢过在途执行，之后的收尾会被 ConcludeOperation 拒绝。
func (s *Store) TransitionOperation(ctx context.Context, id string, from, to domain.OperationState, message string, now time.Time) (domain.Operation, error) {
	return s.transitionOperation(ctx, id, from, to, message, now, 0)
}

// ConcludeOperation 是执行实例的收尾入口：只有持有当前 attempt 的执行者才能把
// running 推进到结局态。过期或被接替的实例拿到 ErrStateConflict，不能改写
// 后继尝试的状态（执行归属不变量）。
func (s *Store) ConcludeOperation(ctx context.Context, id string, attempt int, to domain.OperationState, message string, now time.Time) (domain.Operation, error) {
	if attempt <= 0 {
		return domain.Operation{}, fmt.Errorf("execution attempt is required: %w", domain.ErrInvalid)
	}
	return s.transitionOperation(ctx, id, domain.OperationRunning, to, message, now, attempt)
}

// AssertActiveAttempt 是执行侧在准备提案前的快速自检；受保护写入的真正围栏在各自事务内
// 完成（PutWorkspaceArtifact、SaveExecutionDerivedDocument、CommitExecutionProposal、ConcludeOperation）。
func (s *Store) AssertActiveAttempt(ctx context.Context, id string, attempt int) error {
	return assertActiveAttempt(ctx, s.db, id, attempt)
}

// assertActiveAttempt 在调用方的事务内校验归属，让检查与受保护写入原子化（D42）。
func assertActiveAttempt(ctx context.Context, query rowQuerier, id string, attempt int) error {
	var active bool
	err := query.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM operations WHERE id = ? AND state = ? AND attempt = ?)`,
		id, domain.OperationRunning, attempt).Scan(&active)
	if err != nil {
		return fmt.Errorf("check operation execution: %w", err)
	}
	if !active {
		return fmt.Errorf("operation %q attempt %d is no longer the active execution: %w", id, attempt, ErrStateConflict)
	}
	return nil
}

// transitionOperation 的 attempt 为 0 时不做执行归属围栏。
func (s *Store) transitionOperation(ctx context.Context, id string, from, to domain.OperationState, message string, now time.Time, attempt int) (domain.Operation, error) {
	if strings.TrimSpace(id) == "" || now.IsZero() {
		return domain.Operation{}, fmt.Errorf("operation id and time are required: %w", domain.ErrInvalid)
	}
	if to == domain.OperationRunning || !domain.CanTransitionOperation(from, to) {
		return domain.Operation{}, fmt.Errorf("operation transition %s -> %s: %w", from, to, ErrStateConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, fmt.Errorf("begin operation transition: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		UPDATE operations
		SET state = ?, error = ?, lease_owner = NULL, lease_until_unix_ms = NULL, updated_at_unix_ms = ?
		WHERE id = ? AND state = ? AND (? = 0 OR attempt = ?)
		RETURNING `+operationColumns,
		to, message, now.UnixMilli(), id, from, attempt, attempt)
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Operation{}, ErrStateConflict
	}
	if err != nil {
		return domain.Operation{}, fmt.Errorf("transition operation: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, tx, operation.ID)
	if err != nil {
		return domain.Operation{}, err
	}
	payload, err := json.Marshal(map[string]any{"from": from, "to": to, "message": message})
	if err != nil {
		return domain.Operation{}, fmt.Errorf("encode operation transition event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, domain.OperationEvent{
		OperationID: id, StepID: "transition", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("transition:%s:%s:%d", from, to, now.UnixNano()),
		Kind:           "operation.transitioned", Payload: payload, CreatedAt: now,
	}); err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Operation{}, fmt.Errorf("commit operation transition: %w", err)
	}
	return operation, nil
}

func (s *Store) RecoverExpiredOperations(ctx context.Context, now time.Time) ([]string, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("recovery time is required: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin expired operation recovery: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, attempt FROM operations
		WHERE state = ? AND lease_until_unix_ms < ?
		ORDER BY id`, domain.OperationRunning, now.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("find expired operations: %w", err)
	}
	type expiredOperation struct {
		id      string
		attempt int
	}
	var expired []expiredOperation
	for rows.Next() {
		var operation expiredOperation
		if err := rows.Scan(&operation.id, &operation.attempt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan expired operation: %w", err)
		}
		expired = append(expired, operation)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close expired operation rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired operations: %w", err)
	}

	ids := make([]string, 0, len(expired))
	for _, operation := range expired {
		id := operation.id
		ids = append(ids, id)
		recovered := domain.OperationQueued
		message := fmt.Sprintf("worker lease expired before %s; operation requeued", now.UTC().Format(time.RFC3339Nano))
		if operation.attempt >= MaxOperationAttempts {
			recovered = domain.OperationFailed
			message = fmt.Sprintf(
				"worker lease expired before %s after %d attempts (limit %d); operation failed instead of requeued",
				now.UTC().Format(time.RFC3339Nano), operation.attempt, MaxOperationAttempts,
			)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE operations
			SET state = ?, error = ?, lease_owner = NULL, lease_until_unix_ms = NULL, updated_at_unix_ms = ?
			WHERE id = ? AND state = ? AND lease_until_unix_ms < ?`,
			recovered, message, now.UnixMilli(), id, domain.OperationRunning, now.UnixMilli())
		if err != nil {
			return nil, fmt.Errorf("recover operation %q: %w", id, err)
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return nil, fmt.Errorf("recover operation %q: %w", id, ErrStateConflict)
		}
		payload, err := json.Marshal(map[string]any{"reason": message, "state": recovered})
		if err != nil {
			return nil, fmt.Errorf("encode lease recovery event: %w", err)
		}
		if _, err := appendEventTx(ctx, tx, domain.OperationEvent{
			OperationID: id, StepID: "lease-recovery", Attempt: operation.attempt,
			IdempotencyKey: fmt.Sprintf("lease-recovery:%d", now.UnixNano()),
			Kind:           "operation.lease_expired", Payload: payload, CreatedAt: now,
		}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit expired operation recovery: %w", err)
	}
	return ids, nil
}

// PutWorkspaceArtifact 只接受当前执行实例的写入：writerAttempt 必须等于 Operation
// 的当前 attempt，过期实例不能污染后继尝试的工作区。
func (s *Store) PutWorkspaceArtifact(ctx context.Context, artifact domain.WorkspaceArtifact, expectedVersion int64, writerAttempt int) (domain.WorkspaceArtifact, error) {
	if strings.TrimSpace(artifact.OperationID) == "" || strings.TrimSpace(artifact.Key) == "" || strings.TrimSpace(artifact.MediaType) == "" || len(artifact.Content) == 0 {
		return domain.WorkspaceArtifact{}, fmt.Errorf("artifact operation, key, media type and content are required: %w", domain.ErrInvalid)
	}
	if expectedVersion < 0 || artifact.UpdatedAt.IsZero() {
		return domain.WorkspaceArtifact{}, fmt.Errorf("artifact expected version and time are invalid: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.WorkspaceArtifact{}, fmt.Errorf("begin workspace write: %w", err)
	}
	defer tx.Rollback()

	var state domain.OperationState
	var attempt int
	if err := tx.QueryRowContext(ctx, `SELECT state, attempt FROM operations WHERE id = ?`, artifact.OperationID).Scan(&state, &attempt); errors.Is(err, sql.ErrNoRows) {
		return domain.WorkspaceArtifact{}, ErrNotFound
	} else if err != nil {
		return domain.WorkspaceArtifact{}, fmt.Errorf("read workspace operation: %w", err)
	}
	if state != domain.OperationRunning {
		return domain.WorkspaceArtifact{}, fmt.Errorf("operation %q is %s: %w", artifact.OperationID, state, ErrStateConflict)
	}
	if writerAttempt != attempt {
		return domain.WorkspaceArtifact{}, fmt.Errorf(
			"operation %q is on attempt %d, writer holds attempt %d: %w", artifact.OperationID, attempt, writerAttempt, ErrStateConflict)
	}

	var current int64
	err = tx.QueryRowContext(ctx, `
		SELECT version FROM operation_artifacts
		WHERE operation_id = ? AND artifact_key = ?`, artifact.OperationID, artifact.Key).Scan(&current)
	artifact.Digest = domain.Digest(artifact.Content)
	artifact.Version = expectedVersion + 1
	if errors.Is(err, sql.ErrNoRows) {
		if expectedVersion != 0 {
			return domain.WorkspaceArtifact{}, ErrWorkspaceConflict
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO operation_artifacts (
				operation_id, artifact_key, version, media_type, content, content_digest, updated_at_unix_ms
			) VALUES (?, ?, 1, ?, ?, ?, ?)`,
			artifact.OperationID, artifact.Key, artifact.MediaType, artifact.Content, artifact.Digest, artifact.UpdatedAt.UnixMilli()); err != nil {
			return domain.WorkspaceArtifact{}, fmt.Errorf("insert workspace artifact: %w", err)
		}
	} else if err != nil {
		return domain.WorkspaceArtifact{}, fmt.Errorf("read workspace artifact version: %w", err)
	} else {
		if current != expectedVersion {
			return domain.WorkspaceArtifact{}, fmt.Errorf("expected workspace version %d, current %d: %w", expectedVersion, current, ErrWorkspaceConflict)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE operation_artifacts
			SET version = ?, media_type = ?, content = ?, content_digest = ?, updated_at_unix_ms = ?
			WHERE operation_id = ? AND artifact_key = ? AND version = ?`,
			artifact.Version, artifact.MediaType, artifact.Content, artifact.Digest, artifact.UpdatedAt.UnixMilli(),
			artifact.OperationID, artifact.Key, expectedVersion)
		if err != nil {
			return domain.WorkspaceArtifact{}, fmt.Errorf("update workspace artifact: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return domain.WorkspaceArtifact{}, ErrWorkspaceConflict
		}
	}
	payload, err := json.Marshal(map[string]any{"key": artifact.Key, "version": artifact.Version, "digest": artifact.Digest})
	if err != nil {
		return domain.WorkspaceArtifact{}, fmt.Errorf("encode workspace event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, domain.OperationEvent{
		OperationID: artifact.OperationID, StepID: "workspace", Attempt: attempt,
		IdempotencyKey: fmt.Sprintf("workspace:%s:%d", artifact.Key, artifact.Version),
		Kind:           "workspace.artifact_written", Payload: payload, CreatedAt: artifact.UpdatedAt,
	}); err != nil {
		return domain.WorkspaceArtifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.WorkspaceArtifact{}, fmt.Errorf("commit workspace artifact: %w", err)
	}
	artifact.Content = append([]byte(nil), artifact.Content...)
	return artifact, nil
}

func (s *Store) GetWorkspaceArtifact(ctx context.Context, operationID, key string) (domain.WorkspaceArtifact, error) {
	var artifact domain.WorkspaceArtifact
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT version, media_type, content, content_digest, updated_at_unix_ms
		FROM operation_artifacts WHERE operation_id = ? AND artifact_key = ?`, operationID, key).
		Scan(&artifact.Version, &artifact.MediaType, &artifact.Content, &artifact.Digest, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkspaceArtifact{}, ErrNotFound
	}
	if err != nil {
		return domain.WorkspaceArtifact{}, fmt.Errorf("read workspace artifact: %w", err)
	}
	artifact.OperationID = operationID
	artifact.Key = key
	artifact.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	artifact.Content = append([]byte(nil), artifact.Content...)
	return artifact, nil
}

func (s *Store) ListWorkspaceArtifacts(ctx context.Context, operationID string) ([]domain.WorkspaceArtifact, error) {
	if strings.TrimSpace(operationID) == "" {
		return nil, fmt.Errorf("operation id is required: %w", domain.ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact_key, version, media_type, content, content_digest, updated_at_unix_ms
		FROM operation_artifacts WHERE operation_id = ? ORDER BY artifact_key`, operationID)
	if err != nil {
		return nil, fmt.Errorf("list workspace artifacts: %w", err)
	}
	defer rows.Close()
	artifacts := make([]domain.WorkspaceArtifact, 0)
	for rows.Next() {
		artifact := domain.WorkspaceArtifact{OperationID: operationID}
		var updatedAt int64
		if err := rows.Scan(
			&artifact.Key, &artifact.Version, &artifact.MediaType, &artifact.Content,
			&artifact.Digest, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan workspace artifact: %w", err)
		}
		artifact.UpdatedAt = time.UnixMilli(updatedAt).UTC()
		artifact.Content = append([]byte(nil), artifact.Content...)
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace artifacts: %w", err)
	}
	return artifacts, nil
}

func (s *Store) CopyWorkspaceArtifacts(ctx context.Context, fromOperationID, toOperationID string, at time.Time) error {
	if strings.TrimSpace(fromOperationID) == "" || strings.TrimSpace(toOperationID) == "" ||
		fromOperationID == toOperationID || at.IsZero() {
		return fmt.Errorf("distinct source, target and copy time are required: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace copy: %w", err)
	}
	defer tx.Rollback()
	var targetState domain.OperationState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM operations WHERE id = ?`, toOperationID).Scan(&targetState); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read workspace copy target: %w", err)
	}
	if targetState != domain.OperationQueued {
		return fmt.Errorf("workspace copy target %q is %s: %w", toOperationID, targetState, ErrStateConflict)
	}
	var sourceExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM operations WHERE id = ?`, fromOperationID).Scan(&sourceExists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read workspace copy source: %w", err)
	}
	var targetArtifacts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operation_artifacts WHERE operation_id = ?`, toOperationID).Scan(&targetArtifacts); err != nil {
		return fmt.Errorf("inspect workspace copy target: %w", err)
	}
	if targetArtifacts != 0 {
		// 幂等重入：源工件必须已全部一致地存在于目标；目标可另有播种的反馈工件。
		var sourceArtifacts, matchingArtifacts int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operation_artifacts WHERE operation_id = ?`, fromOperationID).Scan(&sourceArtifacts); err != nil {
			return fmt.Errorf("inspect workspace copy source: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM operation_artifacts target
			JOIN operation_artifacts source
			  ON source.operation_id = ? AND source.artifact_key = target.artifact_key
			 AND source.version = target.version AND source.media_type = target.media_type
			 AND source.content_digest = target.content_digest AND source.content = target.content
			WHERE target.operation_id = ?`, fromOperationID, toOperationID).Scan(&matchingArtifacts); err != nil {
			return fmt.Errorf("compare copied workspace: %w", err)
		}
		if matchingArtifacts == sourceArtifacts {
			return tx.Commit()
		}
		return fmt.Errorf("workspace copy target %q contains different artifacts: %w", toOperationID, ErrStateConflict)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO operation_artifacts (
			operation_id, artifact_key, version, media_type, content, content_digest, updated_at_unix_ms
		)
		SELECT ?, artifact_key, version, media_type, content, content_digest, ?
		FROM operation_artifacts WHERE operation_id = ?`, toOperationID, at.UnixMilli(), fromOperationID)
	if err != nil {
		return fmt.Errorf("copy workspace artifacts: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect workspace copy: %w", err)
	}
	payload, err := json.Marshal(map[string]any{"source_operation_id": fromOperationID, "artifact_count": count})
	if err != nil {
		return fmt.Errorf("encode workspace copy event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, domain.OperationEvent{
		OperationID: toOperationID, StepID: "workspace.seed", Attempt: 0,
		IdempotencyKey: "workspace-seed:" + fromOperationID,
		Kind:           "workspace.seeded", Payload: payload, CreatedAt: at,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace copy: %w", err)
	}
	return nil
}

func (s *Store) AppendOperationEvent(ctx context.Context, event domain.OperationEvent) (domain.OperationEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.OperationEvent{}, fmt.Errorf("begin operation event: %w", err)
	}
	defer tx.Rollback()
	event, err = appendEventTx(ctx, tx, event)
	if err != nil {
		return domain.OperationEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.OperationEvent{}, fmt.Errorf("commit operation event: %w", err)
	}
	return event, nil
}

func (s *Store) ListOperationEvents(ctx context.Context, operationID string) ([]domain.OperationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, step_id, attempt, idempotency_key, kind, payload, created_at_unix_ms
		FROM operation_events WHERE operation_id = ? ORDER BY sequence`, operationID)
	if err != nil {
		return nil, fmt.Errorf("list operation events: %w", err)
	}
	defer rows.Close()
	events := make([]domain.OperationEvent, 0)
	for rows.Next() {
		var event domain.OperationEvent
		var createdAt int64
		if err := rows.Scan(&event.Sequence, &event.StepID, &event.Attempt, &event.IdempotencyKey, &event.Kind, &event.Payload, &createdAt); err != nil {
			return nil, fmt.Errorf("scan operation event: %w", err)
		}
		event.OperationID = operationID
		event.CreatedAt = time.UnixMilli(createdAt).UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operation events: %w", err)
	}
	return events, nil
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
	dependencyState domain.OperationState
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
		domain.OperationQueued,
		domain.OperationFailed, domain.OperationCancelled, domain.OperationStale,
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

func scanOperation(scanner rowScanner) (domain.Operation, error) {
	var operation domain.Operation
	var snapshot []byte
	var input []byte
	var leaseOwner sql.NullString
	var leaseUntil sql.NullInt64
	var createdAt, updatedAt int64
	err := scanner.Scan(
		&operation.ID, &operation.Kind, &operation.Target.Kind, &operation.Target.ID, &operation.Target.Scope,
		&operation.Priority, &operation.State, &operation.Attempt, &snapshot, &input, &operation.Error,
		&leaseOwner, &leaseUntil, &createdAt, &updatedAt, &operation.RunID, &operation.RunPolicyVersion,
	)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := json.Unmarshal(snapshot, &operation.Snapshot); err != nil {
		return domain.Operation{}, fmt.Errorf("decode operation snapshot: %w", err)
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
		return domain.Operation{}, fmt.Errorf("stored operation is invalid: %w", err)
	}
	return operation, nil
}

func operationDigest(operation domain.Operation) (string, []byte, error) {
	snapshot, err := json.Marshal(operation.Snapshot)
	if err != nil {
		return "", nil, fmt.Errorf("marshal execution snapshot: %w", err)
	}
	payload, err := json.Marshal(operation)
	if err != nil {
		return "", nil, fmt.Errorf("marshal operation digest: %w", err)
	}
	return domain.Digest(payload), snapshot, nil
}

func appendEventTx(ctx context.Context, tx *sql.Tx, event domain.OperationEvent) (domain.OperationEvent, error) {
	if strings.TrimSpace(event.OperationID) == "" || strings.TrimSpace(event.StepID) == "" ||
		strings.TrimSpace(event.IdempotencyKey) == "" || strings.TrimSpace(event.Kind) == "" ||
		event.Attempt < 0 || event.CreatedAt.IsZero() || len(event.Payload) == 0 || !json.Valid(event.Payload) {
		return domain.OperationEvent{}, fmt.Errorf("operation event fields are invalid: %w", domain.ErrInvalid)
	}
	digest := domain.Digest(event.Payload)

	var existing domain.OperationEvent
	var existingPayload []byte
	var existingDigest string
	var existingCreated int64
	err := tx.QueryRowContext(ctx, `
		SELECT sequence, step_id, attempt, kind, payload, payload_digest, created_at_unix_ms
		FROM operation_events WHERE operation_id = ? AND idempotency_key = ?`,
		event.OperationID, event.IdempotencyKey).
		Scan(&existing.Sequence, &existing.StepID, &existing.Attempt, &existing.Kind, &existingPayload, &existingDigest, &existingCreated)
	if err == nil {
		if existingDigest != digest || existing.StepID != event.StepID || existing.Attempt != event.Attempt || existing.Kind != event.Kind {
			return domain.OperationEvent{}, fmt.Errorf("event %q: %w", event.IdempotencyKey, ErrIdempotencyConflict)
		}
		existing.OperationID = event.OperationID
		existing.IdempotencyKey = event.IdempotencyKey
		existing.Payload = append(json.RawMessage(nil), existingPayload...)
		existing.CreatedAt = time.UnixMilli(existingCreated).UTC()
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.OperationEvent{}, fmt.Errorf("find operation event: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM operation_events WHERE operation_id = ?`, event.OperationID).
		Scan(&event.Sequence); err != nil {
		return domain.OperationEvent{}, fmt.Errorf("allocate operation event sequence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO operation_events (
			operation_id, sequence, step_id, attempt, idempotency_key,
			kind, payload, payload_digest, created_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.OperationID, event.Sequence, event.StepID, event.Attempt, event.IdempotencyKey,
		event.Kind, []byte(event.Payload), digest, event.CreatedAt.UnixMilli()); err != nil {
		return domain.OperationEvent{}, fmt.Errorf("insert operation event: %w", err)
	}
	return event, nil
}
