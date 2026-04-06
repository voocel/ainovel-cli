package policy

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
	orchestratoraction "github.com/voocel/ainovel-cli/internal/orchestrator/action"
)

type snapshot struct {
	Progress *domain.Progress
	RunMeta  *domain.RunMeta
	Commit   *domain.CommitResult
	Review   *domain.ReviewEntry
}

type commitRule func(snapshot) (bool, []orchestratoraction.Action)
type reviewRule func(snapshot) (bool, []orchestratoraction.Action)

type engine struct {
	commitRules []commitRule
	reviewRules []reviewRule
}

var defaultEngine = engine{
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

func EvaluateCommit(progress *domain.Progress, runMeta *domain.RunMeta, result *domain.CommitResult) []orchestratoraction.Action {
	return defaultEngine.evaluateCommit(snapshot{Progress: progress, RunMeta: runMeta, Commit: result})
}

func EvaluateReview(runMeta *domain.RunMeta, review *domain.ReviewEntry) []orchestratoraction.Action {
	return defaultEngine.evaluateReview(snapshot{RunMeta: runMeta, Review: review})
}

func (e engine) evaluateCommit(s snapshot) []orchestratoraction.Action {
	var actions []orchestratoraction.Action
	actions = append(actions, commitFeedbackActions(s)...)

	for _, rule := range e.commitRules {
		if matched, ruleActions := rule(s); matched {
			actions = append(actions, ruleActions...)
			return actions
		}
	}
	return actions
}

func (e engine) evaluateReview(s snapshot) []orchestratoraction.Action {
	for _, rule := range e.reviewRules {
		if matched, actions := rule(s); matched {
			return actions
		}
	}
	return nil
}

func pendingSteerActions(runMeta *domain.RunMeta) []orchestratoraction.Action {
	if runMeta == nil || runMeta.PendingSteer == "" {
		return nil
	}
	return []orchestratoraction.Action{
		orchestratoraction.EmitNotice("SYSTEM", "提醒 Coordinator 处理用户干预", "info"),
		orchestratoraction.FollowUp(fmt.Sprintf(
			"[系统-重要] 用户在写作期间提交了干预指令：「%s」。请优先处理此干预（可能需要修改设定或重写章节），然后再继续后续写作。",
			runMeta.PendingSteer)),
		orchestratoraction.ClearHandledSteerAction(),
	}
}

func truncateText(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
