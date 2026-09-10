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
	OperationID string
	ProjectID   string
	Kind        domain.OperationKind
	Priority    int
	DependsOn   []string
	Input       json.RawMessage
	// LLM 执行族的编译输入：Worker、资产引用、协议版本与模型配置摘要。
	WorkerProfileID     string
	Packs               []PackRef
	CreatorProfiles     []CreatorProfileRef
	CoreProtocolVersion string
	ModelConfigDigest   string
	// ConfigDigest 是外部执行族的自身配置摘要，冻结进快照（D45）。
	ConfigDigest   string
	ApprovalPolicy domain.ApprovalPolicy
	// RunID 非空时 Operation 归属该 CreationRun（§6.3）：血统事件与策略版本
	// 由存储层在创建事务内一并落盘。
	RunID     string
	CreatedAt time.Time
}

type RestartOperationCommand struct {
	FromOperationID     string
	ConfigDigest        string
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

// StartOperation 冻结执行快照并入队（D45）：按种类的执行族取身份与配置——
// LLM 族编译 Execution Profile，外部族透传自身配置摘要并绑定已装配的执行器。
func (s *Service) StartOperation(ctx context.Context, command StartOperationCommand) (domain.Operation, error) {
	spec, err := domain.KindSpec(command.Kind)
	if err != nil {
		return domain.Operation{}, err
	}
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return domain.Operation{}, err
	}
	policy, err := s.resolveApprovalPolicy(ctx, target, revision, command.ApprovalPolicy)
	if err != nil {
		return domain.Operation{}, err
	}
	snapshot := domain.ExecutionSnapshot{
		BaseRevision: revision, InputDigest: domain.Digest(command.Input), ApprovalPolicy: policy,
	}
	switch spec.Executor {
	case domain.ExecutorLLM:
		compiled, err := s.compileExecutionProfile(ctx, executionProfileCommand{
			ProjectID: command.ProjectID, Revision: revision, Kind: command.Kind, WorkerProfileID: command.WorkerProfileID,
			Input: command.Input, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
			CoreProtocolVersion: command.CoreProtocolVersion, ModelConfigDigest: command.ModelConfigDigest,
			CreatedAt: command.CreatedAt,
		})
		if err != nil {
			return domain.Operation{}, err
		}
		snapshot.Executor, snapshot.ConfigDigest = prompt.ExecutorIdentity(compiled.ModelConfigDigest), compiled.ProfileDigest
	case domain.ExecutorExternal:
		if s.executors.External == nil || strings.TrimSpace(command.ConfigDigest) == "" {
			return domain.Operation{}, fmt.Errorf("%s requires a configured executor and its config digest: %w", command.Kind, domain.ErrInvalid)
		}
		snapshot.Executor, snapshot.ConfigDigest = s.executors.External.Identity(), command.ConfigDigest
	}
	return s.store.CreateOperation(ctx, domain.Operation{
		ID: command.OperationID, Kind: command.Kind, Target: target,
		DependsOn: command.DependsOn, Priority: command.Priority, State: domain.OperationQueued,
		RunID:    command.RunID,
		Snapshot: snapshot, Input: append(json.RawMessage(nil), command.Input...),
		CreatedAt: command.CreatedAt, UpdatedAt: command.CreatedAt,
	})
}

// resolveApprovalPolicy 解析启动时刻的审批策略：未显式指定时取当前 Revision 的
// 权威设置（§6.3），快照记录它作为历史最低约束；custom 需要显式契约。
func (s *Service) resolveApprovalPolicy(
	ctx context.Context,
	target domain.AuthorityTarget,
	revision domain.Revision,
	requested domain.ApprovalPolicy,
) (domain.ApprovalPolicy, error) {
	policy := requested
	if policy == "" {
		policy = domain.ApprovalAuto
		if revision > domain.InitialRevision {
			settings, err := loadDocuments[domain.ApprovalSetting](ctx, s.store, target, domain.DocumentApproval, revision)
			if err != nil {
				return "", err
			}
			if len(settings) > 0 {
				policy = settings[0].Policy
			}
		}
	}
	if policy == domain.ApprovalCustom {
		return "", fmt.Errorf("custom approval policy requires a policy contract, which this command did not provide: %w", domain.ErrInvalid)
	}
	return policy, nil
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
	approval := command.ApprovalPolicy
	if approval == "" {
		approval = previous.Snapshot.ApprovalPolicy
	}
	successor := StartOperationCommand{
		OperationID: command.OperationID, ProjectID: previous.Target.ID, Kind: previous.Kind,
		Priority: previous.Priority, DependsOn: previous.DependsOn,
		Input: append(json.RawMessage(nil), previous.Input...), ApprovalPolicy: approval,
		RunID: runID, CreatedAt: command.CreatedAt,
	}
	if len(command.Input) != 0 {
		successor.Input = append(json.RawMessage(nil), command.Input...)
	}
	spec, err := domain.KindSpec(previous.Kind)
	if err != nil {
		return domain.Operation{}, err
	}
	switch spec.Executor {
	case domain.ExecutorLLM:
		// 沿用前任冻结的 Execution Profile 作为默认编译输入；资产引用按记录的
		// 来源恢复，显式传入的才替换。
		compiled, err := s.prompts.Load(ctx, previous.Snapshot.ConfigDigest)
		if err != nil {
			return domain.Operation{}, err
		}
		successor.WorkerProfileID = prompt.WorkerID(compiled.WorkerProfile)
		successor.CoreProtocolVersion, successor.ModelConfigDigest = compiled.CoreProtocolVersion, compiled.ModelConfigDigest
		if s.executors.LLM != nil {
			successor.ModelConfigDigest = ""
		}
		successor.Packs, err = packRefsFromSources(compiled.Sources)
		if err != nil {
			return domain.Operation{}, err
		}
		successor.CreatorProfiles, err = creatorProfileRefsFromSources(compiled.Sources)
		if err != nil {
			return domain.Operation{}, err
		}
		if command.WorkerProfileID != "" {
			successor.WorkerProfileID = command.WorkerProfileID
		}
		if command.CoreProtocolVersion != "" {
			successor.CoreProtocolVersion = command.CoreProtocolVersion
		}
		if command.ModelConfigDigest != "" {
			successor.ModelConfigDigest = command.ModelConfigDigest
		}
		if command.Packs != nil {
			successor.Packs = command.Packs
		}
		if command.CreatorProfiles != nil {
			successor.CreatorProfiles = command.CreatorProfiles
		}
	case domain.ExecutorExternal:
		successor.ConfigDigest = previous.Snapshot.ConfigDigest
		if command.ConfigDigest != "" {
			successor.ConfigDigest = command.ConfigDigest
		}
	}
	if existing, err := s.store.GetOperation(ctx, command.OperationID); err == nil {
		if err := s.validateRestartTarget(ctx, existing, previous, successor); err != nil {
			return domain.Operation{}, err
		}
		if err := s.store.CopyWorkspaceArtifacts(ctx, previous.ID, existing.ID, command.CreatedAt); err != nil {
			return domain.Operation{}, err
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.Operation{}, err
	}
	restarted, err := s.StartOperation(ctx, successor)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := s.store.CopyWorkspaceArtifacts(ctx, previous.ID, restarted.ID, command.CreatedAt); err != nil {
		return domain.Operation{}, err
	}
	return restarted, nil
}

// validateRestartTarget 校验同 ID 的既有 Operation 就是本次重启请求（幂等）。
func (s *Service) validateRestartTarget(
	ctx context.Context,
	existing, previous domain.Operation,
	successor StartOperationCommand,
) error {
	if existing.State != domain.OperationQueued || existing.Target != previous.Target || existing.Kind != previous.Kind ||
		existing.Priority != previous.Priority || !bytes.Equal(existing.Input, successor.Input) ||
		existing.Snapshot.ApprovalPolicy != successor.ApprovalPolicy ||
		existing.RunID != successor.RunID || !slices.Equal(existing.DependsOn, previous.DependsOn) {
		return fmt.Errorf("operation %q is not the requested restart target: %w", existing.ID, store.ErrIdempotencyConflict)
	}
	if successor.ConfigDigest != "" {
		if existing.Snapshot.ConfigDigest != successor.ConfigDigest {
			return fmt.Errorf("operation %q is not the requested restart target: %w", existing.ID, store.ErrIdempotencyConflict)
		}
		return nil
	}
	compiled, err := s.prompts.Load(ctx, existing.Snapshot.ConfigDigest)
	if err != nil {
		return err
	}
	if prompt.WorkerID(compiled.WorkerProfile) != successor.WorkerProfileID ||
		compiled.CoreProtocolVersion != successor.CoreProtocolVersion ||
		(successor.ModelConfigDigest != "" && compiled.ModelConfigDigest != successor.ModelConfigDigest) {
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
	Revision            domain.Revision
	Kind                domain.OperationKind
	WorkerProfileID     string
	Input               json.RawMessage
	Packs               []PackRef
	CreatorProfiles     []CreatorProfileRef
	CoreProtocolVersion string
	ModelConfigDigest   string
	CreatedAt           time.Time
}

func (s *Service) compileExecutionProfile(ctx context.Context, command executionProfileCommand) (prompt.Compiled, error) {
	// 模型配置摘要以已装配的 LLM Runtime 为准（可选接口，同 SemanticAnalyzer 的断言模式）。
	modelConfigDigest := command.ModelConfigDigest
	if runtime, ok := s.executors.LLM.(interface{ ModelConfigDigest() string }); ok {
		runtimeDigest := runtime.ModelConfigDigest()
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
	revision := command.Revision
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
			Plan: project.Plan, Entities: project.Entities, Canon: project.Canon,
			Manuscript: project.Manuscript, Ownership: project.Ownership,
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
	task, err := promptTask(command.Input)
	if err != nil {
		return prompt.Compiled{}, err
	}
	return s.prompts.Reload(ctx, prompt.CompileRequest{
		ProjectID: command.ProjectID, CoreProtocolVersion: command.CoreProtocolVersion,
		Worker: worker, Packs: packs, CreatorProfiles: creatorProfiles,
		Intent: intent, Ownership: ownership, OverlayRules: overlayRules,
		StoryContext: storyContext, Task: task,
		BaseRevision: revision, ProjectOverlayRevision: revision,
		ModelConfigDigest: modelConfigDigest,
	}, command.CreatedAt)
}

func (s *Service) RunNextOperation(
	ctx context.Context,
	workerID string,
	leaseDuration time.Duration,
	now time.Time,
) (operationengine.RunResult, error) {
	return s.operations.RunNextWithExecutors(ctx, s.executors.all(), workerID, leaseDuration, now)
}

func (s *Service) RunOperation(
	ctx context.Context,
	operationID, workerID string,
	leaseDuration time.Duration,
	now time.Time,
) (operationengine.RunResult, error) {
	operation, err := s.store.GetOperation(ctx, operationID)
	if err != nil {
		return operationengine.RunResult{}, err
	}
	for _, executor := range s.executors.all() {
		if executor.Identity() == operation.Snapshot.Executor {
			return s.operations.Run(ctx, executor, operationID, workerID, leaseDuration, now)
		}
	}
	return operationengine.RunResult{}, fmt.Errorf("executor %q is not configured: %w", operation.Snapshot.Executor, domain.ErrInvalid)
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

// promptTask 去掉任务输入里的证据基线：基线是内核判定有效性的依据，不是给模型的指令。
func promptTask(input json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return nil, fmt.Errorf("decode task input: %w", err)
	}
	delete(fields, domain.BasisField)
	return json.Marshal(fields)
}
