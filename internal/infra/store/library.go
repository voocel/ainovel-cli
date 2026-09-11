package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// ProjectSummary reads only the intent and live chapter count. Manuscript bodies
// never leave SQLite when listing the library.
type ProjectSummary struct {
	ID      string
	Intent  json.RawMessage
	Written int
}

func (s *Store) ListProjectSummaries(ctx context.Context) ([]ProjectSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
 SELECT a.target_id,
  (SELECT d.content FROM document_versions d
   WHERE d.target_kind=a.target_kind AND d.target_id=a.target_id AND d.target_scope=''
    AND d.document_kind=? AND d.document_id='root' AND d.revision<=a.current_revision
   ORDER BY d.revision DESC LIMIT 1),
  (SELECT COUNT(*) FROM document_versions d
   WHERE d.target_kind=a.target_kind AND d.target_id=a.target_id AND d.target_scope=''
    AND d.document_kind=? AND d.operation='put' AND d.revision<=a.current_revision
    AND NOT EXISTS (SELECT 1 FROM document_versions newer
     WHERE newer.target_kind=d.target_kind AND newer.target_id=d.target_id AND newer.target_scope=d.target_scope
      AND newer.document_kind=d.document_kind AND newer.document_id=d.document_id
      AND newer.revision>d.revision AND newer.revision<=a.current_revision))
 FROM authority_streams a
 WHERE a.target_kind=? AND a.target_scope='' AND a.current_revision>0
 ORDER BY a.target_id`, model.DocumentIntent, model.DocumentManuscript, model.AuthorityProject)
	if err != nil {
		return nil, fmt.Errorf("list project summaries: %w", err)
	}
	defer rows.Close()
	var result []ProjectSummary
	for rows.Next() {
		var summary ProjectSummary
		if err := rows.Scan(&summary.ID, &summary.Intent, &summary.Written); err != nil {
			return nil, fmt.Errorf("scan project summary: %w", err)
		}
		result = append(result, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project summaries: %w", err)
	}
	return result, nil
}
