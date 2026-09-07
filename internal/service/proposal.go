package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func (s *Service) Propose(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	return s.changes.Prepare(ctx, proposal)
}

func (s *Service) ProposeWithSemantic(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	return s.changes.PrepareWithSemantic(ctx, proposal)
}

func (s *Service) Proposal(ctx context.Context, id string) (domain.Proposal, error) {
	return s.store.GetProposal(ctx, id)
}

func (s *Service) ProposalForOperation(ctx context.Context, operationID string) (domain.Proposal, error) {
	return s.store.GetProposalByOperation(ctx, operationID)
}

type ResolveProposalCommand struct {
	ProposalID          string
	UserID              string
	Strategy            string
	Reason              string
	Packs               []PackRef
	CreatorProfiles     []CreatorProfileRef
	CoreProtocolVersion string
	ModelConfigDigest   string
	RunID               string
	CreatedAt           time.Time
}

type ResolveProposalResult struct {
	Strategy   string             `json:"strategy"`
	Proposal   *domain.Proposal   `json:"proposal,omitempty"`
	ChangeSet  *domain.ChangeSet  `json:"change_set,omitempty"`
	Operations []domain.Operation `json:"operations,omitempty"`
}

func (s *Service) ResolveProposal(ctx context.Context, command ResolveProposalCommand) (ResolveProposalResult, error) {
	if command.ProposalID == "" || command.UserID == "" || command.CreatedAt.IsZero() {
		return ResolveProposalResult{}, fmt.Errorf("proposal resolution requires proposal, user and time: %w", domain.ErrInvalid)
	}
	proposal, err := s.store.GetProposal(ctx, command.ProposalID)
	if err != nil {
		return ResolveProposalResult{}, err
	}
	strategy := change.ResolutionStrategy(command.Strategy)
	report, option, err := semanticResolutionOption(proposal, strategy)
	if err != nil {
		return ResolveProposalResult{}, err
	}
	if report.Status == change.SemanticImpactConsistent {
		return ResolveProposalResult{}, fmt.Errorf("consistent proposal does not require a resolution strategy; approve it directly: %w", domain.ErrInvalid)
	}
	result := ResolveProposalResult{Strategy: command.Strategy}
	if strategy == change.ResolutionAbandon {
		rejected, err := s.Reject(ctx, proposal.ID, command.UserID, command.Reason, command.CreatedAt)
		if err != nil {
			return ResolveProposalResult{}, err
		}
		result.Proposal = &rejected
		return result, nil
	}
	if strategy == change.ResolutionRewriteAffected && command.ModelConfigDigest == "" && s.executor == nil {
		return ResolveProposalResult{}, fmt.Errorf("affected rewrite requires a configured runtime or model config digest: %w", domain.ErrInvalid)
	}
	if strategy == change.ResolutionRewriteAffected && strings.TrimSpace(command.RunID) == "" {
		return ResolveProposalResult{}, fmt.Errorf("affected rewrite requires a creation run: %w", domain.ErrInvalid)
	}
	if strategy == change.ResolutionRewriteAffected {
		for _, ref := range command.Packs {
			if _, err := loadPack(ctx, s.store, ref); err != nil {
				return ResolveProposalResult{}, err
			}
		}
		for _, ref := range command.CreatorProfiles {
			if _, err := loadCreatorProfile(ctx, s.store, ref); err != nil {
				return ResolveProposalResult{}, err
			}
		}
	}
	committed, err := s.Approve(ctx, proposal.ID, command.UserID, command.CreatedAt)
	if err != nil {
		return ResolveProposalResult{}, err
	}
	result.ChangeSet = &committed
	if strategy == change.ResolutionReinterpretFuture {
		return result, nil
	}
	operationID := proposal.ID + ":rewrite-affected"
	existing, err := s.store.GetOperation(ctx, operationID)
	if err == nil {
		if err := validateResolutionOperation(existing, proposal.ID, option.ChapterIDs, committed.NewRevision, command.RunID); err != nil {
			return result, err
		}
		result.Operations = []domain.Operation{existing}
		return result, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return result, err
	}

	coreVersion := command.CoreProtocolVersion
	if coreVersion == "" {
		coreVersion = "core-v1"
	}
	input, err := json.Marshal(struct {
		ChapterIDs           []string        `json:"chapter_ids"`
		BaseRevision         domain.Revision `json:"base_revision"`
		ResolutionProposalID string          `json:"resolution_proposal_id"`
		Reason               string          `json:"reason"`
	}{
		ChapterIDs: option.ChapterIDs, BaseRevision: committed.NewRevision,
		ResolutionProposalID: proposal.ID, Reason: option.Explanation,
	})
	if err != nil {
		return ResolveProposalResult{}, fmt.Errorf("encode affected rewrite task: %w", err)
	}
	operation, err := s.StartOperation(ctx, StartOperationCommand{
		OperationID: operationID, ProjectID: proposal.Target.ID,
		Kind:  domain.OperationRewriteAffected,
		Input: input, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
		CoreProtocolVersion: coreVersion, ModelConfigDigest: command.ModelConfigDigest,
		ApprovalPolicy: domain.ApprovalManual,
		RunID:          command.RunID, CreatedAt: *committed.DecidedAt,
	})
	if err != nil {
		return result, fmt.Errorf("proposal was committed but affected rewrite operation could not be created; retry the same resolution: %w", err)
	}
	result.Operations = []domain.Operation{operation}
	return result, nil
}

func validateResolutionOperation(
	operation domain.Operation,
	proposalID string,
	chapterIDs []string,
	baseRevision domain.Revision,
	runID string,
) error {
	if operation.Kind != domain.OperationRewriteAffected || operation.Snapshot.BaseRevision != baseRevision ||
		operation.RunID != runID {
		return fmt.Errorf("operation %q is not the requested semantic resolution: %w", operation.ID, store.ErrIdempotencyConflict)
	}
	var input struct {
		ChapterIDs           []string        `json:"chapter_ids"`
		BaseRevision         domain.Revision `json:"base_revision"`
		ResolutionProposalID string          `json:"resolution_proposal_id"`
		Reason               string          `json:"reason"`
	}
	if err := json.Unmarshal(operation.Input, &input); err != nil || input.ResolutionProposalID != proposalID ||
		input.BaseRevision != baseRevision || !slices.Equal(input.ChapterIDs, chapterIDs) {
		return fmt.Errorf("operation %q is not the requested semantic resolution: %w", operation.ID, store.ErrIdempotencyConflict)
	}
	return nil
}

func semanticResolutionOption(
	proposal domain.Proposal,
	strategy change.ResolutionStrategy,
) (change.SemanticImpactReport, change.ResolutionOption, error) {
	if strategy != change.ResolutionRewriteAffected && strategy != change.ResolutionReinterpretFuture && strategy != change.ResolutionAbandon {
		return change.SemanticImpactReport{}, change.ResolutionOption{}, fmt.Errorf("unknown resolution strategy %q: %w", strategy, domain.ErrInvalid)
	}
	if len(proposal.Impact.Semantic) == 0 {
		return change.SemanticImpactReport{}, change.ResolutionOption{}, fmt.Errorf("proposal %q has no semantic impact report: %w", proposal.ID, domain.ErrInvalid)
	}
	var report change.SemanticImpactReport
	if err := domain.DecodeStrict(proposal.Impact.Semantic, &report); err != nil {
		return change.SemanticImpactReport{}, change.ResolutionOption{}, fmt.Errorf("decode semantic impact report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return change.SemanticImpactReport{}, change.ResolutionOption{}, err
	}
	for _, option := range report.Options {
		if option.Strategy == strategy {
			return report, option, nil
		}
	}
	return change.SemanticImpactReport{}, change.ResolutionOption{}, fmt.Errorf("semantic impact does not offer strategy %q: %w", strategy, domain.ErrInvalid)
}

func (s *Service) Approve(ctx context.Context, proposalID, userID string, at time.Time) (domain.ChangeSet, error) {
	proposal, err := s.store.GetProposal(ctx, proposalID)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	var committed domain.ChangeSet
	switch proposal.ApprovalState {
	case domain.ApprovalApproved:
		committed, err = s.store.GetChangeSet(ctx, proposal.ID)
	case domain.ApprovalRejected:
		return domain.ChangeSet{}, fmt.Errorf("proposal %q was rejected: %w", proposal.ID, store.ErrStateConflict)
	case domain.ApprovalPending:
		var approved domain.Proposal
		approved, err = change.Decide(proposal, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorUser, ID: userID}, at)
		if err == nil {
			committed, err = s.changes.Commit(ctx, approved)
		}
	}
	if err != nil {
		return domain.ChangeSet{}, err
	}
	if proposal.OperationID != "" {
		if err := s.finishApprovedOperation(ctx, proposal.OperationID, at); err != nil {
			return domain.ChangeSet{}, err
		}
	}
	return committed, nil
}

func (s *Service) Reject(ctx context.Context, proposalID, userID, reason string, at time.Time) (domain.Proposal, error) {
	proposal, err := s.store.GetProposal(ctx, proposalID)
	if err != nil {
		return domain.Proposal{}, err
	}
	var rejected domain.Proposal
	switch proposal.ApprovalState {
	case domain.ApprovalRejected:
		rejected = proposal
	case domain.ApprovalApproved:
		return domain.Proposal{}, fmt.Errorf("proposal %q was approved: %w", proposal.ID, store.ErrStateConflict)
	case domain.ApprovalPending:
		rejected, err = change.Decide(proposal, domain.ApprovalRejected, domain.Author{Kind: domain.AuthorUser, ID: userID}, at)
		if err == nil {
			rejected.DecisionReason = strings.TrimSpace(reason)
			rejected, err = s.changes.Reject(ctx, rejected)
		}
	}
	if err != nil {
		return domain.Proposal{}, err
	}
	if proposal.OperationID != "" {
		operation, err := s.store.GetOperation(ctx, proposal.OperationID)
		if err != nil {
			return domain.Proposal{}, err
		}
		if operation.State == domain.OperationAwaitingApproval {
			if _, err := s.store.TransitionOperation(
				ctx, operation.ID, operation.State, domain.OperationFailed,
				"proposal rejected by user", at,
			); err != nil {
				return domain.Proposal{}, err
			}
		} else if operation.State != domain.OperationFailed {
			return domain.Proposal{}, fmt.Errorf("operation %q is %s: %w", operation.ID, operation.State, store.ErrStateConflict)
		}
		if err := s.rejectionDirective(ctx, rejected, operation, userID, at); err != nil {
			return domain.Proposal{}, err
		}
	}
	return rejected, nil
}

// rejectionDirective 把否决理由入账为 Directive（§4.9 / S10）：章节任务落到该章
// Plan 节点，其余落到整书；后继按当前快照装配到它，审阅逐条核验。幂等：
// 同一提案重复否决沿用同一 ChangeID 与已记录的理由。
func (s *Service) rejectionDirective(
	ctx context.Context,
	rejected domain.Proposal,
	operation domain.Operation,
	userID string,
	at time.Time,
) error {
	if rejected.DecisionReason == "" || rejected.Target.Kind != domain.AuthorityProject {
		return nil
	}
	scope := domain.DirectiveScopeProject
	var input struct {
		ChapterPlanID string `json:"chapter_plan_id"`
	}
	if json.Unmarshal(operation.Input, &input) == nil && input.ChapterPlanID != "" {
		scope = domain.DirectiveScopePlanNode(input.ChapterPlanID)
	}
	_, err := s.AddDirective(ctx, AddDirectiveCommand{
		ProjectID: rejected.Target.ID, ChangeID: rejected.ID + ":directive", UserID: userID,
		DirectiveID: rejected.ID + ":directive", Scope: scope, Text: rejected.DecisionReason,
		Reason: "否决候选 " + rejected.ID + " 的理由", CreatedAt: at,
	})
	return err
}

func (s *Service) finishApprovedOperation(ctx context.Context, operationID string, at time.Time) error {
	operation, err := s.store.GetOperation(ctx, operationID)
	if err != nil {
		return err
	}
	switch operation.State {
	case domain.OperationSucceeded:
		return nil
	case domain.OperationAwaitingApproval:
		_, err = s.store.TransitionOperation(ctx, operation.ID, operation.State, domain.OperationSucceeded, "", at)
		return err
	default:
		return fmt.Errorf("operation %q is %s: %w", operation.ID, operation.State, store.ErrStateConflict)
	}
}

func (s *Service) PrepareRevert(
	ctx context.Context,
	proposalID, projectID, userID, reason string,
	to domain.Revision,
	createdAt time.Time,
) (domain.Proposal, error) {
	return s.changes.PrepareRevert(
		ctx, proposalID,
		domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: projectID}, to,
		domain.Author{Kind: domain.AuthorUser, ID: userID}, reason, createdAt,
	)
}

func (s *Service) commitUserProposal(
	ctx context.Context,
	requested domain.Proposal,
	decidedAt time.Time,
) (domain.ChangeSet, error) {
	if requested.Author.Kind != domain.AuthorUser || requested.ApprovalState != domain.ApprovalPending {
		return domain.ChangeSet{}, fmt.Errorf("direct user commit requires a pending user proposal: %w", domain.ErrInvalid)
	}
	prepared, err := s.store.GetProposal(ctx, requested.ID)
	if errors.Is(err, store.ErrNotFound) {
		prepared, err = s.changes.Prepare(ctx, requested)
	} else if err == nil {
		same, compareErr := sameProposalRequest(requested, prepared)
		if compareErr != nil {
			return domain.ChangeSet{}, compareErr
		}
		if !same {
			return domain.ChangeSet{}, fmt.Errorf("proposal %q: %w", requested.ID, store.ErrIdempotencyConflict)
		}
	} else {
		return domain.ChangeSet{}, err
	}
	if err != nil {
		return domain.ChangeSet{}, err
	}
	switch prepared.ApprovalState {
	case domain.ApprovalApproved:
		return s.store.GetChangeSet(ctx, prepared.ID)
	case domain.ApprovalRejected:
		return domain.ChangeSet{}, fmt.Errorf("proposal %q was rejected: %w", prepared.ID, store.ErrStateConflict)
	case domain.ApprovalPending:
		approved, err := change.Decide(prepared, domain.ApprovalApproved, requested.Author, decidedAt)
		if err != nil {
			return domain.ChangeSet{}, err
		}
		return s.changes.Commit(ctx, approved)
	default:
		return domain.ChangeSet{}, fmt.Errorf("proposal %q has invalid state %q: %w", prepared.ID, prepared.ApprovalState, store.ErrStateConflict)
	}
}

func sameProposalRequest(left, right domain.Proposal) (bool, error) {
	normalize := func(proposal domain.Proposal) ([]byte, error) {
		proposal.Impact = domain.ImpactReport{}
		proposal.ApprovalState = domain.ApprovalPending
		proposal.DecidedBy = nil
		proposal.DecidedAt = nil
		proposal.CreatedAt = time.Time{}
		return json.Marshal(proposal)
	}
	leftPayload, err := normalize(left)
	if err != nil {
		return false, fmt.Errorf("encode requested proposal identity: %w", err)
	}
	rightPayload, err := normalize(right)
	if err != nil {
		return false, fmt.Errorf("encode stored proposal identity: %w", err)
	}
	return bytes.Equal(leftPayload, rightPayload), nil
}
