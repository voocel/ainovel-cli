package sim

import (
	"time"

	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
)

type Deps struct {
	Store   *store.Store
	LLM     llmcontract.Model
	Prompts Prompts
}

type Prompts struct {
	Source string
	Merge  string
}

type Options struct {
	SourceDir string
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
	Err     error
}

type ImportResult struct {
	ImportedSources int
	SkippedSources  int
	ProfilePath     string
}
