package store

import (
	"context"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// ListDocumentVersions 读取截至 at 的全部已提交内容版本，包括后来已被替换或删除
// 的文档。它用于重建来源记录，不把历史版本当成当前权威内容。
func (s *Store) ListDocumentVersions(ctx context.Context, target domain.AuthorityTarget, kind domain.DocumentKind, at domain.Revision) ([]domain.DocumentVersion, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if _, err := domain.DocumentType(kind); err != nil {
		return nil, err
	}
	if at <= domain.InitialRevision {
		return nil, fmt.Errorf("revision must be positive: %w", domain.ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT document_id, revision, content, change_set_id FROM document_versions
		WHERE target_kind = ? AND target_id = ? AND target_scope = ?
		AND document_kind = ? AND revision <= ? AND operation = 'put'
		ORDER BY revision, document_id`, target.Kind, target.ID, target.Scope, kind, at)
	if err != nil {
		return nil, fmt.Errorf("list document versions: %w", err)
	}
	defer rows.Close()
	var versions []domain.DocumentVersion
	for rows.Next() {
		version := domain.DocumentVersion{Target: target, Document: domain.DocumentRef{Kind: kind}}
		if err := rows.Scan(&version.Document.ID, &version.Revision, &version.Content, &version.ChangeSetID); err != nil {
			return nil, fmt.Errorf("scan document version: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate document versions: %w", err)
	}
	return versions, nil
}
