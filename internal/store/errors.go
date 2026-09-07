package store

import "errors"

var (
	ErrNotFound            = errors.New("store value not found")
	ErrRevisionConflict    = errors.New("authority revision conflict")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different input")
	ErrNotApproved         = errors.New("change set is not approved")
	ErrStateConflict       = errors.New("operation state conflict")
	ErrWorkspaceConflict   = errors.New("workspace version conflict")
	ErrDependencyBlocked   = errors.New("queued operations are blocked by dependencies that cannot succeed")
)
