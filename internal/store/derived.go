package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func (s *Store) SaveDerivedDocument(ctx context.Context, document domain.DerivedDocument) (domain.DerivedDocument, error) {
	if err := document.Validate(); err != nil {
		return domain.DerivedDocument{}, err
	}
	document.Digest = domain.Digest(document.Content)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO derived_documents (
			project_id, revision, kind, cache_key, content, content_digest, created_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id, revision, kind, cache_key) DO NOTHING`,
		document.ProjectID, document.Revision, document.Kind, document.Key,
		[]byte(document.Content), document.Digest, document.CreatedAt.UnixMilli())
	if err != nil {
		return domain.DerivedDocument{}, fmt.Errorf("save derived document: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.DerivedDocument{}, fmt.Errorf("inspect derived document save: %w", err)
	}
	stored, err := s.GetDerivedDocument(ctx, document.ProjectID, document.Revision, document.Kind, document.Key)
	if err != nil {
		return domain.DerivedDocument{}, err
	}
	if rows == 0 && stored.Digest != document.Digest {
		return domain.DerivedDocument{}, fmt.Errorf("derived document %q: %w", document.Key, ErrIdempotencyConflict)
	}
	return stored, nil
}

func (s *Store) GetDerivedDocument(
	ctx context.Context,
	projectID string,
	revision domain.Revision,
	kind, key string,
) (domain.DerivedDocument, error) {
	if strings.TrimSpace(projectID) == "" || revision <= domain.InitialRevision ||
		strings.TrimSpace(kind) == "" || strings.TrimSpace(key) == "" {
		return domain.DerivedDocument{}, fmt.Errorf("derived document identity and revision are required: %w", domain.ErrInvalid)
	}
	var document domain.DerivedDocument
	var content []byte
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT content, content_digest, created_at_unix_ms
		FROM derived_documents
		WHERE project_id = ? AND revision = ? AND kind = ? AND cache_key = ?`,
		projectID, revision, kind, key).Scan(&content, &document.Digest, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DerivedDocument{}, ErrNotFound
	}
	if err != nil {
		return domain.DerivedDocument{}, fmt.Errorf("read derived document: %w", err)
	}
	document.ProjectID, document.Revision, document.Kind, document.Key = projectID, revision, kind, key
	document.Content = append([]byte(nil), content...)
	document.CreatedAt = time.UnixMilli(createdAt).UTC()
	if domain.Digest(document.Content) != document.Digest {
		return domain.DerivedDocument{}, fmt.Errorf("derived document %q digest mismatch: %w", key, ErrStateConflict)
	}
	if err := document.Validate(); err != nil {
		return domain.DerivedDocument{}, fmt.Errorf("stored derived document is invalid: %w", err)
	}
	return document, nil
}

func (s *Store) ListDerivedDocuments(
	ctx context.Context,
	projectID string,
	revision domain.Revision,
) ([]domain.DerivedDocument, error) {
	if strings.TrimSpace(projectID) == "" || revision <= domain.InitialRevision {
		return nil, fmt.Errorf("derived project and revision are required: %w", domain.ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind, cache_key, content, content_digest, created_at_unix_ms
		FROM derived_documents
		WHERE project_id = ? AND revision = ?
		ORDER BY kind, cache_key`, projectID, revision)
	if err != nil {
		return nil, fmt.Errorf("list derived documents: %w", err)
	}
	defer rows.Close()
	var documents []domain.DerivedDocument
	for rows.Next() {
		var document domain.DerivedDocument
		var content []byte
		var createdAt int64
		if err := rows.Scan(&document.Kind, &document.Key, &content, &document.Digest, &createdAt); err != nil {
			return nil, fmt.Errorf("scan derived document: %w", err)
		}
		document.ProjectID, document.Revision = projectID, revision
		document.Content = append([]byte(nil), content...)
		document.CreatedAt = time.UnixMilli(createdAt).UTC()
		if domain.Digest(document.Content) != document.Digest {
			return nil, fmt.Errorf("derived document %q digest mismatch: %w", document.Key, ErrStateConflict)
		}
		if err := document.Validate(); err != nil {
			return nil, fmt.Errorf("stored derived document is invalid: %w", err)
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate derived documents: %w", err)
	}
	return documents, nil
}
