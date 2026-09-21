package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Materialization uses the frozen base, never the latest revision. Explicit
// old_value assertions remain subject to the change engine's strict comparison.
func (r *Runtime) materializeCanon(ctx context.Context, operation model.Operation, patches []model.Patch, confirmations []string) ([]model.Patch, error) {
	result := append([]model.Patch(nil), patches...)
	seen := make(map[string]bool)
	for i, patch := range result {
		if patch.Document.Kind != model.DocumentCanon {
			continue
		}
		if seen[patch.Document.ID] {
			return nil, fmt.Errorf("duplicate canon action %q: %w", patch.Document.ID, model.ErrInvalid)
		}
		seen[patch.Document.ID] = true
		if patch.Operation != model.PatchPut {
			continue
		}
		var fact model.CanonFact
		if err := decodeToolArgs(patch.Content, &fact); err != nil {
			return nil, fmt.Errorf("decode canon %q: %w", patch.Document.ID, err)
		}
		if len(fact.PreviousValue) != 0 || operation.Snapshot.BaseRevision == model.InitialRevision {
			continue
		}
		previous, err := r.store.GetDocument(ctx, operation.Target, patch.Document, operation.Snapshot.BaseRevision)
		if errors.Is(err, model.ErrNotFound) {
			continue // New facts have no previous value.
		}
		if err != nil {
			return nil, fmt.Errorf("read canon baseline %q: %w", patch.Document.ID, err)
		}
		var base model.CanonFact
		if err := decodeToolArgs(previous.Content, &base); err != nil {
			return nil, err
		}
		fact.PreviousValue = base.Value
		result[i].Content, err = json.Marshal(fact)
		if err != nil {
			return nil, err
		}
	}
	for _, id := range confirmations {
		if id == "" || seen[id] {
			return nil, fmt.Errorf("confirm_canon requires distinct fact IDs without overlapping patches: %q: %w", id, model.ErrInvalid)
		}
		seen[id] = true
		ref := model.DocumentRef{Kind: model.DocumentCanon, ID: id}
		previous, err := r.store.GetDocument(ctx, operation.Target, ref, operation.Snapshot.BaseRevision)
		if err != nil {
			return nil, fmt.Errorf("confirm canon %q at revision %d: %w", id, operation.Snapshot.BaseRevision, err)
		}
		var fact model.CanonFact
		if err := decodeToolArgs(previous.Content, &fact); err != nil {
			return nil, err
		}
		fact.PreviousValue = fact.Value
		content, err := json.Marshal(fact)
		if err != nil {
			return nil, err
		}
		result = append(result, model.Patch{Document: ref, Operation: model.PatchPut, Content: content})
	}
	return result, nil
}
