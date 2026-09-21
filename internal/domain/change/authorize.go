package change

import (
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func authorize(change model.Proposal, base documentState) error {
	decider := change.DecidedBy
	if decider == nil {
		return fmt.Errorf("approval decider is missing: %w", ErrUnauthorized)
	}
	if change.Author.Kind == model.AuthorUser && decider.Kind != model.AuthorUser {
		return fmt.Errorf("user-authored change requires user decision: %w", ErrUnauthorized)
	}
	if change.Target.Kind == model.AuthorityProfile || change.Target.Kind == model.AuthorityPack {
		if change.Author.Kind != model.AuthorUser && decider.Kind != model.AuthorUser {
			return fmt.Errorf("AI changes to creator assets require user approval: %w", ErrUnauthorized)
		}
	}
	for _, patch := range change.Patches {
		// D25/D31/D33：用户专属文档是创作边界，AI 可以提出 Proposal，但任何作者的
		// 变更都必须由用户裁决。
		spec, err := model.DocumentType(patch.Document.Kind)
		if err != nil {
			return err
		}
		if spec.UserOnly && decider.Kind != model.AuthorUser {
			return fmt.Errorf("creative boundary changes require user approval: %w", ErrUnauthorized)
		}
		control, err := controlLevel(base, patch.Document)
		if err != nil {
			return err
		}
		if change.Author.Kind == model.AuthorUser {
			continue
		}
		// D25/D28：Intent 变更无条件用户确认；唯一例外是初始化事务
		// （BaseRevision == InitialRevision，即项目的第一个 Revision）。
		if patch.Document.Kind == model.DocumentIntent &&
			change.BaseRevision != model.InitialRevision && decider.Kind != model.AuthorUser {
			return fmt.Errorf("intent changes after initialization require user approval: %w", ErrUnauthorized)
		}
		switch control {
		case model.ControlLocked:
			if decider.Kind != model.AuthorUser {
				return fmt.Errorf("locked document %q requires user approval: %w", patch.Document.Key(), ErrUnauthorized)
			}
		case model.ControlGuided:
			if decider.Kind != model.AuthorUser {
				if err := compliancePasses(change.Impact.Compliance); err != nil {
					return fmt.Errorf("guided document %q: %w", patch.Document.Key(), err)
				}
			}
		}
	}
	return nil
}

// compliancePasses 兑现 D14：只有独立合规裁定明确返回 pass 才允许自动提交；
// 证据缺失、不合法或任何非 pass 状态都不得放行。
func compliancePasses(compliance json.RawMessage) error {
	if len(compliance) == 0 {
		return fmt.Errorf("semantic compliance evidence is required: %w", ErrUnauthorized)
	}
	var report model.SemanticComplianceReport
	if err := json.Unmarshal(compliance, &report); err != nil {
		return fmt.Errorf("decode semantic compliance report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return err
	}
	if report.Status != model.SemanticCompliancePass {
		return fmt.Errorf("semantic compliance is %s: %w", report.Status, ErrUnauthorized)
	}
	return nil
}

// controlLevel 取文档的控制级别：有 Ownership 规则以规则为准；没有规则时，用户
// 亲笔的章节默认 locked（D34），其余 open。
func controlLevel(state documentState, ref model.DocumentRef) (model.ControlLevel, error) {
	ownershipRef := model.DocumentRef{Kind: model.DocumentOwnership, ID: ref.Key()}
	document, ok := state[ownershipRef.Key()]
	if !ok {
		if base, exists := state[ref.Key()]; exists && ref.Kind == model.DocumentManuscript {
			var chapter model.ManuscriptChapter
			if err := json.Unmarshal(base.Content, &chapter); err != nil {
				return "", fmt.Errorf("decode chapter author for %q: %w", ref.Key(), err)
			}
			if chapter.Author == model.AuthorUser {
				return model.ControlLocked, nil
			}
		}
		return model.ControlOpen, nil
	}
	var rule model.OwnershipRule
	if err := json.Unmarshal(document.Content, &rule); err != nil {
		return "", fmt.Errorf("decode ownership for %q: %w", ref.Key(), err)
	}
	if err := rule.Validate(); err != nil {
		return "", err
	}
	return rule.Control, nil
}
