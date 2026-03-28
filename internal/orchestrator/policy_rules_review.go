package orchestrator

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

var criticalScorecardDimensions = map[string]struct{}{
	"consistency": {},
	"character":   {},
	"continuity":  {},
}

type scorecardGateDecision struct {
	EscalatedVerdict string
	Reason           string
}

func reviewRewriteRule(snapshot policySnapshot) (bool, []policyAction) {
	review := snapshot.Review
	if review == nil || review.Verdict != "rewrite" {
		return false, nil
	}
	return true, reviewQueueActions(snapshot.RunMeta, review, domain.FlowRewriting, "rewrite", "重写")
}

func reviewPolishRule(snapshot policySnapshot) (bool, []policyAction) {
	review := snapshot.Review
	if review == nil || review.Verdict != "polish" {
		return false, nil
	}
	return true, reviewQueueActions(snapshot.RunMeta, review, domain.FlowPolishing, "polish", "打磨")
}

func reviewAcceptRule(snapshot policySnapshot) (bool, []policyAction) {
	review := snapshot.Review
	if review == nil {
		return false, nil
	}
	if review.Verdict == "accept" && review.ContractStatus == "missed" {
		actions := reviewQueueActions(snapshot.RunMeta, review, domain.FlowRewriting, "rewrite", "重写")
		actions = append([]policyAction{
			emitNotice("REVIEW", "contract_status=missed，accept 被提升为 rewrite", "warn"),
		}, actions...)
		return true, actions
	}
	if review.Verdict == "accept" && review.ContractStatus == "partial" {
		actions := reviewQueueActions(snapshot.RunMeta, review, domain.FlowPolishing, "polish", "打磨")
		actions = append([]policyAction{
			emitNotice("REVIEW", "contract_status=partial，accept 被提升为 polish", "warn"),
		}, actions...)
		return true, actions
	}
	if review.Verdict == "accept" {
		if gate := evaluateScorecardGate(review); gate != nil {
			switch gate.EscalatedVerdict {
			case "rewrite":
				actions := reviewQueueActions(snapshot.RunMeta, review, domain.FlowRewriting, "rewrite", "重写")
				actions = append([]policyAction{
					emitNotice("REVIEW", "scorecard gate 触发，accept 被提升为 rewrite", "warn"),
					followUp("[系统] " + gate.Reason),
				}, actions...)
				return true, actions
			case "polish":
				actions := reviewQueueActions(snapshot.RunMeta, review, domain.FlowPolishing, "polish", "打磨")
				actions = append([]policyAction{
					emitNotice("REVIEW", "scorecard gate 触发，accept 被提升为 polish", "warn"),
					followUp("[系统] " + gate.Reason),
				}, actions...)
				return true, actions
			}
		}
	}
	actions := []policyAction{
		setFlow(domain.FlowWriting),
		emitNotice("REVIEW", "verdict=accept 审阅通过", "success"),
	}
	actions = append(actions, pendingSteerActions(snapshot.RunMeta)...)
	actions = append(actions,
		saveCheckpointAction(fmt.Sprintf("review-ch%02d-%s", review.Chapter, review.Verdict)),
		saveHandoffAction(fmt.Sprintf("review-ch%02d-%s", review.Chapter, review.Verdict)),
		emitNotice("CHECK", fmt.Sprintf("saved review-ch%02d-%s", review.Chapter, review.Verdict), "info"),
	)
	return true, actions
}

func reviewQueueActions(runMeta *domain.RunMeta, review *domain.ReviewEntry, flow domain.FlowState, verdict, verb string) []policyAction {
	chaptersInfo := ""
	if len(review.AffectedChapters) > 0 {
		chaptersInfo = fmt.Sprintf("受影响章节：%v。", review.AffectedChapters)
	}
	contractInfo := ""
	if review.ContractStatus != "" && review.ContractStatus != "met" {
		contractInfo = fmt.Sprintf("章节契约完成度=%s。", review.ContractStatus)
		if len(review.ContractMisses) > 0 {
			contractInfo += fmt.Sprintf("未达成项：%v。", review.ContractMisses)
		}
		if review.ContractNotes != "" {
			contractInfo += review.ContractNotes
		}
	}
	actions := []policyAction{
		setPendingRewrites(review.AffectedChapters, review.Summary),
		setFlow(flow),
		emitNotice("REVIEW", fmt.Sprintf("verdict=%s affected=%v", verdict, review.AffectedChapters), "warn"),
		followUp(fmt.Sprintf(
			"[系统] Editor 审阅结论：%s。%s%s%s请逐章调用 writer %s受影响章节，全部完成后继续正常写作。",
			verdict, review.Summary, chaptersInfo, contractInfo, verb)),
	}
	actions = append(actions, pendingSteerActions(runMeta)...)
	actions = append(actions,
		saveCheckpointAction(fmt.Sprintf("review-ch%02d-%s", review.Chapter, verdict)),
		saveHandoffAction(fmt.Sprintf("review-ch%02d-%s", review.Chapter, verdict)),
		emitNotice("CHECK", fmt.Sprintf("saved review-ch%02d-%s", review.Chapter, verdict), "info"),
	)
	return actions
}

func evaluateScorecardGate(review *domain.ReviewEntry) *scorecardGateDecision {
	if review == nil || len(review.Dimensions) == 0 {
		return nil
	}

	var criticalFails []string
	var polishReasons []string

	for _, dim := range review.Dimensions {
		name := dim.Dimension
		score := dim.Score
		verdict := dim.Verdict

		_, critical := criticalScorecardDimensions[name]
		if critical && (verdict == "fail" || score < 60) {
			criticalFails = append(criticalFails, fmt.Sprintf("%s=%d/%s", name, score, verdict))
			continue
		}
		if critical && (verdict == "warning" || score < 80) {
			polishReasons = append(polishReasons, fmt.Sprintf("%s=%d/%s", name, score, verdict))
			continue
		}
		if verdict == "fail" || score < 60 {
			polishReasons = append(polishReasons, fmt.Sprintf("%s=%d/%s", name, score, verdict))
		}
	}

	if len(criticalFails) > 0 {
		return &scorecardGateDecision{
			EscalatedVerdict: "rewrite",
			Reason:           "评分卡关键维度未过线：" + strings.Join(criticalFails, ", "),
		}
	}
	if len(polishReasons) > 0 {
		return &scorecardGateDecision{
			EscalatedVerdict: "polish",
			Reason:           "评分卡存在需返工维度：" + strings.Join(polishReasons, ", "),
		}
	}
	return nil
}
