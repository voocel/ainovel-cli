package sim

import (
	"context"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

type LLMChat interface {
	Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error)
}

type Deps struct {
	Store   *store.Store
	LLM     LLMChat
	Prompts Prompts
}

type Prompts struct {
	Source string
	Merge  string
}

type Options struct {
	SourceDir         string
	HeartbeatInterval time.Duration
}

type Stage string

const (
	StageScan    Stage = "scan"
	StageAnalyze Stage = "analyze"
	StageMerge   Stage = "merge"
	StageImport  Stage = "import"
	StageDone    Stage = "done"
	StageError   Stage = "error"
)

type Event struct {
	Time    time.Time
	Stage   Stage
	Current int
	Total   int
	Message string
	Key     string
	Err     error
}

type ImportResult struct {
	ImportedSources int
	SkippedSources  int
	ProfilePath     string
}
