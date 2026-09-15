package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Each operation carries three attempts and a substantial event history. Fixture
// construction is outside timing: the benchmark measures an interactive read.
func seedDiagHistory(tb testing.TB, count int) *Store {
	tb.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(tb.TempDir(), "history.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`INSERT INTO authority_streams(target_kind,target_id,current_revision) VALUES ('project','book',1)`,
		`INSERT INTO creation_runs(id,content_digest,project_id,goal,strategy,preset,state,created_at_unix_ms,updated_at_unix_ms) VALUES ('run','digest','book',x'7b7d',x'7b7d',x'7b7d','running',1,1)`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			tb.Fatal(err)
		}
	}
	op, err := tx.Prepare(`INSERT INTO operations(id,content_digest,kind,target_kind,target_id,priority,state,attempt,execution_snapshot,input,executor,run_id,created_at_unix_ms,updated_at_unix_ms) VALUES (?,'digest','write_chapter','project','book',0,'succeeded',3,?,x'7b7d','llm.agent@1/profile','run',1,200)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer op.Close()
	event, err := tx.Prepare(`INSERT INTO operation_events(operation_id,sequence,step_id,attempt,idempotency_key,kind,payload,payload_digest,created_at_unix_ms) VALUES (?,?,'',?,?,?,?,'digest',?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer event.Close()
	message := []byte(`{"role":"assistant","content":"` + strings.Repeat("content ", 256) + `"}`)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("op-%04d", i)
		if _, err = op.Exec(id, []byte(`{"executor":"llm.agent@1/profile","config_digest":"profile"}`)); err != nil {
			tb.Fatal(err)
		}
		for sequence := 1; sequence <= 200; sequence++ {
			attempt := 1 + (sequence-1)/67
			kind := "agent.message_committed"
			payload := message
			if sequence == 67 || sequence == 134 || sequence == 200 {
				kind = "agent.run_ended"
				payload = []byte(`{"usage":{"input":100,"output":200,"cache_read":0,"cache_write":0,"total_tokens":300}}`)
			} else if sequence == 1 || sequence == 68 || sequence == 135 {
				kind = "operation.running"
				payload = []byte(`{"state":"running"}`)
			}
			if _, err = event.Exec(id, sequence, attempt, fmt.Sprint(sequence), kind, payload, sequence); err != nil {
				tb.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	return s
}

func BenchmarkDiagLongHistory(b *testing.B) {
	for _, count := range []int{500, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := seedDiagHistory(b, count)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				snap, err := s.ReadDiagSnapshot(context.Background(), DiagRequest{ProjectID: "book"})
				if err != nil {
					b.Fatal(err)
				}
				if snap.OperationCount != count || snap.EventCount != count*200 || snap.AttemptCount != count*3 || len(snap.Operations) != 50 || len(snap.Events) != 200 || snap.MissingCompletionEventCount != 0 {
					b.Fatalf("incorrect long-history projection: operations=%d events=%d attempts=%d task page=%d event page=%d missing=%d", snap.OperationCount, snap.EventCount, snap.AttemptCount, len(snap.Operations), len(snap.Events), snap.MissingCompletionEventCount)
				}
				for _, event := range snap.Events {
					if len(event.Payload) != 0 {
						b.Fatal("summary read payload")
					}
				}
			}
		})
	}
}

func TestDiagHistoryCancelledContext(t *testing.T) {
	s := seedDiagHistory(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query: %v", err)
	}
	// A cancelled read must release its transaction and leave the store usable.
	if snap, err := s.ReadDiagSnapshot(context.Background(), DiagRequest{ProjectID: "book"}); err != nil || snap.EventCount != 600 {
		t.Fatalf("read after cancellation: events=%d err=%v", snap.EventCount, err)
	}
}

func BenchmarkDiagLongHistoryShare(b *testing.B) {
	s := seedDiagHistory(b, 1000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		snapshot, err := s.ReadDiagSnapshot(context.Background(), DiagRequest{ProjectID: "book", ForShare: true})
		if err != nil {
			b.Fatal(err)
		}
		if len(snapshot.Operations) != 200 || len(snapshot.Events) != 200 || snapshot.EventCount != 200000 {
			b.Fatal("share exceeded its context budget")
		}
		ids := map[string]bool{}
		for _, op := range snapshot.Operations {
			ids[op.ID] = true
		}
		for _, event := range snapshot.Events {
			if !ids[event.OperationID] {
				b.Fatal("event lacks task context")
			}
			if event.Kind != "agent.run_ended" && len(event.Payload) != 0 {
				b.Fatal("share read message payload")
			}
		}
	}
}
