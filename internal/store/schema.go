package store

import (
	"context"
	"fmt"
)

// schemaVersion 是当前唯一支持的库结构版本。v1 没有历史数据，结构演进直接改
// schema 并升版本号；不保留迁移阶梯，版本不符即拒绝打开。
const schemaVersion = 1

func (s *Store) ensureSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema setup: %w", err)
	}
	defer tx.Rollback()

	var current int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	switch current {
	case schemaVersion:
		return nil
	case 0:
		for _, statement := range schema {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema setup: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("database schema version %d is not supported (expected %d)", current, schemaVersion)
	}
}

var schema = []string{
	`CREATE TABLE authority_streams (
		target_kind TEXT NOT NULL,
		target_id TEXT NOT NULL,
		target_scope TEXT NOT NULL DEFAULT '',
		current_revision INTEGER NOT NULL DEFAULT 0 CHECK (current_revision >= 0),
		PRIMARY KEY (target_kind, target_id, target_scope)
	) STRICT`,
	`CREATE TABLE change_sets (
		id TEXT PRIMARY KEY,
		content_digest TEXT NOT NULL,
		target_kind TEXT NOT NULL,
		target_id TEXT NOT NULL,
		target_scope TEXT NOT NULL DEFAULT '',
		base_revision INTEGER NOT NULL CHECK (base_revision >= 0),
		new_revision INTEGER NOT NULL CHECK (new_revision > 0),
		author_kind TEXT NOT NULL,
		author_id TEXT NOT NULL,
		reason TEXT NOT NULL,
		structural_impact BLOB,
		semantic_impact BLOB,
		compliance_impact BLOB,
		approval_state TEXT NOT NULL,
		decision_author_kind TEXT,
		decision_author_id TEXT,
		decided_at_unix_ms INTEGER,
		operation_id TEXT,
		created_at_unix_ms INTEGER NOT NULL,
		UNIQUE (target_kind, target_id, target_scope, new_revision),
		FOREIGN KEY (target_kind, target_id, target_scope)
			REFERENCES authority_streams (target_kind, target_id, target_scope)
	) STRICT`,
	`CREATE INDEX change_sets_by_operation ON change_sets (operation_id)`,
	`CREATE TABLE document_versions (
		target_kind TEXT NOT NULL,
		target_id TEXT NOT NULL,
		target_scope TEXT NOT NULL DEFAULT '',
		document_kind TEXT NOT NULL,
		document_id TEXT NOT NULL,
		revision INTEGER NOT NULL CHECK (revision > 0),
		operation TEXT NOT NULL CHECK (operation IN ('put', 'delete')),
		content BLOB,
		change_set_id TEXT NOT NULL,
		PRIMARY KEY (target_kind, target_id, target_scope, document_kind, document_id, revision),
		FOREIGN KEY (change_set_id) REFERENCES change_sets (id)
	) STRICT`,
	`CREATE INDEX document_versions_at_revision
		ON document_versions (target_kind, target_id, target_scope, revision, document_kind, document_id)`,
	`CREATE TABLE proposals (
		id TEXT PRIMARY KEY,
		content_digest TEXT NOT NULL,
		payload BLOB NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'rejected')),
		target_kind TEXT NOT NULL,
		target_id TEXT NOT NULL,
		target_scope TEXT NOT NULL DEFAULT '',
		base_revision INTEGER NOT NULL CHECK (base_revision >= 0),
		operation_id TEXT,
		created_at_unix_ms INTEGER NOT NULL,
		updated_at_unix_ms INTEGER NOT NULL
	) STRICT`,
	`CREATE INDEX proposals_by_authority
		ON proposals (target_kind, target_id, target_scope, state, created_at_unix_ms, id)`,
	`CREATE UNIQUE INDEX proposals_by_operation
		ON proposals (operation_id) WHERE operation_id IS NOT NULL`,
	`CREATE TABLE operations (
		id TEXT PRIMARY KEY,
		content_digest TEXT NOT NULL,
		kind TEXT NOT NULL,
		target_kind TEXT NOT NULL,
		target_id TEXT NOT NULL,
		target_scope TEXT NOT NULL DEFAULT '',
		priority INTEGER NOT NULL,
		state TEXT NOT NULL,
		attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
		execution_snapshot BLOB NOT NULL,
		input BLOB NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		model_config_digest TEXT NOT NULL,
		lease_owner TEXT,
		lease_until_unix_ms INTEGER,
		run_id TEXT NOT NULL DEFAULT '',
		run_policy_version INTEGER NOT NULL DEFAULT 0,
		created_at_unix_ms INTEGER NOT NULL,
		updated_at_unix_ms INTEGER NOT NULL
	) STRICT`,
	`CREATE INDEX operations_queue
		ON operations (state, priority DESC, created_at_unix_ms, id)`,
	`CREATE INDEX operations_queue_by_model
		ON operations (state, model_config_digest, priority DESC, created_at_unix_ms, id)`,
	`CREATE INDEX operations_by_run ON operations (run_id) WHERE run_id != ''`,
	`CREATE TABLE operation_dependencies (
		operation_id TEXT NOT NULL,
		dependency_id TEXT NOT NULL,
		PRIMARY KEY (operation_id, dependency_id),
		CHECK (operation_id <> dependency_id),
		FOREIGN KEY (operation_id) REFERENCES operations (id),
		FOREIGN KEY (dependency_id) REFERENCES operations (id)
	) STRICT`,
	`CREATE INDEX operation_dependencies_reverse
		ON operation_dependencies (dependency_id, operation_id)`,
	`CREATE TABLE operation_artifacts (
		operation_id TEXT NOT NULL,
		artifact_key TEXT NOT NULL,
		version INTEGER NOT NULL CHECK (version > 0),
		media_type TEXT NOT NULL,
		content BLOB NOT NULL,
		content_digest TEXT NOT NULL,
		updated_at_unix_ms INTEGER NOT NULL,
		PRIMARY KEY (operation_id, artifact_key),
		FOREIGN KEY (operation_id) REFERENCES operations (id)
	) STRICT`,
	`CREATE TABLE operation_events (
		operation_id TEXT NOT NULL,
		sequence INTEGER NOT NULL CHECK (sequence > 0),
		step_id TEXT NOT NULL,
		attempt INTEGER NOT NULL CHECK (attempt >= 0),
		idempotency_key TEXT NOT NULL,
		kind TEXT NOT NULL,
		payload BLOB NOT NULL,
		payload_digest TEXT NOT NULL,
		created_at_unix_ms INTEGER NOT NULL,
		PRIMARY KEY (operation_id, sequence),
		UNIQUE (operation_id, idempotency_key),
		FOREIGN KEY (operation_id) REFERENCES operations (id)
	) STRICT`,
	`CREATE TABLE execution_profiles (
		digest TEXT PRIMARY KEY,
		content_digest TEXT NOT NULL,
		project_id TEXT NOT NULL,
		worker_profile TEXT NOT NULL,
		prompt_digest TEXT NOT NULL,
		tool_schema_digest TEXT NOT NULL,
		snapshot BLOB NOT NULL,
		stable_prefix TEXT NOT NULL,
		dynamic_tail TEXT NOT NULL,
		tools BLOB NOT NULL,
		sources BLOB NOT NULL,
		created_at_unix_ms INTEGER NOT NULL
	) STRICT`,
	`CREATE INDEX execution_profiles_by_project
		ON execution_profiles (project_id, worker_profile, created_at_unix_ms, digest)`,
	`CREATE TABLE preference_candidates (
		profile_id TEXT NOT NULL,
		profile_scope TEXT NOT NULL,
		candidate_id TEXT NOT NULL,
		payload BLOB NOT NULL,
		content_digest TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('pending', 'confirmed')),
		created_at_unix_ms INTEGER NOT NULL,
		confirmed_at_unix_ms INTEGER,
		PRIMARY KEY (profile_id, profile_scope, candidate_id)
	) STRICT`,
	`CREATE INDEX preference_candidates_pending
		ON preference_candidates (profile_id, profile_scope, state, created_at_unix_ms, candidate_id)`,
	`CREATE TABLE derived_documents (
		project_id TEXT NOT NULL,
		revision INTEGER NOT NULL CHECK (revision > 0),
		kind TEXT NOT NULL,
		cache_key TEXT NOT NULL,
		content BLOB NOT NULL,
		content_digest TEXT NOT NULL,
		created_at_unix_ms INTEGER NOT NULL,
		PRIMARY KEY (project_id, revision, kind, cache_key)
	) STRICT`,
	`CREATE INDEX derived_documents_by_project
		ON derived_documents (project_id, revision, kind, cache_key)`,
	// CreationRun（§6.3）：goal 与 strategy 是版本化运行策略，preset 只追溯启动边界。
	`CREATE TABLE creation_runs (
		id TEXT PRIMARY KEY,
		content_digest TEXT NOT NULL,
		project_id TEXT NOT NULL,
		goal BLOB NOT NULL,
		strategy BLOB NOT NULL,
		preset BLOB NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('running', 'waiting_user', 'paused', 'completed', 'failed', 'cancelled')),
		state_reason TEXT NOT NULL DEFAULT '',
		completed_revision INTEGER NOT NULL DEFAULT 0 CHECK (completed_revision >= 0),
		created_at_unix_ms INTEGER NOT NULL,
		updated_at_unix_ms INTEGER NOT NULL
	) STRICT`,
	// 每个 Project 同时最多一个非终态 CreationRun，由存储层唯一索引保证（§6.3）。
	`CREATE UNIQUE INDEX creation_runs_single_active
		ON creation_runs (project_id) WHERE state IN ('running', 'waiting_user', 'paused')`,
	`CREATE INDEX creation_runs_by_project
		ON creation_runs (project_id, created_at_unix_ms, id)`,
	// Run 事件（§6.3）：目标与运行策略的每次修改都是版本化事件，
	// Operation 创建时绑定当时的策略版本（policy_version = 最近策略事件序号）。
	`CREATE TABLE creation_run_events (
		run_id TEXT NOT NULL,
		sequence INTEGER NOT NULL CHECK (sequence > 0),
		kind TEXT NOT NULL CHECK (kind IN ('created', 'goal_updated', 'strategy_updated', 'operation_created')),
		payload BLOB NOT NULL,
		created_at_unix_ms INTEGER NOT NULL,
		PRIMARY KEY (run_id, sequence),
		FOREIGN KEY (run_id) REFERENCES creation_runs (id)
	) STRICT`,
	`PRAGMA user_version = 1`,
}
