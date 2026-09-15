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
