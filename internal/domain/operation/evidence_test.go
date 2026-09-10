package operation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

func TestEvidenceContractCannotDropFrozenDependencies(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	op := createAssetOperation(t, ctx, s, time.Now())
	engine := NewEngine(s, VerdictContract{Kind: model.OperationGenerateAsset, Validate: func(context.Context, model.Operation, json.RawMessage) (model.EvidenceBasis, error) {
		return model.EvidenceBasis{}, nil
	}})
	if _, err := engine.ValidateEvidence(ctx, op, json.RawMessage(`{"passed":true}`)); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("contract dropped source dependencies: %v", err)
	}
}
