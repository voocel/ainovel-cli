package creation

import (
	"context"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Tasks binds application execution settings for one drive invocation. The
// coordinator owns successor selection; implementations prepare and execute work.
type Tasks interface {
	Start(context.Context, string, WorkItem, time.Time) (model.Operation, error)
	Restart(context.Context, string, string, WorkItem, time.Time) (model.Operation, error)
	Resume(context.Context, string, time.Time) (model.Operation, error)
	Run(context.Context, string, string, time.Duration, time.Time) (model.Operation, error)
}

// Store persists runs and exposes the task identities needed for successor
// selection. Implementations must retain the atomic transition semantics.
type Store interface {
	CurrentRevision(context.Context, model.AuthorityTarget) (model.Revision, error)
	GetOperation(context.Context, string) (model.Operation, error)
	GetProposalByOperation(context.Context, string) (model.Proposal, error)
	CreateCreationRun(context.Context, model.CreationRun) (model.CreationRun, error)
	GetCreationRun(context.Context, string) (model.CreationRun, error)
	ActiveCreationRun(context.Context, string) (model.CreationRun, error)
	LatestCreationRun(context.Context, string) (model.CreationRun, error)
	ListCreationRunEvents(context.Context, string) ([]model.CreationRunEvent, error)
	TransitionCreationRun(context.Context, string, model.CreationRunState, model.CreationRunState, string, model.Revision, time.Time) (model.CreationRun, error)
	// SettleCreationRun atomically checks the observed goal, strategy and authority
	// revision before recording a goal decision. Conflicts require re-evaluation.
	SettleCreationRun(context.Context, model.CreationRun, model.Revision, model.CreationRunState, string, time.Time) (model.CreationRun, error)
	UpdateCreationRunStrategy(context.Context, string, model.CreationRunStrategy, time.Time) (model.CreationRun, error)
}
