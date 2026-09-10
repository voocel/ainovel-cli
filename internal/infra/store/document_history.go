package store

import (
	"context"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// ListDocumentVersions 读取截至 at 的全部已提交内容版本，包括后来已被替换或删除
// 的文档。它用于重建来源记录，不把历史版本当成当前权威内容。
func (s *Store) ListDocumentVersions(ctx context.Context, target model.AuthorityTarget, kind model.DocumentKind, at model.Revision) ([]model.DocumentVersion, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if _, err := model.DocumentType(kind); err != nil {
		return nil, err
	}
	if at <= model.InitialRevision {
		return nil, fmt.Errorf("revision must be positive: %w", model.ErrInvalid)
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
	var versions []model.DocumentVersion
	for rows.Next() {
		version := model.DocumentVersion{Target: target, Document: model.DocumentRef{Kind: kind}}
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
