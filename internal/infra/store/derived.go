package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func (s *Store) SaveDerivedDocument(ctx context.Context, document model.DerivedDocument) (model.DerivedDocument, error) {
	return saveDerivedDocument(ctx, s.db, document)
}

// SaveExecutionDerivedDocument 是执行侧写派生产出（如审阅裁定）的路径（D42）：归属检查与
// 写入同事务，被接替或已取消的执行落不了盘；幂等语义同 SaveDerivedDocument。
func (s *Store) SaveExecutionDerivedDocument(
	ctx context.Context, document model.DerivedDocument, operationID string, attempt int,
) (model.DerivedDocument, error) {
	if attempt <= 0 || strings.TrimSpace(operationID) == "" {
		return model.DerivedDocument{}, fmt.Errorf("execution derived write requires the operation and its attempt: %w", model.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.DerivedDocument{}, fmt.Errorf("begin execution derived write: %w", err)
	}
	defer tx.Rollback()
	if err := assertActiveAttempt(ctx, tx, operationID, attempt); err != nil {
		return model.DerivedDocument{}, err
	}
	stored, err := saveDerivedDocument(ctx, tx, document)
	if err != nil {
		return model.DerivedDocument{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.DerivedDocument{}, fmt.Errorf("commit execution derived write: %w", err)
	}
	return stored, nil
}

func saveDerivedDocument(ctx context.Context, db execQuerier, document model.DerivedDocument) (model.DerivedDocument, error) {
	if err := document.Validate(); err != nil {
		return model.DerivedDocument{}, err
	}
	document.Digest = model.Digest(document.Content)
	result, err := db.ExecContext(ctx, `
		INSERT INTO derived_documents (
			project_id, revision, kind, cache_key, content, content_digest, created_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id, revision, kind, cache_key) DO NOTHING`,
		document.ProjectID, document.Revision, document.Kind, document.Key,
		[]byte(document.Content), document.Digest, document.CreatedAt.UnixMilli())
	if err != nil {
		return model.DerivedDocument{}, fmt.Errorf("save derived document: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.DerivedDocument{}, fmt.Errorf("inspect derived document save: %w", err)
	}
	stored, err := getDerivedDocument(ctx, db, document.ProjectID, document.Revision, document.Kind, document.Key)
	if err != nil {
		return model.DerivedDocument{}, err
	}
	if rows == 0 && stored.Digest != document.Digest {
		return model.DerivedDocument{}, fmt.Errorf("derived document %q: %w", document.Key, model.ErrIdempotencyConflict)
	}
	return stored, nil
}

func (s *Store) GetDerivedDocument(
	ctx context.Context,
	projectID string,
	revision model.Revision,
	kind, key string,
) (model.DerivedDocument, error) {
	return getDerivedDocument(ctx, s.db, projectID, revision, kind, key)
}

func getDerivedDocument(
	ctx context.Context, query rowQuerier, projectID string, revision model.Revision, kind, key string,
) (model.DerivedDocument, error) {
	if strings.TrimSpace(projectID) == "" || revision <= model.InitialRevision ||
		strings.TrimSpace(kind) == "" || strings.TrimSpace(key) == "" {
		return model.DerivedDocument{}, fmt.Errorf("derived document identity and revision are required: %w", model.ErrInvalid)
	}
	var document model.DerivedDocument
	var content []byte
	var createdAt int64
	err := query.QueryRowContext(ctx, `
		SELECT content, content_digest, created_at_unix_ms
		FROM derived_documents
		WHERE project_id = ? AND revision = ? AND kind = ? AND cache_key = ?`,
		projectID, revision, kind, key).Scan(&content, &document.Digest, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DerivedDocument{}, model.ErrNotFound
	}
	if err != nil {
		return model.DerivedDocument{}, fmt.Errorf("read derived document: %w", err)
	}
	document.ProjectID, document.Revision, document.Kind, document.Key = projectID, revision, kind, key
	document.Content = append([]byte(nil), content...)
	document.CreatedAt = time.UnixMilli(createdAt).UTC()
	if model.Digest(document.Content) != document.Digest {
		return model.DerivedDocument{}, fmt.Errorf("derived document %q digest mismatch: %w", key, model.ErrStateConflict)
	}
	if err := document.Validate(); err != nil {
		return model.DerivedDocument{}, fmt.Errorf("stored derived document is invalid: %w", err)
	}
	return document, nil
}

func (s *Store) ListDerivedDocuments(
	ctx context.Context,
	projectID string,
	revision model.Revision,
) ([]model.DerivedDocument, error) {
	if strings.TrimSpace(projectID) == "" || revision <= model.InitialRevision {
		return nil, fmt.Errorf("derived project and revision are required: %w", model.ErrInvalid)
	}
	return s.listDerivedDocuments(ctx, `WHERE project_id = ? AND revision = ? ORDER BY kind, cache_key`, projectID, revision)
}

// ListDerivedDocumentsByKind 跨 Revision 列出一类派生文档（D48）：证据的有效性由
// 调用方按基线判定，按写入时间排序便于选"最新"。
func (s *Store) ListDerivedDocumentsByKind(
	ctx context.Context,
	projectID string,
	kind string,
) ([]model.DerivedDocument, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(kind) == "" {
		return nil, fmt.Errorf("derived project and kind are required: %w", model.ErrInvalid)
	}
	return s.listDerivedDocuments(ctx,
		`WHERE project_id = ? AND kind = ? ORDER BY created_at_unix_ms, revision, cache_key`, projectID, kind)
}

func (s *Store) listDerivedDocuments(ctx context.Context, clause string, args ...any) ([]model.DerivedDocument, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, revision, kind, cache_key, content, content_digest, created_at_unix_ms
		FROM derived_documents `+clause, args...)
	if err != nil {
		return nil, fmt.Errorf("list derived documents: %w", err)
	}
	defer rows.Close()
	var documents []model.DerivedDocument
	for rows.Next() {
		var document model.DerivedDocument
		var content []byte
		var createdAt int64
		if err := rows.Scan(&document.ProjectID, &document.Revision, &document.Kind, &document.Key,
			&content, &document.Digest, &createdAt); err != nil {
			return nil, fmt.Errorf("scan derived document: %w", err)
		}
		document.Content = append([]byte(nil), content...)
		document.CreatedAt = time.UnixMilli(createdAt).UTC()
		if model.Digest(document.Content) != document.Digest {
			return nil, fmt.Errorf("derived document %q digest mismatch: %w", document.Key, model.ErrStateConflict)
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
