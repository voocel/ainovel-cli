package model

import (
	"errors"
	"fmt"
)

// Persistence errors belong to the contracts implemented by storage adapters.
var (
	ErrNotFound            = errors.New("store value not found")
	ErrRevisionConflict    = errors.New("authority revision conflict")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different input")
	ErrNotApproved         = errors.New("change set is not approved")
	ErrStateConflict       = errors.New("operation state conflict")
	// ErrOperationHeld：任务正被另一个 worker 的租约持有（另一进程在跑，或上一个进程
	// 崩溃后租约尚未到期）。是 ErrStateConflict 的一种，入口可据此给出等待提示。
	ErrOperationHeld     = fmt.Errorf("operation is held by another worker lease: %w", ErrStateConflict)
	ErrWorkspaceConflict = errors.New("workspace version conflict")
	ErrDependencyBlocked = errors.New("queued operations are blocked by dependencies that cannot succeed")
)
