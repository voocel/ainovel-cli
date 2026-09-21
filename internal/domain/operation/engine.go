package operation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Executor 是执行器契约（D45）：Identity 是冻结进快照、领取时过滤的身份；
// Execute 按快照自行加载配置并产出统一的 OperationOutcome（D30）。
type Executor interface {
	Identity() string
	Execute(context.Context, model.Operation) (model.OperationOutcome, error)
}

// SemanticComplianceAnalyzer 由 capability 层实现：对 AI 正文做独立于创作
// Worker 的语义合规裁定。接口定义在消费方（Go 惯例），报告类型定义在 domain，
// 二者共同保证生产方无需反向依赖 operation。
type SemanticComplianceAnalyzer interface {
	AnalyzeSemanticCompliance(
		context.Context,
		model.Operation,
		model.Proposal,
		[]model.OwnershipRule,
	) (model.SemanticComplianceReport, error)
}

type Engine struct {
	store    Store
	changes  *change.Engine
	verdicts map[model.OperationKind]VerdictValidator
}

var errLeaseLost = errors.New("operation lease renewal failed")

type RunResult struct {
	Operation model.Operation  `json:"operation"`
	Proposal  model.Proposal   `json:"proposal,omitempty"`
	ChangeSet *model.ChangeSet `json:"change_set,omitempty"`
	Verdict   json.RawMessage  `json:"verdict,omitempty"`
	Artifacts []model.Artifact `json:"artifacts,omitempty"`
}

func NewEngine(authorityStore Store, changes *change.Engine, contracts ...VerdictContract) *Engine {
	e := &Engine{store: authorityStore, changes: changes, verdicts: make(map[model.OperationKind]VerdictValidator)}
	e.verdicts[model.OperationReviewRange] = e.validateReviewEvidence
	for _, contract := range contracts {
		if _, err := model.KindSpec(contract.Kind); err != nil {
			panic(err)
		}
		if _, exists := e.verdicts[contract.Kind]; exists || contract.Validate == nil {
			panic("invalid or duplicate evidence contract: " + string(contract.Kind))
		}
		e.verdicts[contract.Kind] = contract.Validate
	}
	return e
}

// RunNextWithExecutors 在所有已装配执行器之间按同一队列优先级原子领取，
// 再把任务交给其冻结身份对应的执行器。
func (e *Engine) RunNextWithExecutors(ctx context.Context, executors []Executor, workerID string, leaseDuration time.Duration, now time.Time) (RunResult, error) {
	byID := make(map[string]Executor, len(executors))
	identities := make([]string, 0, len(executors))
	for _, executor := range executors {
		if executor == nil || strings.TrimSpace(executor.Identity()) == "" {
			return RunResult{}, fmt.Errorf("executor identity is required: %w", model.ErrInvalid)
		}
		id := executor.Identity()
		if _, exists := byID[id]; exists {
			return RunResult{}, fmt.Errorf("duplicate executor identity %q: %w", id, model.ErrInvalid)
		}
		byID[id] = executor
		identities = append(identities, id)
	}
	if len(identities) == 0 {
		return RunResult{}, fmt.Errorf("operation executor is required: %w", model.ErrInvalid)
	}
	op, err := e.store.ClaimNextOperationForExecutors(ctx, workerID, identities, leaseDuration, now)
	if err != nil {
		return RunResult{}, err
	}
	return e.runClaimed(ctx, byID[op.Snapshot.Executor], op, workerID, leaseDuration, now)
}

func (e *Engine) RunNext(
	ctx context.Context,
	executor Executor,
	workerID string,
	leaseDuration time.Duration,
	now time.Time,
) (RunResult, error) {
	if executor == nil {
		return RunResult{}, fmt.Errorf("operation executor is required: %w", model.ErrInvalid)
	}
	operation, err := e.store.ClaimNextOperationForExecutor(ctx, workerID, executor.Identity(), leaseDuration, now)
	if err != nil {
		return RunResult{}, err
	}
	return e.runClaimed(ctx, executor, operation, workerID, leaseDuration, now)
}

func (e *Engine) Run(
	ctx context.Context,
	executor Executor,
	operationID, workerID string,
	leaseDuration time.Duration,
	now time.Time,
) (RunResult, error) {
	if executor == nil {
		return RunResult{}, fmt.Errorf("operation executor is required: %w", model.ErrInvalid)
	}
	operation, err := e.store.ClaimOperationForExecutor(ctx, operationID, workerID, executor.Identity(), leaseDuration, now)
	if err != nil {
		return RunResult{}, err
	}
	return e.runClaimed(ctx, executor, operation, workerID, leaseDuration, now)
}

func (e *Engine) runClaimed(
	ctx context.Context,
	executor Executor,
	operation model.Operation,
	workerID string,
	leaseDuration time.Duration,
	now time.Time,
) (RunResult, error) {
	storedProposal, err := e.store.GetProposalByOperation(ctx, operation.ID)
	if err == nil {
		return e.finalize(ctx, executor, workerID, leaseDuration, operation, storedProposal, now)
	}
	if !errors.Is(err, model.ErrNotFound) {
		return e.fail(ctx, operation, err, now)
	}
	// 裁定恢复：崩溃后重入时已落盘的 Verdict 不重跑模型，直接收尾。
	if stored, err := e.store.GetDerivedDocument(
		ctx, operation.Target.ID, operation.Snapshot.BaseRevision, model.DerivedVerdictKind, operation.ID,
	); err == nil {
		return e.finalizeVerdict(ctx, operation, stored.Content, now)
	} else if !errors.Is(err, model.ErrNotFound) {
		return e.fail(ctx, operation, err, now)
	}
	var outcome model.OperationOutcome
	err = e.withLease(ctx, operation, workerID, leaseDuration, func(callContext context.Context) error {
		var executeErr error
		outcome, executeErr = executor.Execute(callContext, operation)
		return executeErr
	})
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if err := outcome.Validate(); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	// 工件元数据先于提案落盘（D47）：附件校验才查得到；对象与元数据都幂等，
	// 重入不为仅工件的产出加捷径。
	for _, artifact := range outcome.Artifacts {
		required, err := model.OperationBasis(operation)
		if err != nil {
			return e.fail(ctx, operation, err, now)
		}
		if !artifact.Basis.Covers(required) {
			return e.fail(ctx, operation, fmt.Errorf("artifact %q omits task evidence: %w", artifact.ID, model.ErrInvalid), now)
		}
		if err := e.verifyBasis(ctx, operation, artifact.Basis); err != nil {
			return e.fail(ctx, operation, fmt.Errorf("artifact %q: %w", artifact.ID, err), now)
		}
	}
	if len(outcome.Artifacts) > 0 {
		if err := e.store.SaveExecutionArtifacts(ctx, outcome.Artifacts, operation.ID, operation.Attempt); err != nil {
			return e.fail(ctx, operation, err, now)
		}
	}
	if outcome.Proposal == nil && len(outcome.Verdict) == 0 {
		succeeded, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, model.OperationSucceeded, "", now)
		if err != nil {
			return RunResult{}, err
		}
		return RunResult{Operation: succeeded, Artifacts: outcome.Artifacts}, nil
	}
	if outcome.Proposal == nil {
		return e.finalizeVerdict(ctx, operation, outcome.Verdict, now)
	}
	proposal := *outcome.Proposal
	if proposal.OperationID != operation.ID {
		return e.fail(ctx, operation, fmt.Errorf(
			"proposal operation %q does not match %q: %w",
			proposal.OperationID, operation.ID, model.ErrInvalid,
		), now)
	}
	if proposal.Target != operation.Target || proposal.BaseRevision != operation.Snapshot.BaseRevision {
		return e.fail(ctx, operation, fmt.Errorf("proposal does not belong to the frozen operation target and revision: %w", model.ErrInvalid), now)
	}
	if err := proposal.Validate(); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	// 故事依赖由宿主写入（D40）：执行器声明的 depends_on 不作数。
	if proposal.Patches, err = model.BindChapterDependencies(proposal.Patches); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if len(proposal.Impact.Semantic) != 0 || len(proposal.Impact.Compliance) != 0 {
		return e.fail(ctx, operation, fmt.Errorf("executor cannot self-assert semantic compliance: %w", model.ErrInvalid), now)
	}
	if err := e.validatePlanTarget(ctx, operation, proposal); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	// 提案重定位（D51）：基线落后但中间只有用户专属变化且任务基线仍成立时搬到当前
	// Revision，让在途约束有机会参与合规分析；否则任务失效，后继继承工作区。
	if proposal, _, err = e.relocate(ctx, operation, proposal); errors.Is(err, model.ErrRevisionConflict) {
		result, err := e.stale(ctx, operation, err, now)
		result.Artifacts = outcome.Artifacts
		return result, err
	} else if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	constraints, err := e.changes.SemanticConstraints(ctx, operation.Snapshot.BaseRevision, proposal)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if shouldAnalyzeSemanticCompliance(operation, proposal, constraints) {
		proposal.Impact.Compliance, err = e.analyzeCompliance(ctx, executor, workerID, leaseDuration, operation, proposal, constraints)
		if err != nil {
			return e.fail(ctx, operation, err, now)
		}
	}
	prepared, err := e.changes.PrepareExecution(ctx, proposal, operation.Attempt)
	if errors.Is(err, model.ErrRevisionConflict) {
		result, err := e.stale(ctx, operation, err, now)
		result.Artifacts = outcome.Artifacts
		return result, err
	}
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	result, err := e.finalize(ctx, executor, workerID, leaseDuration, operation, prepared, now)
	result.Artifacts = outcome.Artifacts
	return result, err
}

// relocate 按任务基线重定位提案（D51）；不落库。
func (e *Engine) relocate(ctx context.Context, operation model.Operation, proposal model.Proposal) (model.Proposal, bool, error) {
	basis, err := model.OperationBasis(operation)
	if err != nil {
		return model.Proposal{}, false, err
	}
	return e.changes.Relocate(ctx, proposal, basis)
}

// stale 收尾为 stale（§5.5）：基线无法重定位，由后继继承工作区重做。
func (e *Engine) stale(ctx context.Context, operation model.Operation, cause error, now time.Time) (RunResult, error) {
	stale, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, model.OperationStale, cause.Error(), now)
	if err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	return RunResult{Operation: stale}, cause
}

// validatePlanTarget 把滚动规划请求的数量变成提交边界不变量：模型可以自由
// 设计层级和内容，但不能悄悄多产或少产章节节点。
func (e *Engine) validatePlanTarget(
	ctx context.Context,
	operation model.Operation,
	proposal model.Proposal,
) error {
	expected, ok, err := model.PlanChapterTargetForOperation(operation)
	if err != nil || !ok {
		return err
	}
	base, err := e.store.ListPlanNodes(ctx, operation.Target, operation.Snapshot.BaseRevision)
	if err != nil {
		return err
	}
	return model.ValidatePlanChapterTarget(base, proposal.Patches, expected)
}

// finalizeVerdict 收尾无 Proposal 的 Operation（D30）：裁定绑定启动快照的 Revision
// （§6.4），有效性按基线判定（D48）——基线在当前 Revision 不再成立即转 stale，
// 不相干的变化不作废裁定；落盘为派生文档后成功收尾。
func (e *Engine) finalizeVerdict(
	ctx context.Context,
	operation model.Operation,
	payload json.RawMessage,
	now time.Time,
) (RunResult, error) {
	basis, err := e.ValidateEvidence(ctx, operation, payload)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	currentRevision, err := e.store.CurrentRevision(ctx, operation.Target)
	if errors.Is(err, model.ErrNotFound) {
		currentRevision = model.InitialRevision
	} else if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if err := e.changes.VerifyBasis(ctx, operation.Target, basis, currentRevision); errors.Is(err, change.ErrBasisMismatch) {
		return e.stale(ctx, operation, fmt.Errorf("%w: %w", model.ErrRevisionConflict, err), now)
	} else if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if _, err := e.store.SaveExecutionDerivedDocument(ctx, model.DerivedDocument{
		ProjectID: operation.Target.ID, Revision: operation.Snapshot.BaseRevision,
		Kind: model.DerivedVerdictKind, Key: operation.ID,
		Content: payload, CreatedAt: now,
	}, operation.ID, operation.Attempt); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	succeeded, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, model.OperationSucceeded, "", now)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Operation: succeeded, Verdict: payload}, nil
}

func (e *Engine) withLease(
	ctx context.Context,
	operation model.Operation,
	workerID string,
	leaseDuration time.Duration,
	call func(context.Context) error,
) error {
	callContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	interval := leaseDuration / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-callContext.Done():
				return
			case <-ticker.C:
				_, err := e.store.RenewOperationLease(
					callContext, operation.ID, workerID, operation.Attempt, leaseDuration, time.Now().UTC(),
				)
				if err != nil {
					leaseErr := fmt.Errorf("%w: %v", errLeaseLost, err)
					heartbeatErr <- leaseErr
					cancel(leaseErr)
					return
				}
			}
		}
	}()
	callErr := call(callContext)
	close(stop)
	<-stopped
	select {
	case leaseErr := <-heartbeatErr:
		return errors.Join(callErr, leaseErr)
	default:
		return callErr
	}
}

func (e *Engine) finalize(
	ctx context.Context,
	executor Executor,
	workerID string,
	leaseDuration time.Duration,
	operation model.Operation,
	prepared model.Proposal,
	now time.Time,
) (RunResult, error) {
	result := RunResult{Operation: operation, Proposal: prepared}
	if prepared.ApprovalState == model.ApprovalApproved {
		committed, err := e.store.GetChangeSet(ctx, prepared.ID)
		if err != nil {
			return e.fail(ctx, operation, err, now)
		}
		succeeded, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, model.OperationSucceeded, "", now)
		if err != nil {
			return RunResult{}, err
		}
		result.Operation = succeeded
		result.ChangeSet = &committed
		return result, nil
	}
	if prepared.ApprovalState == model.ApprovalRejected {
		return e.fail(ctx, operation, fmt.Errorf("proposal %q was rejected", prepared.ID), now)
	}
	// 基线漂移按 D51 重定位：只有用户专属变化时搬到当前 Revision 继续裁决，否则 stale。
	relocated, moved, err := e.relocate(ctx, operation, prepared)
	if errors.Is(err, model.ErrRevisionConflict) {
		result, err := e.stale(ctx, operation, err, now)
		result.Proposal = prepared
		return result, err
	}
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if moved {
		if err := e.store.RelocateProposal(ctx, relocated, operation.Attempt, now); err != nil {
			return e.fail(ctx, operation, err, now)
		}
		prepared, result.Proposal = relocated, relocated
	}
	policy, err := e.changes.EffectiveApprovalPolicy(ctx, operation.Snapshot.ApprovalPolicy, prepared)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if policy == model.ApprovalCustom {
		return e.fail(ctx, operation, fmt.Errorf("custom approval policy requires an explicit policy contract: %w", model.ErrInvalid), now)
	}
	if policy == model.ApprovalManual || (policy == model.ApprovalMilestone && change.Milestone(prepared)) {
		awaiting, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, model.OperationAwaitingApproval,
			"proposal awaits user approval", now,
		)
		if err != nil {
			return RunResult{}, err
		}
		result.Operation = awaiting
		return result, nil
	}
	// 恢复或重定位不能把旧约束下的 pass 用在新约束上。需要时只重做独立检查，
	// 保留已经生成的候选内容；检查不可用仍沿既有规则等待用户裁决。
	constraints, err := e.changes.SemanticConstraints(ctx, operation.Snapshot.BaseRevision, prepared)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if len(constraints) > 0 {
		basis, err := change.ComplianceBasisDigest(prepared, constraints)
		if err != nil {
			return e.fail(ctx, operation, err, now)
		}
		var existing model.SemanticComplianceReport
		if len(prepared.Impact.Compliance) > 0 {
			if err := json.Unmarshal(prepared.Impact.Compliance, &existing); err != nil {
				return e.fail(ctx, operation, err, now)
			}
		}
		if existing.BasisDigest != basis {
			prepared.Impact.Compliance, err = e.analyzeCompliance(ctx, executor, workerID, leaseDuration, operation, prepared, constraints)
			if err != nil {
				return e.fail(ctx, operation, err, now)
			}
			if err := e.store.RelocateProposal(ctx, prepared, operation.Attempt, now); err != nil {
				return e.fail(ctx, operation, err, now)
			}
			result.Proposal = prepared
		}
	}
	if reason, err := change.ComplianceReason(prepared, constraints); err != nil {
		return e.fail(ctx, operation, err, now)
	} else if reason != "" {
		awaiting, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, model.OperationAwaitingApproval, reason, now)
		if err != nil {
			return RunResult{}, err
		}
		result.Operation = awaiting
		return result, nil
	}

	approved, err := change.Decide(
		prepared, model.ApprovalApproved,
		model.Author{Kind: model.AuthorSystem, ID: "approval-policy:" + string(policy)}, now,
	)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	committed, err := e.changes.CommitExecution(ctx, approved, operation.Attempt)
	if errors.Is(err, change.ErrUnauthorized) {
		awaiting, transitionErr := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, model.OperationAwaitingApproval,
			err.Error(), now,
		)
		if transitionErr != nil {
			return RunResult{}, errors.Join(err, transitionErr)
		}
		result.Operation = awaiting
		return result, nil
	}
	if errors.Is(err, model.ErrRevisionConflict) {
		// 重定位与提交之间又有提交：任务失效而非失败，后继继承工作区。
		result, err := e.stale(ctx, operation, err, now)
		result.Proposal = prepared
		return result, err
	}
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	succeeded, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, model.OperationSucceeded, "", now)
	if err != nil {
		return RunResult{}, err
	}
	result.Operation = succeeded
	result.ChangeSet = &committed
	return result, nil
}

func shouldAnalyzeSemanticCompliance(
	operation model.Operation,
	proposal model.Proposal,
	constraints []model.OwnershipRule,
) bool {
	if len(constraints) == 0 || operation.Snapshot.ApprovalPolicy == model.ApprovalManual ||
		operation.Snapshot.ApprovalPolicy == model.ApprovalCustom ||
		(operation.Snapshot.ApprovalPolicy == model.ApprovalMilestone && change.Milestone(proposal)) {
		return false
	}
	return true
}

func unavailableComplianceReport(
	constraints []model.OwnershipRule,
	explanation string,
) model.SemanticComplianceReport {
	report := model.SemanticComplianceReport{Status: model.SemanticComplianceUnavailable}
	for _, constraint := range constraints {
		report.Findings = append(report.Findings, model.SemanticComplianceFinding{
			Constraint: constraint.Target, Explanation: explanation,
		})
	}
	return report
}

// fail 收尾失败并按原因打码（D46）：结果未知不是普通失败，用户对账或重提前不会自动重试。
// fail 落盘执行失败。进程正在退出（ctx 已取消）时不是失败：任务放回队列、释放租约，
// 下次进入接着对话与工作区续跑，不消耗失败预算。
func (e *Engine) fail(ctx context.Context, operation model.Operation, cause error, now time.Time) (RunResult, error) {
	if ctx.Err() != nil {
		return e.release(ctx, operation, cause)
	}
	failed, err := e.store.FailOperation(ctx, operation.ID, operation.Attempt, model.FailureCodeFor(cause), cause.Error(), now)
	if err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	return RunResult{Operation: failed}, cause
}

// releaseTimeout 是释放写入的上限：取消后执行器已停手，释放只是一次本地写入。
const releaseTimeout = 5 * time.Second

// releasedMessage 记在任务上，诊断与续跑提示都能看到中断原因。
const releasedMessage = "进程退出，任务已放回队列等待续跑"

// release 用脱离取消的短上下文把任务 running → queued（attempt 围栏内），旧执行的
// 对话与工作区保留；释放失败就交给租约到期回收。
func (e *Engine) release(ctx context.Context, operation model.Operation, cause error) (RunResult, error) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	released, err := e.store.ConcludeOperation(detached, operation.ID, operation.Attempt, model.OperationQueued, releasedMessage, time.Now().UTC())
	if err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	return RunResult{Operation: released}, cause
}

// verifyBasis 核对证据基线在启动快照上属实（D48）：文档 revision、要求作用域摘要与
// 工件摘要都必须与 BaseRevision 上的状态一致，不实的基线是执行器的无效产出。
func (e *Engine) verifyBasis(ctx context.Context, operation model.Operation, basis model.EvidenceBasis) error {
	err := e.changes.VerifyBasis(ctx, operation.Target, basis, operation.Snapshot.BaseRevision)
	if errors.Is(err, change.ErrBasisMismatch) {
		return fmt.Errorf("%w: %w", model.ErrInvalid, err)
	}
	return err
}

// analyzeCompliance 只允许宿主声明报告的适用范围；分析失败沿既有合规规则显式等待裁决。
func (e *Engine) analyzeCompliance(ctx context.Context, executor Executor, workerID string, leaseDuration time.Duration, operation model.Operation, proposal model.Proposal, constraints []model.OwnershipRule) (json.RawMessage, error) {
	report := unavailableComplianceReport(constraints, "configured executor does not provide independent semantic compliance analysis")
	if analyzer, ok := executor.(SemanticComplianceAnalyzer); ok {
		err := e.withLease(ctx, operation, workerID, leaseDuration, func(callContext context.Context) error {
			var analyzeErr error
			report, analyzeErr = analyzer.AnalyzeSemanticCompliance(callContext, operation, proposal, constraints)
			return analyzeErr
		})
		if errors.Is(err, errLeaseLost) {
			return nil, err
		}
		if err != nil {
			report = unavailableComplianceReport(constraints, err.Error())
		} else if err := report.Validate(); err != nil {
			return nil, err
		}
	}
	basis, err := change.ComplianceBasisDigest(proposal, constraints)
	if err != nil {
		return nil, err
	}
	report.BasisDigest = basis
	return json.Marshal(report)
}
