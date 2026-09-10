// Package bootstrap assembles use-case components. Business rules belong to
// those components; this package contains no command forwarding or persistence.
package bootstrap

import (
	"time"

	"github.com/voocel/ainovel-cli/internal/app/decision"
	"github.com/voocel/ainovel-cli/internal/app/evidence"
	"github.com/voocel/ainovel-cli/internal/app/novel"
	"github.com/voocel/ainovel-cli/internal/app/profile"
	"github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/operation"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

type App struct {
	Projects  *project.Repository
	Resources *resource.Catalog
	Prompts   *profile.Compiler
	Tasks     *task.Manager
	Runs      *creation.Coordinator
	Novels    *novel.Application
	Reviews   *novel.Reviews
	Decisions *decision.Review
	Workbench *workbench.Query
	Evidence  *evidence.Reader
}

type Options struct {
	Executors task.ExecutorSet
	Contracts []operation.VerdictContract
	Goals     map[model.GoalKind]creation.Goal
	Now       func() time.Time
}

func New(s *store.Store, options Options) *App {
	changes := change.New(s)
	if analyzer, ok := options.Executors.LLM.(change.SemanticAnalyzer); ok {
		changes = change.NewWithSemanticAnalyzer(s, analyzer)
	}
	engine := operation.NewEngine(s, options.Contracts...)
	projects := project.New(s, changes)
	analyzer, _ := options.Executors.LLM.(resource.PreferenceAnalyzer)
	resources := resource.New(s, changes, projects, analyzer)
	prompts := profile.New(s, projects, options.Executors.LLM)
	tasks := task.New(s, engine, options.Executors, prompts)
	reviews := novel.NewReviews(s, changes, projects)
	goals := map[model.GoalKind]creation.Goal{model.GoalNovel: novel.NewGoal(reviews)}
	for kind, goal := range options.Goals {
		if _, exists := goals[kind]; exists {
			panic("duplicate goal: " + string(kind))
		}
		goals[kind] = goal
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	runs := creation.New(s, goals, now)
	decisions := decision.New(s, changes, projects, tasks)
	return &App{
		Projects: projects, Resources: resources, Prompts: prompts,
		Tasks: tasks, Runs: runs, Reviews: reviews, Decisions: decisions,
		Novels:    novel.New(s, changes, projects, runs, tasks),
		Workbench: workbench.New(s, projects, runs, reviews, decisions),
		Evidence:  evidence.New(s, changes, engine),
	}
}
