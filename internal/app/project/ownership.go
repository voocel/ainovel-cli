package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type SetOwnershipCommand struct {
	ProjectID string
	ChangeID  string
	UserID    string
	Target    model.DocumentRef
	// Control 为 open 时移除规则：open 是默认约定，无需成文。
	Control   model.ControlLevel
	Guidance  []string
	Reason    string
	CreatedAt time.Time
}

type OwnershipResult struct {
	ProjectID string                `json:"project_id"`
	Revision  model.Revision        `json:"revision"`
	Ownership []model.OwnershipRule `json:"ownership"`
}

// SetOwnership 是 lock/unlock 的唯一入口：所有权规则本身就是权威文档，
// 修改同样走 Proposal → ChangeSet 单一写路径，立即对新 Operation 生效（D23）。
func (s *Repository) SetOwnership(ctx context.Context, command SetOwnershipCommand) (OwnershipResult, error) {
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.ChangeID) == "" ||
		strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.Reason) == "" || command.CreatedAt.IsZero() {
		return OwnershipResult{}, fmt.Errorf("ownership change project, change, user, reason and time are required: %w", model.ErrInvalid)
	}
	if err := command.Target.Validate(); err != nil {
		return OwnershipResult{}, err
	}
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return OwnershipResult{}, err
	}
	ruleRef := model.DocumentRef{Kind: model.DocumentOwnership, ID: command.Target.Key()}
	existing, err := s.store.GetDocument(ctx, target, ruleRef, revision)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return OwnershipResult{}, err
	}
	hasRule := err == nil

	var patch model.Patch
	switch command.Control {
	case "", model.ControlOpen:
		if !hasRule {
			return s.ownershipResult(ctx, target, revision)
		}
		patch = model.Patch{Document: ruleRef, Operation: model.PatchDelete}
	case model.ControlLocked, model.ControlGuided:
		rule := model.OwnershipRule{Target: command.Target, Control: command.Control, Guidance: command.Guidance}
		if err := rule.Validate(); err != nil {
			return OwnershipResult{}, err
		}
		if hasRule {
			var current model.OwnershipRule
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
		patch = model.Patch{Document: ruleRef, Operation: model.PatchPut, Content: content}
	default:
		return OwnershipResult{}, fmt.Errorf("unknown control level %q: %w", command.Control, model.ErrInvalid)
	}

	committed, err := s.changes.CommitUser(ctx, model.Proposal{
		ID: command.ChangeID, Target: target, BaseRevision: revision,
		Author: model.Author{Kind: model.AuthorUser, ID: command.UserID},
		Reason: command.Reason, Patches: []model.Patch{patch},
		ApprovalState: model.ApprovalPending, CreatedAt: command.CreatedAt,
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
	Policy    model.ApprovalPolicy
	Reason    string
	CreatedAt time.Time
}

type ApprovalPolicyResult struct {
	ProjectID string               `json:"project_id"`
	Revision  model.Revision       `json:"revision"`
	Policy    model.ApprovalPolicy `json:"policy"`
}

// SetApprovalPolicy 修改本书当前审批策略（权威文档，§6.3）：同样走唯一写路径，
// 收紧对之后的裁决立即生效（D23），变更本身必须由用户裁决（D25）。
func (s *Repository) SetApprovalPolicy(ctx context.Context, command SetApprovalPolicyCommand) (ApprovalPolicyResult, error) {
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.ChangeID) == "" ||
		strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.Reason) == "" || command.CreatedAt.IsZero() {
		return ApprovalPolicyResult{}, fmt.Errorf("approval change project, change, user, reason and time are required: %w", model.ErrInvalid)
	}
	setting := model.ApprovalSetting{Policy: command.Policy}
	if err := setting.Validate(); err != nil {
		return ApprovalPolicyResult{}, err
	}
	project, err := s.Project(ctx, command.ProjectID, model.InitialRevision)
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
	committed, err := s.changes.CommitUser(ctx, model.Proposal{
		ID:           command.ChangeID,
		Target:       model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID},
		BaseRevision: project.Revision,
		Author:       model.Author{Kind: model.AuthorUser, ID: command.UserID},
		Reason:       command.Reason,
		Patches: []model.Patch{{
			Document:  model.DocumentRef{Kind: model.DocumentApproval, ID: "root"},
			Operation: model.PatchPut, Content: content,
		}},
		ApprovalState: model.ApprovalPending, CreatedAt: command.CreatedAt,
	}, command.CreatedAt)
	if err != nil {
		return ApprovalPolicyResult{}, err
	}
	return ApprovalPolicyResult{ProjectID: command.ProjectID, Revision: committed.NewRevision, Policy: command.Policy}, nil
}

func (s *Repository) ownershipResult(
	ctx context.Context,
	target model.AuthorityTarget,
	revision model.Revision,
) (OwnershipResult, error) {
	ownership, err := LoadDocuments[model.OwnershipRule](ctx, s.store, target, model.DocumentOwnership, revision)
	if err != nil {
		return OwnershipResult{}, err
	}
	return OwnershipResult{ProjectID: target.ID, Revision: revision, Ownership: ownership}, nil
}
