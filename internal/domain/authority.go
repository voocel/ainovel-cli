package domain

import (
	"errors"
	"fmt"
	"strings"
)

type AuthorityKind string

const (
	AuthorityProject AuthorityKind = "project"
	AuthorityProfile AuthorityKind = "creator_profile"
	AuthorityPack    AuthorityKind = "pack"
)

type AuthorityTarget struct {
	Kind  AuthorityKind `json:"kind"`
	ID    string        `json:"id"`
	Scope string        `json:"scope,omitempty"`
}

func (t AuthorityTarget) Validate() error {
	switch t.Kind {
	case AuthorityProject, AuthorityPack:
		if t.Scope != "" {
			return fmt.Errorf("%s authority cannot have scope: %w", t.Kind, ErrInvalid)
		}
	case AuthorityProfile:
		if t.Scope == "" {
			return fmt.Errorf("creator profile scope is required: %w", ErrInvalid)
		}
	default:
		return fmt.Errorf("unknown authority kind %q: %w", t.Kind, ErrInvalid)
	}
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("authority id is required: %w", ErrInvalid)
	}
	return nil
}

func (t AuthorityTarget) Key() string {
	if t.Scope == "" {
		return string(t.Kind) + ":" + t.ID
	}
	return string(t.Kind) + ":" + t.ID + "/" + t.Scope
}

var ErrInvalid = errors.New("invalid domain value")
