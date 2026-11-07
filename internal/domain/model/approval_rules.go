package model

import (
	"encoding/json"
	"fmt"
	"slices"
)

// 授权与审批里的创作领域判定（§5.4 / D64）：Change Engine 只执行机制（用户专属、
// Ownership 控制、审批策略、合规证据），哪些内容变化需要用户、什么算重大变化由这里回答。

// RequiresUser 返回 AI 变更即使在 auto 档下也必须由用户裁决的原因，空串表示无需：
//   - Intent 只装用户的原话（D25/D70）：AI 的任何改动都等用户裁决；
//   - 故事罗盘的篇幅护栏（D63/D96）：删除罗盘、篇幅上限超出 AI 可自行决定的范围。
func RequiresUser(proposal Proposal, base DocumentSet) (string, error) {
	for _, patch := range proposal.Patches {
		switch patch.Document.Kind {
		case DocumentIntent:
			return "intent changes require user approval", nil
		case DocumentCompass:
			if reason, err := compassBeyondAutonomy(base, patch); reason != "" || err != nil {
				return reason, err
			}
		}
	}
	return "", nil
}

func compassBeyondAutonomy(base DocumentSet, patch Patch) (string, error) {
	if patch.Operation != PatchPut {
		return "removing the compass requires user approval", nil
	}
	var next Compass
	if err := json.Unmarshal(patch.Content, &next); err != nil {
		return "", fmt.Errorf("decode proposed compass: %w", err)
	}
	// 基线没有罗盘时按空罗盘判：只有护栏。
	var current Compass
	if document, ok := base[patch.Document.Key()]; ok {
		if err := json.Unmarshal(document.Content, &current); err != nil {
			return "", fmt.Errorf("decode base compass: %w", err)
		}
	}
	if limit := current.Limit(); next.ScaleMax > limit {
		return fmt.Sprintf("a compass scale_max of %d (above %d) requires user approval", next.ScaleMax, limit), nil
	}
	return "", nil
}

// ComplianceApplies 报告提案是否受在途 locked/guided 约束的独立语义合规检查：约束作用于
// AI 写下的正文（D14），不含正文的提案按控制级别直接走授权。
func ComplianceApplies(proposal Proposal) bool {
	return slices.ContainsFunc(proposal.Patches, func(patch Patch) bool {
		return patch.Document.Kind == DocumentManuscript
	})
}

// DefaultControl 是没有 Ownership 规则时文档的控制级别：用户亲笔的章节默认 locked
// （D34），其余 open。
func DefaultControl(ref DocumentRef, base DocumentSet) (ControlLevel, error) {
	document, exists := base[ref.Key()]
	if !exists || ref.Kind != DocumentManuscript {
		return ControlOpen, nil
	}
	var chapter ManuscriptChapter
	if err := json.Unmarshal(document.Content, &chapter); err != nil {
		return "", fmt.Errorf("decode chapter author for %q: %w", ref.Key(), err)
	}
	if chapter.Author == AuthorUser {
		return ControlLocked, nil
	}
	return ControlOpen, nil
}

// IsMilestone 判定变化的重大性（§5.4）：Intent 与 Ownership 的变化、删除或跨章改动
// Canon、删除蓝图节点、增改卷与弧都是 milestone。例行推进——章节正文与随章提交的
// Canon Delta——不是，否则 milestone 塌缩为 manual、中间档消失。
func IsMilestone(proposal Proposal) bool {
	chapters := manuscriptPuts(proposal.Patches)
	for _, patch := range proposal.Patches {
		switch patch.Document.Kind {
		case DocumentOwnership, DocumentIntent:
			return true
		case DocumentCanon:
			if patch.Operation == PatchDelete {
				return true
			}
			var fact CanonFact
			if json.Unmarshal(patch.Content, &fact) != nil {
				return true
			}
			if _, routine := chapters[fact.SourceChapterID]; !routine {
				return true
			}
		case DocumentPlan:
			if patch.Operation == PatchDelete {
				return true
			}
			var node PlanNode
			if json.Unmarshal(patch.Content, &node) == nil && (node.Kind == PlanVolume || node.Kind == PlanArc) {
				return true
			}
		}
	}
	return false
}
