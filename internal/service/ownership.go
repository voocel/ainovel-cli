package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type SetOwnershipCommand struct {
	ProjectID string
	ChangeID  string
	UserID    string
	Target    domain.DocumentRef
	// Control 为 open 时移除规则：open 是默认约定，无需成文。
	Control   domain.ControlLevel
	Guidance  []string
	Reason    string
	CreatedAt time.Time
}

type OwnershipResult struct {
	ProjectID string                 `json:"project_id"`
	Revision  domain.Revision        `json:"revision"`
	Ownership []domain.OwnershipRule `json:"ownership"`
}

// SetOwnership 是 lock/unlock 的唯一入口：所有权规则本身就是权威文档，
// 修改同样走 Proposal → ChangeSet 单一写路径，立即对新 Operation 生效（D23）。
func (s *Service) SetOwnership(ctx context.Context, command SetOwnershipCommand) (OwnershipResult, error) {
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.ChangeID) == "" ||
		strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.Reason) == "" || command.CreatedAt.IsZero() {
		return OwnershipResult{}, fmt.Errorf("ownership change project, change, user, reason and time are required: %w", domain.ErrInvalid)
	}
	if err := command.Target.Validate(); err != nil {
		return OwnershipResult{}, err
	}
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return OwnershipResult{}, err
	}
	ruleRef := domain.DocumentRef{Kind: domain.DocumentOwnership, ID: command.Target.Key()}
	existing, err := s.store.GetDocument(ctx, target, ruleRef, revision)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return OwnershipResult{}, err
	}
	hasRule := err == nil

	var patch domain.Patch
	switch command.Control {
	case "", domain.ControlOpen:
		if !hasRule {
			return s.ownershipResult(ctx, target, revision)
		}
		patch = domain.Patch{Document: ruleRef, Operation: domain.PatchDelete}
	case domain.ControlLocked, domain.ControlGuided:
		rule := domain.OwnershipRule{Target: command.Target, Control: command.Control, Guidance: command.Guidance}
		if err := rule.Validate(); err != nil {
			return OwnershipResult{}, err
		}
		if hasRule {
			var current domain.OwnershipRule
			if err := json.Unmarshal(existing.Content, &current); err != nil {
				return OwnershipResult{}, fmt.Errorf("decode ownership rule: %w", err)
			}
			if current.Control == rule.Control && slices.Equal(current.Guidance, rule.Guidance) {
				return s.ownershipResult(ctx, target, revision)
			}
		}
		content, err := json.Marshal(rule)
		if err != nil {
			return OwnershipResult{}, fmt.Errorf("encode ownership rule: %w", err)
		}
		patch = domain.Patch{Document: ruleRef, Operation: domain.PatchPut, Content: content}
	default:
		return OwnershipResult{}, fmt.Errorf("unknown control level %q: %w", command.Control, domain.ErrInvalid)
	}

	committed, err := s.commitUserProposal(ctx, domain.Proposal{
		ID: command.ChangeID, Target: target, BaseRevision: revision,
		Author: domain.Author{Kind: domain.AuthorUser, ID: command.UserID},
		Reason: command.Reason, Patches: []domain.Patch{patch},
		ApprovalState: domain.ApprovalPending, CreatedAt: command.CreatedAt,
	}, command.CreatedAt)
	if err != nil {
		return OwnershipResult{}, err
	}
	return s.ownershipResult(ctx, target, committed.NewRevision)
}

type SetApprovalPolicyCommand struct {
	ProjectID string
	ChangeID  string
	UserID    string
	Policy    domain.ApprovalPolicy
	Reason    string
	CreatedAt time.Time
}

type ApprovalPolicyResult struct {
	ProjectID string                `json:"project_id"`
	Revision  domain.Revision       `json:"revision"`
	Policy    domain.ApprovalPolicy `json:"policy"`
}

// SetApprovalPolicy 修改本书当前审批策略（权威文档，§6.3）：同样走唯一写路径，
// 收紧对之后的裁决立即生效（D23），变更本身必须由用户裁决（D25）。
func (s *Service) SetApprovalPolicy(ctx context.Context, command SetApprovalPolicyCommand) (ApprovalPolicyResult, error) {
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.ChangeID) == "" ||
		strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.Reason) == "" || command.CreatedAt.IsZero() {
		return ApprovalPolicyResult{}, fmt.Errorf("approval change project, change, user, reason and time are required: %w", domain.ErrInvalid)
	}
	setting := domain.ApprovalSetting{Policy: command.Policy}
	if err := setting.Validate(); err != nil {
		return ApprovalPolicyResult{}, err
	}
	project, err := s.Project(ctx, command.ProjectID, domain.InitialRevision)
	if err != nil {
		return ApprovalPolicyResult{}, err
	}
	if project.Approval == command.Policy {
		return ApprovalPolicyResult{ProjectID: project.ID, Revision: project.Revision, Policy: project.Approval}, nil
	}
	content, err := json.Marshal(setting)
	if err != nil {
		return ApprovalPolicyResult{}, fmt.Errorf("encode approval setting: %w", err)
	}
	committed, err := s.commitUserProposal(ctx, domain.Proposal{
		ID:           command.ChangeID,
		Target:       domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID},
		BaseRevision: project.Revision,
		Author:       domain.Author{Kind: domain.AuthorUser, ID: command.UserID},
		Reason:       command.Reason,
		Patches: []domain.Patch{{
			Document:  domain.DocumentRef{Kind: domain.DocumentApproval, ID: "root"},
			Operation: domain.PatchPut, Content: content,
		}},
		ApprovalState: domain.ApprovalPending, CreatedAt: command.CreatedAt,
	}, command.CreatedAt)
	if err != nil {
		return ApprovalPolicyResult{}, err
	}
	return ApprovalPolicyResult{ProjectID: command.ProjectID, Revision: committed.NewRevision, Policy: command.Policy}, nil
}

func (s *Service) ownershipResult(
	ctx context.Context,
	target domain.AuthorityTarget,
	revision domain.Revision,
) (OwnershipResult, error) {
	ownership, err := loadDocuments[domain.OwnershipRule](ctx, s.store, target, domain.DocumentOwnership, revision)
	if err != nil {
		return OwnershipResult{}, err
	}
	return OwnershipResult{ProjectID: target.ID, Revision: revision, Ownership: ownership}, nil
}
