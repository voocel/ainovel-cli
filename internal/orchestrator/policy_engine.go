package orchestrator

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type policyActionKind string

const (
	actionEmitNotice         policyActionKind = "emit_notice"
	actionFollowUp           policyActionKind = "follow_up"
	actionSetFlow            policyActionKind = "set_flow"
	actionSetPendingRewrites policyActionKind = "set_pending_rewrites"
	actionCompleteRewrite    policyActionKind = "complete_rewrite"
	actionClearHandledSteer  policyActionKind = "clear_handled_steer"
	actionSaveCheckpoint     policyActionKind = "save_checkpoint"
	actionSaveHandoff        policyActionKind = "save_handoff"
	actionMarkComplete       policyActionKind = "mark_complete"
)

type policyAction struct {
	Kind     policyActionKind
	DedupKey string
	Category string
	Summary  string
	Level    string
	Message  string
	Flow     domain.FlowState
	Chapters []int
	Reason   string
	Chapter  int
	Label    string
}

type policySnapshot struct {
	Progress *domain.Progress
	RunMeta  *domain.RunMeta
	Commit   *domain.CommitResult
	Review   *domain.ReviewEntry
}

type commitRule func(policySnapshot) (bool, []policyAction)
type reviewRule func(policySnapshot) (bool, []policyAction)

type policyEngine struct {
	commitRules []commitRule
	reviewRules []reviewRule
}

var defaultPolicyEngine = policyEngine{
	commitRules: []commitRule{
		commitRewriteFlowRule,
		commitLayeredArcEndRule,
		commitBookCompleteRule,
		commitReviewRequiredRule,
		commitDefaultRule,
	},
	reviewRules: []reviewRule{
		reviewRewriteRule,
		reviewPolishRule,
		reviewAcceptRule,
	},
}

func evaluateCommitPolicy(progress *domain.Progress, runMeta *domain.RunMeta, result *domain.CommitResult) []policyAction {
	return defaultPolicyEngine.evaluateCommit(policySnapshot{Progress: progress, RunMeta: runMeta, Commit: result})
}

func evaluateReviewPolicy(runMeta *domain.RunMeta, review *domain.ReviewEntry) []policyAction {
	return defaultPolicyEngine.evaluateReview(policySnapshot{RunMeta: runMeta, Review: review})
}

func (e policyEngine) evaluateCommit(snapshot policySnapshot) []policyAction {
	var actions []policyAction
	actions = append(actions, commitFeedbackActions(snapshot)...)

	for _, rule := range e.commitRules {
		if matched, ruleActions := rule(snapshot); matched {
			actions = append(actions, ruleActions...)
			return actions
		}
	}
	return actions
}

func (e policyEngine) evaluateReview(snapshot policySnapshot) []policyAction {
	for _, rule := range e.reviewRules {
		if matched, actions := rule(snapshot); matched {
			return actions
		}
	}
	return nil
}

func pendingSteerActions(runMeta *domain.RunMeta) []policyAction {
	if runMeta == nil || runMeta.PendingSteer == "" {
		return nil
	}
	return []policyAction{
		emitNotice("SYSTEM", "提醒 Coordinator 处理用户干预", "info"),
		followUp(fmt.Sprintf(
			"[系统-重要] 用户在写作期间提交了干预指令：「%s」。请优先处理此干预（可能需要修改设定或重写章节），然后再继续后续写作。",
			runMeta.PendingSteer)),
		clearHandledSteerAction(),
	}
}

func emitNotice(category, summary, level string) policyAction {
	return policyAction{Kind: actionEmitNotice, Category: category, Summary: summary, Level: level}
}

func withDedupKey(action policyAction, key string) policyAction {
	action.DedupKey = key
	return action
}

func followUp(message string) policyAction {
	return policyAction{Kind: actionFollowUp, Message: message}
}

func setFlow(flow domain.FlowState) policyAction {
	return policyAction{Kind: actionSetFlow, Flow: flow}
}

func setPendingRewrites(chapters []int, reason string) policyAction {
	return policyAction{Kind: actionSetPendingRewrites, Chapters: chapters, Reason: reason}
}

func completeRewrite(chapter int) policyAction {
	return policyAction{Kind: actionCompleteRewrite, Chapter: chapter}
}

func clearHandledSteerAction() policyAction {
	return policyAction{Kind: actionClearHandledSteer}
}

func saveCheckpointAction(label string) policyAction {
	return policyAction{Kind: actionSaveCheckpoint, Label: label}
}

func saveHandoffAction(label string) policyAction {
	return policyAction{Kind: actionSaveHandoff, Label: label}
}

func markComplete() policyAction {
	return policyAction{Kind: actionMarkComplete}
}
