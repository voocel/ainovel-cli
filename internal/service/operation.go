package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/derive"
	"github.com/voocel/ainovel-cli/internal/domain"
	operationengine "github.com/voocel/ainovel-cli/internal/operation"
	"github.com/voocel/ainovel-cli/internal/store"
)

type StartOperationCommand struct {
	OperationID         string
	ProjectID           string
	Kind                domain.OperationKind
	WorkerProfileID     string
	Priority            int
	DependsOn           []string
	Input               json.RawMessage
	Packs               []PackRef
	CreatorProfiles     []CreatorProfileRef
	CoreProtocolVersion string
	ModelConfigDigest   string
	ApprovalPolicy      domain.ApprovalPolicy
	// RunID 非空时 Operation 归属该 CreationRun（§6.3）：血统事件与策略版本
	// 由存储层在创建事务内一并落盘。
	RunID     string
	CreatedAt time.Time
}

type RestartOperationCommand struct {
	FromOperationID     string
	OperationID         string
	WorkerProfileID     string
	Packs               []PackRef
	CreatorProfiles     []CreatorProfileRef
	CoreProtocolVersion string
	ModelConfigDigest   string
	ApprovalPolicy      domain.ApprovalPolicy
	RunID               string
	// Input 非空时替代前任输入：后继按当前快照重算的任务输入（含新命中的要求）。
	Input     json.RawMessage
	CreatedAt time.Time
}

func (s *Service) StartOperation(ctx context.Context, command StartOperationCommand) (domain.Operation, error) {
	compiled, err := s.compileExecutionProfile(ctx, executionProfileCommand{
		ProjectID: command.ProjectID, Kind: command.Kind, WorkerProfileID: command.WorkerProfileID,
		Input: command.Input, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
		CoreProtocolVersion: command.CoreProtocolVersion, ModelConfigDigest: command.ModelConfigDigest,
		ApprovalPolicy: command.ApprovalPolicy, CreatedAt: command.CreatedAt,
	})
	if err != nil {
		return domain.Operation{}, err
	}
	return s.store.CreateOperation(ctx, domain.Operation{
		ID: command.OperationID, Kind: command.Kind,
		Target:    domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID},
		DependsOn: command.DependsOn, Priority: command.Priority, State: domain.OperationQueued,
		RunID:    command.RunID,
		Snapshot: compiled.Snapshot, Input: append(json.RawMessage(nil), command.Input...),
		CreatedAt: command.CreatedAt, UpdatedAt: command.CreatedAt,
	})
}

func (s *Service) RestartOperation(ctx context.Context, command RestartOperationCommand) (domain.Operation, error) {
	if strings.TrimSpace(command.FromOperationID) == "" || strings.TrimSpace(command.OperationID) == "" ||
		command.FromOperationID == command.OperationID || command.CreatedAt.IsZero() {
		return domain.Operation{}, fmt.Errorf("restart requires distinct source, target and time: %w", domain.ErrInvalid)
	}
	previous, err := s.store.GetOperation(ctx, command.FromOperationID)
	if err != nil {
		return domain.Operation{}, err
	}
	switch previous.State {
	case domain.OperationFailed, domain.OperationCancelled, domain.OperationStale:
	default:
		return domain.Operation{}, fmt.Errorf(
			"operation %q must be failed, cancelled or stale before restart, got %s: %w",
			previous.ID, previous.State, store.ErrStateConflict,
		)
	}
	runID := command.RunID
	if runID == "" {
		runID = previous.RunID
	}
	if previous.RunID != "" && runID != previous.RunID {
		return domain.Operation{}, fmt.Errorf(
			"restart cannot move operation %q to another creation run: %w",
			previous.ID, domain.ErrInvalid,
		)
	}
	workerProfileID := command.WorkerProfileID
	if workerProfileID == "" {
		separator := strings.LastIndex(previous.Snapshot.WorkerProfileVersion, "@")
		if separator <= 0 {
			return domain.Operation{}, fmt.Errorf("previous worker profile %q is invalid: %w", previous.Snapshot.WorkerProfileVersion, domain.ErrInvalid)
		}
		workerProfileID = previous.Snapshot.WorkerProfileVersion[:separator]
	}
	coreVersion := command.CoreProtocolVersion
	if coreVersion == "" {
		coreVersion = previous.Snapshot.CoreProtocolVersion
	}
	modelDigest := command.ModelConfigDigest
	if modelDigest == "" {
		if s.executor == nil {
			modelDigest = previous.Snapshot.ModelConfigDigest
		} else {
			modelDigest = s.executor.ModelConfigDigest()
		}
	}
	approval := command.ApprovalPolicy
	if approval == "" {
		approval = previous.Snapshot.ApprovalPolicy
	}
	packs, profiles := command.Packs, command.CreatorProfiles
	if packs == nil || profiles == nil {
		oldSources, err := s.prompts.Sources(ctx, previous.Snapshot.ExecutionProfileDigest)
		if err != nil {
			return domain.Operation{}, err
		}
		if packs == nil {
			packs, err = packRefsFromSources(oldSources)
			if err != nil {
				return domain.Operation{}, err
			}
		}
		if profiles == nil {
			profiles, err = creatorProfileRefsFromSources(oldSources)
			if err != nil {
				return domain.Operation{}, err
			}
		}
	}
	input := previous.Input
	if len(command.Input) != 0 {
		input = command.Input
	}
	if existing, err := s.store.GetOperation(ctx, command.OperationID); err == nil {
		if err := validateRestartTarget(existing, previous, input, workerProfileID, coreVersion, modelDigest, approval, runID); err != nil {
			return domain.Operation{}, err
		}
		if err := s.store.CopyWorkspaceArtifacts(ctx, previous.ID, existing.ID, command.CreatedAt); err != nil {
			return domain.Operation{}, err
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.Operation{}, err
	}
	restarted, err := s.StartOperation(ctx, StartOperationCommand{
		OperationID: command.OperationID, ProjectID: previous.Target.ID, Kind: previous.Kind,
		WorkerProfileID: workerProfileID, Priority: previous.Priority, DependsOn: previous.DependsOn,
		Input: append(json.RawMessage(nil), input...), Packs: packs, CreatorProfiles: profiles,
		CoreProtocolVersion: coreVersion, ModelConfigDigest: modelDigest,
		ApprovalPolicy: approval, RunID: runID, CreatedAt: command.CreatedAt,
	})
	if err != nil {
		return domain.Operation{}, err
	}
	if err := s.store.CopyWorkspaceArtifacts(ctx, previous.ID, restarted.ID, command.CreatedAt); err != nil {
		return domain.Operation{}, err
	}
	return restarted, nil
}

func validateRestartTarget(
	existing, previous domain.Operation,
	input json.RawMessage,
	workerProfileID, coreVersion, modelDigest string,
	approval domain.ApprovalPolicy,
	runID string,
) error {
	workerVersion := existing.Snapshot.WorkerProfileVersion
	separator := strings.LastIndex(workerVersion, "@")
	if existing.State != domain.OperationQueued || existing.Target != previous.Target || existing.Kind != previous.Kind ||
		existing.Priority != previous.Priority || !bytes.Equal(existing.Input, input) ||
		separator <= 0 || workerVersion[:separator] != workerProfileID ||
		existing.Snapshot.CoreProtocolVersion != coreVersion || existing.Snapshot.ApprovalPolicy != approval ||
		(modelDigest != "" && existing.Snapshot.ModelConfigDigest != modelDigest) ||
		existing.RunID != runID || !slices.Equal(existing.DependsOn, previous.DependsOn) {
		return fmt.Errorf("operation %q is not the requested restart target: %w", existing.ID, store.ErrIdempotencyConflict)
	}
	return nil
}

func packRefsFromSources(sources []prompt.Source) ([]PackRef, error) {
	refs := make([]PackRef, 0)
	for _, source := range sources {
		if source.Layer != "pack_defaults" {
			continue
		}
		separator := strings.LastIndex(source.ID, "@")
		if separator <= 0 || source.Revision <= domain.InitialRevision {
			return nil, fmt.Errorf("pack source %q is invalid: %w", source.ID, domain.ErrInvalid)
		}
		refs = append(refs, PackRef{ID: source.ID[:separator], Revision: source.Revision})
	}
	return refs, nil
}

func creatorProfileRefsFromSources(sources []prompt.Source) ([]CreatorProfileRef, error) {
	refs := make([]CreatorProfileRef, 0)
	for _, source := range sources {
		if source.Layer != "creator_profile" {
			continue
		}
		separator := strings.LastIndex(source.ID, "/")
		if separator <= 0 || separator == len(source.ID)-1 || source.Revision <= domain.InitialRevision {
			return nil, fmt.Errorf("creator profile source %q is invalid: %w", source.ID, domain.ErrInvalid)
		}
		refs = append(refs, CreatorProfileRef{
			ID: source.ID[:separator], Scope: source.ID[separator+1:], Revision: source.Revision,
		})
	}
	return refs, nil
}

type executionProfileCommand struct {
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

func (s *Service) compileExecutionProfile(ctx context.Context, command executionProfileCommand) (prompt.Compiled, error) {
	modelConfigDigest := command.ModelConfigDigest
	if s.executor != nil {
		runtimeDigest := s.executor.ModelConfigDigest()
		if modelConfigDigest == "" {
			modelConfigDigest = runtimeDigest
		} else if modelConfigDigest != runtimeDigest {
			return prompt.Compiled{}, fmt.Errorf("execution profile model config does not match configured runtime: %w", domain.ErrInvalid)
		}
	}
	if strings.TrimSpace(modelConfigDigest) == "" {
		return prompt.Compiled{}, fmt.Errorf("model config digest is required: %w", domain.ErrInvalid)
	}
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return prompt.Compiled{}, err
	}
	// 审批策略权威在 Project 文档（§6.3）：未显式指定时从当前 Revision 解析，
	// 快照记录启动时刻的策略作为历史最低约束。
	approvalPolicy := command.ApprovalPolicy
	if approvalPolicy == "" {
		approvalPolicy = domain.ApprovalAuto
		if revision > domain.InitialRevision {
			settings, err := loadDocuments[domain.ApprovalSetting](ctx, s.store, target, domain.DocumentApproval, revision)
			if err != nil {
				return prompt.Compiled{}, err
			}
			if len(settings) > 0 {
				approvalPolicy = settings[0].Policy
			}
		}
	}
	if approvalPolicy == domain.ApprovalCustom {
		return prompt.Compiled{}, fmt.Errorf("custom approval policy requires a policy contract, which this command did not provide: %w", domain.ErrInvalid)
	}
	contextKey, err := derive.ContextKey(command.Kind, command.Input)
	if err != nil {
		return prompt.Compiled{}, err
	}
	var intent domain.Intent
	var ownership []domain.OwnershipRule
	var storyContext json.RawMessage
	cachedContext, err := s.store.GetDerivedDocument(ctx, command.ProjectID, revision, derive.StoryContextKind, contextKey)
	switch {
	case err == nil:
		intentDocument, err := s.store.GetDocument(ctx, target, domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, revision)
		if err != nil {
			return prompt.Compiled{}, err
		}
		if err := json.Unmarshal(intentDocument.Content, &intent); err != nil {
			return prompt.Compiled{}, fmt.Errorf("decode project intent: %w", err)
		}
		ownership, err = loadDocuments[domain.OwnershipRule](ctx, s.store, target, domain.DocumentOwnership, revision)
		if err != nil {
			return prompt.Compiled{}, err
		}
		storyContext = append(json.RawMessage(nil), cachedContext.Content...)
	case errors.Is(err, store.ErrNotFound):
		project, err := s.Project(ctx, command.ProjectID, revision)
		if err != nil {
			return prompt.Compiled{}, err
		}
		intent, ownership = project.Intent, project.Ownership
		contextValue, err := derive.BuildStoryContext(derive.ProjectContent{
			ID: project.ID, Revision: project.Revision, Intent: project.Intent,
			Plan: project.Plan, Canon: project.Canon, Manuscript: project.Manuscript,
			Ownership: project.Ownership,
		}, command.Kind, command.Input)
		if err != nil {
			return prompt.Compiled{}, err
		}
		storyContext, err = json.Marshal(contextValue)
		if err != nil {
			return prompt.Compiled{}, fmt.Errorf("encode project context: %w", err)
		}
		stored, err := s.store.SaveDerivedDocument(ctx, domain.DerivedDocument{
			ProjectID: command.ProjectID, Revision: revision, Kind: derive.StoryContextKind,
			Key: contextKey, Content: storyContext, CreatedAt: command.CreatedAt,
		})
		if err != nil {
			return prompt.Compiled{}, err
		}
		storyContext = stored.Content
	default:
		return prompt.Compiled{}, err
	}
	capability, err := prompt.BuiltinCapability(command.Kind)
	if err != nil {
		return prompt.Compiled{}, err
	}
	workerProfileID := command.WorkerProfileID
	if workerProfileID == "" {
		workerProfileID = capability.Worker.ID
	}
	if workerProfileID != capability.Worker.ID {
		return prompt.Compiled{}, fmt.Errorf(
			"operation %s requires worker profile %s, got %s: %w",
			command.Kind, capability.Worker.ID, workerProfileID, domain.ErrInvalid,
		)
	}
	worker := capability.Worker
	// 资产自动装配（D31）：命令未显式指定时，按 Project 的固定引用加载已启用的
	// Pack 与 Creator Profile——给这本书固定一套风格与方法，此后自动沿用。
	var overlayRules []string
	if revision > domain.InitialRevision {
		assets, err := loadDocuments[domain.ProjectAssetRefs](ctx, s.store, target, domain.DocumentAssets, revision)
		if err != nil {
			return prompt.Compiled{}, err
		}
		if len(assets) > 0 {
			if len(command.Packs) == 0 {
				for _, ref := range assets[0].Packs {
					command.Packs = append(command.Packs, PackRef{ID: ref.ID, Revision: ref.Revision})
				}
			}
			if len(command.CreatorProfiles) == 0 {
				for _, ref := range assets[0].CreatorProfiles {
					command.CreatorProfiles = append(command.CreatorProfiles,
						CreatorProfileRef{ID: ref.ID, Scope: ref.Scope, Revision: ref.Revision})
				}
			}
		}
		overlays, err := loadDocuments[domain.ProjectOverlay](ctx, s.store, target, domain.DocumentOverlay, revision)
		if err != nil {
			return prompt.Compiled{}, err
		}
		if len(overlays) > 0 {
			overlayRules = overlays[0].Rules
		}
	}
	packs := make([]prompt.VersionedPack, len(command.Packs))
	for i, ref := range command.Packs {
		pack, err := loadPack(ctx, s.store, ref)
		if err != nil {
			return prompt.Compiled{}, err
		}
		packs[i] = pack
	}
	creatorProfiles := make([]prompt.VersionedCreatorProfile, len(command.CreatorProfiles))
	for i, ref := range command.CreatorProfiles {
		profile, err := loadCreatorProfile(ctx, s.store, ref)
		if err != nil {
			return prompt.Compiled{}, err
		}
		creatorProfiles[i] = profile
	}
	return s.prompts.Reload(ctx, prompt.CompileRequest{
		ProjectID: command.ProjectID, CoreProtocolVersion: command.CoreProtocolVersion,
		Worker: worker, Packs: packs, CreatorProfiles: creatorProfiles,
		Intent: intent, Ownership: ownership, OverlayRules: overlayRules,
		StoryContext: storyContext, Task: command.Input,
		BaseRevision: revision, ProjectOverlayRevision: revision,
		ModelConfigDigest: modelConfigDigest, ApprovalPolicy: approvalPolicy,
		ApprovalPolicyDigest: domain.Digest([]byte(approvalPolicy)),
	}, command.CreatedAt)
}

func (s *Service) RunNextOperation(
	ctx context.Context,
	workerID string,
	leaseDuration time.Duration,
	now time.Time,
) (operationengine.RunResult, error) {
	if s.executor == nil {
		return operationengine.RunResult{}, fmt.Errorf("operation executor is not configured: %w", domain.ErrInvalid)
	}
	return s.operations.RunNext(ctx, s.executor, workerID, leaseDuration, now)
}

func (s *Service) RunOperation(
	ctx context.Context,
	operationID, workerID string,
	leaseDuration time.Duration,
	now time.Time,
) (operationengine.RunResult, error) {
	if s.executor == nil {
		return operationengine.RunResult{}, fmt.Errorf("operation executor is not configured: %w", domain.ErrInvalid)
	}
	return s.operations.Run(ctx, s.executor, operationID, workerID, leaseDuration, now)
}

func (s *Service) PauseOperation(ctx context.Context, id string, at time.Time) (domain.Operation, error) {
	operation, err := s.store.GetOperation(ctx, id)
	if err != nil {
		return domain.Operation{}, err
	}
	if operation.State == domain.OperationPaused {
		return operation, nil
	}
	return s.store.TransitionOperation(ctx, id, operation.State, domain.OperationPaused, "paused by user", at)
}

func (s *Service) ResumeOperation(ctx context.Context, id string, at time.Time) (domain.Operation, error) {
	operation, err := s.store.GetOperation(ctx, id)
	if err != nil {
		return domain.Operation{}, err
	}
	if operation.State == domain.OperationQueued {
		return operation, nil
	}
	if operation.State != domain.OperationPaused && operation.State != domain.OperationFailed {
		return domain.Operation{}, fmt.Errorf("operation %q cannot resume from %s: %w", id, operation.State, store.ErrStateConflict)
	}
	if operation.State == domain.OperationFailed {
		proposal, err := s.store.GetProposalByOperation(ctx, operation.ID)
		if err == nil && proposal.ApprovalState == domain.ApprovalRejected {
			return domain.Operation{}, fmt.Errorf("operation %q has a rejected proposal; create an explicit restart instead: %w", id, store.ErrStateConflict)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return domain.Operation{}, err
		}
	}
	return s.store.TransitionOperation(ctx, id, operation.State, domain.OperationQueued, "resumed by user", at)
}

func (s *Service) CancelOperation(ctx context.Context, id string, at time.Time) (domain.Operation, error) {
	operation, err := s.store.GetOperation(ctx, id)
	if err != nil {
		return domain.Operation{}, err
	}
	if operation.State == domain.OperationCancelled {
		return operation, nil
	}
	return s.store.TransitionOperation(ctx, id, operation.State, domain.OperationCancelled, "cancelled by user; workspace retained", at)
}

func (s *Service) ReprioritizeOperation(ctx context.Context, id string, priority int, at time.Time) (domain.Operation, error) {
	return s.store.SetOperationPriority(ctx, id, priority, at)
}

func (s *Service) RecoverOperations(ctx context.Context, at time.Time) ([]string, error) {
	return s.store.RecoverExpiredOperations(ctx, at)
}

func (s *Service) Operation(ctx context.Context, id string) (domain.Operation, error) {
	return s.store.GetOperation(ctx, id)
}

func (s *Service) OperationEvents(ctx context.Context, id string) ([]domain.OperationEvent, error) {
	return s.store.ListOperationEvents(ctx, id)
}

// ToolIssue 是一次工具报错的诊断摘要（可能已被模型自纠，任务本身未必失败）。
type ToolIssue struct {
	Tool string // 报错调用的工具名；消息里找不到对应调用时为空
	Err  string // 工具返回的原始错误文本
}

// OperationToolIssues 从已持久化的 agent 消息（agent.message_committed）里
// 确定性提取工具报错：role=tool 且 metadata.is_error 的消息即报错结果，
// 工具名经同批 assistant 消息的 tool_call（id→name）映射。成功任务自纠过的
// 报错也在其中——诊断下钻不只覆盖最终失败（workbench §4）。
func (s *Service) OperationToolIssues(ctx context.Context, id string) ([]ToolIssue, error) {
	events, err := s.store.ListOperationEvents(ctx, id)
	if err != nil {
		return nil, err
	}
	// 消息 JSON 形状即 capability 落库的 agentcore 消息序列化契约。
	type committed struct {
		Role    string `json:"role"`
		Content []struct {
			Text     string `json:"text"`
			ToolCall *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"tool_call"`
		} `json:"content"`
		Metadata struct {
			ToolCallID string `json:"tool_call_id"`
			IsError    bool   `json:"is_error"`
		} `json:"metadata"`
	}
	callNames := make(map[string]string)
	var issues []ToolIssue
	for _, event := range events {
		if event.Kind != "agent.message_committed" {
			continue
		}
		var message committed
		if err := json.Unmarshal(event.Payload, &message); err != nil {
			// 形状解不开=落库契约漂移，显式报错而非静默少报（Debug-First）。
			return nil, fmt.Errorf("decode committed message #%d of operation %s: %w",
				event.Sequence, id, err)
		}
		for _, block := range message.Content {
			if block.ToolCall != nil {
				callNames[block.ToolCall.ID] = block.ToolCall.Name
			}
		}
		if message.Role != "tool" || !message.Metadata.IsError {
			continue
		}
		var text strings.Builder
		for _, block := range message.Content {
			text.WriteString(block.Text)
		}
		// 工具错误结果是 json.Marshal 过的字符串字面量：解掉外层引号与转义，
		// 诊断呈现原始错误文本。
		raw := text.String()
		var unquoted string
		if json.Unmarshal([]byte(raw), &unquoted) == nil {
			raw = unquoted
		}
		issues = append(issues, ToolIssue{
			Tool: callNames[message.Metadata.ToolCallID],
			Err:  raw,
		})
	}
	return issues, nil
}
