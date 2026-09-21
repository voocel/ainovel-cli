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

// Operation 事件日志：追加与按任务读取。

func (s *Store) AppendOperationEvent(ctx context.Context, event model.OperationEvent) (model.OperationEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.OperationEvent{}, fmt.Errorf("begin operation event: %w", err)
	}
	defer tx.Rollback()
	event, err = appendEventTx(ctx, tx, event)
	if err != nil {
		return model.OperationEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.OperationEvent{}, fmt.Errorf("commit operation event: %w", err)
	}
	return event, nil
}

func (s *Store) ListOperationEvents(ctx context.Context, operationID string) ([]model.OperationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, step_id, attempt, idempotency_key, kind, payload, created_at_unix_ms
		FROM operation_events WHERE operation_id = ? ORDER BY sequence`, operationID)
	if err != nil {
		return nil, fmt.Errorf("list operation events: %w", err)
	}
	defer rows.Close()
	events := make([]model.OperationEvent, 0)
	for rows.Next() {
		var event model.OperationEvent
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

func appendEventTx(ctx context.Context, tx *sql.Tx, event model.OperationEvent) (model.OperationEvent, error) {
	if strings.TrimSpace(event.OperationID) == "" || strings.TrimSpace(event.StepID) == "" ||
		strings.TrimSpace(event.IdempotencyKey) == "" || strings.TrimSpace(event.Kind) == "" ||
		event.Attempt < 0 || event.CreatedAt.IsZero() || len(event.Payload) == 0 || !json.Valid(event.Payload) {
		return model.OperationEvent{}, fmt.Errorf("operation event fields are invalid: %w", model.ErrInvalid)
	}
	digest := model.Digest(event.Payload)

	var existing model.OperationEvent
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
			return model.OperationEvent{}, fmt.Errorf("event %q: %w", event.IdempotencyKey, model.ErrIdempotencyConflict)
		}
		existing.OperationID = event.OperationID
		existing.IdempotencyKey = event.IdempotencyKey
		existing.Payload = append(json.RawMessage(nil), existingPayload...)
		existing.CreatedAt = time.UnixMilli(existingCreated).UTC()
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.OperationEvent{}, fmt.Errorf("find operation event: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM operation_events WHERE operation_id = ?`, event.OperationID).
		Scan(&event.Sequence); err != nil {
		return model.OperationEvent{}, fmt.Errorf("allocate operation event sequence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO operation_events (
			operation_id, sequence, step_id, attempt, idempotency_key,
			kind, payload, payload_digest, created_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.OperationID, event.Sequence, event.StepID, event.Attempt, event.IdempotencyKey,
		event.Kind, []byte(event.Payload), digest, event.CreatedAt.UnixMilli()); err != nil {
		return model.OperationEvent{}, fmt.Errorf("insert operation event: %w", err)
	}
	return event, nil
}
