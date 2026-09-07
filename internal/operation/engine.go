package operation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type Executor interface {
	ModelConfigDigest() string
	// Execute 产出统一的 OperationOutcome（D30）：需要改变 Authority 时携带
	// Proposal，审阅/校验类携带 Verdict；审阅通过不制造空 Patch。
	Execute(context.Context, domain.Operation, prompt.Compiled) (domain.OperationOutcome, error)
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
	store   *store.Store
	changes *change.Engine
	prompts *prompt.Registry
}

var errLeaseLost = errors.New("operation lease renewal failed")

type RunResult struct {
	Operation domain.Operation  `json:"operation"`
	Proposal  domain.Proposal   `json:"proposal,omitempty"`
	ChangeSet *domain.ChangeSet `json:"change_set,omitempty"`
	Verdict   json.RawMessage   `json:"verdict,omitempty"`
}

func NewEngine(authorityStore *store.Store) *Engine {
	return &Engine{
		store: authorityStore, changes: change.New(authorityStore),
		prompts: prompt.NewRegistry(authorityStore),
	}
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
	operation, err := e.store.ClaimNextOperationForModel(ctx, workerID, executor.ModelConfigDigest(), leaseDuration, now)
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
	operation, err := e.store.ClaimOperationForModel(
		ctx, operationID, workerID, executor.ModelConfigDigest(), leaseDuration, now,
	)
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
		return e.finalize(ctx, operation, storedProposal, now)
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
	compiled, err := e.prompts.Load(ctx, operation.Snapshot.ExecutionProfileDigest)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	var outcome domain.OperationOutcome
	err = e.withLease(ctx, operation, workerID, leaseDuration, func(callContext context.Context) error {
		var executeErr error
		outcome, executeErr = executor.Execute(callContext, operation, compiled)
		return executeErr
	})
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if err := outcome.Validate(); err != nil {
		return e.fail(ctx, operation, err, now)
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
	if err := proposal.Validate(); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if len(proposal.Impact.Semantic) != 0 || len(proposal.Impact.Compliance) != 0 {
		return e.fail(ctx, operation, fmt.Errorf("executor cannot self-assert semantic compliance: %w", domain.ErrInvalid), now)
	}
	if err := e.validatePlanTarget(ctx, operation, proposal); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	constraints, err := e.semanticConstraints(ctx, operation, proposal)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if shouldAnalyzeSemanticCompliance(operation, proposal, constraints) {
		report := domain.SemanticComplianceReport{}
		analyzer, ok := executor.(SemanticComplianceAnalyzer)
		if !ok {
			report = unavailableComplianceReport(constraints, "configured executor does not provide independent semantic compliance analysis")
		} else {
			err = e.withLease(ctx, operation, workerID, leaseDuration, func(callContext context.Context) error {
				var analyzeErr error
				report, analyzeErr = analyzer.AnalyzeSemanticCompliance(callContext, operation, proposal, constraints)
				return analyzeErr
			})
			if errors.Is(err, errLeaseLost) {
				return e.fail(ctx, operation, err, now)
			}
			if err != nil {
				report = unavailableComplianceReport(constraints, err.Error())
			} else if err := report.Validate(); err != nil {
				return e.fail(ctx, operation, err, now)
			}
		}
		compliance, err := json.Marshal(report)
		if err != nil {
			return e.fail(ctx, operation, fmt.Errorf("encode semantic compliance report: %w", err), now)
		}
		proposal.Impact.Compliance = compliance
	}
	prepared, err := e.changes.Prepare(ctx, proposal)
	if err != nil {
		if errors.Is(err, store.ErrRevisionConflict) {
			stale, transitionErr := e.store.TransitionOperation(
				ctx, operation.ID, domain.OperationRunning, domain.OperationStale, err.Error(), now,
			)
			if transitionErr != nil {
				return RunResult{}, errors.Join(err, transitionErr)
			}
			return RunResult{Operation: stale}, err
		}
		return e.fail(ctx, operation, err, now)
	}
	return e.finalize(ctx, operation, prepared, now)
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

// finalizeVerdict 收尾无 Proposal 的 Operation（D30）：裁定绑定启动快照的
// Revision（§6.4），基线漂移即失效转 stale；落盘为派生文档后成功收尾。
func (e *Engine) finalizeVerdict(
	ctx context.Context,
	operation domain.Operation,
	verdict json.RawMessage,
	now time.Time,
) (RunResult, error) {
	if err := e.validateReviewVerdict(ctx, operation, verdict); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	currentRevision, err := e.store.CurrentRevision(ctx, operation.Target)
	if errors.Is(err, store.ErrNotFound) {
		currentRevision = domain.InitialRevision
	} else if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if currentRevision != operation.Snapshot.BaseRevision {
		cause := fmt.Errorf(
			"verdict base revision %d, current revision %d: %w",
			operation.Snapshot.BaseRevision, currentRevision, store.ErrRevisionConflict,
		)
		stale, transitionErr := e.store.TransitionOperation(
			ctx, operation.ID, domain.OperationRunning, domain.OperationStale, cause.Error(), now,
		)
		if transitionErr != nil {
			return RunResult{}, errors.Join(cause, transitionErr)
		}
		return RunResult{Operation: stale}, cause
	}
	if _, err := e.store.SaveDerivedDocument(ctx, domain.DerivedDocument{
		ProjectID: operation.Target.ID, Revision: operation.Snapshot.BaseRevision,
		Kind: domain.DerivedVerdictKind, Key: operation.ID,
		Content: verdict, CreatedAt: now,
	}); err != nil {
		return e.fail(ctx, operation, err, now)
	}
	succeeded, err := e.store.TransitionOperation(
		ctx, operation.ID, domain.OperationRunning, domain.OperationSucceeded, "", now,
	)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Operation: succeeded, Verdict: verdict}, nil
}

func (e *Engine) validateReviewVerdict(
	ctx context.Context,
	operation domain.Operation,
	payload json.RawMessage,
) error {
	if operation.Kind != domain.OperationReviewRange {
		return fmt.Errorf("%s operation cannot produce a verdict: %w", operation.Kind, domain.ErrInvalid)
	}
	var verdict domain.ReviewVerdict
	if err := domain.DecodeStrict(payload, &verdict); err != nil {
		return fmt.Errorf("decode review verdict: %w", err)
	}
	if err := domain.ValidateReviewVerdictForOperation(operation, verdict); err != nil {
		return err
	}
	artifact, err := e.store.GetWorkspaceArtifact(ctx, operation.ID, verdict.ReviewKey)
	if err != nil {
		return fmt.Errorf("read review artifact %q: %w", verdict.ReviewKey, err)
	}
	if artifact.MediaType != domain.ReviewArtifactMediaType {
		return fmt.Errorf("workspace artifact %q is not a review record: %w", verdict.ReviewKey, domain.ErrInvalid)
	}
	var findings []domain.ReviewFinding
	if err := domain.DecodeStrict(artifact.Content, &findings); err != nil {
		return fmt.Errorf("decode review artifact %q: %w", verdict.ReviewKey, err)
	}
	if findings == nil {
		return fmt.Errorf("review artifact %q must contain a findings array: %w", verdict.ReviewKey, domain.ErrInvalid)
	}
	if !slices.Equal(findings, verdict.Findings) {
		return fmt.Errorf("verdict findings do not match review artifact %q: %w", verdict.ReviewKey, domain.ErrInvalid)
	}
	return nil
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
		succeeded, err := e.store.TransitionOperation(
			ctx, operation.ID, domain.OperationRunning, domain.OperationSucceeded, "", now,
		)
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
	currentRevision, err := e.store.CurrentRevision(ctx, operation.Target)
	if errors.Is(err, store.ErrNotFound) {
		currentRevision = domain.InitialRevision
	} else if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if currentRevision != prepared.BaseRevision {
		cause := fmt.Errorf(
			"proposal base revision %d, current revision %d: %w",
			prepared.BaseRevision, currentRevision, store.ErrRevisionConflict,
		)
		stale, transitionErr := e.store.TransitionOperation(
			ctx, operation.ID, domain.OperationRunning, domain.OperationStale, cause.Error(), now,
		)
		if transitionErr != nil {
			return RunResult{}, errors.Join(cause, transitionErr)
		}
		return RunResult{Operation: stale, Proposal: prepared}, cause
	}
	policy, err := e.effectiveApprovalPolicy(ctx, operation, prepared.BaseRevision)
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	if policy == domain.ApprovalCustom {
		return e.fail(ctx, operation, fmt.Errorf("custom approval policy requires an explicit policy contract: %w", domain.ErrInvalid), now)
	}
	if policy == domain.ApprovalManual || (policy == domain.ApprovalMilestone && milestoneProposal(prepared)) {
		awaiting, err := e.store.TransitionOperation(
			ctx, operation.ID, domain.OperationRunning, domain.OperationAwaitingApproval,
			"proposal awaits user approval", now,
		)
		if err != nil {
			return RunResult{}, err
		}
		result.Operation = awaiting
		return result, nil
	}
	if reason, err := e.semanticApprovalReason(ctx, operation, prepared); err != nil {
		return e.fail(ctx, operation, err, now)
	} else if reason != "" {
		awaiting, err := e.store.TransitionOperation(
			ctx, operation.ID, domain.OperationRunning, domain.OperationAwaitingApproval, reason, now,
		)
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
	committed, err := e.changes.Commit(ctx, approved)
	if errors.Is(err, change.ErrUnauthorized) {
		awaiting, transitionErr := e.store.TransitionOperation(
			ctx, operation.ID, domain.OperationRunning, domain.OperationAwaitingApproval,
			err.Error(), now,
		)
		if transitionErr != nil {
			return RunResult{}, errors.Join(err, transitionErr)
		}
		result.Operation = awaiting
		return result, nil
	}
	if err != nil {
		return e.fail(ctx, operation, err, now)
	}
	succeeded, err := e.store.TransitionOperation(
		ctx, operation.ID, domain.OperationRunning, domain.OperationSucceeded, "", now,
	)
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
	if report.Status != domain.SemanticCompliancePass {
		return fmt.Sprintf("semantic compliance is %s; user approval is required", report.Status), nil
	}
	return "", nil
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

func (e *Engine) fail(ctx context.Context, operation domain.Operation, cause error, now time.Time) (RunResult, error) {
	failed, err := e.store.TransitionOperation(
		ctx, operation.ID, domain.OperationRunning, domain.OperationFailed, cause.Error(), now,
	)
	if err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	return RunResult{Operation: failed}, cause
}
