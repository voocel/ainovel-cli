package domain

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/errs"
)

// Trạng thái迁移规则（最小版）
//
// Phase 表示大阶段，采用“只前进不回退”的约束：
//
//	init -> premise -> outline -> writing -> complete
//	  \---------> outline ------^
//	  \-----------------> writing
//
// Flow 表示Hiện tại活跃流程，允许在Viết期内切换，但Không允 phép明显反常的跳转：
//
//	writing   -> reviewing / rewriting / polishing / steering / writing
//	reviewing -> writing / rewriting / polishing / steering / reviewing
//	rewriting -> writing / steering / rewriting
//	polishing -> writing / steering / polishing
//	steering  -> writing / reviewing / rewriting / polishing / steering
//
// RỗngTrạng thái（零值）视为“未初始化”，允许迁移到任意合法非RỗngTrạng thái。

var phaseOrder = map[Phase]int{
	PhaseInit:     1,
	PhasePremise:  2,
	PhaseOutline:  3,
	PhaseWriting:  4,
	PhaseComplete: 5,
}

// CanTransitionPhase 判断 Phase Có czy không允许迁移。
// 规则保持简单：允许同态迁移、允许前进，Không允 phép回退。
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

// ValidatePhaseTransition 校验 Phase 迁移Có czy không合法。
func ValidatePhaseTransition(from, to Phase) error {
	if CanTransitionPhase(from, to) {
		return nil
	}
	return fmt.Errorf("invalid phase transition: %q -> %q: %w", from, to, errs.ErrPhaseTransition)
}

// CanTransitionFlow 判断 FlowState Có czy không允许迁移。
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

// ValidateFlowTransition 校验 FlowState 迁移Có czy không合法。
func ValidateFlowTransition(from, to FlowState) error {
	if CanTransitionFlow(from, to) {
		return nil
	}
	return fmt.Errorf("invalid flow transition: %q -> %q: %w", from, to, errs.ErrFlowTransition)
}
