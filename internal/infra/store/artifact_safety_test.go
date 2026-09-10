package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func publishSafetyArtifact(t *testing.T, s *Store, op, content string) model.Artifact {
	t.Helper()
	w, err := s.NewArtifactWriter(op, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	digest, size, err := w.Publish()
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{ID: model.ArtifactID(op, "asset"), ProjectID: "book-1", Digest: digest, Size: size, MediaType: "text/plain", OperationID: op, Attempt: 1, CreatedAt: time.Now()}
}

func seedSafetyRun(t *testing.T, s *Store) {
	t.Helper()
	now := operationTime()
	_, err := s.CreateCreationRun(context.Background(), model.CreationRun{ID: "run:book-1", ProjectID: "book-1", Goal: model.NovelGoal{Premise: "测试", TargetChapters: 1}.Goal(), Strategy: model.CreationRunStrategy{PlanWindowChapters: 1, ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: 1}, Preset: model.CreationRunPreset{Source: "test", Digest: "test", Approval: model.ApprovalAuto}, State: model.RunRunning, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArtifactDatabasesHaveIndependentOwnership(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, err := Open(ctx, filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, filepath.Join(dir, "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	now := operationTime()
	seedSafetyRun(t, a)
	if _, err = a.CreateOperation(ctx, testOperation("op", 1, now)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ClaimNextOperation(ctx, "worker", time.Minute, now); err != nil {
		t.Fatal(err)
	}
	artifact := publishSafetyArtifact(t, a, "op", "kept")
	if err = a.SaveExecutionArtifacts(ctx, []model.Artifact{artifact}, "op", 1); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err = os.Chtimes(objectPath(a.artifactRoot, artifact.Digest), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err = b.CollectArtifactGarbage(ctx, time.Now(), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if content, err := a.ReadArtifact(artifact.Digest); err != nil || string(content) != "kept" {
		t.Fatalf("referenced object lost: %q %v", content, err)
	}
}

func TestArtifactPublicationPinSurvivesCollectionFromAnotherStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	now := operationTime()
	seedSafetyRun(t, a)
	if _, err = a.CreateOperation(ctx, testOperation("op", 1, now)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ClaimNextOperation(ctx, "worker", time.Minute, now); err != nil {
		t.Fatal(err)
	}
	oldArtifact := publishSafetyArtifact(t, a, "abandoned", "same bytes")
	old := time.Now().Add(-48 * time.Hour)
	if err = os.Chtimes(objectPath(a.artifactRoot, oldArtifact.Digest), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE artifact_publications SET created_at_unix_ms = ?`, old.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	artifact := publishSafetyArtifact(t, a, "op", "same bytes")
	if _, err = b.CollectArtifactGarbage(ctx, time.Now().Add(48*time.Hour), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err = a.SaveExecutionArtifacts(ctx, []model.Artifact{artifact}, "op", 1); err != nil {
		t.Fatalf("active publication collected: %v", err)
	}
	if _, err = b.CollectArtifactGarbage(ctx, time.Now().Add(48*time.Hour), 0); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReadArtifact(artifact.Digest); err != nil {
		t.Fatal(err)
	}
	changed := publishSafetyArtifact(t, a, "op", "different bytes")
	if err = a.SaveExecutionArtifacts(ctx, []model.Artifact{changed}, "op", 1); !errors.Is(err, model.ErrIdempotencyConflict) {
		t.Fatalf("immutable artifact replaced: %v", err)
	}
}

func TestArtifactDigestBoundaryRejectsMalformedValues(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("op", 1, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextOperation(ctx, "worker", time.Minute, now); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{"x", "../secret", string(make([]byte, 64))} {
		a := model.Artifact{ID: "op/a", ProjectID: "book-1", Digest: digest, MediaType: "text/plain", OperationID: "op", Attempt: 1, CreatedAt: now}
		if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{a}, "op", 1); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("invalid digest accepted: %v", err)
		}
		if _, err := s.ReadArtifact(digest); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("invalid read accepted: %v", err)
		}
	}
}

func TestArtifactIDCollisionCannotChangeExecutionOwnership(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	for _, id := range []string{"a", "a/b"} {
		if _, err := s.CreateOperation(ctx, testOperation(id, 1, now)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimOperationForExecutor(ctx, id, "worker", testExecutor, time.Minute, now); err != nil {
			t.Fatal(err)
		}
	}
	first := publishSafetyArtifact(t, s, "a", "same bytes")
	first.ID = model.ArtifactID("a", "b/c")
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{first}, "a", 1); err != nil {
		t.Fatal(err)
	}
	second := publishSafetyArtifact(t, s, "a/b", "same bytes")
	second.ID = model.ArtifactID("a/b", "c")
	second.CreatedAt = first.CreatedAt.Add(time.Hour)
	if first.ID != second.ID {
		t.Fatal("fixture must exercise an ID collision")
	}
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{second}, "a/b", 1); !errors.Is(err, model.ErrIdempotencyConflict) {
		t.Fatalf("different execution with colliding ID = %v, want idempotency conflict", err)
	}
	stored, err := s.GetArtifact(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OperationID != first.OperationID || stored.CreatedAt.UnixMilli() != first.CreatedAt.UnixMilli() {
		t.Fatalf("collision changed original artifact: %#v", stored)
	}
}
