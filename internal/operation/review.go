package operation

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/voocel/ainovel-cli/internal/domain"
	"slices"
)

// 小说审阅的领域校验与通用证据收尾分开；其他证据通过静态契约装配。
func (e *Engine) validateReviewEvidence(ctx context.Context, op domain.Operation, payload json.RawMessage) (domain.EvidenceBasis, error) {
	verdict, err := e.validateReviewVerdict(ctx, op, payload)
	return verdict.Basis, err
}

func (e *Engine) validateReviewVerdict(
	ctx context.Context,
	operation domain.Operation,
	payload json.RawMessage,
) (domain.ReviewVerdict, error) {
	if operation.Kind != domain.OperationReviewRange {
		return domain.ReviewVerdict{}, fmt.Errorf("%s operation cannot produce a verdict: %w", operation.Kind, domain.ErrInvalid)
	}
	var verdict domain.ReviewVerdict
	if err := domain.DecodeStrict(payload, &verdict); err != nil {
		return domain.ReviewVerdict{}, fmt.Errorf("decode review verdict: %w", err)
	}
	if err := domain.ValidateReviewVerdictForOperation(operation, verdict); err != nil {
		return domain.ReviewVerdict{}, err
	}
	artifact, err := e.store.GetWorkspaceArtifact(ctx, operation.ID, verdict.ReviewKey)
	if err != nil {
		return domain.ReviewVerdict{}, fmt.Errorf("read review artifact %q: %w", verdict.ReviewKey, err)
	}
	if artifact.MediaType != domain.ReviewArtifactMediaType {
		return domain.ReviewVerdict{}, fmt.Errorf("workspace artifact %q is not a review record: %w", verdict.ReviewKey, domain.ErrInvalid)
	}
	var findings []domain.ReviewFinding
	if err := domain.DecodeStrict(artifact.Content, &findings); err != nil {
		return domain.ReviewVerdict{}, fmt.Errorf("decode review artifact %q: %w", verdict.ReviewKey, err)
	}
	if findings == nil {
		return domain.ReviewVerdict{}, fmt.Errorf("review artifact %q must contain a findings array: %w", verdict.ReviewKey, domain.ErrInvalid)
	}
	if !slices.Equal(findings, verdict.Findings) {
		return domain.ReviewVerdict{}, fmt.Errorf("verdict findings do not match review artifact %q: %w", verdict.ReviewKey, domain.ErrInvalid)
	}
	return verdict, nil
}
