package orchestrator

import (
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func planningTierGuidance(runMeta *domain.RunMeta) string {
	if runMeta == nil {
		return ""
	}
	switch runMeta.PlanningTier {
	case domain.PlanningTierShort:
		return "当前规划级别：short。如需调整设定或重做大纲，优先调用 architect_short。"
	case domain.PlanningTierMid:
		return "当前规划级别：mid。如需调整设定或重做大纲，优先调用 architect_mid。"
	case domain.PlanningTierLong:
		return "当前规划级别：long。如需调整设定或重做大纲，优先调用 architect_long，并保持分层大纲的一致性。"
	default:
		return ""
	}
}

// recoveryResult 恢复链的判断结果。
type recoveryResult struct {
	PromptText string
	Label      string
	IsNew      bool
}

// determineRecovery 根据 Progress 和 RunMeta 判断恢复类型和 Prompt 文本。
func determineRecovery(progress *domain.Progress, runMeta *domain.RunMeta, store ...*storepkg.Store) recoveryResult {
	var activeStore *storepkg.Store
	if len(store) > 0 {
		activeStore = store[0]
	}
	return defaultRecoveryEngine.evaluate(recoverySnapshot{
		Progress: progress,
		RunMeta:  runMeta,
		Store:    activeStore,
	})
}
