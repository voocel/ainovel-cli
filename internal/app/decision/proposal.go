package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"

	"github.com/voocel/ainovel-cli/internal/app/task"
)

func (s *Review) Propose(ctx context.Context, proposal model.Proposal) (model.Proposal, error) {
	return s.changes.Prepare(ctx, proposal)
}

func (s *Review) ProposeWithSemantic(ctx context.Context, proposal model.Proposal) (model.Proposal, error) {
	return s.changes.PrepareWithSemantic(ctx, proposal)
}

func (s *Review) Proposal(ctx context.Context, id string) (model.Proposal, error) {
	return s.store.GetProposal(ctx, id)
}

func (s *Review) ProposalForOperation(ctx context.Context, operationID string) (model.Proposal, error) {
	return s.store.GetProposalByOperation(ctx, operationID)
}

type ResolveProposalCommand struct {
	ProposalID          string
	UserID              string
	Strategy            string
	Reason              string
	Packs               []resource.PackRef
	CreatorProfiles     []resource.CreatorProfileRef
	CoreProtocolVersion string
	RunID               string
	CreatedAt           time.Time
}

type ResolveProposalResult struct {
	Strategy   string            `json:"strategy"`
	Proposal   *model.Proposal   `json:"proposal,omitempty"`
	ChangeSet  *model.ChangeSet  `json:"change_set,omitempty"`
	Operations []model.Operation `json:"operations,omitempty"`
}

func (s *Review) ResolveProposal(ctx context.Context, command ResolveProposalCommand) (ResolveProposalResult, error) {
	if command.ProposalID == "" || command.UserID == "" || command.CreatedAt.IsZero() {
		return ResolveProposalResult{}, fmt.Errorf("proposal resolution requires proposal, user and time: %w", model.ErrInvalid)
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
		return ResolveProposalResult{}, fmt.Errorf("consistent proposal does not require a resolution strategy; approve it directly: %w", model.ErrInvalid)
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
	if strategy == change.ResolutionRewriteAffected && !s.tasks.HasLLM() {
		return ResolveProposalResult{}, fmt.Errorf("affected rewrite requires a configured model: %w", model.ErrInvalid)
	}
	if strategy == change.ResolutionRewriteAffected && strings.TrimSpace(command.RunID) == "" {
		return ResolveProposalResult{}, fmt.Errorf("affected rewrite requires a creation run: %w", model.ErrInvalid)
	}
	if strategy == change.ResolutionRewriteAffected {
		for _, ref := range command.Packs {
			if _, err := resource.LoadPack(ctx, s.store, ref); err != nil {
				return ResolveProposalResult{}, err
			}
		}
		for _, ref := range command.CreatorProfiles {
			if _, err := resource.LoadCreatorProfile(ctx, s.store, ref); err != nil {
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
		result.Operations = []model.Operation{existing}
		return result, nil
	}
	if !errors.Is(err, model.ErrNotFound) {
		return result, err
	}

	coreVersion := command.CoreProtocolVersion
	if coreVersion == "" {
		coreVersion = "core-v1"
	}
	input, err := json.Marshal(model.RewriteAffectedInput{
		ChapterIDs: option.ChapterIDs, BaseRevision: committed.NewRevision,
		ResolutionProposalID: proposal.ID, Reason: option.Explanation,
	})
	if err != nil {
		return ResolveProposalResult{}, fmt.Errorf("encode affected rewrite task: %w", err)
	}
	operation, err := s.tasks.StartOperation(ctx, task.StartOperationCommand{
		OperationID: operationID, ProjectID: proposal.Target.ID,
		Kind:  model.OperationRewriteAffected,
		Input: input, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
		CoreProtocolVersion: coreVersion, ApprovalPolicy: model.ApprovalManual,
		RunID: command.RunID, CreatedAt: *committed.DecidedAt,
	})
	if err != nil {
		return result, fmt.Errorf("proposal was committed but affected rewrite operation could not be created; retry the same resolution: %w", err)
	}
	result.Operations = []model.Operation{operation}
	return result, nil
}

func validateResolutionOperation(
	operation model.Operation,
	proposalID string,
	chapterIDs []string,
	baseRevision model.Revision,
	runID string,
) error {
	if operation.Kind != model.OperationRewriteAffected || operation.Snapshot.BaseRevision != baseRevision ||
		operation.RunID != runID {
		return fmt.Errorf("operation %q is not the requested semantic resolution: %w", operation.ID, model.ErrIdempotencyConflict)
	}
	input, err := model.TaskInputAs[model.RewriteAffectedInput](operation)
	if err != nil || input.ResolutionProposalID != proposalID ||
		input.BaseRevision != baseRevision || !slices.Equal(input.ChapterIDs, chapterIDs) {
		return fmt.Errorf("operation %q is not the requested semantic resolution: %w", operation.ID, model.ErrIdempotencyConflict)
	}
	return nil
}

func semanticResolutionOption(
	proposal model.Proposal,
	strategy change.ResolutionStrategy,
) (change.SemanticImpactReport, change.ResolutionOption, error) {
	if strategy != change.ResolutionRewriteAffected && strategy != change.ResolutionReinterpretFuture && strategy != change.ResolutionAbandon {
		return change.SemanticImpactReport{}, change.ResolutionOption{}, fmt.Errorf("unknown resolution strategy %q: %w", strategy, model.ErrInvalid)
	}
	if len(proposal.Impact.Semantic) == 0 {
		return change.SemanticImpactReport{}, change.ResolutionOption{}, fmt.Errorf("proposal %q has no semantic impact report: %w", proposal.ID, model.ErrInvalid)
	}
	var report change.SemanticImpactReport
	if err := model.DecodeStrict(proposal.Impact.Semantic, &report); err != nil {
		return change.SemanticImpactReport{}, change.ResolutionOption{}, fmt.Errorf("decode semantic impact report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return change.SemanticImpactReport{}, change.ResolutionOption{}, err
	}
	// 一致的报告没有候选策略，先返回让调用方给出“直接批准即可”，不要误报成策略不存在。
	if report.Status == change.SemanticImpactConsistent {
		return report, change.ResolutionOption{}, nil
	}
	for _, option := range report.Options {
		if option.Strategy == strategy {
			return report, option, nil
		}
	}
	return change.SemanticImpactReport{}, change.ResolutionOption{}, fmt.Errorf("semantic impact does not offer strategy %q: %w", strategy, model.ErrInvalid)
}

func (s *Review) Approve(ctx context.Context, proposalID, userID string, at time.Time) (model.ChangeSet, error) {
	proposal, err := s.store.GetProposal(ctx, proposalID)
	if err != nil {
		return model.ChangeSet{}, err
	}
	var committed model.ChangeSet
	switch proposal.ApprovalState {
	case model.ApprovalApproved:
		committed, err = s.store.GetChangeSet(ctx, proposal.ID)
	case model.ApprovalRejected:
		return model.ChangeSet{}, fmt.Errorf("proposal %q was rejected: %w", proposal.ID, model.ErrStateConflict)
	case model.ApprovalPending:
		committed, err = s.commitPending(ctx, proposal, userID, at)
	}
	if err != nil {
		return model.ChangeSet{}, err
	}
	if proposal.OperationID != "" {
		if err := s.finishApprovedOperation(ctx, proposal.OperationID, at); err != nil {
			return model.ChangeSet{}, err
		}
	}
	return committed, nil
}

// commitPending 由用户批准并提交待裁决提案：任务提案先按 D51 重定位到当前 Revision，
// 不能重定位的原样返回 ErrRevisionConflict，由用户按工作台指引重写。
func (s *Review) commitPending(ctx context.Context, proposal model.Proposal, userID string, at time.Time) (model.ChangeSet, error) {
	relocated, moved, err := s.RelocateProposal(ctx, proposal)
	if err != nil {
		return model.ChangeSet{}, err
	}
	if moved {
		if err := s.store.RelocateProposal(ctx, relocated, 0, at); err != nil {
			return model.ChangeSet{}, err
		}
		proposal = relocated
	}
	approved, err := change.Decide(proposal, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: userID}, at)
	if err != nil {
		return model.ChangeSet{}, err
	}
	return s.changes.Commit(ctx, approved)
}

// RelocateProposal 按所属任务的基线重定位提案（D51），不落库；用户直接发起的提案
// 没有任务基线，基线落后即冲突。
func (s *Review) RelocateProposal(ctx context.Context, proposal model.Proposal) (model.Proposal, bool, error) {
	var basis model.EvidenceBasis
	if proposal.OperationID != "" {
		operation, err := s.store.GetOperation(ctx, proposal.OperationID)
		if err != nil {
			return model.Proposal{}, false, err
		}
		if basis, err = model.OperationBasis(operation); err != nil {
			return model.Proposal{}, false, err
		}
	} else {
		return proposal, false, nil
	}
	return s.changes.Relocate(ctx, proposal, basis)
}

func (s *Review) Reject(ctx context.Context, proposalID, userID, reason string, at time.Time) (model.Proposal, error) {
	proposal, err := s.store.GetProposal(ctx, proposalID)
	if err != nil {
		return model.Proposal{}, err
	}
	var rejected model.Proposal
	switch proposal.ApprovalState {
	case model.ApprovalRejected:
		rejected = proposal
	case model.ApprovalApproved:
		return model.Proposal{}, fmt.Errorf("proposal %q was approved: %w", proposal.ID, model.ErrStateConflict)
	case model.ApprovalPending:
		rejected, err = change.Decide(proposal, model.ApprovalRejected, model.Author{Kind: model.AuthorUser, ID: userID}, at)
		if err == nil {
			rejected.DecisionReason = strings.TrimSpace(reason)
			rejected, err = s.changes.Reject(ctx, rejected)
		}
	}
	if err != nil {
		return model.Proposal{}, err
	}
	if proposal.OperationID != "" {
		operation, err := s.store.GetOperation(ctx, proposal.OperationID)
		if err != nil {
			return model.Proposal{}, err
		}
		if operation.State == model.OperationAwaitingApproval {
			if _, err := s.store.TransitionOperation(
				ctx, operation.ID, operation.State, model.OperationFailed,
				"proposal rejected by user", at,
			); err != nil {
				return model.Proposal{}, err
			}
		} else if operation.State != model.OperationFailed {
			return model.Proposal{}, fmt.Errorf("operation %q is %s: %w", operation.ID, operation.State, model.ErrStateConflict)
		}
		if err := s.rejectionDirective(ctx, rejected, operation, userID, at); err != nil {
			return model.Proposal{}, err
		}
	}
	return rejected, nil
}

// rejectionDirective 把否决理由入账为 Directive（§4.9 / S10）：章节任务落到该章
// Plan 节点，其余落到整书；后继按当前快照装配到它，审阅逐条核验。幂等：
// 同一提案重复否决沿用同一 ChangeID 与已记录的理由。
func (s *Review) rejectionDirective(
	ctx context.Context,
	rejected model.Proposal,
	operation model.Operation,
	userID string,
	at time.Time,
) error {
	if rejected.DecisionReason == "" || rejected.Target.Kind != model.AuthorityProject {
		return nil
	}
	scope := model.DirectiveScopeProject
	if planID := chapterPlanIDOf(operation); planID != "" {
		scope = model.DirectiveScopePlanNode(planID)
	}
	_, err := s.projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
		ProjectID: rejected.Target.ID, ChangeID: rejected.ID + ":directive", UserID: userID,
		DirectiveID: rejected.ID + ":directive", Scope: scope, Text: rejected.DecisionReason,
		Reason: "否决候选 " + rejected.ID + " 的理由", CreatedAt: at,
	})
	return err
}

// chapterPlanIDOf 取章节任务对应的 Plan 节点；非章节任务为空。
func chapterPlanIDOf(operation model.Operation) string {
	input, err := model.DecodeTaskInput(operation.Kind, operation.Input)
	if err != nil {
		return ""
	}
	switch input := input.(type) {
	case *model.WriteChapterInput:
		return input.ChapterPlanID
	case *model.RewriteChapterInput:
		return input.ChapterPlanID
	default:
		return ""
	}
}

func (s *Review) finishApprovedOperation(ctx context.Context, operationID string, at time.Time) error {
	operation, err := s.store.GetOperation(ctx, operationID)
	if err != nil {
		return err
	}
	switch operation.State {
	case model.OperationSucceeded:
		return nil
	case model.OperationAwaitingApproval:
		_, err = s.store.TransitionOperation(ctx, operation.ID, operation.State, model.OperationSucceeded, "", at)
		return err
	default:
		return fmt.Errorf("operation %q is %s: %w", operation.ID, operation.State, model.ErrStateConflict)
	}
}

func (s *Review) PrepareRevert(
	ctx context.Context,
	proposalID, projectID, userID, reason string,
	to model.Revision,
	createdAt time.Time,
) (model.Proposal, error) {
	return s.changes.PrepareRevert(
		ctx, proposalID,
		model.AuthorityTarget{Kind: model.AuthorityProject, ID: projectID}, to,
		model.Author{Kind: model.AuthorUser, ID: userID}, reason, createdAt,
	)
}
