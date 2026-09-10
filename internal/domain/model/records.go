package model

import "time"

type ProjectRecord struct {
	ID       string   `json:"id"`
	Revision Revision `json:"revision"`
}

type PreferenceCandidateRecord struct {
	ProfileID   string              `json:"profile_id"`
	Scope       string              `json:"scope"`
	Candidate   PreferenceCandidate `json:"candidate"`
	State       string              `json:"state"`
	CreatedAt   time.Time           `json:"created_at"`
	ConfirmedAt *time.Time          `json:"confirmed_at,omitempty"`
}
