package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestDerivedDocumentsAreRevisionScopedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	document := domain.DerivedDocument{
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
	if _, err := s.SaveDerivedDocument(ctx, document); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed derived cache error = %v", err)
	}
	documents, err := s.ListDerivedDocuments(ctx, "book-1", 2)
	if err != nil || len(documents) != 1 {
		t.Fatalf("list derived documents = %#v, %v", documents, err)
	}
}

// TestExecutionDerivedWriteIsFencedInsideTheTransaction 守护 D42 对证据类派生产出的围栏：
// 审阅裁定只能由当前执行落盘，取消或被接替的执行写入被拒且不留痕。
func TestExecutionDerivedWriteIsFencedInsideTheTransaction(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	start := operationTime()
	verdict := domain.DerivedDocument{
		ProjectID: "book-1", Revision: 1, Kind: domain.DerivedVerdictKind, Key: "review",
		Content: json.RawMessage(`{"status":"pass"}`), CreatedAt: start,
	}

	if _, err := s.CreateOperation(ctx, testOperation("review", 1, start)); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, start)
	if err != nil || claimed.Attempt != 1 {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if _, err := s.SaveExecutionDerivedDocument(ctx, verdict, claimed.ID, claimed.Attempt+1); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("foreign attempt write error = %v, want ErrStateConflict", err)
	}
	if _, err := s.TransitionOperation(ctx, claimed.ID, domain.OperationRunning, domain.OperationCancelled, "cancelled by user", start.Add(time.Minute)); err != nil {
		t.Fatalf("user cancel: %v", err)
	}
	if _, err := s.SaveExecutionDerivedDocument(ctx, verdict, claimed.ID, claimed.Attempt); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("write after cancel error = %v, want ErrStateConflict", err)
	}
	if _, err := s.GetDerivedDocument(ctx, verdict.ProjectID, verdict.Revision, verdict.Kind, verdict.Key); !errors.Is(err, ErrNotFound) {
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
