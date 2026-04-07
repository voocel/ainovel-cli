package domain

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/apperr"
)

// 状态迁移规则（最小版）
//
// Phase 表示大阶段，采用“只前进不回退”的约束：
//
//	init -> premise -> outline -> writing -> complete
//	  \---------> outline ------^
//	  \-----------------> writing
//
// Flow 表示当前活跃流程，允许在写作期内切换，但不允许明显反常的跳转：
//
//	writing   -> reviewing / rewriting / polishing / steering / writing
//	reviewing -> writing / rewriting / polishing / steering / reviewing
//	rewriting -> writing / steering / rewriting
//	polishing -> writing / steering / polishing
//	steering  -> writing / reviewing / rewriting / polishing / steering
//
// 空状态（零值）视为“未初始化”，允许迁移到任意合法非空状态。

var phaseOrder = map[Phase]int{
	PhaseInit:     1,
	PhasePremise:  2,
	PhaseOutline:  3,
	PhaseWriting:  4,
	PhaseComplete: 5,
}

// CanTransitionPhase 判断 Phase 是否允许迁移。
// 规则保持简单：允许同态迁移、允许前进，不允许回退。
func CanTransitionPhase(from, to Phase) bool {
	if to == "" {
		return false
	}
	if from == "" || from == to {
		return true
	}
	fromOrder, fromOK := phaseOrder[from]
	toOrder, toOK := phaseOrder[to]
	if !fromOK || !toOK {
		return false
	}
	return toOrder >= fromOrder
}

// ValidatePhaseTransition 校验 Phase 迁移是否合法。
func ValidatePhaseTransition(from, to Phase) error {
	if CanTransitionPhase(from, to) {
		return nil
	}
	return apperr.New(
		apperr.CodePhaseTransitionInvalid,
		"domain.validate_phase_transition",
		fmt.Sprintf("invalid phase transition: %q -> %q", from, to),
	)
}

// CanTransitionFlow 判断 FlowState 是否允许迁移。
func CanTransitionFlow(from, to FlowState) bool {
	if to == "" {
		return false
	}
	if from == "" || from == to {
		return true
	}

	switch from {
	case FlowWriting:
		return to == FlowReviewing || to == FlowRewriting || to == FlowPolishing || to == FlowSteering
	case FlowReviewing:
		return to == FlowWriting || to == FlowRewriting || to == FlowPolishing || to == FlowSteering
	case FlowRewriting:
		return to == FlowWriting || to == FlowSteering
	case FlowPolishing:
		return to == FlowWriting || to == FlowSteering
	case FlowSteering:
		return to == FlowWriting || to == FlowReviewing || to == FlowRewriting || to == FlowPolishing
	default:
		return false
	}
}

// ValidateFlowTransition 校验 FlowState 迁移是否合法。
func ValidateFlowTransition(from, to FlowState) error {
	if CanTransitionFlow(from, to) {
		return nil
	}
	return apperr.New(
		apperr.CodeFlowTransitionInvalid,
		"domain.validate_flow_transition",
		fmt.Sprintf("invalid flow transition: %q -> %q", from, to),
	)
}
