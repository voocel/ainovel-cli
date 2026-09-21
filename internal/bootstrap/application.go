// Package bootstrap assembles use-case components. Business rules belong to
// those components; this package contains no command forwarding or persistence.
package bootstrap

import (
	"time"

	"github.com/voocel/ainovel-cli/internal/app/binding"
	"github.com/voocel/ainovel-cli/internal/app/decision"
	"github.com/voocel/ainovel-cli/internal/app/diag"
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
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	"github.com/voocel/ainovel-cli/internal/infra/capability"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

type App struct {
	Diag      *diag.Query
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
	// Models 是模型绑定用例：常驻 capability Runtime 随时重绑，不重建应用。
	Models *binding.Service
}

type Options struct {
	Version   string
	Executors task.ExecutorSet
	Contracts []operation.VerdictContract
	Goals     map[model.GoalKind]creation.Goal
	Now       func() time.Time
	// ConfigDir 是模型配置落盘位置；Interactive 装配实时活动通道（发布与订阅共用
	// 同一 Hub），脚本入口保持零活动开销。
	ConfigDir   string
	Interactive bool
}

// New 装配应用。未注入 LLM 执行器时装配常驻的 capability Runtime：模型绑定是运行时
// 属性（D57），未绑定的 Runtime 不领取任务，绑定后队列任务自然接上。
func New(s *store.Store, options Options) *App {
	var runtime *capability.Runtime
	binder, _ := options.Executors.LLM.(binding.Binder) // 注入的执行器能重绑就用它
	if options.Executors.LLM == nil {
		runtime = capability.NewRuntime(s)
		options.Executors.LLM, binder = runtime, runtime
		if options.Interactive {
			runtime.SetActivitySink(activity.NewHub())
		}
	}
	changes := change.New(s)
	if analyzer, ok := options.Executors.LLM.(change.SemanticAnalyzer); ok {
		changes = change.NewWithSemanticAnalyzer(s, analyzer)
	}
	engine := operation.NewEngine(s, changes, options.Contracts...)
	projects := project.New(s, changes)
	analyzer, _ := options.Executors.LLM.(resource.PreferenceAnalyzer)
	resources := resource.New(s, changes, projects, analyzer)
	prompts := profile.New(s, projects)
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
	app := &App{
		Diag:     diag.New(s, options.Version, now),
		Projects: projects, Resources: resources, Prompts: prompts,
		Tasks: tasks, Runs: runs, Reviews: reviews, Decisions: decisions,
		Novels:    novel.New(s, changes, projects, runs, tasks),
		Workbench: workbench.New(s, projects, runs, reviews, decisions),
		Evidence:  evidence.New(s, changes, engine),
		Models:    binding.New(options.ConfigDir, binder),
	}
	if runtime != nil && options.Interactive {
		app.Workbench.AttachActivityFeed(runtime.ActivityHub())
	}
	return app
}

// NewDiagnostics assembles only the read-only reporting use case.
func NewDiagnostics(s *store.Store, version string, databaseIssue ...string) *App {
	query := diag.New(s, version, nil)
	if len(databaseIssue) > 0 {
		query = query.WithDatabaseIssue(databaseIssue[0])
	}
	return &App{Diag: query}
}
