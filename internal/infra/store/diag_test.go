package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func seedDiagProject(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO authority_streams(target_kind,target_id,current_revision) VALUES ('project','book-1',1)`); err != nil {
		t.Fatal(err)
	}
}

func TestDiagScopePaginationAndPayloadBudget(t *testing.T) {
	s := openOperationStore(t)
	seedDiagProject(t, s)
	ctx := context.Background()
	for i := 0; i < 55; i++ {
		if _, err := s.CreateOperation(ctx, testOperation(fmt.Sprintf("op-%03d", i), 0, operationTime())); err != nil {
			t.Fatal(err)
		}
	}
	payload := []byte(strings.Repeat("sensitive", 20000))
	for i := 1; i <= 205; i++ {
		_, err := s.db.Exec(`INSERT INTO operation_events(operation_id,sequence,step_id,attempt,idempotency_key,kind,payload,payload_digest,created_at_unix_ms) VALUES ('op-000',?,'',0,?,'agent.message_committed',?,'digest',?)`, i, fmt.Sprint(i), payload, i)
		if err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.OperationCount != 55 || len(snap.Operations) != 50 || snap.NextOperationID != "op-049" || snap.EventCount != 205 || len(snap.Events) != 200 || !snap.EventsTruncated {
		t.Fatalf("unexpected pagination: %+v", snap)
	}
	for _, event := range snap.Events {
		if len(event.Payload) != 0 {
			t.Fatal("run snapshot read private payload")
		}
	}
	next, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1", AfterOperationID: snap.NextOperationID})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Operations) != 5 || next.NextOperationID != "" || next.OperationCount != 55 {
		t.Fatalf("unexpected final page: %+v", next)
	}
	selected, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1", OperationID: "op-000"})
	if err != nil {
		t.Fatal(err)
	}
	if selected.OperationCount != 1 || selected.EventCount != 205 || len(selected.Events) != 100 || selected.NextEventSequence != 100 {
		t.Fatalf("unexpected event page: %+v", selected)
	}
	if len(selected.Events[0].Payload) != 65536 || !selected.Events[0].PayloadTruncated {
		t.Fatal("payload budget was not applied")
	}
	end, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1", OperationID: "op-000", AfterEventSequence: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(end.Events) != 5 || end.NextEventSequence != 0 || end.EventsTruncated {
		t.Fatal("incorrect final event page")
	}
	for _, req := range []DiagRequest{{ProjectID: "book-1", RunID: "other", OperationID: "op-000"}, {ProjectID: "book-1", AfterOperationID: "other"}} {
		if _, err = s.ReadDiagSnapshot(ctx, req); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("scope accepted: %+v %v", req, err)
		}
	}
	if _, err := s.db.Exec(`UPDATE operations SET state='failed' WHERE id='op-054'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO operation_events(operation_id,sequence,step_id,attempt,idempotency_key,kind,payload,payload_digest,created_at_unix_ms) VALUES ('op-054',1,'',0,'failure','operation.transitioned',x'7b7d','digest',0)`); err != nil {
		t.Fatal(err)
	}
	prioritized, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prioritized.Events) != 200 || prioritized.Events[0].OperationID != "op-054" {
		t.Fatal("recent message stream evicted old failure evidence")
	}
}

// 工具报错必须在运行级就能统计出来：运行级不读载荷，统计只能走 SQL 谓词。
// 回归的样子是这里归零而选中任务时才有数——那正是用户看不到错误的那个缺口。
func TestDiagCountsToolErrorsWithoutReadingPayloads(t *testing.T) {
	s := openOperationStore(t)
	seedDiagProject(t, s)
	ctx := context.Background()
	for _, id := range []string{"op-a", "op-b"} {
		if _, err := s.CreateOperation(ctx, testOperation(id, 0, operationTime())); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(operationID string, sequence int, attempt int, payload string) {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO operation_events(operation_id,sequence,step_id,attempt,idempotency_key,kind,payload,payload_digest,created_at_unix_ms)
			VALUES (?,?,'',?,?,'agent.message_committed',?,'digest',?)`,
			operationID, sequence, attempt, fmt.Sprint(operationID, sequence), []byte(payload), sequence); err != nil {
			t.Fatal(err)
		}
	}
	const toolError = `{"role":"tool","metadata":{"is_error":true},"content":"workspace version conflict"}`
	commit("op-a", 1, 1, toolError)
	commit("op-a", 2, 1, toolError)
	commit("op-a", 3, 2, toolError)                                           // 同一任务的另一次尝试单独成组
	commit("op-a", 4, 2, `{"role":"tool","metadata":{"is_error":false}}`)     // 成功的工具结果不计
	commit("op-a", 5, 2, `{"role":"assistant","metadata":{"is_error":true}}`) // 非工具消息不计
	commit("op-a", 6, 2, `not json at all`)                                   // 非法载荷不能中断查询，也不能记为零
	commit("op-b", 7, 1, toolError)

	snap, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.ToolErrorCount != 4 {
		t.Fatalf("工具错误计数 = %d, want 4", snap.ToolErrorCount)
	}
	if snap.UnreadableMessages != 1 {
		t.Fatalf("无法解析的消息数 = %d, want 1", snap.UnreadableMessages)
	}
	for _, event := range snap.Events {
		if len(event.Payload) != 0 {
			t.Fatal("运行级快照为统计工具错误读取了载荷")
		}
	}
	got := map[string]DiagToolError{}
	for _, group := range snap.ToolErrors {
		got[fmt.Sprintf("%s#%d", group.OperationID, group.Attempt)] = group
	}
	want := map[string]struct {
		count int
		last  int64
	}{
		"op-a#1": {2, 2}, "op-a#2": {1, 3}, "op-b#1": {1, 7},
	}
	if len(got) != len(want) {
		t.Fatalf("聚合分组 = %+v, want %d 组", snap.ToolErrors, len(want))
	}
	for key, expected := range want {
		group, ok := got[key]
		if !ok {
			t.Fatalf("缺少分组 %s；实际 %+v", key, snap.ToolErrors)
		}
		if group.Count != expected.count || group.LastSequence != expected.last {
			t.Errorf("%s = 次数 %d 末条 %d, want %d / %d", key, group.Count, group.LastSequence, expected.count, expected.last)
		}
	}
}

func TestDiagEmptyProjectAndReadOnlyOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "diag.db")
	if _, err := OpenReadOnly(ctx, path); err == nil {
		t.Fatal("missing database opened")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readonly open created file: %v", err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedDiagProject(t, s)
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	empty, err := ro.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1"})
	if err != nil || empty.Run != nil || empty.OperationCount != 0 {
		t.Fatalf("empty project: %+v %v", empty, err)
	}
	if _, err = ro.db.Exec(`DELETE FROM authority_streams`); err == nil {
		t.Fatal("read-only connection allowed mutation")
	}
	if _, err = ro.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "missing"}); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing project: %v", err)
	}
	if _, err = s.db.Exec(`PRAGMA user_version=999`); err != nil {
		t.Fatal(err)
	}
	if unsupported, err := OpenReadOnly(ctx, path); err == nil {
		unsupported.Close()
		t.Fatal("unsupported schema accepted")
	}
}

func TestDiagAggregatesCoverHiddenTasksAndCurrentAttempt(t *testing.T) {
	s := openOperationStore(t)
	seedDiagProject(t, s)
	ctx := context.Background()
	for i := 0; i < 55; i++ {
		if _, err := s.CreateOperation(ctx, testOperation(fmt.Sprintf("op-%03d", i), 0, operationTime())); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.db.Exec(`UPDATE operations SET state='failed',attempt=2,executor='llm.agent@1',failure_code='result_unknown' WHERE id='op-054'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO operation_events(operation_id,sequence,step_id,attempt,idempotency_key,kind,payload,payload_digest,created_at_unix_ms) VALUES ('op-054',1,'',1,'ended-1','agent.run_ended',x'7b7d','digest',1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`UPDATE operations SET state='running',attempt=1,lease_owner='owner',lease_until_unix_ms=1 WHERE id='op-053'`)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Counts[model.OperationFailed] != 1 || snap.AttemptCount != 3 || snap.RetriedOperationCount != 1 || snap.MissingCompletionEventCount != 1 || snap.ExpiredLeaseCount != 1 || snap.ResultUnknownCount != 1 {
		t.Fatalf("scope aggregate missed hidden tasks: %+v", snap)
	}
	selected, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1", OperationID: "op-054"})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Operations[0].HasCompletionEvent {
		t.Fatal("previous attempt completion masked current missing observation")
	}
	_, err = s.db.Exec(`UPDATE creation_runs SET state='completed' WHERE id='run:book-1'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO creation_runs SELECT 'new-run',content_digest,project_id,goal,strategy,preset,'running','',0,created_at_unix_ms+1,updated_at_unix_ms FROM creation_runs WHERE id='run:book-1'`)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book-1", OperationID: "op-054"})
	if err != nil {
		t.Fatal(err)
	}
	if historical.Run.ID != "run:book-1" {
		t.Fatal("operation scope selected latest run instead of historical run")
	}
}

func BenchmarkDiagSnapshot(b *testing.B) {
	for _, count := range []int{500, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(b.TempDir(), "diag.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer tx.Rollback()
			for _, statement := range []string{
				`INSERT INTO authority_streams(target_kind,target_id,current_revision) VALUES ('project','book',1)`,
				`INSERT INTO creation_runs(id,content_digest,project_id,goal,strategy,preset,state,created_at_unix_ms,updated_at_unix_ms) VALUES ('run','digest','book',x'7b7d',x'7b7d',x'7b7d','running',1,1)`,
			} {
				if _, err = tx.Exec(statement); err != nil {
					b.Fatal(err)
				}
			}
			payload := []byte(strings.Repeat("private-content-", 4096))
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("op-%04d", i)
				if _, err = tx.Exec(`INSERT INTO operations(id,content_digest,kind,target_kind,target_id,priority,state,attempt,execution_snapshot,input,executor,run_id,created_at_unix_ms,updated_at_unix_ms) VALUES (?,'digest','write_chapter','project','book',0,'succeeded',1,?,?,'llm.agent@1','run',1,1)`, id, []byte(`{"config_digest":"profile"}`), payload); err != nil {
					b.Fatal(err)
				}
				if _, err = tx.Exec(`INSERT INTO operation_events(operation_id,sequence,step_id,attempt,idempotency_key,kind,payload,payload_digest,created_at_unix_ms) VALUES (?,1,'',1,'message','agent.message_committed',?,'digest',1)`, id, payload); err != nil {
					b.Fatal(err)
				}
			}
			if err = tx.Commit(); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				snap, err := s.ReadDiagSnapshot(ctx, DiagRequest{ProjectID: "book"})
				if err != nil {
					b.Fatal(err)
				}
				if snap.OperationCount != count || len(snap.Operations) != 50 || len(snap.Events) != 200 {
					b.Fatal("unbounded diagnostic projection")
				}
				for _, event := range snap.Events {
					if len(event.Payload) != 0 {
						b.Fatal("summary fetched content")
					}
				}
			}
		})
	}
}
