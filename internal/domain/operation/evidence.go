package operation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// VerdictContract 在组合根静态装配一种任务的检查契约。领域负责解码、核对输入与
// 检查结果；内核负责验证返回基线、持久化、归属围栏、恢复和状态收尾。
type VerdictContract struct {
	Kind     model.OperationKind
	Validate VerdictValidator
}

type VerdictValidator func(context.Context, model.Operation, json.RawMessage) (model.EvidenceBasis, error)

func (e *Engine) ValidateEvidence(ctx context.Context, op model.Operation, payload json.RawMessage) (model.EvidenceBasis, error) {
	validate, ok := e.verdicts[op.Kind]
	if !ok || validate == nil {
		return model.EvidenceBasis{}, fmt.Errorf("%s has no evidence contract: %w", op.Kind, model.ErrInvalid)
	}
	basis, err := validate(ctx, op, payload)
	if err != nil {
		return model.EvidenceBasis{}, err
	}
	if err := basis.Validate(); err != nil {
		return model.EvidenceBasis{}, err
	}
	required, err := model.OperationBasis(op)
	if err != nil {
		return model.EvidenceBasis{}, err
	}
	if !basis.Covers(required) {
		return model.EvidenceBasis{}, fmt.Errorf("evidence omits frozen task dependencies: %w", model.ErrInvalid)
	}
	if err := e.verifyBasis(ctx, op, basis); err != nil {
		return model.EvidenceBasis{}, err
	}
	return basis, nil
}
