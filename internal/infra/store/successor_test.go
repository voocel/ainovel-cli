package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestSuccessorPublicationIncludesWorkspaceAtomically(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	previous := testOperation("source", 1, now)
	if _, err := s.CreateOperation(ctx, previous); err != nil {
		t.Fatal(err)
	}
	running, err := s.ClaimNextOperation(ctx, "worker", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutWorkspaceArtifact(ctx, model.WorkspaceArtifact{OperationID: running.ID, Key: model.ExternalRequestKey, MediaType: "application/json", Content: []byte(`{"request_id":"already-submitted"}`), UpdatedAt: now}, 0, running.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionOperation(ctx, running.ID, model.OperationRunning, model.OperationFailed, "result unknown", now); err != nil {
		t.Fatal(err)
	}
	successor := testOperation("successor", 1, now.Add(time.Second))
	eventsBefore, err := s.ListCreationRunEvents(ctx, previous.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate failure after the queued row and lineage event have been inserted.
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_successor_seed BEFORE INSERT ON operation_artifacts WHEN NEW.operation_id = 'successor' BEGIN SELECT RAISE(ABORT, 'injected seed failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSuccessorOperation(ctx, previous.ID, successor); err == nil {
		t.Fatal("expected interrupted seeding")
	}
	if _, err := s.GetOperation(ctx, successor.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("partially published successor: %v", err)
	}
	if _, err := s.ClaimNextOperation(ctx, "other-worker", time.Minute, now.Add(time.Second)); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("worker could claim failed successor: %v", err)
	}
	eventsAfter, err := s.ListCreationRunEvents(ctx, previous.RunID)
	if err != nil || len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("orphan lineage event: %v, %v", eventsAfter, err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER fail_successor_seed`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSuccessorOperation(ctx, previous.ID, successor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSuccessorOperation(ctx, previous.ID, successor); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "other-worker", time.Minute, now.Add(time.Second))
	if err != nil || claimed.ID != successor.ID {
		t.Fatalf("claim successor: %#v %v", claimed, err)
	}
	inherited, err := s.GetWorkspaceArtifact(ctx, claimed.ID, model.ExternalRequestKey)
	if err != nil || string(inherited.Content) != `{"request_id":"already-submitted"}` {
		t.Fatalf("claim lacked original request: %#v %v", inherited, err)
	}
}
