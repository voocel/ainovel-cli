package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func commitProposalTx(ctx context.Context, tx *sql.Tx, proposal domain.Proposal, digest string) (domain.ChangeSet, error) {
	storedRevision, storedDigest, found, err := findCommittedChange(ctx, tx, proposal.ID)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	if found {
		if storedDigest != digest {
			return domain.ChangeSet{}, fmt.Errorf("change set %q: %w", proposal.ID, ErrIdempotencyConflict)
		}
		return domain.ChangeSet{Proposal: proposal, NewRevision: storedRevision}, nil
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO authority_streams (target_kind, target_id, target_scope, current_revision)
		VALUES (?, ?, ?, 0)
		ON CONFLICT (target_kind, target_id, target_scope) DO NOTHING`,
		proposal.Target.Kind, proposal.Target.ID, proposal.Target.Scope); err != nil {
		return domain.ChangeSet{}, fmt.Errorf("ensure authority stream: %w", err)
	}

	newRevision := proposal.BaseRevision + 1
	result, err := tx.ExecContext(ctx, `
		UPDATE authority_streams
		SET current_revision = ?
		WHERE target_kind = ? AND target_id = ? AND target_scope = ? AND current_revision = ?`,
		newRevision, proposal.Target.Kind, proposal.Target.ID, proposal.Target.Scope, proposal.BaseRevision)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("advance authority revision: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("inspect authority revision update: %w", err)
	}
	if rows != 1 {
		current, currentErr := currentRevisionTx(ctx, tx, proposal.Target)
		if currentErr != nil {
			return domain.ChangeSet{}, currentErr
		}
		return domain.ChangeSet{}, fmt.Errorf("base revision %d, current revision %d: %w", proposal.BaseRevision, current, ErrRevisionConflict)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO change_sets (
			id, content_digest, target_kind, target_id, target_scope,
			base_revision, new_revision, author_kind, author_id, reason,
			structural_impact, semantic_impact, compliance_impact, approval_state,
			decision_author_kind, decision_author_id, decided_at_unix_ms, created_at_unix_ms,
			operation_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		proposal.ID, digest, proposal.Target.Kind, proposal.Target.ID, proposal.Target.Scope,
		proposal.BaseRevision, newRevision, proposal.Author.Kind, proposal.Author.ID, proposal.Reason,
		nullableJSON(proposal.Impact.Structural), nullableJSON(proposal.Impact.Semantic),
		nullableJSON(proposal.Impact.Compliance),
		proposal.ApprovalState, proposal.DecidedBy.Kind, proposal.DecidedBy.ID,
		proposal.DecidedAt.UnixMilli(), proposal.CreatedAt.UnixMilli(), nullableString(proposal.OperationID)); err != nil {
		return domain.ChangeSet{}, fmt.Errorf("insert change set: %w", err)
	}

	for i, patch := range proposal.Patches {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO document_versions (
				target_kind, target_id, target_scope, document_kind, document_id,
				revision, operation, content, change_set_id
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			proposal.Target.Kind, proposal.Target.ID, proposal.Target.Scope,
			patch.Document.Kind, patch.Document.ID, newRevision, patch.Operation,
			nullableJSON(patch.Content), proposal.ID); err != nil {
			return domain.ChangeSet{}, fmt.Errorf("insert patch %d: %w", i, err)
		}
	}
	return domain.ChangeSet{Proposal: proposal, NewRevision: newRevision}, nil
}

func (s *Store) CurrentRevision(ctx context.Context, target domain.AuthorityTarget) (domain.Revision, error) {
	if err := target.Validate(); err != nil {
		return 0, err
	}
	var revision domain.Revision
	err := s.db.QueryRowContext(ctx, `
		SELECT current_revision FROM authority_streams
		WHERE target_kind = ? AND target_id = ? AND target_scope = ?`,
		target.Kind, target.ID, target.Scope).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read current revision: %w", err)
	}
	return revision, nil
}

func (s *Store) GetDocument(ctx context.Context, target domain.AuthorityTarget, ref domain.DocumentRef, at domain.Revision) (domain.DocumentVersion, error) {
	if err := target.Validate(); err != nil {
		return domain.DocumentVersion{}, err
	}
	if err := ref.Validate(); err != nil {
		return domain.DocumentVersion{}, err
	}
	if at <= domain.InitialRevision {
		return domain.DocumentVersion{}, fmt.Errorf("revision must be positive: %w", domain.ErrInvalid)
	}

	var operation domain.PatchOperation
	var content []byte
	var version domain.DocumentVersion
	err := s.db.QueryRowContext(ctx, `
		SELECT revision, operation, content, change_set_id
		FROM document_versions
		WHERE target_kind = ? AND target_id = ? AND target_scope = ?
			AND document_kind = ? AND document_id = ? AND revision <= ?
		ORDER BY revision DESC
		LIMIT 1`,
		target.Kind, target.ID, target.Scope, ref.Kind, ref.ID, at).
		Scan(&version.Revision, &operation, &content, &version.ChangeSetID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DocumentVersion{}, ErrNotFound
	}
	if err != nil {
		return domain.DocumentVersion{}, fmt.Errorf("read document: %w", err)
	}
	if operation == domain.PatchDelete {
		return domain.DocumentVersion{}, ErrNotFound
	}
	version.Target = target
	version.Document = ref
	version.Content = json.RawMessage(append([]byte(nil), content...))
	return version, nil
}

func (s *Store) ListDocuments(ctx context.Context, target domain.AuthorityTarget, kind domain.DocumentKind, at domain.Revision) ([]domain.DocumentVersion, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if err := (domain.DocumentRef{Kind: kind, ID: "validation"}).Validate(); err != nil {
		return nil, err
	}
	if at <= domain.InitialRevision {
		return nil, fmt.Errorf("revision must be positive: %w", domain.ErrInvalid)
	}

	rows, err := s.db.QueryContext(ctx, `
		WITH latest AS (
			SELECT document_id, MAX(revision) AS revision
			FROM document_versions
			WHERE target_kind = ? AND target_id = ? AND target_scope = ?
				AND document_kind = ? AND revision <= ?
			GROUP BY document_id
		)
		SELECT v.document_id, v.revision, v.content, v.change_set_id
		FROM latest
		JOIN document_versions v
			ON v.target_kind = ? AND v.target_id = ? AND v.target_scope = ?
			AND v.document_kind = ? AND v.document_id = latest.document_id
			AND v.revision = latest.revision
		WHERE v.operation = 'put'
		ORDER BY v.document_id`,
		target.Kind, target.ID, target.Scope, kind, at,
		target.Kind, target.ID, target.Scope, kind)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	documents := make([]domain.DocumentVersion, 0)
	for rows.Next() {
		var content []byte
		var version domain.DocumentVersion
		if err := rows.Scan(&version.Document.ID, &version.Revision, &content, &version.ChangeSetID); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		version.Target = target
		version.Document.Kind = kind
		version.Content = json.RawMessage(append([]byte(nil), content...))
		documents = append(documents, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate documents: %w", err)
	}
	return documents, nil
}

// ProjectRecord 是作品库列表项。
type ProjectRecord struct {
	ID       string          `json:"id"`
	Revision domain.Revision `json:"revision"`
}

// ListProjects 列举全部已形成初始 Revision 的作品（作品库）。
func (s *Store) ListProjects(ctx context.Context) ([]ProjectRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target_id, current_revision FROM authority_streams
		WHERE target_kind = ? AND target_scope = '' AND current_revision > 0
		ORDER BY target_id`, domain.AuthorityProject)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	records := make([]ProjectRecord, 0)
	for rows.Next() {
		var record ProjectRecord
		if err := rows.Scan(&record.ID, &record.Revision); err != nil {
			return nil, fmt.Errorf("scan project record: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return records, nil
}

// DeleteProject 在单个事务内永久清除一部作品的全部持久化数据。
// running 的 CreationRun 说明可能仍有驱动循环在写入，必须先取消再删；
// waiting_user/paused 没有活跃写入者，随作品一并删除。
func (s *Store) DeleteProject(ctx context.Context, projectID string) error {
	if projectID == "" {
		return fmt.Errorf("project id is required: %w", domain.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete project: %w", err)
	}
	defer tx.Rollback()

	var exists bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM authority_streams WHERE target_kind = ? AND target_id = ?)`,
		domain.AuthorityProject, projectID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check project %q: %w", projectID, err)
	}
	if !exists {
		return fmt.Errorf("project %q: %w", projectID, ErrNotFound)
	}
	var running int
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM creation_runs WHERE project_id = ? AND state = ?`,
		projectID, domain.RunRunning).Scan(&running)
	if err != nil {
		return fmt.Errorf("check active runs of %q: %w", projectID, err)
	}
	if running > 0 {
		return fmt.Errorf("project %q has a running creation run, cancel it first: %w", projectID, ErrStateConflict)
	}

	// 依赖子表先删（外键开启）：operation 附属 → 提案与文档 → operations →
	// run 事件 → runs → 派生/编译产物 → 书级偏好 → 权威流本体。
	const operationScope = `SELECT id FROM operations WHERE target_kind = ? AND target_id = ?`
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM operation_events WHERE operation_id IN (` + operationScope + `)`, []any{domain.AuthorityProject, projectID}},
		{`DELETE FROM operation_artifacts WHERE operation_id IN (` + operationScope + `)`, []any{domain.AuthorityProject, projectID}},
		{`DELETE FROM operation_dependencies WHERE operation_id IN (` + operationScope + `) OR dependency_id IN (` + operationScope + `)`,
			[]any{domain.AuthorityProject, projectID, domain.AuthorityProject, projectID}},
		{`DELETE FROM proposals WHERE target_kind = ? AND target_id = ?`, []any{domain.AuthorityProject, projectID}},
		{`DELETE FROM document_versions WHERE target_kind = ? AND target_id = ?`, []any{domain.AuthorityProject, projectID}},
		{`DELETE FROM change_sets WHERE target_kind = ? AND target_id = ?`, []any{domain.AuthorityProject, projectID}},
		{`DELETE FROM operations WHERE target_kind = ? AND target_id = ?`, []any{domain.AuthorityProject, projectID}},
		{`DELETE FROM creation_run_events WHERE run_id IN (SELECT id FROM creation_runs WHERE project_id = ?)`, []any{projectID}},
		{`DELETE FROM creation_runs WHERE project_id = ?`, []any{projectID}},
		{`DELETE FROM derived_documents WHERE project_id = ?`, []any{projectID}},
		{`DELETE FROM execution_profiles WHERE project_id = ?`, []any{projectID}},
		{`DELETE FROM preference_candidates WHERE profile_scope = ?`, []any{"book:" + projectID}},
		{`DELETE FROM authority_streams WHERE target_kind = ? AND target_id = ?`, []any{domain.AuthorityProject, projectID}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("delete project %q data: %w", projectID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete project %q: %w", projectID, err)
	}
	return nil
}

// ListPlanNodes 以类型化形式读取某 Revision 的全部蓝图节点。
func (s *Store) ListPlanNodes(ctx context.Context, target domain.AuthorityTarget, at domain.Revision) ([]domain.PlanNode, error) {
	documents, err := s.ListDocuments(ctx, target, domain.DocumentPlan, at)
	if err != nil {
		return nil, err
	}
	nodes := make([]domain.PlanNode, 0, len(documents))
	for _, document := range documents {
		var node domain.PlanNode
		if err := json.Unmarshal(document.Content, &node); err != nil {
			return nil, fmt.Errorf("decode plan node %q: %w", document.Document.ID, err)
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func findCommittedChange(ctx context.Context, tx *sql.Tx, id string) (domain.Revision, string, bool, error) {
	var revision domain.Revision
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT new_revision, content_digest FROM change_sets WHERE id = ?`, id).
		Scan(&revision, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("find committed change set: %w", err)
	}
	return revision, digest, true, nil
}

func currentRevisionTx(ctx context.Context, tx *sql.Tx, target domain.AuthorityTarget) (domain.Revision, error) {
	var revision domain.Revision
	err := tx.QueryRowContext(ctx, `
		SELECT current_revision FROM authority_streams
		WHERE target_kind = ? AND target_id = ? AND target_scope = ?`,
		target.Kind, target.ID, target.Scope).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read current revision: %w", err)
	}
	return revision, nil
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
