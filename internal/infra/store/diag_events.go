package store

import (
	"context"
	"database/sql"
	"time"
)

// readDiagTails reserves a tail window for each selected stream. Sequence is the
// stream's durable order; timestamps do not establish cross-stream causality.
// Both the number of streams and the total returned events are bounded by 200.
func readDiagTails(ctx context.Context, tx *sql.Tx, scope string, args []any, snapshot DiagSnapshot, request DiagRequest) ([]DiagEvent, error) {
	var ids []string
	if request.ForShare {
		for _, op := range snapshot.Operations {
			if op.EventCount > 0 {
				ids = append(ids, op.ID)
			}
		}
	} else {
		queryArgs := append(append([]any{}, args...), snapshot.CapturedAt.UnixMilli())
		rows, err := tx.QueryContext(ctx, `SELECT o.id FROM operations o WHERE `+scope+`
			AND EXISTS(SELECT 1 FROM operation_events e WHERE e.operation_id=o.id)
			ORDER BY CASE WHEN `+diagAffectedTask+` THEN 0 ELSE 1 END,o.updated_at_unix_ms DESC,o.id LIMIT 200`, queryArgs...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	events := make([]DiagEvent, 0, 200)
	payload := `NULL,0`
	if request.ForShare {
		// Run-wide shares only decode compact end summaries. A selected task may
		// inspect its bounded message window for tool errors; raw text never ships.
		payload = `CASE WHEN kind='agent.run_ended' THEN substr(payload,1,65536) END,CASE WHEN kind='agent.run_ended' THEN length(payload)>65536 ELSE 0 END`
		if request.OperationID != "" {
			payload = `substr(payload,1,65536),length(payload)>65536`
		}
	}
	for i, id := range ids {
		quota := (200 - len(events)) / (len(ids) - i)
		rows, err := tx.QueryContext(ctx, `SELECT operation_id,sequence,attempt,kind,created_at_unix_ms,`+payload+`
			FROM operation_events WHERE operation_id=? ORDER BY sequence DESC LIMIT ?`, id, quota)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var event DiagEvent
			var created int64
			if err = rows.Scan(&event.OperationID, &event.Sequence, &event.Attempt, &event.Kind, &created, &event.Payload, &event.PayloadTruncated); err != nil {
				rows.Close()
				return nil, err
			}
			event.CreatedAt = time.UnixMilli(created).UTC()
			events = append(events, event)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return events, nil
}

// 工具报错写在 role=tool 消息的 metadata 里。谓词下推到 SQL，调用方拿到的是计数与
// 坐标而非正文，所选范围内全量统计，不受事件明细窗口影响。
// json_valid 守卫必须排在最前：SQLite 只在 WHERE 与 CASE 条件里按序短路，遇到非法
// 载荷时 json_extract 会中断整条查询，而不是当作不匹配。
const (
	diagMessages  = `e.kind='agent.message_committed'`
	diagJSONOK    = `json_valid(CAST(e.payload AS TEXT))`
	diagToolError = diagJSONOK + `
		AND json_extract(CAST(e.payload AS TEXT),'$.role')='tool'
		AND json_extract(CAST(e.payload AS TEXT),'$.metadata.is_error')=1`
)

// 聚合粒度是 attempt：同一次尝试里反复报同一个错是一条线索，不是多条。
const diagToolErrorGroups = 200

func readDiagToolErrors(ctx context.Context, tx *sql.Tx, scope string, args []any, out *DiagSnapshot) error {
	from := ` FROM operation_events e JOIN operations o ON o.id=e.operation_id WHERE ` + scope + ` AND ` + diagMessages
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN `+diagToolError+` THEN 1 ELSE 0 END),0),`+
		`COALESCE(SUM(NOT `+diagJSONOK+`),0)`+from, args...).Scan(&out.ToolErrorCount, &out.UnreadableMessages)
	if err != nil {
		return err
	}
	if out.ToolErrorCount == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.operation_id,e.attempt,MAX(e.sequence),COUNT(*)`+from+` AND `+diagToolError+
		` GROUP BY e.operation_id,e.attempt ORDER BY COUNT(*) DESC,e.operation_id LIMIT ?`,
		append(append([]any{}, args...), diagToolErrorGroups+1)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var group DiagToolError
		if err = rows.Scan(&group.OperationID, &group.Attempt, &group.LastSequence, &group.Count); err != nil {
			return err
		}
		out.ToolErrors = append(out.ToolErrors, group)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(out.ToolErrors) > diagToolErrorGroups {
		out.ToolErrors = out.ToolErrors[:diagToolErrorGroups]
		out.ToolErrorsTruncated = true
	}
	return nil
}
