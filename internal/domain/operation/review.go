package operation

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"slices"
)

// 小说审阅的领域校验与通用证据收尾分开；其他证据通过静态契约装配。
func (e *Engine) validateReviewEvidence(ctx context.Context, op model.Operation, payload json.RawMessage) (model.EvidenceBasis, error) {
	verdict, err := e.validateReviewVerdict(ctx, op, payload)
	return verdict.Basis, err
}

func (e *Engine) validateReviewVerdict(
	ctx context.Context,
	operation model.Operation,
	payload json.RawMessage,
) (model.ReviewVerdict, error) {
	if operation.Kind != model.OperationReviewRange {
		return model.ReviewVerdict{}, fmt.Errorf("%s operation cannot produce a verdict: %w", operation.Kind, model.ErrInvalid)
	}
	var verdict model.ReviewVerdict
	if err := model.DecodeStrict(payload, &verdict); err != nil {
		return model.ReviewVerdict{}, fmt.Errorf("decode review verdict: %w", err)
	}
	if err := model.ValidateReviewVerdictForOperation(operation, verdict); err != nil {
		return model.ReviewVerdict{}, err
	}
	artifact, err := e.store.GetWorkspaceArtifact(ctx, operation.ID, verdict.ReviewKey)
	if err != nil {
		return model.ReviewVerdict{}, fmt.Errorf("read review artifact %q: %w", verdict.ReviewKey, err)
	}
	if artifact.MediaType != model.ReviewArtifactMediaType {
		return model.ReviewVerdict{}, fmt.Errorf("workspace artifact %q is not a review record: %w", verdict.ReviewKey, model.ErrInvalid)
	}
	var findings []model.ReviewFinding
	if err := model.DecodeStrict(artifact.Content, &findings); err != nil {
		return model.ReviewVerdict{}, fmt.Errorf("decode review artifact %q: %w", verdict.ReviewKey, err)
	}
	if findings == nil {
		return model.ReviewVerdict{}, fmt.Errorf("review artifact %q must contain a findings array: %w", verdict.ReviewKey, model.ErrInvalid)
	}
	if !slices.Equal(findings, verdict.Findings) {
		return model.ReviewVerdict{}, fmt.Errorf("verdict findings do not match review artifact %q: %w", verdict.ReviewKey, model.ErrInvalid)
	}
	return verdict, nil
}
