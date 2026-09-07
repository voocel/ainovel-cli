package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type PreferenceCandidateRecord struct {
	ProfileID   string                     `json:"profile_id"`
	Scope       string                     `json:"scope"`
	Candidate   domain.PreferenceCandidate `json:"candidate"`
	State       string                     `json:"state"`
	CreatedAt   time.Time                  `json:"created_at"`
	ConfirmedAt *time.Time                 `json:"confirmed_at,omitempty"`
}

func (s *Store) SavePreferenceCandidate(
	ctx context.Context,
	profileID, scope string,
	candidate domain.PreferenceCandidate,
	createdAt time.Time,
) (PreferenceCandidateRecord, error) {
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(scope) == "" || createdAt.IsZero() {
		return PreferenceCandidateRecord{}, fmt.Errorf("preference profile, scope and time are required: %w", domain.ErrInvalid)
	}
	if err := candidate.Validate(); err != nil {
		return PreferenceCandidateRecord{}, err
	}
	payload, err := json.Marshal(candidate)
	if err != nil {
		return PreferenceCandidateRecord{}, fmt.Errorf("encode preference candidate: %w", err)
	}
	digest := domain.Digest(payload)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO preference_candidates (
			profile_id, profile_scope, candidate_id, payload, content_digest, state, created_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, 'pending', ?)
		ON CONFLICT (profile_id, profile_scope, candidate_id) DO NOTHING`,
		profileID, scope, candidate.ID, payload, digest, createdAt.UnixMilli())
	if err != nil {
		return PreferenceCandidateRecord{}, fmt.Errorf("save preference candidate: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return PreferenceCandidateRecord{}, fmt.Errorf("inspect preference candidate save: %w", err)
	}
	record, err := s.GetPreferenceCandidate(ctx, profileID, scope, candidate.ID)
	if err != nil {
		return PreferenceCandidateRecord{}, err
	}
	if rows == 0 {
		storedDigest, err := domain.DigestJSON(record.Candidate)
		if err != nil {
			return PreferenceCandidateRecord{}, err
		}
		if storedDigest != digest {
			return PreferenceCandidateRecord{}, fmt.Errorf("preference candidate %q: %w", candidate.ID, ErrIdempotencyConflict)
		}
	}
	return record, nil
}

func (s *Store) GetPreferenceCandidate(ctx context.Context, profileID, scope, candidateID string) (PreferenceCandidateRecord, error) {
	var record PreferenceCandidateRecord
	var payload []byte
	var createdAt int64
	var confirmedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT payload, state, created_at_unix_ms, confirmed_at_unix_ms
		FROM preference_candidates
		WHERE profile_id = ? AND profile_scope = ? AND candidate_id = ?`, profileID, scope, candidateID).
		Scan(&payload, &record.State, &createdAt, &confirmedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PreferenceCandidateRecord{}, ErrNotFound
	}
	if err != nil {
		return PreferenceCandidateRecord{}, fmt.Errorf("read preference candidate: %w", err)
	}
	record.ProfileID, record.Scope = profileID, scope
	record.CreatedAt = time.UnixMilli(createdAt).UTC()
	if confirmedAt.Valid {
		value := time.UnixMilli(confirmedAt.Int64).UTC()
		record.ConfirmedAt = &value
	}
	if err := json.Unmarshal(payload, &record.Candidate); err != nil {
		return PreferenceCandidateRecord{}, fmt.Errorf("decode preference candidate: %w", err)
	}
	return record, nil
}

func (s *Store) ListPreferenceCandidates(ctx context.Context, profileID, scope string) ([]PreferenceCandidateRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT candidate_id FROM preference_candidates
		WHERE profile_id = ? AND profile_scope = ? AND state = 'pending'
		ORDER BY created_at_unix_ms, candidate_id`, profileID, scope)
	if err != nil {
		return nil, fmt.Errorf("list preference candidates: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan preference candidate: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate preference candidates: %w", err)
	}
	records := make([]PreferenceCandidateRecord, 0, len(ids))
	for _, id := range ids {
		record, err := s.GetPreferenceCandidate(ctx, profileID, scope, id)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) ConfirmPreferenceCandidate(ctx context.Context, profileID, scope, candidateID string, at time.Time) error {
	if at.IsZero() {
		return fmt.Errorf("preference confirmation time is required: %w", domain.ErrInvalid)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE preference_candidates SET state = 'confirmed', confirmed_at_unix_ms = ?
		WHERE profile_id = ? AND profile_scope = ? AND candidate_id = ? AND state = 'pending'`,
		at.UnixMilli(), profileID, scope, candidateID)
	if err != nil {
		return fmt.Errorf("confirm preference candidate: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect preference confirmation: %w", err)
	}
	if count == 1 {
		return nil
	}
	record, err := s.GetPreferenceCandidate(ctx, profileID, scope, candidateID)
	if err != nil {
		return err
	}
	if record.State == "confirmed" {
		return nil
	}
	return ErrStateConflict
}
