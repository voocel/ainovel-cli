package task

import (
	"context"

	"github.com/voocel/ainovel-cli/internal/app/profile"
	operationengine "github.com/voocel/ainovel-cli/internal/domain/operation"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

type Profiles interface {
	Compile(context.Context, profile.CompileCommand) (prompt.Compiled, error)
	Load(context.Context, string) (prompt.Compiled, error)
}
type Manager struct {
	store      *store.Store
	operations *operationengine.Engine
	executors  ExecutorSet
	profiles   Profiles
}

func New(s *store.Store, engine *operationengine.Engine, executors ExecutorSet, profiles Profiles) *Manager {
	return &Manager{store: s, operations: engine, executors: executors, profiles: profiles}
}
func (s *Manager) HasLLM() bool { return s.executors.LLM != nil }
func (s *Manager) ExternalConfigDigest() string {
	if configured, ok := s.executors.External.(interface{ ConfigDigest() string }); ok {
		return configured.ConfigDigest()
	}
	return ""
}

type ExecutorSet struct {
	LLM      operationengine.Executor
	External operationengine.Executor
}

func (e ExecutorSet) all() []operationengine.Executor {
	var result []operationengine.Executor
	for _, executor := range []operationengine.Executor{e.LLM, e.External} {
		if executor != nil {
			result = append(result, executor)
		}
	}
	return result
}
