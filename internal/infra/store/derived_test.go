package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestDerivedDocumentsAreRevisionScopedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openOperationStore(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	document := model.DerivedDocument{
		ProjectID: "book-1", Revision: 2, Kind: "story_context.v1", Key: "write-1",
		Content: json.RawMessage(`{"chapter":"chapter-1"}`), CreatedAt: now,
	}
	first, err := s.SaveDerivedDocument(ctx, document)
	if err != nil || first.Digest == "" {
		t.Fatalf("save derived document = %#v, %v", first, err)
	}
	document.CreatedAt = now.Add(time.Minute)
	second, err := s.SaveDerivedDocument(ctx, document)
	if err != nil || second.Digest != first.Digest || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("idempotent derived document = %#v, %v", second, err)
	}
	document.Content = json.RawMessage(`{"chapter":"chapter-2"}`)
	if _, err := s.SaveDerivedDocument(ctx, document); !errors.Is(err, model.ErrIdempotencyConflict) {
		t.Fatalf("changed derived cache error = %v", err)
	}
	documents, err := s.ListDerivedDocuments(ctx, "book-1", 2)
	if err != nil || len(documents) != 1 {
		t.Fatalf("list derived documents = %#v, %v", documents, err)
	}
	// 按种类跨 Revision 列出（D48）：按写入时间排序，其他种类不混入。
	later := model.DerivedDocument{
		ProjectID: "book-1", Revision: 3, Kind: "story_context.v1", Key: "write-2",
		Content: json.RawMessage(`{"chapter":"chapter-2"}`), CreatedAt: now.Add(time.Hour),
	}
	other := model.DerivedDocument{
		ProjectID: "book-1", Revision: 3, Kind: model.DerivedVerdictKind, Key: "review-1",
		Content: json.RawMessage(`{"status":"pass"}`), CreatedAt: now.Add(2 * time.Hour),
	}
	for _, document := range []model.DerivedDocument{later, other} {
		if _, err := s.SaveDerivedDocument(ctx, document); err != nil {
			t.Fatalf("save %s: %v", document.Key, err)
		}
	}
	byKind, err := s.ListDerivedDocumentsByKind(ctx, "book-1", "story_context.v1")
	if err != nil || len(byKind) != 2 || byKind[0].Revision != 2 || byKind[1].Revision != 3 || byKind[1].Key != "write-2" {
		t.Fatalf("list by kind = %#v, %v", byKind, err)
	}
}

// TestExecutionDerivedWriteIsFencedInsideTheTransaction 守护 D42 对证据类派生产出的围栏：
// 审阅裁定只能由当前执行落盘，取消或被接替的执行写入被拒且不留痕。
func TestExecutionDerivedWriteIsFencedInsideTheTransaction(t *testing.T) {
	ctx := context.Background()
	s := openOperationStore(t)
	start := operationTime()
	verdict := model.DerivedDocument{
		ProjectID: "book-1", Revision: 1, Kind: model.DerivedVerdictKind, Key: "review",
		Content: json.RawMessage(`{"status":"pass"}`), CreatedAt: start,
	}

	if _, err := s.CreateOperation(ctx, testOperation("review", 1, start)); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, start)
	if err != nil || claimed.Attempt != 1 {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if _, err := s.SaveExecutionDerivedDocument(ctx, verdict, claimed.ID, claimed.Attempt+1); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("foreign attempt write error = %v, want model.ErrStateConflict", err)
	}
	if _, err := s.TransitionOperation(ctx, claimed.ID, model.OperationRunning, model.OperationCancelled, "cancelled by user", start.Add(time.Minute)); err != nil {
		t.Fatalf("user cancel: %v", err)
	}
	if _, err := s.SaveExecutionDerivedDocument(ctx, verdict, claimed.ID, claimed.Attempt); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("write after cancel error = %v, want model.ErrStateConflict", err)
	}
	if _, err := s.GetDerivedDocument(ctx, verdict.ProjectID, verdict.Revision, verdict.Kind, verdict.Key); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("cancelled execution must not persist a verdict, got %v", err)
	}

	if _, err := s.CreateOperation(ctx, testOperation("review-2", 1, start)); err != nil {
		t.Fatalf("create second operation: %v", err)
	}
	active, err := s.ClaimNextOperation(ctx, "worker-2", time.Minute, start.Add(2*time.Minute))
	if err != nil || active.ID != "review-2" {
		t.Fatalf("claim second = %#v, %v", active, err)
	}
	verdict.Key = active.ID
	stored, err := s.SaveExecutionDerivedDocument(ctx, verdict, active.ID, active.Attempt)
	if err != nil || stored.Digest == "" {
		t.Fatalf("active execution write = %#v, %v", stored, err)
	}
}
