package domain

import (
	"fmt"
	"strings"
)

// SemanticComplianceStatus 是 AI 正文对 locked/guided 约束的独立语义合规裁定。
// 它由 capability 层的独立结构化模型调用产出、operation 层消费，因此定义在
// domain：契约放在双方共同依赖的底层，避免生产方反向依赖消费方。
//
// 只有 pass 允许自动提交；conflict / uncertain / unavailable 一律进入用户确认，
// 不因分析不可用而放行（v1-architecture-plan D13/D14）。
type SemanticComplianceStatus string

const (
	SemanticCompliancePass        SemanticComplianceStatus = "pass"
	SemanticComplianceConflict    SemanticComplianceStatus = "conflict"
	SemanticComplianceUncertain   SemanticComplianceStatus = "uncertain"
	SemanticComplianceUnavailable SemanticComplianceStatus = "unavailable"
)

type SemanticComplianceFinding struct {
	Constraint  DocumentRef `json:"constraint"`
	Explanation string      `json:"explanation"`
}

type SemanticComplianceReport struct {
	Status   SemanticComplianceStatus    `json:"status"`
	Findings []SemanticComplianceFinding `json:"findings"`
}

func (r SemanticComplianceReport) Validate() error {
	switch r.Status {
	case SemanticCompliancePass:
		if len(r.Findings) != 0 {
			return fmt.Errorf("passing semantic compliance cannot contain findings: %w", ErrInvalid)
		}
	case SemanticComplianceConflict, SemanticComplianceUncertain, SemanticComplianceUnavailable:
		if len(r.Findings) == 0 {
			return fmt.Errorf("non-passing semantic compliance requires findings: %w", ErrInvalid)
		}
	default:
		return fmt.Errorf("unknown semantic compliance status %q: %w", r.Status, ErrInvalid)
	}
	for i, finding := range r.Findings {
		if err := finding.Constraint.Validate(); err != nil {
			return fmt.Errorf("semantic compliance finding %d: %w", i, err)
		}
		if strings.TrimSpace(finding.Explanation) == "" {
			return fmt.Errorf("semantic compliance finding %d explanation is required: %w", i, ErrInvalid)
		}
	}
	return nil
}
