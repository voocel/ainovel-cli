package novel

import (
	"strings"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// Application exposes novel creation presets on the shared run and task mechanisms.
type Application struct {
	store    *store.Store
	changes  *change.Engine
	projects *projectdoc.Repository
	runs     *creation.Coordinator
	tasks    *task.Manager
}

func New(s *store.Store, changes *change.Engine, projects *projectdoc.Repository, runs *creation.Coordinator, tasks *task.Manager) *Application {
	return &Application{store: s, changes: changes, projects: projects, runs: runs, tasks: tasks}
}
func (s *Application) creationTasks(command QuickWriteCommand) creation.Tasks {
	return s.tasks.ForCreation(task.StartOperationCommand{
		ProjectID: command.ProjectID, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
		CoreProtocolVersion: "core-v1", ConfigDigest: s.tasks.ExternalConfigDigest(),
	})
}

func driveCommand(command QuickWriteCommand) creation.DriveCommand {
	return creation.DriveCommand{
		WorkerID: command.WorkerID, LeaseDuration: command.LeaseDuration, CreatedAt: command.CreatedAt,
	}
}

func runQuickID(runID string, parts ...string) string { return runID + ":" + strings.Join(parts, ":") }
