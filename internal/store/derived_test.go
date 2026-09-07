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
