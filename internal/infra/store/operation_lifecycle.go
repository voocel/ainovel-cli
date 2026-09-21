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

// 任务生命周期：领取、续租、状态转移、过期回收与失败计数；写入都在 attempt 围栏内（D42）。

func (s *Store) ClaimNextOperation(ctx context.Context, workerID string, leaseDuration time.Duration, now time.Time) (model.Operation, error) {
	return s.claimOperation(ctx, "", workerID, nil, leaseDuration, now)
}

// ClaimNextOperationForExecutor 只领取按该执行器身份冻结的任务（D45）。
func (s *Store) ClaimNextOperationForExecutor(
	ctx context.Context,
	workerID, executor string,
	leaseDuration time.Duration,
	now time.Time,
) (model.Operation, error) {
	if strings.TrimSpace(executor) == "" {
		return model.Operation{}, fmt.Errorf("executor identity is required: %w", model.ErrInvalid)
	}
	return s.claimOperation(ctx, "", workerID, []string{executor}, leaseDuration, now)
}

func (s *Store) ClaimOperationForExecutor(
	ctx context.Context,
	id, workerID, executor string,
	leaseDuration time.Duration,
	now time.Time,
) (model.Operation, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(executor) == "" {
		return model.Operation{}, fmt.Errorf("operation and executor identity are required: %w", model.ErrInvalid)
	}
	return s.claimOperation(ctx, id, workerID, []string{executor}, leaseDuration, now)
}

// ClaimNextOperationForExecutors preserves queue priority across the configured executors.
func (s *Store) ClaimNextOperationForExecutors(ctx context.Context, workerID string, executors []string, leaseDuration time.Duration, now time.Time) (model.Operation, error) {
	if len(executors) == 0 {
		return model.Operation{}, fmt.Errorf("executor identities are required: %w", model.ErrInvalid)
	}
	for _, executor := range executors {
		if strings.TrimSpace(executor) == "" {
			return model.Operation{}, fmt.Errorf("executor identity is required: %w", model.ErrInvalid)
		}
	}
	return s.claimOperation(ctx, "", workerID, executors, leaseDuration, now)
}

func (s *Store) claimOperation(
	ctx context.Context,
	id, workerID string,
	executors []string,
	leaseDuration time.Duration,
	now time.Time,
) (model.Operation, error) {
	executorFilter := ""
	if executors != nil {
		encoded, err := json.Marshal(executors)
		if err != nil {
			return model.Operation{}, err
		}
		executorFilter = string(encoded)
	}
	if strings.TrimSpace(workerID) == "" || leaseDuration <= 0 || now.IsZero() {
		return model.Operation{}, fmt.Errorf("worker, positive lease duration and time are required: %w", model.ErrInvalid)
	}
	leaseUntil := now.Add(leaseDuration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Operation{}, fmt.Errorf("begin claim operation: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		WITH next AS (
			SELECT candidate.id FROM operations candidate
			WHERE candidate.state = ?
				AND (? = '' OR candidate.id = ?)
				AND (? = '' OR candidate.executor IN (SELECT value FROM json_each(?)))
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
		model.OperationQueued, id, id, executorFilter, executorFilter, model.OperationSucceeded,
		model.OperationRunning, workerID, leaseUntil.UnixMilli(), now.UnixMilli(), model.OperationQueued)
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		// 领不到任务有两种成因："队列已空"和"后继全被不可能成功的依赖挡住"。
		// 二者都表现为无任务可领，后者却是必须暴露的故障——否则一次失败会让
		// 整条后续队列静默停摆，看上去与"全部完成"无法区分。
		blocked, blockedErr := blockedByFailedDependencies(ctx, tx, id)
		if blockedErr != nil {
			return model.Operation{}, blockedErr
		}
		if len(blocked) > 0 {
			return model.Operation{}, fmt.Errorf("%w: %s", model.ErrDependencyBlocked, describeBlockedDependencies(blocked))
		}
		if id != "" {
			if _, lookupErr := s.GetOperation(ctx, id); lookupErr != nil {
				return model.Operation{}, lookupErr
			}
			return model.Operation{}, fmt.Errorf("operation %q is not runnable: %w", id, model.ErrStateConflict)
		}
		return model.Operation{}, model.ErrNotFound
	}
	if err != nil {
		return model.Operation{}, fmt.Errorf("claim operation: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, tx, operation.ID)
	if err != nil {
		return model.Operation{}, err
	}
	payload, err := json.Marshal(map[string]any{"worker": workerID, "lease_until": leaseUntil})
	if err != nil {
		return model.Operation{}, fmt.Errorf("encode operation claim event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, model.OperationEvent{
		OperationID: operation.ID, StepID: "claim", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("claim:%d", leaseUntil.UnixNano()),
		Kind:           "operation.claimed", Payload: payload, CreatedAt: now,
	}); err != nil {
		return model.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Operation{}, fmt.Errorf("commit operation claim: %w", err)
	}
	return operation, nil
}

func (s *Store) RenewOperationLease(ctx context.Context, id, workerID string, attempt int, leaseDuration time.Duration, now time.Time) (model.Operation, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(workerID) == "" || attempt <= 0 || leaseDuration <= 0 || now.IsZero() {
		return model.Operation{}, fmt.Errorf("operation, worker, positive attempt, lease duration and time are required: %w", model.ErrInvalid)
	}
	leaseUntil := now.Add(leaseDuration)
	row := s.db.QueryRowContext(ctx, `
		UPDATE operations
		SET lease_until_unix_ms = ?, updated_at_unix_ms = ?
		WHERE id = ? AND state = ? AND lease_owner = ? AND attempt = ? AND lease_until_unix_ms >= ?
		RETURNING `+operationColumns,
		leaseUntil.UnixMilli(), now.UnixMilli(), id, model.OperationRunning, workerID, attempt, now.UnixMilli())
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Operation{}, model.ErrStateConflict
	}
	if err != nil {
		return model.Operation{}, fmt.Errorf("renew operation lease: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, s.db, operation.ID)
	if err != nil {
		return model.Operation{}, err
	}
	return operation, nil
}

// TransitionOperation 是用户控制入口（暂停、恢复、取消、裁决）：只按状态机校验，
// 不受执行归属限制——取消永远赢过在途执行，之后的收尾会被 ConcludeOperation 拒绝。
func (s *Store) TransitionOperation(ctx context.Context, id string, from, to model.OperationState, message string, now time.Time) (model.Operation, error) {
	return s.transitionOperation(ctx, id, from, to, message, "", now, 0)
}

// ConcludeOperation 是执行实例的收尾入口：只有持有当前 attempt 的执行者才能把
// running 推进到结局态。过期或被接替的实例拿到 model.ErrStateConflict，不能改写
// 后继尝试的状态（执行归属不变量）。
func (s *Store) ConcludeOperation(ctx context.Context, id string, attempt int, to model.OperationState, message string, now time.Time) (model.Operation, error) {
	if attempt <= 0 {
		return model.Operation{}, fmt.Errorf("execution attempt is required: %w", model.ErrInvalid)
	}
	return s.transitionOperation(ctx, id, model.OperationRunning, to, message, "", now, attempt)
}

// FailOperation 是带失败码的收尾（D46）：围栏同 ConcludeOperation，失败码随下一次
// 状态变化（用户重排或取消）清空。
func (s *Store) FailOperation(ctx context.Context, id string, attempt int, code model.FailureCode, message string, now time.Time) (model.Operation, error) {
	if attempt <= 0 {
		return model.Operation{}, fmt.Errorf("execution attempt is required: %w", model.ErrInvalid)
	}
	return s.transitionOperation(ctx, id, model.OperationRunning, model.OperationFailed, message, code, now, attempt)
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
		id, model.OperationRunning, attempt).Scan(&active)
	if err != nil {
		return fmt.Errorf("check operation execution: %w", err)
	}
	if !active {
		return fmt.Errorf("operation %q attempt %d is no longer the active execution: %w", id, attempt, model.ErrStateConflict)
	}
	return nil
}

// transitionOperation 的 attempt 为 0 时不做执行归属围栏；code 只在转入 failed 时有意义。
func (s *Store) transitionOperation(ctx context.Context, id string, from, to model.OperationState, message string, code model.FailureCode, now time.Time, attempt int) (model.Operation, error) {
	if strings.TrimSpace(id) == "" || now.IsZero() {
		return model.Operation{}, fmt.Errorf("operation id and time are required: %w", model.ErrInvalid)
	}
	if to == model.OperationRunning || !model.CanTransitionOperation(from, to) {
		return model.Operation{}, fmt.Errorf("operation transition %s -> %s: %w", from, to, model.ErrStateConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Operation{}, fmt.Errorf("begin operation transition: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		UPDATE operations
		SET state = ?, error = ?, failure_code = ?, lease_owner = NULL, lease_until_unix_ms = NULL, updated_at_unix_ms = ?
		WHERE id = ? AND state = ? AND (? = 0 OR attempt = ?)
		RETURNING `+operationColumns,
		to, message, code, now.UnixMilli(), id, from, attempt, attempt)
	operation, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Operation{}, model.ErrStateConflict
	}
	if err != nil {
		return model.Operation{}, fmt.Errorf("transition operation: %w", err)
	}
	operation.DependsOn, err = loadOperationDependencies(ctx, tx, operation.ID)
	if err != nil {
		return model.Operation{}, err
	}
	payload, err := json.Marshal(map[string]any{"from": from, "to": to, "message": message, "failure_code": code})
	if err != nil {
		return model.Operation{}, fmt.Errorf("encode operation transition event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, model.OperationEvent{
		OperationID: id, StepID: "transition", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("transition:%s:%s:%d", from, to, now.UnixNano()),
		Kind:           "operation.transitioned", Payload: payload, CreatedAt: now,
	}); err != nil {
		return model.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Operation{}, fmt.Errorf("commit operation transition: %w", err)
	}
	return operation, nil
}

func (s *Store) RecoverExpiredOperations(ctx context.Context, now time.Time) ([]string, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("recovery time is required: %w", model.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin expired operation recovery: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, attempt FROM operations
		WHERE state = ? AND lease_until_unix_ms < ?
		ORDER BY id`, model.OperationRunning, now.UnixMilli())
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
		var expiries int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM operation_events WHERE operation_id = ? AND kind = 'operation.lease_expired'`, id,
		).Scan(&expiries); err != nil {
			return nil, fmt.Errorf("count lease expiries of %q: %w", id, err)
		}
		expiries++
		recovered := model.OperationQueued
		message := fmt.Sprintf("worker lease expired before %s; operation requeued", now.UTC().Format(time.RFC3339Nano))
		if expiries >= MaxLeaseExpiries {
			recovered = model.OperationFailed
			message = fmt.Sprintf(
				"worker lease expired before %s for the %d. time (limit %d); operation failed instead of requeued",
				now.UTC().Format(time.RFC3339Nano), expiries, MaxLeaseExpiries,
			)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE operations
			SET state = ?, error = ?, failure_code = '', lease_owner = NULL, lease_until_unix_ms = NULL, updated_at_unix_ms = ?
			WHERE id = ? AND state = ? AND lease_until_unix_ms < ?`,
			recovered, message, now.UnixMilli(), id, model.OperationRunning, now.UnixMilli())
		if err != nil {
			return nil, fmt.Errorf("recover operation %q: %w", id, err)
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return nil, fmt.Errorf("recover operation %q: %w", id, model.ErrStateConflict)
		}
		payload, err := json.Marshal(map[string]any{"reason": message, "state": recovered})
		if err != nil {
			return nil, fmt.Errorf("encode lease recovery event: %w", err)
		}
		if _, err := appendEventTx(ctx, tx, model.OperationEvent{
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

// CountOperationFailures 从事件日志数 Operation 落过几次 failed：执行失败的收尾与
// 租约过期判失败都算。D56 的自动重开预算只数失败——attempt 是执行围栏，进程退出
// 释放的执行不在其中。
func (s *Store) CountOperationFailures(ctx context.Context, id string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM operation_events
		WHERE operation_id = ?
			AND ((kind = 'operation.transitioned' AND json_extract(CAST(payload AS TEXT), '$.to') = ?)
				OR (kind = 'operation.lease_expired' AND json_extract(CAST(payload AS TEXT), '$.state') = ?))`,
		id, model.OperationFailed, model.OperationFailed).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count failures of operation %q: %w", id, err)
	}
	return count, nil
}

// PutWorkspaceArtifact 只接受当前执行实例的写入：writerAttempt 必须等于 Operation
// 的当前 attempt，过期实例不能污染后继尝试的工作区。
