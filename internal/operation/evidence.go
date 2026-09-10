package operation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// VerdictContract 在组合根静态装配一种任务的检查契约。领域负责解码、核对输入与
// 检查结果；内核负责验证返回基线、持久化、归属围栏、恢复和状态收尾。
type VerdictContract struct {
	Kind     domain.OperationKind
	Validate VerdictValidator
}

type VerdictValidator func(context.Context, domain.Operation, json.RawMessage) (domain.EvidenceBasis, error)

func (e *Engine) ValidateEvidence(ctx context.Context, op domain.Operation, payload json.RawMessage) (domain.EvidenceBasis, error) {
	validate, ok := e.verdicts[op.Kind]
	if !ok || validate == nil {
		return domain.EvidenceBasis{}, fmt.Errorf("%s has no evidence contract: %w", op.Kind, domain.ErrInvalid)
	}
	basis, err := validate(ctx, op, payload)
	if err != nil {
		return domain.EvidenceBasis{}, err
	}
	if err := basis.Validate(); err != nil {
		return domain.EvidenceBasis{}, err
	}
	required, err := domain.OperationBasis(op)
	if err != nil {
		return domain.EvidenceBasis{}, err
	}
	if !basis.Covers(required) {
		return domain.EvidenceBasis{}, fmt.Errorf("evidence omits frozen task dependencies: %w", domain.ErrInvalid)
	}
	if err := e.verifyBasis(ctx, op, basis); err != nil {
		return domain.EvidenceBasis{}, err
	}
	return basis, nil
}
