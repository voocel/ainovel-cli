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

// Operation Workspace 工件：写入、读取、后继继承（D19、D55、D60）。
//
// expectedVersion 为 nil 表示全量覆盖——写入方不依赖旧内容，写在当前版本之上。
// 只有读改写（按 block 编辑）才传版本前提：那时版本来自刚才的读取，不是猜的。
// 跨执行实例的保护由 writerAttempt 围栏承担（D42），不靠这个前提。
func (s *Store) PutWorkspaceArtifact(ctx context.Context, artifact model.WorkspaceArtifact, expectedVersion *int64, writerAttempt int) (model.WorkspaceArtifact, error) {
	if strings.TrimSpace(artifact.OperationID) == "" || strings.TrimSpace(artifact.Key) == "" || strings.TrimSpace(artifact.MediaType) == "" || len(artifact.Content) == 0 {
		return model.WorkspaceArtifact{}, fmt.Errorf("artifact operation, key, media type and content are required: %w", model.ErrInvalid)
	}
	if (expectedVersion != nil && *expectedVersion < 0) || artifact.UpdatedAt.IsZero() {
		return model.WorkspaceArtifact{}, fmt.Errorf("artifact expected version and time are invalid: %w", model.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.WorkspaceArtifact{}, fmt.Errorf("begin workspace write: %w", err)
	}
	defer tx.Rollback()

	var state model.OperationState
	var attempt int
	if err := tx.QueryRowContext(ctx, `SELECT state, attempt FROM operations WHERE id = ?`, artifact.OperationID).Scan(&state, &attempt); errors.Is(err, sql.ErrNoRows) {
		return model.WorkspaceArtifact{}, model.ErrNotFound
	} else if err != nil {
		return model.WorkspaceArtifact{}, fmt.Errorf("read workspace operation: %w", err)
	}
	if state != model.OperationRunning {
		return model.WorkspaceArtifact{}, fmt.Errorf("operation %q is %s: %w", artifact.OperationID, state, model.ErrStateConflict)
	}
	if writerAttempt != attempt {
		return model.WorkspaceArtifact{}, fmt.Errorf(
			"operation %q is on attempt %d, writer holds attempt %d: %w", artifact.OperationID, attempt, writerAttempt, model.ErrStateConflict)
	}

	var current int64
	err = tx.QueryRowContext(ctx, `
		SELECT version FROM operation_artifacts
		WHERE operation_id = ? AND artifact_key = ?`, artifact.OperationID, artifact.Key).Scan(&current)
	exists := true
	if errors.Is(err, sql.ErrNoRows) {
		exists, current = false, 0
	} else if err != nil {
		return model.WorkspaceArtifact{}, fmt.Errorf("read workspace artifact version: %w", err)
	}
	if expectedVersion != nil && *expectedVersion != current {
		return model.WorkspaceArtifact{}, fmt.Errorf(
			"workspace key %q is at version %d, not %d; re-read it before editing: %w",
			artifact.Key, current, *expectedVersion, model.ErrWorkspaceConflict)
	}
	artifact.Digest = model.Digest(artifact.Content)
	artifact.Version = current + 1
	if !exists {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO operation_artifacts (
				operation_id, artifact_key, version, media_type, content, content_digest, updated_at_unix_ms
			) VALUES (?, ?, 1, ?, ?, ?, ?)`,
			artifact.OperationID, artifact.Key, artifact.MediaType, artifact.Content, artifact.Digest, artifact.UpdatedAt.UnixMilli()); err != nil {
			return model.WorkspaceArtifact{}, fmt.Errorf("insert workspace artifact: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE operation_artifacts
			SET version = ?, media_type = ?, content = ?, content_digest = ?, updated_at_unix_ms = ?
			WHERE operation_id = ? AND artifact_key = ? AND version = ?`,
			artifact.Version, artifact.MediaType, artifact.Content, artifact.Digest, artifact.UpdatedAt.UnixMilli(),
			artifact.OperationID, artifact.Key, current); err != nil {
			return model.WorkspaceArtifact{}, fmt.Errorf("update workspace artifact: %w", err)
		}
	}
	payload, err := json.Marshal(map[string]any{"key": artifact.Key, "version": artifact.Version, "digest": artifact.Digest})
	if err != nil {
		return model.WorkspaceArtifact{}, fmt.Errorf("encode workspace event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, model.OperationEvent{
		OperationID: artifact.OperationID, StepID: "workspace", Attempt: attempt,
		IdempotencyKey: fmt.Sprintf("workspace:%s:%d", artifact.Key, artifact.Version),
		Kind:           "workspace.artifact_written", Payload: payload, CreatedAt: artifact.UpdatedAt,
	}); err != nil {
		return model.WorkspaceArtifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.WorkspaceArtifact{}, fmt.Errorf("commit workspace artifact: %w", err)
	}
	artifact.Content = append([]byte(nil), artifact.Content...)
	return artifact, nil
}

func (s *Store) GetWorkspaceArtifact(ctx context.Context, operationID, key string) (model.WorkspaceArtifact, error) {
	var artifact model.WorkspaceArtifact
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT version, media_type, content, content_digest, updated_at_unix_ms
		FROM operation_artifacts WHERE operation_id = ? AND artifact_key = ?`, operationID, key).
		Scan(&artifact.Version, &artifact.MediaType, &artifact.Content, &artifact.Digest, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.WorkspaceArtifact{}, model.ErrNotFound
	}
	if err != nil {
		return model.WorkspaceArtifact{}, fmt.Errorf("read workspace artifact: %w", err)
	}
	artifact.OperationID = operationID
	artifact.Key = key
	artifact.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	artifact.Content = append([]byte(nil), artifact.Content...)
	return artifact, nil
}

func (s *Store) ListWorkspaceArtifacts(ctx context.Context, operationID string) ([]model.WorkspaceArtifact, error) {
	if strings.TrimSpace(operationID) == "" {
		return nil, fmt.Errorf("operation id is required: %w", model.ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact_key, version, media_type, content, content_digest, updated_at_unix_ms
		FROM operation_artifacts WHERE operation_id = ? ORDER BY artifact_key`, operationID)
	if err != nil {
		return nil, fmt.Errorf("list workspace artifacts: %w", err)
	}
	defer rows.Close()
	artifacts := make([]model.WorkspaceArtifact, 0)
	for rows.Next() {
		artifact := model.WorkspaceArtifact{OperationID: operationID}
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace copy: %w", err)
	}
	defer tx.Rollback()
	if err := copyWorkspaceArtifactsTx(ctx, tx, fromOperationID, toOperationID, at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace copy: %w", err)
	}
	return nil
}

func copyWorkspaceArtifactsTx(ctx context.Context, tx *sql.Tx, fromOperationID, toOperationID string, at time.Time) error {
	if strings.TrimSpace(fromOperationID) == "" || strings.TrimSpace(toOperationID) == "" ||
		fromOperationID == toOperationID || at.IsZero() {
		return fmt.Errorf("distinct source, target and copy time are required: %w", model.ErrInvalid)
	}
	var targetState model.OperationState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM operations WHERE id = ?`, toOperationID).Scan(&targetState); errors.Is(err, sql.ErrNoRows) {
		return model.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read workspace copy target: %w", err)
	}
	if targetState != model.OperationQueued {
		return fmt.Errorf("workspace copy target %q is %s: %w", toOperationID, targetState, model.ErrStateConflict)
	}
	var sourceExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM operations WHERE id = ?`, fromOperationID).Scan(&sourceExists); errors.Is(err, sql.ErrNoRows) {
		return model.ErrNotFound
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
			return nil
		}
		return fmt.Errorf("workspace copy target %q contains different artifacts: %w", toOperationID, model.ErrStateConflict)
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
	if _, err := appendEventTx(ctx, tx, model.OperationEvent{
		OperationID: toOperationID, StepID: "workspace.seed", Attempt: 0,
		IdempotencyKey: "workspace-seed:" + fromOperationID,
		Kind:           "workspace.seeded", Payload: payload, CreatedAt: at,
	}); err != nil {
		return err
	}
	return nil
}
