package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestLeaseRenewalRejectsSupersededAttemptWithSameWorker(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("op", 1, now)); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimNextOperation(ctx, "worker", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverExpiredOperations(ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimNextOperation(ctx, "worker", time.Minute, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	at := now.Add(2*time.Minute + time.Second)
	if _, err := s.RenewOperationLease(ctx, first.ID, "worker", first.Attempt, time.Hour, at); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("superseded heartbeat = %v, want state conflict", err)
	}
	stored, err := s.GetOperation(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LeaseUntil.Equal(*second.LeaseUntil) {
		t.Fatalf("old heartbeat changed current lease: got %v, want %v", stored.LeaseUntil, second.LeaseUntil)
	}
	renewed, err := s.RenewOperationLease(ctx, second.ID, "worker", second.Attempt, time.Minute, at)
	if err != nil || !renewed.LeaseUntil.Equal(at.Add(time.Minute)) {
		t.Fatalf("current heartbeat = %#v, %v", renewed, err)
	}
}
