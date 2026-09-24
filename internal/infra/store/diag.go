package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type DiagRequest struct {
	ProjectID, RunID, OperationID, AfterOperationID string
	AfterEventSequence                              int64
	ForShare                                        bool
}
type DiagSnapshot struct {
	ProjectID                   string
	Revision                    model.Revision
	SchemaVersion               int
	CapturedAt                  time.Time
	Run                         *DiagRun
	Counts                      map[model.OperationState]int
	AttemptCount                int
	OperationCount              int
	EventCount                  int
	ExpiredLeaseCount           int
	ResultUnknownCount          int
	MissingCompletionEventCount int
	ToolErrorCount              int
	ToolErrors                  []DiagToolError
	ToolErrorsTruncated         bool
	UnreadableMessages          int
	RetriedOperationCount       int
	RunEventBoundary            int64
	LastEventAt                 *time.Time
	Operations                  []DiagOperation
	Events                      []DiagEvent
	NextOperationID             string
	NextEventSequence           int64
	EventsTruncated             bool
}
type DiagRun struct {
	ID, ProjectID        string
	State                model.CreationRunState
	StateReason          string
	CompletedRevision    model.Revision
	CreatedAt, UpdatedAt time.Time
}
type DiagOperation struct {
	ID, RunID                     string
	Kind                          model.OperationKind
	State                         model.OperationState
	Attempt                       int
	FailureCode                   model.FailureCode
	Error, Executor, ConfigDigest string
	LeaseUntil                    *time.Time
	CreatedAt, UpdatedAt          time.Time
	EventCount                    int
	EventBoundary                 int64
	HasCompletionEvent            bool
}
type DiagEvent struct {
	OperationID      string
	Sequence         int64
	Attempt          int
	Kind             string
	CreatedAt        time.Time
	Payload          []byte
	PayloadTruncated bool
}

// DiagToolError 把一次 attempt 内的工具报错聚合成一个坐标：报错标记在消息载荷里，
// 但统计只走 SQL 谓词，载荷不进投影，因此运行级诊断也能给出结论。
type DiagToolError struct {
	OperationID  string
	Attempt      int
	LastSequence int64
	Count        int
}

// ReadDiagSnapshot reads bounded details and scope-wide aggregates from a single
// SQLite snapshot. No project documents or operation inputs enter this projection.
func (s *Store) ReadDiagSnapshot(ctx context.Context, req DiagRequest) (DiagSnapshot, error) {
	out := DiagSnapshot{ProjectID: req.ProjectID, CapturedAt: time.Now().UTC(), Counts: map[model.OperationState]int{}}
	if strings.TrimSpace(req.ProjectID) == "" || req.AfterEventSequence < 0 || (req.AfterEventSequence != 0 && req.OperationID == "") || (req.AfterOperationID != "" && req.OperationID != "") {
		return out, model.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&out.SchemaVersion); err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `SELECT current_revision FROM authority_streams WHERE target_kind=? AND target_id=? AND target_scope=''`, model.AuthorityProject, req.ProjectID).Scan(&out.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return out, model.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if req.OperationID != "" {
		var projectID, runID, kind string
		err = tx.QueryRowContext(ctx, `SELECT target_id,run_id,target_kind FROM operations WHERE id=?`, req.OperationID).Scan(&projectID, &runID, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			return out, model.ErrNotFound
		}
		if err != nil {
			return out, err
		}
		if projectID != req.ProjectID || kind != string(model.AuthorityProject) || (req.RunID != "" && req.RunID != runID) {
			return out, fmt.Errorf("operation does not belong to diagnostic scope: %w", model.ErrInvalid)
		}
		req.RunID = runID
	}
	runSQL := `SELECT id,project_id,state,state_reason,completed_revision,created_at_unix_ms,updated_at_unix_ms FROM creation_runs WHERE id=?`
	arg := req.RunID
	if req.RunID == "" {
		runSQL = `SELECT id,project_id,state,state_reason,completed_revision,created_at_unix_ms,updated_at_unix_ms FROM creation_runs WHERE project_id=? ORDER BY created_at_unix_ms DESC,id DESC LIMIT 1`
		arg = req.ProjectID
	}
	run := DiagRun{}
	var created, updated int64
	err = tx.QueryRowContext(ctx, runSQL, arg).Scan(&run.ID, &run.ProjectID, &run.State, &run.StateReason, &run.CompletedRevision, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		if req.RunID != "" {
			return out, model.ErrNotFound
		}
		return out, tx.Commit()
	}
	if err != nil {
		return out, err
	}
	if run.ProjectID != req.ProjectID {
		return out, fmt.Errorf("run does not belong to project: %w", model.ErrInvalid)
	}
	switch run.State {
	case model.RunRunning, model.RunWaitingUser, model.RunPaused, model.RunCompleted, model.RunFailed, model.RunCancelled:
	default:
		return out, fmt.Errorf("invalid stored run state %q", run.State)
	}
	run.CreatedAt = time.UnixMilli(created).UTC()
	run.UpdatedAt = time.UnixMilli(updated).UTC()
	out.Run = &run
	var lastRun sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0),MAX(created_at_unix_ms) FROM creation_run_events WHERE run_id=?`, run.ID).Scan(&out.RunEventBoundary, &lastRun); err != nil {
		return out, err
	}
	scope := `o.run_id=? AND o.target_kind=? AND o.target_id=?`
	args := []any{run.ID, model.AuthorityProject, req.ProjectID}
	if req.OperationID != "" {
		scope += ` AND o.id=?`
		args = append(args, req.OperationID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.state,COUNT(*),COALESCE(SUM(o.attempt),0),COALESCE(SUM(o.attempt>1),0) FROM operations o WHERE `+scope+` GROUP BY o.state`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var state model.OperationState
		var count, attempts, retries int
		if err = rows.Scan(&state, &count, &attempts, &retries); err != nil {
			rows.Close()
			return out, err
		}
		switch state {
		case model.OperationQueued, model.OperationRunning, model.OperationPaused, model.OperationAwaitingApproval, model.OperationSucceeded, model.OperationFailed, model.OperationCancelled, model.OperationStale:
		default:
			rows.Close()
			return out, fmt.Errorf("invalid stored operation state %q", state)
		}
		out.Counts[state] = count
		out.OperationCount += count
		out.AttemptCount += attempts
		out.RetriedOperationCount += retries
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	aggregateArgs := append([]any{out.CapturedAt.UnixMilli()}, args...)
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(o.state='running' AND o.lease_until_unix_ms<=?),0),COALESCE(SUM(o.failure_code='result_unknown'),0),COALESCE(SUM(o.executor LIKE 'llm.agent@%' AND o.attempt>0 AND o.state IN ('succeeded','failed','cancelled','stale') AND (SELECT sequence FROM operation_events e WHERE e.operation_id=o.id AND e.attempt=o.attempt AND e.kind='agent.run_ended' ORDER BY sequence DESC LIMIT 1) IS NULL),0) FROM operations o WHERE `+scope, aggregateArgs...).Scan(&out.ExpiredLeaseCount, &out.ResultUnknownCount, &out.MissingCompletionEventCount)
	if err != nil {
		return out, err
	}
	var lastEvent sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM((SELECT COUNT(*) FROM operation_events e WHERE e.operation_id=o.id)),0),MAX((SELECT created_at_unix_ms FROM operation_events e WHERE e.operation_id=o.id ORDER BY sequence DESC LIMIT 1)) FROM operations o WHERE `+scope, args...).Scan(&out.EventCount, &lastEvent)
	if err != nil {
		return out, err
	}
	if err = readDiagToolErrors(ctx, tx, scope, args, &out); err != nil {
		return out, err
	}
	if lastRun.Valid && (!lastEvent.Valid || lastRun.Int64 > lastEvent.Int64) && req.OperationID == "" {
		lastEvent = lastRun
	}
	if lastEvent.Valid {
		v := time.UnixMilli(lastEvent.Int64).UTC()
		out.LastEventAt = &v
	}
	pageScope := scope
	pageArgs := append([]any{}, args...)
	if req.AfterOperationID != "" {
		var found int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations o WHERE `+scope+` AND o.id=?`, append(append([]any{}, args...), req.AfterOperationID)...).Scan(&found); err != nil {
			return out, err
		}
		if found != 1 {
			return out, fmt.Errorf("cursor does not belong to diagnostic scope: %w", model.ErrInvalid)
		}
		pageScope += ` AND o.id>?`
		pageArgs = append(pageArgs, req.AfterOperationID)
	}
	errorColumn := `''`
	if req.OperationID != "" && !req.ForShare {
		errorColumn = `o.error`
	}
	operationLimit, operationOrder := 50, `o.id`
	if req.ForShare {
		operationLimit = 200
		operationOrder = `CASE WHEN ` + diagAffectedTask + ` THEN 0 WHEN o.state='running' OR o.attempt>1 THEN 1 ELSE 2 END, o.updated_at_unix_ms DESC,o.id`
		pageArgs = append(pageArgs, out.CapturedAt.UnixMilli())
	}
	pageArgs = append(pageArgs, operationLimit+1)
	rows, err = tx.QueryContext(ctx, `SELECT o.id,o.run_id,o.kind,o.state,o.attempt,o.failure_code,`+errorColumn+`,o.executor,COALESCE(json_extract(o.execution_snapshot,'$.config_digest'),''),o.lease_until_unix_ms,o.created_at_unix_ms,o.updated_at_unix_ms,(SELECT COUNT(*) FROM operation_events e WHERE e.operation_id=o.id),(SELECT COALESCE(MAX(sequence),0) FROM operation_events e WHERE e.operation_id=o.id),(SELECT sequence FROM operation_events e WHERE e.operation_id=o.id AND e.attempt=o.attempt AND e.kind='agent.run_ended' ORDER BY sequence DESC LIMIT 1) IS NOT NULL FROM operations o WHERE `+pageScope+` ORDER BY `+operationOrder+` LIMIT ?`, pageArgs...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var op DiagOperation
		var lease sql.NullInt64
		if err = rows.Scan(&op.ID, &op.RunID, &op.Kind, &op.State, &op.Attempt, &op.FailureCode, &op.Error, &op.Executor, &op.ConfigDigest, &lease, &created, &updated, &op.EventCount, &op.EventBoundary, &op.HasCompletionEvent); err != nil {
			rows.Close()
			return out, err
		}
		if op.Executor == "" || op.ConfigDigest == "" {
			rows.Close()
			return out, fmt.Errorf("operation %q is missing execution identity: %w", op.ID, model.ErrInvalid)
		}
		op.CreatedAt = time.UnixMilli(created).UTC()
		op.UpdatedAt = time.UnixMilli(updated).UTC()
		if lease.Valid {
			v := time.UnixMilli(lease.Int64).UTC()
			op.LeaseUntil = &v
		}
		out.Operations = append(out.Operations, op)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Operations) > operationLimit {
		out.Operations = out.Operations[:operationLimit]
		if !req.ForShare {
			out.NextOperationID = out.Operations[operationLimit-1].ID
		}
	}
	if req.ForShare || req.OperationID == "" {
		out.Events, err = readDiagTails(ctx, tx, scope, args, out, req)
		if err != nil {
			return out, err
		}
		out.EventsTruncated = len(out.Events) < out.EventCount
		return out, tx.Commit()
	}
	limit := 100
	eventArgs := append(append([]any{}, args...), req.AfterEventSequence)
	eventSQL := `SELECT e.operation_id,e.sequence,e.attempt,e.kind,e.created_at_unix_ms,substr(e.payload,1,65536),length(e.payload)>65536
		FROM operation_events e JOIN operations o ON o.id=e.operation_id WHERE ` + scope + ` AND e.sequence>? ORDER BY e.sequence LIMIT ?`
	eventArgs = append(eventArgs, limit+1)
	rows, err = tx.QueryContext(ctx, eventSQL, eventArgs...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var event DiagEvent
		if err = rows.Scan(&event.OperationID, &event.Sequence, &event.Attempt, &event.Kind, &created, &event.Payload, &event.PayloadTruncated); err != nil {
			rows.Close()
			return out, err
		}
		event.CreatedAt = time.UnixMilli(created).UTC()
		out.Events = append(out.Events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Events) > limit {
		out.Events = out.Events[:limit]
		out.EventsTruncated = true
		if req.OperationID != "" && !req.ForShare {
			out.NextEventSequence = out.Events[limit-1].Sequence
		}
	}
	return out, tx.Commit()
}

// The parameter is the snapshot capture time. This is an observation predicate,
// never a lifecycle transition or a recovery authorization.
const diagAffectedTask = `(o.state='failed' OR o.failure_code='result_unknown' OR (o.state='running' AND o.lease_until_unix_ms<=?)
	OR (o.executor LIKE 'llm.agent@%' AND o.attempt>0 AND o.state IN ('succeeded','cancelled','stale')
	AND (SELECT sequence FROM operation_events ended WHERE ended.operation_id=o.id AND ended.attempt=o.attempt AND ended.kind='agent.run_ended' ORDER BY sequence DESC LIMIT 1) IS NULL))`
