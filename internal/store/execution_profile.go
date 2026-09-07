package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func (s *Store) SaveExecutionProfile(ctx context.Context, profile domain.ExecutionProfileRecord) (domain.ExecutionProfileRecord, error) {
	if err := profile.Validate(); err != nil {
		return domain.ExecutionProfileRecord{}, err
	}
	identity := profile
	identity.CreatedAt = time.Time{}
	contentDigest, err := domain.DigestJSON(identity)
	if err != nil {
		return domain.ExecutionProfileRecord{}, fmt.Errorf("encode execution profile: %w", err)
	}
	snapshot, err := json.Marshal(profile.Snapshot)
	if err != nil {
		return domain.ExecutionProfileRecord{}, fmt.Errorf("encode execution snapshot: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO execution_profiles (
			digest, content_digest, project_id, worker_profile, prompt_digest,
			tool_schema_digest, snapshot, stable_prefix, dynamic_tail, tools, sources,
			created_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (digest) DO NOTHING`,
		profile.Digest, contentDigest, profile.ProjectID, profile.WorkerProfile,
		profile.PromptDigest, profile.ToolSchemaDigest, snapshot,
		profile.StablePrefix, profile.DynamicTail, []byte(profile.Tools), []byte(profile.Sources),
		profile.CreatedAt.UnixMilli())
	if err != nil {
		return domain.ExecutionProfileRecord{}, fmt.Errorf("save execution profile: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.ExecutionProfileRecord{}, fmt.Errorf("inspect execution profile insert: %w", err)
	}
	if rows == 1 {
		return profile, nil
	}
	var storedDigest string
	if err := s.db.QueryRowContext(ctx, `SELECT content_digest FROM execution_profiles WHERE digest = ?`, profile.Digest).Scan(&storedDigest); err != nil {
		return domain.ExecutionProfileRecord{}, fmt.Errorf("inspect existing execution profile: %w", err)
	}
	if storedDigest != contentDigest {
		return domain.ExecutionProfileRecord{}, fmt.Errorf("execution profile %q: %w", profile.Digest, ErrIdempotencyConflict)
	}
	return s.GetExecutionProfile(ctx, profile.Digest)
}

func (s *Store) GetExecutionProfile(ctx context.Context, digest string) (domain.ExecutionProfileRecord, error) {
	var profile domain.ExecutionProfileRecord
	var snapshot, tools, sources []byte
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT project_id, worker_profile, prompt_digest, tool_schema_digest,
			snapshot, stable_prefix, dynamic_tail, tools, sources, created_at_unix_ms
		FROM execution_profiles WHERE digest = ?`, digest).
		Scan(&profile.ProjectID, &profile.WorkerProfile, &profile.PromptDigest, &profile.ToolSchemaDigest,
			&snapshot, &profile.StablePrefix, &profile.DynamicTail, &tools, &sources, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExecutionProfileRecord{}, ErrNotFound
	}
	if err != nil {
		return domain.ExecutionProfileRecord{}, fmt.Errorf("read execution profile: %w", err)
	}
	profile.Digest = digest
	profile.Tools = append(json.RawMessage(nil), tools...)
	profile.Sources = append(json.RawMessage(nil), sources...)
	profile.CreatedAt = time.UnixMilli(createdAt).UTC()
	if err := json.Unmarshal(snapshot, &profile.Snapshot); err != nil {
		return domain.ExecutionProfileRecord{}, fmt.Errorf("decode execution snapshot: %w", err)
	}
	if err := profile.Validate(); err != nil {
		return domain.ExecutionProfileRecord{}, fmt.Errorf("stored execution profile is invalid: %w", err)
	}
	return profile, nil
}
