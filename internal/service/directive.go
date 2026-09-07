package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type AddDirectiveCommand struct {
	ProjectID string
	ChangeID  string
	UserID    string
	// DirectiveID 为空时按创建时间生成。
	DirectiveID string
	Scope       string
	Text        string
	Constraints *domain.DirectiveConstraints
	Reason      string
	CreatedAt   time.Time
}

type RetireDirectiveCommand struct {
	ProjectID   string
	ChangeID    string
	UserID      string
	DirectiveID string
	Reason      string
	CreatedAt   time.Time
}

type DirectiveResult struct {
	ProjectID string           `json:"project_id"`
	Revision  domain.Revision  `json:"revision"`
	Directive domain.Directive `json:"directive"`
}

// AddDirective 记录一条用户创作要求（§4.9）：原话保留、带作用域入账，作者只能是
// 用户，走唯一写路径；同 ID 同内容重复提交幂等，不产生新 Revision。
func (s *Service) AddDirective(ctx context.Context, command AddDirectiveCommand) (DirectiveResult, error) {
	if err := validateDirectiveCommand(command.ProjectID, command.ChangeID, command.UserID, command.Reason, command.CreatedAt); err != nil {
		return DirectiveResult{}, err
	}
	id := command.DirectiveID
	if strings.TrimSpace(id) == "" {
		id = NewID("directive", command.CreatedAt)
	}
	directive := domain.Directive{
		ID: id, Scope: strings.TrimSpace(command.Scope), Text: strings.TrimSpace(command.Text),
		Constraints: command.Constraints, Status: domain.DirectiveActive,
	}
	if err := directive.Validate(); err != nil {
		return DirectiveResult{}, err
	}
	return s.putDirective(ctx, command.ProjectID, command.ChangeID, command.UserID, command.Reason, directive, command.CreatedAt)
}

// RetireDirective 退役一条要求：不删除文档，历史 Revision 仍可查它曾对哪些章生效。
func (s *Service) RetireDirective(ctx context.Context, command RetireDirectiveCommand) (DirectiveResult, error) {
	if err := validateDirectiveCommand(command.ProjectID, command.ChangeID, command.UserID, command.Reason, command.CreatedAt); err != nil {
		return DirectiveResult{}, err
	}
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return DirectiveResult{}, err
	}
	existing, err := s.store.GetDocument(ctx, target, domain.DocumentRef{Kind: domain.DocumentDirective, ID: command.DirectiveID}, revision)
	if err != nil {
		return DirectiveResult{}, err
	}
	var directive domain.Directive
	if err := json.Unmarshal(existing.Content, &directive); err != nil {
		return DirectiveResult{}, fmt.Errorf("decode directive: %w", err)
	}
	directive.Status = domain.DirectiveRetired
	return s.putDirective(ctx, command.ProjectID, command.ChangeID, command.UserID, command.Reason, directive, command.CreatedAt)
}

func (s *Service) putDirective(
	ctx context.Context,
	projectID, changeID, userID, reason string,
	directive domain.Directive,
	at time.Time,
) (DirectiveResult, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: projectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return DirectiveResult{}, err
	}
	content, err := json.Marshal(directive)
	if err != nil {
		return DirectiveResult{}, fmt.Errorf("encode directive: %w", err)
	}
	ref := domain.DocumentRef{Kind: domain.DocumentDirective, ID: directive.ID}
	existing, err := s.store.GetDocument(ctx, target, ref, revision)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return DirectiveResult{}, err
	}
	if err == nil && string(existing.Content) == string(content) {
		return DirectiveResult{ProjectID: projectID, Revision: revision, Directive: directive}, nil
	}
	committed, err := s.commitUserProposal(ctx, domain.Proposal{
		ID: changeID, Target: target, BaseRevision: revision,
		Author: domain.Author{Kind: domain.AuthorUser, ID: userID},
		Reason: reason, Patches: []domain.Patch{{Document: ref, Operation: domain.PatchPut, Content: content}},
		ApprovalState: domain.ApprovalPending, CreatedAt: at,
	}, at)
	if err != nil {
		return DirectiveResult{}, err
	}
	return DirectiveResult{ProjectID: projectID, Revision: committed.NewRevision, Directive: directive}, nil
}

func validateDirectiveCommand(projectID, changeID, userID, reason string, at time.Time) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(changeID) == "" ||
		strings.TrimSpace(userID) == "" || strings.TrimSpace(reason) == "" || at.IsZero() {
		return fmt.Errorf("directive change project, change, user, reason and time are required: %w", domain.ErrInvalid)
	}
	return nil
}
