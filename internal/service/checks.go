package service

import (
	"context"
	"encoding/json"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// storedCheck 保留检查结果的领域载荷；是否满足目标由相应推导器解释。
type storedCheck struct {
	OperationID string
	Kind        domain.OperationKind
	Basis       domain.EvidenceBasis
	Content     json.RawMessage
}

func (s *Service) validChecks(ctx context.Context, project ProjectSnapshot) ([]storedCheck, error) {
	documents, err := s.store.ListDerivedDocumentsByKind(ctx, project.ID, domain.DerivedVerdictKind)
	if err != nil {
		return nil, err
	}
	var checks []storedCheck
	for _, document := range documents {
		op, err := s.store.GetOperation(ctx, document.Key)
		if err != nil {
			return nil, err
		}
		if op.State != domain.OperationSucceeded || op.Kind == domain.OperationReviewRange {
			continue
		}
		basis, err := s.operations.ValidateEvidence(ctx, op, document.Content)
		if err != nil {
			return nil, err
		}
		stale, err := s.staleBasis(ctx, project, basis)
		if err != nil {
			return nil, err
		}
		if len(stale) == 0 {
			checks = append(checks, storedCheck{OperationID: op.ID, Kind: op.Kind, Basis: basis, Content: document.Content})
		}
	}
	return checks, nil
}
