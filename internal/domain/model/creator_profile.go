package model

import (
	"fmt"
	"strings"
)

type CreatorProfile struct {
	ID                   string                `json:"id"`
	Scope                string                `json:"scope"`
	ExplicitRules        []string              `json:"explicit_rules,omitempty"`
	PositiveExamples     []string              `json:"positive_examples,omitempty"`
	NegativeExamples     []string              `json:"negative_examples,omitempty"`
	Preferences          map[string]string     `json:"style_preferences,omitempty"`
	PreferenceCandidates []PreferenceCandidate `json:"preference_candidates,omitempty"`
}

type PreferenceCandidate struct {
	ID                  string            `json:"id"`
	Summary             string            `json:"summary"`
	Evidence            []string          `json:"evidence"`
	ProposedRules       []string          `json:"proposed_rules,omitempty"`
	ProposedPreferences map[string]string `json:"proposed_style_preferences,omitempty"`
	SourceProjectID     string            `json:"source_project_id"`
	SourceRevision      Revision          `json:"source_revision"`
}

type ManuscriptEditEvidence struct {
	ChapterID string `json:"chapter_id"`
	BlockID   string `json:"block_id"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
}

type PreferenceLearningInput struct {
	CandidateID     string                   `json:"candidate_id"`
	ProfileID       string                   `json:"profile_id"`
	Scope           string                   `json:"scope"`
	ProjectID       string                   `json:"project_id"`
	FromRevision    Revision                 `json:"from_revision"`
	ToRevision      Revision                 `json:"to_revision"`
	ManuscriptEdits []ManuscriptEditEvidence `json:"manuscript_edits"`
}

func (v PreferenceLearningInput) Validate() error {
	if strings.TrimSpace(v.CandidateID) == "" || strings.TrimSpace(v.ProfileID) == "" ||
		strings.TrimSpace(v.Scope) == "" || strings.TrimSpace(v.ProjectID) == "" ||
		v.FromRevision <= InitialRevision || v.ToRevision <= v.FromRevision || len(v.ManuscriptEdits) == 0 {
		return fmt.Errorf("preference learning identity, revision range and edits are required: %w", ErrInvalid)
	}
	for index, edit := range v.ManuscriptEdits {
		if strings.TrimSpace(edit.ChapterID) == "" || strings.TrimSpace(edit.BlockID) == "" || edit.Before == edit.After {
			return fmt.Errorf("manuscript edit %d is invalid: %w", index, ErrInvalid)
		}
	}
	return nil
}

func (v PreferenceCandidate) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Summary) == "" ||
		strings.TrimSpace(v.SourceProjectID) == "" || v.SourceRevision <= InitialRevision || len(v.Evidence) == 0 {
		return fmt.Errorf("preference candidate is invalid: %w", ErrInvalid)
	}
	if err := validateDistinctStrings("candidate evidence", v.Evidence); err != nil {
		return err
	}
	if err := validateDistinctStrings("candidate rules", v.ProposedRules); err != nil {
		return err
	}
	if len(v.ProposedRules) == 0 && len(v.ProposedPreferences) == 0 {
		return fmt.Errorf("preference candidate requires at least one proposed rule or preference: %w", ErrInvalid)
	}
	for key := range v.ProposedPreferences {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("candidate preference key is required: %w", ErrInvalid)
		}
	}
	return nil
}

func (v CreatorProfile) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Scope) == "" {
		return fmt.Errorf("creator profile id and scope are required: %w", ErrInvalid)
	}
	if err := validateDistinctStrings("profile rules", v.ExplicitRules); err != nil {
		return err
	}
	for key := range v.Preferences {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("profile preference key is required: %w", ErrInvalid)
		}
	}
	seenCandidates := make(map[string]struct{}, len(v.PreferenceCandidates))
	for i, candidate := range v.PreferenceCandidates {
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("profile preference candidate %d: %w", i, err)
		}
		if _, ok := seenCandidates[candidate.ID]; ok {
			return fmt.Errorf("duplicate preference candidate %q: %w", candidate.ID, ErrInvalid)
		}
		seenCandidates[candidate.ID] = struct{}{}
	}
	return nil
}
