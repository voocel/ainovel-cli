package operation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Executor 是执行器契约（D45）：Identity 是冻结进快照、领取时过滤的身份；
// Execute 按快照自行加载配置并产出统一的 OperationOutcome（D30）。
type Executor interface {
	Identity() string
	Execute(context.Context, domain.Operation) (domain.OperationOutcome, error)
}

// SemanticComplianceAnalyzer 由 capability 层实现：对 AI 正文做独立于创作
// Worker 的语义合规裁定。接口定义在消费方（Go 惯例），报告类型定义在 domain，
// 二者共同保证生产方无需反向依赖 operation。
type SemanticComplianceAnalyzer interface {
	AnalyzeSemanticCompliance(
		context.Context,
		domain.Operation,
		domain.Proposal,
		[]domain.OwnershipRule,
	) (domain.SemanticComplianceReport, error)
}

type Engine struct {
	store    *store.Store
	changes  *change.Engine
	verdicts map[domain.OperationKind]VerdictValidator
}

var errLeaseLost = errors.New("operation lease renewal failed")

type RunResult struct {
	Operation domain.Operation  `json:"operation"`
	Proposal  domain.Proposal   `json:"proposal,omitempty"`
	ChangeSet *domain.ChangeSet `json:"change_set,omitempty"`
	Verdict   json.RawMessage   `json:"verdict,omitempty"`
	Artifacts []domain.Artifact `json:"artifacts,omitempty"`
}

func NewEngine(authorityStore *store.Store, contracts ...VerdictContract) *Engine {
	e := &Engine{store: authorityStore, changes: change.New(authorityStore), verdicts: make(map[domain.OperationKind]VerdictValidator)}
	e.verdicts[domain.OperationReviewRange] = e.validateReviewEvidence
	for _, contract := range contracts {
		if _, err := domain.KindSpec(contract.Kind); err != nil {
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
			return RunResult{}, fmt.Errorf("executor identity is required: %w", domain.ErrInvalid)
		}
		id := executor.Identity()
		if _, exists := byID[id]; exists {
			return RunResult{}, fmt.Errorf("duplicate executor identity %q: %w", id, domain.ErrInvalid)
		}
		byID[id] = executor
		identities = append(identities, id)
	}
	if len(identities) == 0 {
		return RunResult{}, fmt.Errorf("operation executor is required: %w", domain.ErrInvalid)
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
		return RunResult{}, fmt.Errorf("operation executor is required: %w", domain.ErrInvalid)
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
		return RunResult{}, fmt.Errorf("operation executor is required: %w", domain.ErrInvalid)
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
	operation domain.Operation,
	workerID string,
	leaseDuration time.Duration,
	now time.Time,
) (RunResult, error) {
	storedProposal, err := e.store.GetProposalByOperation(ctx, operation.ID)
	if err == nil {
		return e.finalize(ctx, executor, workerID, leaseDuration, operation, storedProposal, now)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return e.fail(ctx, operation, err, now)
	}
	// 裁定恢复：崩溃后重入时已落盘的 Verdict 不重跑模型，直接收尾。
	if stored, err := e.store.GetDerivedDocument(
		ctx, operation.Target.ID, operation.Snapshot.BaseRevision, domain.DerivedVerdictKind, operation.ID,
	); err == nil {
		return e.finalizeVerdict(ctx, operation, stored.Content, now)
	} else if !errors.Is(err, store.ErrNotFound) {
		return e.fail(ctx, operation, err, now)
	}
	var outcome domain.OperationOutcome
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
		required, err := domain.OperationBasis(operation)
		if err != nil {
			return e.fail(ctx, operation, err, now)
		}
		if !artifact.Basis.Covers(required) {
			return e.fail(ctx, operation, fmt.Errorf("artifact %q omits task evidence: %w", artifact.ID, domain.ErrInvalid), now)
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
		succeeded, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, domain.OperationSucceeded, "", now)
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
			proposal.OperationID, operation.ID, domain.ErrInvalid,
		), now)
	}
	if proposal.Target != operation.Target || proposal.BaseRevision != operation.Snapshot.BaseRevision {
		return e.fail(ctx, operation, fmt.Errorf("proposal does not belong to the frozen operation target and revision: %w", domain.ErrInvalid), now)
	}
	if err := proposal.Validate(); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	// 故事依赖由宿主写入（D40）：执行器声明的 depends_on 不作数。
	if proposal.Patches, err = domain.BindChapterDependencies(proposal.Patches); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if len(proposal.Impact.Semantic) != 0 || len(proposal.Impact.Compliance) != 0 {
		return e.fail(ctx, operation, fmt.Errorf("executor cannot self-assert semantic compliance: %w", domain.ErrInvalid), now)
	}
	if err := e.validatePlanTarget(ctx, operation, proposal); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	// 提案重定位（D51）：基线落后但中间只有用户专属变化且任务基线仍成立时搬到当前
	// Revision，让在途约束有机会参与合规分析；否则任务失效，后继继承工作区。
	if proposal, _, err = e.relocate(ctx, operation, proposal); errors.Is(err, store.ErrRevisionConflict) {
		result, err := e.stale(ctx, operation, err, now)
		result.Artifacts = outcome.Artifacts
		return result, err
	} else if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	constraints, err := e.semanticConstraints(ctx, operation, proposal)
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
	if errors.Is(err, store.ErrRevisionConflict) {
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
func (e *Engine) relocate(ctx context.Context, operation domain.Operation, proposal domain.Proposal) (domain.Proposal, bool, error) {
	basis, err := domain.OperationBasis(operation)
	if err != nil {
		return domain.Proposal{}, false, err
	}
	return e.changes.Relocate(ctx, proposal, basis)
}

// stale 收尾为 stale（§5.5）：基线无法重定位，由后继继承工作区重做。
func (e *Engine) stale(ctx context.Context, operation domain.Operation, cause error, now time.Time) (RunResult, error) {
	stale, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, domain.OperationStale, cause.Error(), now)
	if err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	return RunResult{Operation: stale}, cause
}

// validatePlanTarget 把滚动规划请求的数量变成提交边界不变量：模型可以自由
// 设计层级和内容，但不能悄悄多产或少产章节节点。
func (e *Engine) validatePlanTarget(
	ctx context.Context,
	operation domain.Operation,
	proposal domain.Proposal,
) error {
	expected, ok, err := domain.PlanChapterTargetForOperation(operation)
	if err != nil || !ok {
		return err
	}
	base, err := e.store.ListPlanNodes(ctx, operation.Target, operation.Snapshot.BaseRevision)
	if err != nil {
		return err
	}
	return domain.ValidatePlanChapterTarget(base, proposal.Patches, expected)
}

// finalizeVerdict 收尾无 Proposal 的 Operation（D30）：裁定绑定启动快照的 Revision
// （§6.4），有效性按基线判定（D48）——基线在当前 Revision 不再成立即转 stale，
// 不相干的变化不作废裁定；落盘为派生文档后成功收尾。
func (e *Engine) finalizeVerdict(
	ctx context.Context,
	operation domain.Operation,
	payload json.RawMessage,
	now time.Time,
) (RunResult, error) {
	basis, err := e.ValidateEvidence(ctx, operation, payload)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	currentRevision, err := e.store.CurrentRevision(ctx, operation.Target)
	if errors.Is(err, store.ErrNotFound) {
		currentRevision = domain.InitialRevision
	} else if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if err := e.changes.VerifyBasis(ctx, operation.Target, basis, currentRevision); errors.Is(err, change.ErrBasisMismatch) {
		return e.stale(ctx, operation, fmt.Errorf("%w: %w", store.ErrRevisionConflict, err), now)
	} else if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if _, err := e.store.SaveExecutionDerivedDocument(ctx, domain.DerivedDocument{
		ProjectID: operation.Target.ID, Revision: operation.Snapshot.BaseRevision,
		Kind: domain.DerivedVerdictKind, Key: operation.ID,
		Content: payload, CreatedAt: now,
	}, operation.ID, operation.Attempt); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	succeeded, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, domain.OperationSucceeded, "", now)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Operation: succeeded, Verdict: payload}, nil
}

func (e *Engine) withLease(
	ctx context.Context,
	operation domain.Operation,
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
					callContext, operation.ID, workerID, leaseDuration, time.Now().UTC(),
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
	operation domain.Operation,
	prepared domain.Proposal,
	now time.Time,
) (RunResult, error) {
	result := RunResult{Operation: operation, Proposal: prepared}
	if prepared.ApprovalState == domain.ApprovalApproved {
		committed, err := e.store.GetChangeSet(ctx, prepared.ID)
		if err != nil {
			return e.fail(ctx, operation, err, now)
		}
		succeeded, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, domain.OperationSucceeded, "", now)
		if err != nil {
			return RunResult{}, err
		}
		result.Operation = succeeded
		result.ChangeSet = &committed
		return result, nil
	}
	if prepared.ApprovalState == domain.ApprovalRejected {
		return e.fail(ctx, operation, fmt.Errorf("proposal %q was rejected", prepared.ID), now)
	}
	// 基线漂移按 D51 重定位：只有用户专属变化时搬到当前 Revision 继续裁决，否则 stale。
	relocated, moved, err := e.relocate(ctx, operation, prepared)
	if errors.Is(err, store.ErrRevisionConflict) {
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
	policy, err := e.effectiveApprovalPolicy(ctx, operation, prepared.BaseRevision)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if policy == domain.ApprovalCustom {
		return e.fail(ctx, operation, fmt.Errorf("custom approval policy requires an explicit policy contract: %w", domain.ErrInvalid), now)
	}
	if policy == domain.ApprovalManual || (policy == domain.ApprovalMilestone && milestoneProposal(prepared)) {
		awaiting, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, domain.OperationAwaitingApproval,
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
	constraints, err := e.semanticConstraints(ctx, operation, prepared)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if len(constraints) > 0 {
		basis, err := complianceBasisDigest(prepared, constraints)
		if err != nil {
			return e.fail(ctx, operation, err, now)
		}
		var existing domain.SemanticComplianceReport
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
	if reason, err := e.semanticApprovalReason(ctx, operation, prepared); err != nil {
		return e.fail(ctx, operation, err, now)
	} else if reason != "" {
		awaiting, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, domain.OperationAwaitingApproval, reason, now)
		if err != nil {
			return RunResult{}, err
		}
		result.Operation = awaiting
		return result, nil
	}

	approved, err := change.Decide(
		prepared, domain.ApprovalApproved,
		domain.Author{Kind: domain.AuthorSystem, ID: "approval-policy:" + string(policy)}, now,
	)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	committed, err := e.changes.CommitExecution(ctx, approved, operation.Attempt)
	if errors.Is(err, change.ErrUnauthorized) {
		awaiting, transitionErr := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, domain.OperationAwaitingApproval,
			err.Error(), now,
		)
		if transitionErr != nil {
			return RunResult{}, errors.Join(err, transitionErr)
		}
		result.Operation = awaiting
		return result, nil
	}
	if errors.Is(err, store.ErrRevisionConflict) {
		// 重定位与提交之间又有提交：任务失效而非失败，后继继承工作区。
		result, err := e.stale(ctx, operation, err, now)
		result.Proposal = prepared
		return result, err
	}
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	succeeded, err := e.store.ConcludeOperation(ctx, operation.ID, operation.Attempt, domain.OperationSucceeded, "", now)
	if err != nil {
		return RunResult{}, err
	}
	result.Operation = succeeded
	result.ChangeSet = &committed
	return result, nil
}

func shouldAnalyzeSemanticCompliance(
	operation domain.Operation,
	proposal domain.Proposal,
	constraints []domain.OwnershipRule,
) bool {
	if len(constraints) == 0 || operation.Snapshot.ApprovalPolicy == domain.ApprovalManual ||
		operation.Snapshot.ApprovalPolicy == domain.ApprovalCustom ||
		(operation.Snapshot.ApprovalPolicy == domain.ApprovalMilestone && milestoneProposal(proposal)) {
		return false
	}
	return true
}

// effectiveApprovalPolicy 取启动快照与最新已批准 Revision 上的权威审批策略中
// 更严格的一方（D23）：收紧立即生效来自权威文档，放松不追溯来自快照下限。
func (e *Engine) effectiveApprovalPolicy(
	ctx context.Context,
	operation domain.Operation,
	revision domain.Revision,
) (domain.ApprovalPolicy, error) {
	policy := operation.Snapshot.ApprovalPolicy
	if operation.Target.Kind != domain.AuthorityProject || revision == domain.InitialRevision {
		return policy, nil
	}
	document, err := e.store.GetDocument(ctx, operation.Target,
		domain.DocumentRef{Kind: domain.DocumentApproval, ID: "root"}, revision)
	if errors.Is(err, store.ErrNotFound) {
		return policy, nil
	}
	if err != nil {
		return "", err
	}
	var setting domain.ApprovalSetting
	if err := json.Unmarshal(document.Content, &setting); err != nil {
		return "", fmt.Errorf("decode approval setting: %w", err)
	}
	return domain.StricterApproval(policy, setting.Policy), nil
}

func (e *Engine) semanticConstraints(
	ctx context.Context,
	operation domain.Operation,
	proposal domain.Proposal,
) ([]domain.OwnershipRule, error) {
	hasManuscript := false
	for _, patch := range proposal.Patches {
		if patch.Document.Kind == domain.DocumentManuscript {
			hasManuscript = true
			break
		}
	}
	if !hasManuscript || operation.Target.Kind != domain.AuthorityProject {
		return nil, nil
	}
	// D23：约束取“启动快照与提案基线（提交时的最新已批准 Revision）中更严格
	// 的一方”。收紧立即生效来自提案基线，放松不追溯来自启动快照。
	snapshotRules, err := e.ownershipRules(ctx, operation.Target, operation.Snapshot.BaseRevision)
	if err != nil {
		return nil, err
	}
	constraints := snapshotRules
	if proposal.BaseRevision != operation.Snapshot.BaseRevision {
		latestRules, err := e.ownershipRules(ctx, operation.Target, proposal.BaseRevision)
		if err != nil {
			return nil, err
		}
		constraints = mergeStricterRules(snapshotRules, latestRules)
	}
	filtered := constraints[:0]
	for _, rule := range constraints {
		if rule.Control == domain.ControlLocked || rule.Control == domain.ControlGuided {
			filtered = append(filtered, rule)
		}
	}
	slices.SortFunc(filtered, func(left, right domain.OwnershipRule) int {
		if compared := strings.Compare(left.Target.Key(), right.Target.Key()); compared != 0 {
			return compared
		}
		return strings.Compare(strings.Join(left.Guidance, "\n"), strings.Join(right.Guidance, "\n"))
	})
	return filtered, nil
}

func (e *Engine) ownershipRules(
	ctx context.Context,
	target domain.AuthorityTarget,
	revision domain.Revision,
) ([]domain.OwnershipRule, error) {
	if revision == domain.InitialRevision {
		return nil, nil
	}
	documents, err := e.store.ListDocuments(ctx, target, domain.DocumentOwnership, revision)
	if err != nil {
		return nil, err
	}
	rules := make([]domain.OwnershipRule, 0, len(documents))
	for _, document := range documents {
		var rule domain.OwnershipRule
		if err := json.Unmarshal(document.Content, &rule); err != nil {
			return nil, fmt.Errorf("decode ownership constraint %q: %w", document.Document.ID, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func controlRank(level domain.ControlLevel) int {
	switch level {
	case domain.ControlLocked:
		return 2
	case domain.ControlGuided:
		return 1
	default:
		return 0
	}
}

// mergeStricterRules 对同一 target 取控制级别更严格的一方；两侧同为 guided 且
// guidance 不同时二者都保留——在途任务必须同时通过新旧约束校验（基线 §5.5）。
func mergeStricterRules(snapshot, latest []domain.OwnershipRule) []domain.OwnershipRule {
	byTarget := make(map[string][]domain.OwnershipRule)
	for _, rule := range snapshot {
		byTarget[rule.Target.Key()] = append(byTarget[rule.Target.Key()], rule)
	}
	for _, rule := range latest {
		key := rule.Target.Key()
		existing := byTarget[key]
		if len(existing) == 0 {
			byTarget[key] = []domain.OwnershipRule{rule}
			continue
		}
		strongest := existing[0]
		switch {
		case controlRank(rule.Control) > controlRank(strongest.Control):
			byTarget[key] = []domain.OwnershipRule{rule}
		case controlRank(rule.Control) < controlRank(strongest.Control):
		case rule.Control == domain.ControlGuided && !slices.Equal(rule.Guidance, strongest.Guidance):
			byTarget[key] = append(existing, rule)
		}
	}
	merged := make([]domain.OwnershipRule, 0, len(byTarget))
	for _, rules := range byTarget {
		merged = append(merged, rules...)
	}
	return merged
}

func unavailableComplianceReport(
	constraints []domain.OwnershipRule,
	explanation string,
) domain.SemanticComplianceReport {
	report := domain.SemanticComplianceReport{Status: domain.SemanticComplianceUnavailable}
	for _, constraint := range constraints {
		report.Findings = append(report.Findings, domain.SemanticComplianceFinding{
			Constraint: constraint.Target, Explanation: explanation,
		})
	}
	return report
}

func (e *Engine) semanticApprovalReason(
	ctx context.Context,
	operation domain.Operation,
	proposal domain.Proposal,
) (string, error) {
	constraints, err := e.semanticConstraints(ctx, operation, proposal)
	if err != nil || len(constraints) == 0 {
		return "", err
	}
	if len(proposal.Impact.Compliance) == 0 {
		return "semantic compliance evidence is required for locked or guided story constraints", nil
	}
	var report domain.SemanticComplianceReport
	if err := json.Unmarshal(proposal.Impact.Compliance, &report); err != nil {
		return "", fmt.Errorf("decode semantic compliance report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return "", err
	}
	basis, err := complianceBasisDigest(proposal, constraints)
	if err != nil {
		return "", err
	}
	if report.BasisDigest != basis {
		return "semantic compliance evidence does not cover the candidate and current constraints", nil
	}
	if report.Status != domain.SemanticCompliancePass {
		return fmt.Sprintf("semantic compliance is %s; user approval is required", report.Status), nil
	}
	return "", nil
}

func complianceBasisDigest(proposal domain.Proposal, constraints []domain.OwnershipRule) (string, error) {
	payload, err := json.Marshal(struct {
		Patches     []domain.Patch         `json:"patches"`
		Constraints []domain.OwnershipRule `json:"constraints"`
	}{proposal.Patches, constraints})
	if err != nil {
		return "", fmt.Errorf("encode compliance basis: %w", err)
	}
	return domain.Digest(payload), nil
}

// milestoneProposal 判定变化的重大性。例行推进——章节正文与随章提交的 Canon
// Delta——不构成 milestone，否则 milestone 塌缩为 manual、中间档消失（基线 §5.4）。
func milestoneProposal(proposal domain.Proposal) bool {
	chapters := make(map[string]struct{})
	for _, patch := range proposal.Patches {
		if patch.Document.Kind == domain.DocumentManuscript && patch.Operation == domain.PatchPut {
			chapters[patch.Document.ID] = struct{}{}
		}
	}
	for _, patch := range proposal.Patches {
		switch patch.Document.Kind {
		case domain.DocumentOwnership, domain.DocumentIntent:
			return true
		case domain.DocumentCanon:
			if patch.Operation == domain.PatchDelete {
				return true
			}
			var fact domain.CanonFact
			if json.Unmarshal(patch.Content, &fact) != nil {
				return true
			}
			if _, routine := chapters[fact.SourceChapterID]; !routine {
				return true
			}
		case domain.DocumentPlan:
			if patch.Operation == domain.PatchDelete {
				return true
			}
			var node domain.PlanNode
			if json.Unmarshal(patch.Content, &node) == nil && (node.Kind == domain.PlanVolume || node.Kind == domain.PlanArc) {
				return true
			}
		}
	}
	return false
}

// fail 收尾失败并按原因打码（D46）：结果未知不是普通失败，用户对账或重提前不会自动重试。
func (e *Engine) fail(ctx context.Context, operation domain.Operation, cause error, now time.Time) (RunResult, error) {
	failed, err := e.store.FailOperation(ctx, operation.ID, operation.Attempt, domain.FailureCodeFor(cause), cause.Error(), now)
	if err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	return RunResult{Operation: failed}, cause
}

// verifyBasis 核对证据基线在启动快照上属实（D48）：文档 revision、要求作用域摘要与
// 工件摘要都必须与 BaseRevision 上的状态一致，不实的基线是执行器的无效产出。
func (e *Engine) verifyBasis(ctx context.Context, operation domain.Operation, basis domain.EvidenceBasis) error {
	err := e.changes.VerifyBasis(ctx, operation.Target, basis, operation.Snapshot.BaseRevision)
	if errors.Is(err, change.ErrBasisMismatch) {
		return fmt.Errorf("%w: %w", domain.ErrInvalid, err)
	}
	return err
}

// analyzeCompliance 只允许宿主声明报告的适用范围；分析失败沿既有合规规则显式等待裁决。
func (e *Engine) analyzeCompliance(ctx context.Context, executor Executor, workerID string, leaseDuration time.Duration, operation domain.Operation, proposal domain.Proposal, constraints []domain.OwnershipRule) (json.RawMessage, error) {
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
	basis, err := complianceBasisDigest(proposal, constraints)
	if err != nil {
		return nil, err
	}
	report.BasisDigest = basis
	return json.Marshal(report)
}
