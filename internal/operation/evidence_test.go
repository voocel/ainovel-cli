package operation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestEvidenceContractCannotDropFrozenDependencies(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	op := createAssetOperation(t, ctx, s, time.Now())
	engine := NewEngine(s, VerdictContract{Kind: domain.OperationGenerateAsset, Validate: func(context.Context, domain.Operation, json.RawMessage) (domain.EvidenceBasis, error) {
		return domain.EvidenceBasis{}, nil
	}})
	if _, err := engine.ValidateEvidence(ctx, op, json.RawMessage(`{"passed":true}`)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("contract dropped source dependencies: %v", err)
	}
}
