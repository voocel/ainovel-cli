package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// SaveExecutionProfile 落盘不可变的 Execution Profile：Digest 即内容身份，
// 同摘要重复保存天然幂等。
func (s *Store) SaveExecutionProfile(ctx context.Context, profile model.ExecutionProfileRecord) (model.ExecutionProfileRecord, error) {
	if err := profile.Validate(); err != nil {
		return model.ExecutionProfileRecord{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO execution_profiles (
			digest, project_id, worker_profile, core_protocol_version,
			prompt_digest, tool_schema_digest, stable_prefix, dynamic_tail, tools, sources,
			created_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (digest) DO NOTHING`,
		profile.Digest, profile.ProjectID, profile.WorkerProfile, profile.CoreProtocolVersion,
		profile.PromptDigest, profile.ToolSchemaDigest, profile.StablePrefix, profile.DynamicTail,
		[]byte(profile.Tools), []byte(profile.Sources), profile.CreatedAt.UnixMilli())
	if err != nil {
		return model.ExecutionProfileRecord{}, fmt.Errorf("save execution profile: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.ExecutionProfileRecord{}, fmt.Errorf("inspect execution profile insert: %w", err)
	}
	if rows == 1 {
		return profile, nil
	}
	return s.GetExecutionProfile(ctx, profile.Digest)
}

func (s *Store) GetExecutionProfile(ctx context.Context, digest string) (model.ExecutionProfileRecord, error) {
	var profile model.ExecutionProfileRecord
	var tools, sources []byte
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT project_id, worker_profile, core_protocol_version,
			prompt_digest, tool_schema_digest, stable_prefix, dynamic_tail, tools, sources, created_at_unix_ms
		FROM execution_profiles WHERE digest = ?`, digest).
		Scan(&profile.ProjectID, &profile.WorkerProfile, &profile.CoreProtocolVersion,
			&profile.PromptDigest, &profile.ToolSchemaDigest, &profile.StablePrefix, &profile.DynamicTail,
			&tools, &sources, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ExecutionProfileRecord{}, model.ErrNotFound
	}
	if err != nil {
		return model.ExecutionProfileRecord{}, fmt.Errorf("read execution profile: %w", err)
	}
	profile.Digest = digest
	profile.Tools = append(json.RawMessage(nil), tools...)
	profile.Sources = append(json.RawMessage(nil), sources...)
	profile.CreatedAt = time.UnixMilli(createdAt).UTC()
	if err := profile.Validate(); err != nil {
		return model.ExecutionProfileRecord{}, fmt.Errorf("stored execution profile is invalid: %w", err)
	}
	return profile, nil
}
