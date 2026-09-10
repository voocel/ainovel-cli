package profile

import (
	"context"
	"encoding/json"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

type ReloadPromptCommand struct {
	ProjectID           string
	Kind                model.OperationKind
	WorkerProfileID     string
	Input               json.RawMessage
	Packs               []resource.PackRef
	CreatorProfiles     []resource.CreatorProfileRef
	CoreProtocolVersion string
	ModelConfigDigest   string
	CreatedAt           time.Time
}

type ReloadPromptResult struct {
	ExecutionProfileDigest string              `json:"execution_profile_digest"`
	PromptDigest           string              `json:"prompt_digest"`
	ToolSchemaDigest       string              `json:"tool_schema_digest"`
	Sources                []prompt.Source     `json:"sources"`
	Diagnostics            []prompt.Diagnostic `json:"diagnostics"`
}

func (s *Compiler) ReloadPrompt(ctx context.Context, command ReloadPromptCommand) (ReloadPromptResult, error) {
	revision, err := s.store.CurrentRevision(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID})
	if err != nil {
		return ReloadPromptResult{}, err
	}
	compiled, err := s.Compile(ctx, CompileCommand{
		ProjectID: command.ProjectID, Revision: revision, Kind: command.Kind, WorkerProfileID: command.WorkerProfileID,
		Input: command.Input, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
		CoreProtocolVersion: command.CoreProtocolVersion, ModelConfigDigest: command.ModelConfigDigest,
		CreatedAt: command.CreatedAt,
	})
	if err != nil {
		return ReloadPromptResult{}, err
	}
	diagnostics, err := s.prompts.Lint(ctx, compiled.ProfileDigest)
	if err != nil {
		return ReloadPromptResult{}, err
	}
	return ReloadPromptResult{
		ExecutionProfileDigest: compiled.ProfileDigest,
		PromptDigest:           compiled.PromptDigest, ToolSchemaDigest: compiled.ToolSchemaDigest,
		Sources: compiled.Sources, Diagnostics: diagnostics,
	}, nil
}

func (s *Compiler) Prompt(ctx context.Context, executionProfileDigest string) (string, []prompt.Source, error) {
	text, err := s.prompts.Show(ctx, executionProfileDigest)
	if err != nil {
		return "", nil, err
	}
	sources, err := s.prompts.Sources(ctx, executionProfileDigest)
	return text, sources, err
}

func (s *Compiler) PromptDiff(ctx context.Context, left, right string) (prompt.Diff, error) {
	return s.prompts.Diff(ctx, left, right)
}

func (s *Compiler) PromptLint(ctx context.Context, executionProfileDigest string) ([]prompt.Diagnostic, error) {
	return s.prompts.Lint(ctx, executionProfileDigest)
}
