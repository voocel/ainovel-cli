package project

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func (s *Repository) canonRecorded(ctx context.Context, target model.AuthorityTarget, at model.Revision) (map[string]model.Revision, error) {
	versions, err := s.store.ListDocumentVersions(ctx, target, model.DocumentCanon, at)
	if err != nil {
		return nil, err
	}
	recorded := make(map[string]model.Revision)
	for _, version := range versions {
		var fact model.CanonFact
		if err := json.Unmarshal(version.Content, &fact); err != nil {
			return nil, fmt.Errorf("decode historical Canon %s: %w", version.Document.ID, err)
		}
		if fact.SourceChapterID != "" {
			recorded[fact.SourceChapterID] = max(recorded[fact.SourceChapterID], version.Revision)
		}
	}
	return recorded, nil
}
