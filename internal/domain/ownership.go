package domain

import "fmt"

type ControlLevel string

const (
	ControlLocked ControlLevel = "locked"
	ControlGuided ControlLevel = "guided"
	ControlOpen   ControlLevel = "open"
)

type Lifecycle string

const (
	LifecycleDraft      Lifecycle = "draft"
	LifecycleProposed   Lifecycle = "proposed"
	LifecycleAccepted   Lifecycle = "accepted"
	LifecycleRejected   Lifecycle = "rejected"
	LifecycleDeprecated Lifecycle = "deprecated"
)

type Ownership struct {
	Control   ControlLevel `json:"control"`
	Lifecycle Lifecycle    `json:"lifecycle"`
	Author    Author       `json:"author"`
}

func (o Ownership) Validate() error {
	switch o.Control {
	case ControlLocked, ControlGuided, ControlOpen:
	default:
		return fmt.Errorf("unknown control level %q: %w", o.Control, ErrInvalid)
	}
	switch o.Lifecycle {
	case LifecycleDraft, LifecycleProposed, LifecycleAccepted, LifecycleRejected, LifecycleDeprecated:
	default:
		return fmt.Errorf("unknown lifecycle %q: %w", o.Lifecycle, ErrInvalid)
	}
	switch o.Author.Kind {
	case AuthorUser, AuthorAI, AuthorExtension, AuthorSystem:
	default:
		return fmt.Errorf("unknown author kind %q: %w", o.Author.Kind, ErrInvalid)
	}
	if o.Author.ID == "" {
		return fmt.Errorf("author id is required: %w", ErrInvalid)
	}
	return nil
}
