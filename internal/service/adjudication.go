package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type AddAdjudicationCommand struct {
	ProjectID string
	ChangeID  string
	UserID    string
	// Finding 是发现标识：<裁定 Operation id>/<发现序号>（工作台与 headless 均按此引用）。
	Finding   string
	Reason    string
	CreatedAt time.Time
}

type WithdrawAdjudicationCommand struct {
	ProjectID      string
	ChangeID       string
	UserID         string
	AdjudicationID string
	Reason         string
	CreatedAt      time.Time
}

type AdjudicationResult struct {
	ProjectID    string              `json:"project_id"`
	Revision     domain.Revision     `json:"revision"`
	Adjudication domain.Adjudication `json:"adjudication"`
}

// AddAdjudication 记录用户对一条阻塞发现的接受（D43）：基线从被裁决的裁定复制，
// 用户不构造；裁定必须仍然有效。记录 ID = ChangeID，重复提交幂等。
func (s *Service) AddAdjudication(ctx context.Context, command AddAdjudicationCommand) (AdjudicationResult, error) {
	if err := validateDirectiveCommand(command.ProjectID, command.ChangeID, command.UserID, command.Reason, command.CreatedAt); err != nil {
		return AdjudicationResult{}, err
	}
	operationID, index, err := domain.ParseFindingID(command.Finding)
	if err != nil {
		return AdjudicationResult{}, err
	}
	project, err := s.Project(ctx, command.ProjectID, 0)
	if err != nil {
		return AdjudicationResult{}, err
	}
	verdicts, err := s.rawVerdicts(ctx, project)
	if err != nil {
		return AdjudicationResult{}, err
	}
	var findings []domain.ReviewFinding
	var basis domain.EvidenceBasis
	for _, verdict := range verdicts {
		if verdict.key == operationID {
			findings, basis = verdict.verdict.Findings, verdict.verdict.Basis
			break
		}
	}
	if findings == nil {
		return AdjudicationResult{}, fmt.Errorf("finding %q belongs to no valid review verdict: %w", command.Finding, domain.ErrInvalid)
	}
	if index >= len(findings) || findings[index].Severity != domain.FindingBlocking {
		return AdjudicationResult{}, fmt.Errorf("finding %q is not a blocking finding: %w", command.Finding, domain.ErrInvalid)
	}
	return s.putAdjudication(ctx, project, command.UserID, domain.Adjudication{
		ID: command.ChangeID, Finding: command.Finding, Basis: basis,
		Reason: strings.TrimSpace(command.Reason), CreatedAt: command.CreatedAt,
	})
}

// WithdrawAdjudication 以新记录撤回一条接受：历史不变，有效性改变。
func (s *Service) WithdrawAdjudication(ctx context.Context, command WithdrawAdjudicationCommand) (AdjudicationResult, error) {
	if err := validateDirectiveCommand(command.ProjectID, command.ChangeID, command.UserID, command.Reason, command.CreatedAt); err != nil {
		return AdjudicationResult{}, err
	}
	project, err := s.Project(ctx, command.ProjectID, 0)
	if err != nil {
		return AdjudicationResult{}, err
	}
	var target *domain.Adjudication
	for index := range project.Adjudications {
		record := &project.Adjudications[index]
		switch {
		case record.ID == command.AdjudicationID:
			target = record
		case record.Withdraws == command.AdjudicationID:
			return AdjudicationResult{}, fmt.Errorf("adjudication %q is already withdrawn: %w", command.AdjudicationID, store.ErrStateConflict)
		}
	}
	if target == nil {
		return AdjudicationResult{}, fmt.Errorf("adjudication %q: %w", command.AdjudicationID, store.ErrNotFound)
	}
	if target.Finding == "" {
		return AdjudicationResult{}, fmt.Errorf("adjudication %q is a withdrawal record: %w", command.AdjudicationID, domain.ErrInvalid)
	}
	return s.putAdjudication(ctx, project, command.UserID, domain.Adjudication{
		ID: command.ChangeID, Withdraws: target.ID, Reason: strings.TrimSpace(command.Reason), CreatedAt: command.CreatedAt,
	})
}

func (s *Service) putAdjudication(ctx context.Context, project ProjectSnapshot, userID string, record domain.Adjudication) (AdjudicationResult, error) {
	if err := record.Validate(); err != nil {
		return AdjudicationResult{}, err
	}
	// 只追加：同 ID 已存在即同一记录，重试不产生新 Revision。
	for _, existing := range project.Adjudications {
		if existing.ID == record.ID {
			return AdjudicationResult{ProjectID: project.ID, Revision: project.Revision, Adjudication: existing}, nil
		}
	}
	content, err := json.Marshal(record)
	if err != nil {
		return AdjudicationResult{}, fmt.Errorf("encode adjudication: %w", err)
	}
	committed, err := s.commitUserProposal(ctx, domain.Proposal{
		ID: record.ID, Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: project.ID}, BaseRevision: project.Revision,
		Author: domain.Author{Kind: domain.AuthorUser, ID: userID}, Reason: record.Reason,
		Patches: []domain.Patch{{
			Document: domain.DocumentRef{Kind: domain.DocumentAdjudication, ID: record.ID}, Operation: domain.PatchPut, Content: content,
		}},
		ApprovalState: domain.ApprovalPending, CreatedAt: record.CreatedAt,
	}, record.CreatedAt)
	if err != nil {
		return AdjudicationResult{}, err
	}
	return AdjudicationResult{ProjectID: project.ID, Revision: committed.NewRevision, Adjudication: record}, nil
}

// validAdjudications 取基线仍成立的裁决记录（撤回记录无基线，恒有效）；
// 接受了哪些发现由 domain.AcceptedFindings 归并。
func (s *Service) validAdjudications(ctx context.Context, project ProjectSnapshot) ([]domain.Adjudication, error) {
	var valid []domain.Adjudication
	for _, record := range project.Adjudications {
		stale, err := s.staleBasis(ctx, project, record.Basis)
		if err != nil {
			return nil, err
		}
		if len(stale) == 0 {
			valid = append(valid, record)
		}
	}
	return valid, nil
}
