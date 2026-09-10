package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestArtifactPublishIsAtomicAndIdempotent(t *testing.T) {
	s := openOperationStore(t)
	publish := func(content string) (string, int64) {
		t.Helper()
		writer, err := s.NewArtifactWriter("op", 1)
		if err != nil {
			t.Fatalf("new writer: %v", err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatalf("write: %v", err)
		}
		digest, size, err := writer.Publish()
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
		return digest, size
	}
	digest, size := publish("封面图")
	sum := sha256.Sum256([]byte("封面图"))
	if digest != hex.EncodeToString(sum[:]) || size != int64(len("封面图")) {
		t.Fatalf("published %s/%d", digest, size)
	}
	content, err := s.ReadArtifact(digest)
	if err != nil || string(content) != "封面图" {
		t.Fatalf("read artifact = %q, %v", content, err)
	}
	if again, _ := publish("封面图"); again != digest {
		t.Fatalf("same content published as %s, want %s", again, digest)
	}
	staging, err := os.ReadDir(artifactStagingDir(s.artifactRoot, "op"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("staging after publish = %v, %v", staging, err)
	}
	aborted, err := s.NewArtifactWriter("op", 1)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	aborted.Write([]byte("半成品"))
	if err := aborted.Abort(); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if _, _, err := aborted.Publish(); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("publish after abort err = %v", err)
	}
	if _, err := s.ReadArtifact("00" + digest[2:]); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing object err = %v", err)
	}
}

func TestSaveExecutionArtifactsRequiresPublishedObjectAndActiveAttempt(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("op", 1, now)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now); err != nil {
		t.Fatalf("claim: %v", err)
	}
	writer, err := s.NewArtifactWriter("op", 1)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	writer.Write([]byte("cover"))
	digest, size, err := writer.Publish()
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	artifact := model.Artifact{
		ID: model.ArtifactID("op", "cover"), ProjectID: "book-1", Digest: digest, MediaType: "image/png",
		Size: size, OperationID: "op", Attempt: 1, CreatedAt: now,
	}
	unpublished := artifact
	unpublished.Digest = "00" + digest[2:]
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{unpublished}, "op", 1); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("unpublished object err = %v", err)
	}
	wrongSize := artifact
	wrongSize.Size = size + 1
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{wrongSize}, "op", 1); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("size mismatch err = %v", err)
	}
	foreign := artifact
	foreign.ProjectID = "other-book"
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{foreign}, "op", 1); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("foreign project err = %v", err)
	}
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{artifact}, "op", 1); err != nil {
		t.Fatalf("save: %v", err)
	}
	stored, err := s.GetArtifact(ctx, artifact.ID)
	if err != nil || stored.Digest != artifact.Digest || stored.Size != artifact.Size || !stored.Basis.Equal(artifact.Basis) {
		t.Fatalf("stored = %#v, %v", stored, err)
	}
	listed, err := s.ListArtifacts(ctx, "book-1")
	if err != nil || len(listed) != 1 || listed[0].ID != artifact.ID {
		t.Fatalf("listed = %#v, %v", listed, err)
	}
	if _, err := s.GetArtifact(ctx, "op/missing"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing artifact err = %v", err)
	}
}

func TestArtifactGarbageCollectionSparesReferencedStagingAndYoungObjects(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("op", 1, now)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now); err != nil {
		t.Fatalf("claim: %v", err)
	}
	publish := func(content string) string {
		t.Helper()
		writer, err := s.NewArtifactWriter("op", 1)
		if err != nil {
			t.Fatalf("new writer: %v", err)
		}
		writer.Write([]byte(content))
		digest, _, err := writer.Publish()
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
		return digest
	}
	referenced, orphan, young := publish("referenced"), publish("orphan"), publish("young")
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{{
		ID: model.ArtifactID("op", "kept"), ProjectID: "book-1", Digest: referenced, MediaType: "text/plain",
		Size: int64(len("referenced")), OperationID: "op", Attempt: 1, CreatedAt: now,
	}}, "op", 1); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 进行中任务的暂存文件与已结束任务的暂存文件各一份。
	running, _ := s.NewArtifactWriter("op", 1)
	running.Write([]byte("half"))
	finished, _ := s.NewArtifactWriter("gone", 1)
	finished.Write([]byte("half"))
	old := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{objectPath(s.artifactRoot, referenced), objectPath(s.artifactRoot, orphan), running.file.Name(), finished.file.Name()} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("age %s: %v", path, err)
		}
	}
	removed, err := s.CollectArtifactGarbage(ctx, time.Now(), 24*time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("removed = %d, %v", removed, err)
	}
	for digest, want := range map[string]bool{referenced: true, orphan: true, young: true} {
		_, err := os.Stat(objectPath(s.artifactRoot, digest))
		if (err == nil) != want {
			t.Fatalf("object %s exists = %v, want %v", digest, err == nil, want)
		}
	}
	if _, err := os.Stat(running.file.Name()); err != nil {
		t.Fatal("staging of a running operation was collected")
	}
	if _, err := os.Stat(finished.file.Name()); err == nil {
		t.Fatal("stale staging of a finished operation survived")
	}
}

// TestCancelBeforeArtifactMetadataCommitRejectsSave 守护取消与提交的竞争：对象已发布
// 但元数据尚未提交时用户取消，元数据写入被围栏拒绝，孤儿对象留给显式 GC。
func TestCancelBeforeArtifactMetadataCommitRejectsSave(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("op", 1, now)); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	writer, err := s.NewArtifactWriter("op", claimed.Attempt)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	writer.Write([]byte("cover"))
	digest, size, err := writer.Publish()
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := s.TransitionOperation(ctx, "op", model.OperationRunning, model.OperationCancelled, "cancelled by user", now.Add(time.Minute)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	artifact := model.Artifact{
		ID: model.ArtifactID("op", "cover"), ProjectID: "book-1", Digest: digest, MediaType: "image/png",
		Size: size, OperationID: "op", Attempt: claimed.Attempt, CreatedAt: now,
	}
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{artifact}, "op", claimed.Attempt); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("save after cancel err = %v, want model.ErrStateConflict", err)
	}
	if _, err := s.GetArtifact(ctx, artifact.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("metadata must not persist, got %v", err)
	}
	if _, err := s.ReadArtifact(digest); err != nil {
		t.Fatalf("published object stays until explicit gc: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(objectPath(s.artifactRoot, digest), old, old); err != nil {
		t.Fatalf("age object: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artifact_publications SET created_at_unix_ms = ?`, old.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if removed, err := s.CollectArtifactGarbage(ctx, time.Now(), 24*time.Hour); err != nil || removed != 1 {
		t.Fatalf("gc removed = %d, %v", removed, err)
	}
}
