package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/domain"
)

type ReloadPromptCommand struct {
	ProjectID           string
	Kind                domain.OperationKind
	WorkerProfileID     string
	Input               json.RawMessage
	Packs               []PackRef
	CreatorProfiles     []CreatorProfileRef
	CoreProtocolVersion string
	ModelConfigDigest   string
	ApprovalPolicy      domain.ApprovalPolicy
	CreatedAt           time.Time
}

type ReloadPromptResult struct {
	ExecutionProfileDigest string              `json:"execution_profile_digest"`
	PromptDigest           string              `json:"prompt_digest"`
	ToolSchemaDigest       string              `json:"tool_schema_digest"`
	Sources                []prompt.Source     `json:"sources"`
	Diagnostics            []prompt.Diagnostic `json:"diagnostics"`
}

func (s *Service) ReloadPrompt(ctx context.Context, command ReloadPromptCommand) (ReloadPromptResult, error) {
	compiled, err := s.compileExecutionProfile(ctx, executionProfileCommand{
		ProjectID: command.ProjectID, Kind: command.Kind, WorkerProfileID: command.WorkerProfileID,
		Input: command.Input, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
		CoreProtocolVersion: command.CoreProtocolVersion, ModelConfigDigest: command.ModelConfigDigest,
		ApprovalPolicy: command.ApprovalPolicy, CreatedAt: command.CreatedAt,
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

func (s *Service) Prompt(ctx context.Context, executionProfileDigest string) (string, []prompt.Source, error) {
	text, err := s.prompts.Show(ctx, executionProfileDigest)
	if err != nil {
		return "", nil, err
	}
	sources, err := s.prompts.Sources(ctx, executionProfileDigest)
	return text, sources, err
}

func (s *Service) PromptDiff(ctx context.Context, left, right string) (prompt.Diff, error) {
	return s.prompts.Diff(ctx, left, right)
}

func (s *Service) PromptLint(ctx context.Context, executionProfileDigest string) ([]prompt.Diagnostic, error) {
	return s.prompts.Lint(ctx, executionProfileDigest)
}
