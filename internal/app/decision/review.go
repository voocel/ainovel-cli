package decision

import (
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// Review handles user decisions on candidates and their task consequences.
type Review struct {
	store    *store.Store
	changes  *change.Engine
	projects *projectdoc.Repository
	tasks    *task.Manager
}

func New(s *store.Store, changes *change.Engine, projects *projectdoc.Repository, tasks *task.Manager) *Review {
	return &Review{store: s, changes: changes, projects: projects, tasks: tasks}
}
