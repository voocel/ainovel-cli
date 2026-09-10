package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/profile"
	"github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	operationengine "github.com/voocel/ainovel-cli/internal/domain/operation"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

type StartOperationCommand struct {
	OperationID string
	ProjectID   string
	Kind        model.OperationKind
	Priority    int
	DependsOn   []string
	Input       json.RawMessage
	// LLM 执行族的编译输入：Worker、资产引用、协议版本与模型配置摘要。
	WorkerProfileID     string
	Packs               []resource.PackRef
	CreatorProfiles     []resource.CreatorProfileRef
	CoreProtocolVersion string
	ModelConfigDigest   string
	// ConfigDigest 是外部执行族的自身配置摘要，冻结进快照（D45）。
	ConfigDigest   string
	ApprovalPolicy model.ApprovalPolicy
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
	Packs               []resource.PackRef
	CreatorProfiles     []resource.CreatorProfileRef
	CoreProtocolVersion string
	ModelConfigDigest   string
	ApprovalPolicy      model.ApprovalPolicy
	RunID               string
	// Input 非空时替代前任输入：后继按当前快照重算的任务输入（含新命中的要求）。
	Input     json.RawMessage
	CreatedAt time.Time
}

// StartOperation 冻结执行快照并入队（D45）：按种类的执行族取身份与配置——
// LLM 族编译 Execution Profile，外部族透传自身配置摘要并绑定已装配的执行器。
func (s *Manager) StartOperation(ctx context.Context, command StartOperationCommand) (model.Operation, error) {
	operation, err := s.prepareOperation(ctx, command)
	if err != nil {
		return model.Operation{}, err
	}
	return s.store.CreateOperation(ctx, operation)
}

func (s *Manager) prepareOperation(ctx context.Context, command StartOperationCommand) (model.Operation, error) {
	spec, err := model.KindSpec(command.Kind)
	if err != nil {
		return model.Operation{}, err
	}
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return model.Operation{}, err
	}
	policy, err := s.resolveApprovalPolicy(ctx, target, revision, command.ApprovalPolicy)
	if err != nil {
		return model.Operation{}, err
	}
	snapshot := model.ExecutionSnapshot{
		BaseRevision: revision, InputDigest: model.Digest(command.Input), ApprovalPolicy: policy,
	}
	switch spec.Executor {
	case model.ExecutorLLM:
		compiled, err := s.profiles.Compile(ctx, profile.CompileCommand{
			ProjectID: command.ProjectID, Revision: revision, Kind: command.Kind, WorkerProfileID: command.WorkerProfileID,
			Input: command.Input, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
			CoreProtocolVersion: command.CoreProtocolVersion, ModelConfigDigest: command.ModelConfigDigest,
			CreatedAt: command.CreatedAt,
		})
		if err != nil {
			return model.Operation{}, err
		}
		snapshot.Executor, snapshot.ConfigDigest = prompt.ExecutorIdentity(compiled.ModelConfigDigest), compiled.ProfileDigest
	case model.ExecutorExternal:
		if s.executors.External == nil || strings.TrimSpace(command.ConfigDigest) == "" {
			return model.Operation{}, fmt.Errorf("%s requires a configured executor and its config digest: %w", command.Kind, model.ErrInvalid)
		}
		snapshot.Executor, snapshot.ConfigDigest = s.executors.External.Identity(), command.ConfigDigest
	}
	return model.Operation{
		ID: command.OperationID, Kind: command.Kind, Target: target,
		DependsOn: command.DependsOn, Priority: command.Priority, State: model.OperationQueued,
		RunID:    command.RunID,
		Snapshot: snapshot, Input: append(json.RawMessage(nil), command.Input...),
		CreatedAt: command.CreatedAt, UpdatedAt: command.CreatedAt,
	}, nil
}

// resolveApprovalPolicy 解析启动时刻的审批策略：未显式指定时取当前 Revision 的
// 权威设置（§6.3），快照记录它作为历史最低约束；custom 需要显式契约。
func (s *Manager) resolveApprovalPolicy(
	ctx context.Context,
	target model.AuthorityTarget,
	revision model.Revision,
	requested model.ApprovalPolicy,
) (model.ApprovalPolicy, error) {
	policy := requested
	if policy == "" {
		policy = model.ApprovalAuto
		if revision > model.InitialRevision {
			settings, err := project.LoadDocuments[model.ApprovalSetting](ctx, s.store, target, model.DocumentApproval, revision)
			if err != nil {
				return "", err
			}
			if len(settings) > 0 {
				policy = settings[0].Policy
			}
		}
	}
	if policy == model.ApprovalCustom {
		return "", fmt.Errorf("custom approval policy requires a policy contract, which this command did not provide: %w", model.ErrInvalid)
	}
	return policy, nil
}

func (s *Manager) RestartOperation(ctx context.Context, command RestartOperationCommand) (model.Operation, error) {
	if strings.TrimSpace(command.FromOperationID) == "" || strings.TrimSpace(command.OperationID) == "" ||
		command.FromOperationID == command.OperationID || command.CreatedAt.IsZero() {
		return model.Operation{}, fmt.Errorf("restart requires distinct source, target and time: %w", model.ErrInvalid)
	}
	previous, err := s.store.GetOperation(ctx, command.FromOperationID)
	if err != nil {
		return model.Operation{}, err
	}
	switch previous.State {
	case model.OperationFailed, model.OperationCancelled, model.OperationStale:
	default:
		return model.Operation{}, fmt.Errorf(
			"operation %q must be failed, cancelled or stale before restart, got %s: %w",
			previous.ID, previous.State, model.ErrStateConflict,
		)
	}
	runID := command.RunID
	if runID == "" {
		runID = previous.RunID
	}
	if previous.RunID != "" && runID != previous.RunID {
		return model.Operation{}, fmt.Errorf(
			"restart cannot move operation %q to another creation run: %w",
			previous.ID, model.ErrInvalid,
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
	spec, err := model.KindSpec(previous.Kind)
	if err != nil {
		return model.Operation{}, err
	}
	switch spec.Executor {
	case model.ExecutorLLM:
		// 沿用前任冻结的 Execution Profile 作为默认编译输入；资产引用按记录的
		// 来源恢复，显式传入的才替换。
		compiled, err := s.profiles.Load(ctx, previous.Snapshot.ConfigDigest)
		if err != nil {
			return model.Operation{}, err
		}
		successor.WorkerProfileID = prompt.WorkerID(compiled.WorkerProfile)
		successor.CoreProtocolVersion, successor.ModelConfigDigest = compiled.CoreProtocolVersion, compiled.ModelConfigDigest
		if s.executors.LLM != nil {
			successor.ModelConfigDigest = ""
		}
		successor.Packs, err = packRefsFromSources(compiled.Sources)
		if err != nil {
			return model.Operation{}, err
		}
		successor.CreatorProfiles, err = creatorProfileRefsFromSources(compiled.Sources)
		if err != nil {
			return model.Operation{}, err
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
	case model.ExecutorExternal:
		successor.ConfigDigest = previous.Snapshot.ConfigDigest
		if command.ConfigDigest != "" {
			successor.ConfigDigest = command.ConfigDigest
		}
	}
	if existing, err := s.store.GetOperation(ctx, command.OperationID); err == nil {
		if err := s.validateRestartTarget(ctx, existing, previous, successor); err != nil {
			return model.Operation{}, err
		}
		if err := s.store.CopyWorkspaceArtifacts(ctx, previous.ID, existing.ID, command.CreatedAt); err != nil {
			return model.Operation{}, err
		}
		return existing, nil
	} else if !errors.Is(err, model.ErrNotFound) {
		return model.Operation{}, err
	}
	restarted, err := s.prepareOperation(ctx, successor)
	if err != nil {
		return model.Operation{}, err
	}
	return s.store.CreateSuccessorOperation(ctx, previous.ID, restarted)
}

// validateRestartTarget 校验同 ID 的既有 Operation 就是本次重启请求（幂等）。
func (s *Manager) validateRestartTarget(
	ctx context.Context,
	existing, previous model.Operation,
	successor StartOperationCommand,
) error {
	if existing.State != model.OperationQueued || existing.Target != previous.Target || existing.Kind != previous.Kind ||
		existing.Priority != previous.Priority || !bytes.Equal(existing.Input, successor.Input) ||
		existing.Snapshot.ApprovalPolicy != successor.ApprovalPolicy ||
		existing.RunID != successor.RunID || !slices.Equal(existing.DependsOn, previous.DependsOn) {
		return fmt.Errorf("operation %q is not the requested restart target: %w", existing.ID, model.ErrIdempotencyConflict)
	}
	if successor.ConfigDigest != "" {
		if s.executors.External == nil || existing.Snapshot.Executor != s.executors.External.Identity() || existing.Snapshot.ConfigDigest != successor.ConfigDigest {
			return fmt.Errorf("operation %q is not the requested restart target: %w", existing.ID, model.ErrIdempotencyConflict)
		}
		return nil
	}
	// Recompile against the already frozen source revision, so an idempotent
	// retry checks all execution inputs without adopting newer story content.
	compiled, err := s.profiles.Compile(ctx, profile.CompileCommand{
		ProjectID: existing.Target.ID, Revision: existing.Snapshot.BaseRevision,
		Kind: successor.Kind, WorkerProfileID: successor.WorkerProfileID,
		Input: successor.Input, Packs: successor.Packs, CreatorProfiles: successor.CreatorProfiles,
		CoreProtocolVersion: successor.CoreProtocolVersion, ModelConfigDigest: successor.ModelConfigDigest,
		CreatedAt: existing.CreatedAt,
	})
	if err != nil {
		return err
	}
	if compiled.ProfileDigest != existing.Snapshot.ConfigDigest || prompt.ExecutorIdentity(compiled.ModelConfigDigest) != existing.Snapshot.Executor {
		return fmt.Errorf("operation %q uses different execution settings: %w", existing.ID, model.ErrIdempotencyConflict)
	}
	return nil
}

func packRefsFromSources(sources []prompt.Source) ([]resource.PackRef, error) {
	refs := make([]resource.PackRef, 0)
	for _, source := range sources {
		if source.Layer != "pack_defaults" {
			continue
		}
		separator := strings.LastIndex(source.ID, "@")
		if separator <= 0 || source.Revision <= model.InitialRevision {
			return nil, fmt.Errorf("pack source %q is invalid: %w", source.ID, model.ErrInvalid)
		}
		refs = append(refs, resource.PackRef{ID: source.ID[:separator], Revision: source.Revision})
	}
	return refs, nil
}

func creatorProfileRefsFromSources(sources []prompt.Source) ([]resource.CreatorProfileRef, error) {
	refs := make([]resource.CreatorProfileRef, 0)
	for _, source := range sources {
		if source.Layer != "creator_profile" {
			continue
		}
		separator := strings.LastIndex(source.ID, "/")
		if separator <= 0 || separator == len(source.ID)-1 || source.Revision <= model.InitialRevision {
			return nil, fmt.Errorf("creator profile source %q is invalid: %w", source.ID, model.ErrInvalid)
		}
		refs = append(refs, resource.CreatorProfileRef{
			ID: source.ID[:separator], Scope: source.ID[separator+1:], Revision: source.Revision,
		})
	}
	return refs, nil
}

func (s *Manager) RunNextOperation(
	ctx context.Context,
	workerID string,
	leaseDuration time.Duration,
	now time.Time,
) (operationengine.RunResult, error) {
	return s.operations.RunNextWithExecutors(ctx, s.executors.all(), workerID, leaseDuration, now)
}

func (s *Manager) RunOperation(
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
	return operationengine.RunResult{}, fmt.Errorf("executor %q is not configured: %w", operation.Snapshot.Executor, model.ErrInvalid)
}

func (s *Manager) PauseOperation(ctx context.Context, id string, at time.Time) (model.Operation, error) {
	operation, err := s.store.GetOperation(ctx, id)
	if err != nil {
		return model.Operation{}, err
	}
	if operation.State == model.OperationPaused {
		return operation, nil
	}
	return s.store.TransitionOperation(ctx, id, operation.State, model.OperationPaused, "paused by user", at)
}

func (s *Manager) ResumeOperation(ctx context.Context, id string, at time.Time) (model.Operation, error) {
	operation, err := s.store.GetOperation(ctx, id)
	if err != nil {
		return model.Operation{}, err
	}
	if operation.State == model.OperationQueued {
		return operation, nil
	}
	if operation.State != model.OperationPaused && operation.State != model.OperationFailed {
		return model.Operation{}, fmt.Errorf("operation %q cannot resume from %s: %w", id, operation.State, model.ErrStateConflict)
	}
	if operation.State == model.OperationFailed {
		proposal, err := s.store.GetProposalByOperation(ctx, operation.ID)
		if err == nil && proposal.ApprovalState == model.ApprovalRejected {
			return model.Operation{}, fmt.Errorf("operation %q has a rejected proposal; create an explicit restart instead: %w", id, model.ErrStateConflict)
		}
		if err != nil && !errors.Is(err, model.ErrNotFound) {
			return model.Operation{}, err
		}
	}
	return s.store.TransitionOperation(ctx, id, operation.State, model.OperationQueued, "resumed by user", at)
}

func (s *Manager) CancelOperation(ctx context.Context, id string, at time.Time) (model.Operation, error) {
	operation, err := s.store.GetOperation(ctx, id)
	if err != nil {
		return model.Operation{}, err
	}
	if operation.State == model.OperationCancelled {
		return operation, nil
	}
	return s.store.TransitionOperation(ctx, id, operation.State, model.OperationCancelled, "cancelled by user; workspace retained", at)
}

func (s *Manager) ReprioritizeOperation(ctx context.Context, id string, priority int, at time.Time) (model.Operation, error) {
	return s.store.SetOperationPriority(ctx, id, priority, at)
}

func (s *Manager) RecoverOperations(ctx context.Context, at time.Time) ([]string, error) {
	return s.store.RecoverExpiredOperations(ctx, at)
}

func (s *Manager) Operation(ctx context.Context, id string) (model.Operation, error) {
	return s.store.GetOperation(ctx, id)
}

func (s *Manager) OperationEvents(ctx context.Context, id string) ([]model.OperationEvent, error) {
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
func (s *Manager) OperationToolIssues(ctx context.Context, id string) ([]ToolIssue, error) {
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
