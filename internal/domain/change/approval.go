package change

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 审批策略解析（§5.4、D23）：哪条策略管这份提案、它是否构成 milestone、在途正文受哪些
// 约束、合规证据能否自动放行。执行引擎只决定何时做分析、任务落到哪个状态。

// EffectiveApprovalPolicy 取启动快照与提案基线 Revision 上权威审批策略中更严格的一方
// （D23）：收紧立即生效来自权威文档，放松不追溯来自快照下限。
func (e *Engine) EffectiveApprovalPolicy(
	ctx context.Context,
	snapshot model.ApprovalPolicy,
	proposal model.Proposal,
) (model.ApprovalPolicy, error) {
	if proposal.Target.Kind != model.AuthorityProject || proposal.BaseRevision == model.InitialRevision {
		return snapshot, nil
	}
	document, err := e.store.GetDocument(ctx, proposal.Target,
		model.DocumentRef{Kind: model.DocumentApproval, ID: model.SingletonDocumentID}, proposal.BaseRevision)
	if errors.Is(err, model.ErrNotFound) {
		return snapshot, nil
	}
	if err != nil {
		return "", err
	}
	var setting model.ApprovalSetting
	if err := json.Unmarshal(document.Content, &setting); err != nil {
		return "", fmt.Errorf("decode approval setting: %w", err)
	}
	return model.StricterApproval(snapshot, setting.Policy), nil
}

// Milestone 判定变化的重大性。例行推进——章节正文与随章提交的 Canon Delta——不构成
// milestone，否则 milestone 塌缩为 manual、中间档消失（§5.4）。
func Milestone(proposal model.Proposal) bool {
	chapters := make(map[string]struct{})
	for _, patch := range proposal.Patches {
		if patch.Document.Kind == model.DocumentManuscript && patch.Operation == model.PatchPut {
			chapters[patch.Document.ID] = struct{}{}
		}
	}
	for _, patch := range proposal.Patches {
		switch patch.Document.Kind {
		case model.DocumentOwnership, model.DocumentIntent:
			return true
		case model.DocumentCanon:
			if patch.Operation == model.PatchDelete {
				return true
			}
			var fact model.CanonFact
			if json.Unmarshal(patch.Content, &fact) != nil {
				return true
			}
			if _, routine := chapters[fact.SourceChapterID]; !routine {
				return true
			}
		case model.DocumentPlan:
			if patch.Operation == model.PatchDelete {
				return true
			}
			var node model.PlanNode
			if json.Unmarshal(patch.Content, &node) == nil && (node.Kind == model.PlanVolume || node.Kind == model.PlanArc) {
				return true
			}
		}
	}
	return false
}

// SemanticConstraints 取 AI 正文提案所受的 locked/guided 约束：启动快照与提案基线
// （提交时的最新已批准 Revision）中更严格的一方（D23）。收紧立即生效来自提案基线，
// 放松不追溯来自启动快照。非正文提案或非作品目标没有语义约束。
func (e *Engine) SemanticConstraints(
	ctx context.Context,
	snapshotRevision model.Revision,
	proposal model.Proposal,
) ([]model.OwnershipRule, error) {
	hasManuscript := slices.ContainsFunc(proposal.Patches, func(patch model.Patch) bool {
		return patch.Document.Kind == model.DocumentManuscript
	})
	if !hasManuscript || proposal.Target.Kind != model.AuthorityProject {
		return nil, nil
	}
	snapshotRules, err := e.ownershipRules(ctx, proposal.Target, snapshotRevision)
	if err != nil {
		return nil, err
	}
	constraints := snapshotRules
	if proposal.BaseRevision != snapshotRevision {
		latestRules, err := e.ownershipRules(ctx, proposal.Target, proposal.BaseRevision)
		if err != nil {
			return nil, err
		}
		constraints = mergeStricterRules(snapshotRules, latestRules)
	}
	filtered := constraints[:0]
	for _, rule := range constraints {
		if rule.Control == model.ControlLocked || rule.Control == model.ControlGuided {
			filtered = append(filtered, rule)
		}
	}
	slices.SortFunc(filtered, func(left, right model.OwnershipRule) int {
		if compared := strings.Compare(left.Target.Key(), right.Target.Key()); compared != 0 {
			return compared
		}
		return strings.Compare(strings.Join(left.Guidance, "\n"), strings.Join(right.Guidance, "\n"))
	})
	return filtered, nil
}

func (e *Engine) ownershipRules(
	ctx context.Context,
	target model.AuthorityTarget,
	revision model.Revision,
) ([]model.OwnershipRule, error) {
	if revision == model.InitialRevision {
		return nil, nil
	}
	documents, err := e.store.ListDocuments(ctx, target, model.DocumentOwnership, revision)
	if err != nil {
		return nil, err
	}
	rules := make([]model.OwnershipRule, 0, len(documents))
	for _, document := range documents {
		var rule model.OwnershipRule
		if err := json.Unmarshal(document.Content, &rule); err != nil {
			return nil, fmt.Errorf("decode ownership constraint %q: %w", document.Document.ID, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func controlRank(level model.ControlLevel) int {
	switch level {
	case model.ControlLocked:
		return 2
	case model.ControlGuided:
		return 1
	default:
		return 0
	}
}

// mergeStricterRules 对同一 target 取控制级别更严格的一方；两侧同为 guided 且
// guidance 不同时二者都保留——在途任务必须同时通过新旧约束校验（§5.5）。
func mergeStricterRules(snapshot, latest []model.OwnershipRule) []model.OwnershipRule {
	byTarget := make(map[string][]model.OwnershipRule)
	for _, rule := range snapshot {
		byTarget[rule.Target.Key()] = append(byTarget[rule.Target.Key()], rule)
	}
	for _, rule := range latest {
		key := rule.Target.Key()
		existing := byTarget[key]
		if len(existing) == 0 {
			byTarget[key] = []model.OwnershipRule{rule}
			continue
		}
		strongest := existing[0]
		switch {
		case controlRank(rule.Control) > controlRank(strongest.Control):
			byTarget[key] = []model.OwnershipRule{rule}
		case controlRank(rule.Control) < controlRank(strongest.Control):
		case rule.Control == model.ControlGuided && !slices.Equal(rule.Guidance, strongest.Guidance):
			byTarget[key] = append(existing, rule)
		}
	}
	merged := make([]model.OwnershipRule, 0, len(byTarget))
	for _, rules := range byTarget {
		merged = append(merged, rules...)
	}
	return merged
}

// ComplianceBasisDigest 把合规报告绑定到候选补丁与实际检查的约束（D51）：恢复或
// 重定位后任一方变化，旧证据即不可复用。
func ComplianceBasisDigest(proposal model.Proposal, constraints []model.OwnershipRule) (string, error) {
	payload, err := json.Marshal(struct {
		Patches     []model.Patch         `json:"patches"`
		Constraints []model.OwnershipRule `json:"constraints"`
	}{proposal.Patches, constraints})
	if err != nil {
		return "", fmt.Errorf("encode compliance basis: %w", err)
	}
	return model.Digest(payload), nil
}

// ComplianceReason 判断合规证据能否让提案自动放行：返回空串表示证据覆盖当前候选与
// 约束且为 pass；否则返回需要用户裁决的原因（D14：不确定或不可用都不得自动放行）。
func ComplianceReason(proposal model.Proposal, constraints []model.OwnershipRule) (string, error) {
	if len(constraints) == 0 {
		return "", nil
	}
	if len(proposal.Impact.Compliance) == 0 {
		return "semantic compliance evidence is required for locked or guided story constraints", nil
	}
	var report model.SemanticComplianceReport
	if err := json.Unmarshal(proposal.Impact.Compliance, &report); err != nil {
		return "", fmt.Errorf("decode semantic compliance report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return "", err
	}
	basis, err := ComplianceBasisDigest(proposal, constraints)
	if err != nil {
		return "", err
	}
	if report.BasisDigest != basis {
		return "semantic compliance evidence does not cover the candidate and current constraints", nil
	}
	if report.Status != model.SemanticCompliancePass {
		return fmt.Sprintf("semantic compliance is %s; user approval is required", report.Status), nil
	}
	return "", nil
}
