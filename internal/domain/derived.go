package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type DerivedDocument struct {
	ProjectID string          `json:"project_id"`
	Revision  Revision        `json:"revision"`
	Kind      string          `json:"kind"`
	Key       string          `json:"key"`
	Content   json.RawMessage `json:"content"`
	Digest    string          `json:"digest"`
	CreatedAt time.Time       `json:"created_at"`
}

func (d DerivedDocument) Validate() error {
	if strings.TrimSpace(d.ProjectID) == "" || d.Revision <= InitialRevision ||
		strings.TrimSpace(d.Kind) == "" || strings.TrimSpace(d.Key) == "" || d.CreatedAt.IsZero() {
		return fmt.Errorf("derived document identity, revision and time are required: %w", ErrInvalid)
	}
	if len(d.Content) == 0 || !json.Valid(d.Content) {
		return fmt.Errorf("derived document content must be valid JSON: %w", ErrInvalid)
	}
	return nil
}
