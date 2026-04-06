package recovery

import (
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// Result 恢复链的判断结果。
type Result struct {
	PromptText           string
	Label                string
	IsNew                bool
	ConsumesPendingSteer bool
}

type rule func(snapshot) (bool, Result)

type snapshot struct {
	Progress    *domain.Progress
	RunMeta     *domain.RunMeta
	Store       *storepkg.Store
	SaveHandoff func(*storepkg.Store, string) error
}

type engine struct {
	rules []rule
}

var defaultEngine = engine{
	rules: []rule{
		recoveryPendingCommitRule,
		recoveryTaskRule,
		recoveryNewRule,
		recoveryPlanningRule,
		recoveryInProgressChapterRule,
		recoveryPendingRewriteRule,
		recoveryReviewingRule,
		recoverySteeringResetRule,
		recoveryPendingSteerRule,
		recoveryLayeredPlanningRule,
		recoveryResumableRule,
	},
}

func Evaluate(progress *domain.Progress, runMeta *domain.RunMeta, store *storepkg.Store, saveHandoff func(*storepkg.Store, string) error) Result {
	return defaultEngine.evaluate(snapshot{
		Progress:    progress,
		RunMeta:     runMeta,
		Store:       store,
		SaveHandoff: saveHandoff,
	})
}

func (e engine) evaluate(s snapshot) Result {
	for _, rule := range e.rules {
		if matched, result := rule(s); matched {
			return result
		}
	}
	return Result{IsNew: true}
}

func (s snapshot) withGuidance(prompt string) string {
	guidance := PlanningTierGuidance(s.RunMeta)
	if guidance == "" {
		return prompt
	}
	return prompt + "\n" + guidance
}
