package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OperationOutcome 是 Operation 的统一产出（D30）：需要改变 Authority 时提交
// Proposal，审阅/校验类产出结构化 Verdict；审阅通过不制造空 Patch。
type OperationOutcome struct {
	Proposal *Proposal       `json:"proposal,omitempty"`
	Verdict  json.RawMessage `json:"verdict,omitempty"`
}

func (o OperationOutcome) Validate() error {
	hasProposal := o.Proposal != nil
	hasVerdict := len(o.Verdict) != 0
	if hasProposal == hasVerdict {
		return fmt.Errorf("operation outcome requires exactly one of proposal or verdict: %w", ErrInvalid)
	}
	if hasVerdict && !json.Valid(o.Verdict) {
		return fmt.Errorf("operation verdict must be valid JSON: %w", ErrInvalid)
	}
	return nil
}

// DerivedVerdictKind 是裁定在派生数据中的落点：按 (project, revision, operation)
// 版本化绑定——正文再变化时旧裁定随 Revision 自动失效（§6.4）。
const DerivedVerdictKind = "operation_verdict"

// ReviewArtifactMediaType 标识 verdict 引用的结构化审阅过程记录。
const ReviewArtifactMediaType = "application/vnd.ainovel.review-findings+json"

const (
	ReviewPass    = "pass"
	ReviewBlocked = "blocked"

	FindingBlocking = "blocking"
	FindingNote     = "note"
)

type ReviewFinding struct {
	ChapterID string `json:"chapter_id"`
	Severity  string `json:"severity"`
	Note      string `json:"note"`
}

// IntentVerification 是审阅对 Intent 各维度的显式核验声明（§6.4 完成条件 3）：
// 完成契约要求"已验证满足"而不仅是"检查过"，所以终审的 pass 必须逐项声明。
type IntentVerification struct {
	RequiredPresent  bool `json:"required_present"`
	ForbiddenAbsent  bool `json:"forbidden_absent"`
	EndingConsistent bool `json:"ending_consistent"`
}

func (v IntentVerification) Satisfied() bool {
	return v.RequiredPresent && v.ForbiddenAbsent && v.EndingConsistent
}

// DirectiveVerification 是审阅对单条用户要求的显式核验声明（§4.9）：任务输入
// 携带的每条 Directive 都必须逐条声明，未满足以阻塞发现表达并回到重写。
type DirectiveVerification struct {
	DirectiveID string `json:"directive_id"`
	Satisfied   bool   `json:"satisfied"`
	Note        string `json:"note,omitempty"`
}

// ReviewVerdict 是审阅 Operation 的版本化裁定（§6.4）：pass 表示范围内正文与
// Intent 的必须出现 / 禁止出现 / 结局方向全部验证满足，且不存在阻塞级发现。
// Intent 声明在阶段审阅可省略（必须出现的要素可能落在后续章节），终审必备。
type ReviewVerdict struct {
	Status     string                  `json:"status"`
	Revision   Revision                `json:"revision"`
	ChapterIDs []string                `json:"chapter_ids"`
	ReviewKey  string                  `json:"review_key"`
	Intent     *IntentVerification     `json:"intent,omitempty"`
	Directives []DirectiveVerification `json:"directives,omitempty"`
	Findings   []ReviewFinding         `json:"findings"`
}

// IntentSatisfied 报告裁定是否带有全部通过的意图核验声明。
func (v ReviewVerdict) IntentSatisfied() bool {
	return v.Intent != nil && v.Intent.Satisfied()
}

// DirectivesSatisfied 报告已声明的要求核验是否全部满足；覆盖完整性由任务输入校验。
func (v ReviewVerdict) DirectivesSatisfied() bool {
	for _, directive := range v.Directives {
		if !directive.Satisfied {
			return false
		}
	}
	return true
}

func (v ReviewVerdict) Validate() error {
	if v.Revision <= InitialRevision || len(v.ChapterIDs) == 0 || strings.TrimSpace(v.ReviewKey) == "" {
		return fmt.Errorf("review verdict revision, chapter range and review key are required: %w", ErrInvalid)
	}
	if v.Findings == nil {
		return fmt.Errorf("review verdict requires an explicit findings array: %w", ErrInvalid)
	}
	chapters := make(map[string]struct{}, len(v.ChapterIDs))
	for i, id := range v.ChapterIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("review verdict chapter %d is empty: %w", i, ErrInvalid)
		}
		if _, exists := chapters[id]; exists {
			return fmt.Errorf("review verdict chapter %q is duplicated: %w", id, ErrInvalid)
		}
		chapters[id] = struct{}{}
	}
	directives := make(map[string]struct{}, len(v.Directives))
	for i, directive := range v.Directives {
		if strings.TrimSpace(directive.DirectiveID) == "" {
			return fmt.Errorf("review verdict directive %d is empty: %w", i, ErrInvalid)
		}
		if _, exists := directives[directive.DirectiveID]; exists {
			return fmt.Errorf("review verdict directive %q is duplicated: %w", directive.DirectiveID, ErrInvalid)
		}
		directives[directive.DirectiveID] = struct{}{}
	}
	blocking := 0
	for i, finding := range v.Findings {
		if strings.TrimSpace(finding.ChapterID) == "" || strings.TrimSpace(finding.Note) == "" {
			return fmt.Errorf("review finding %d requires chapter and note: %w", i, ErrInvalid)
		}
		switch finding.Severity {
		case FindingBlocking:
			blocking++
		case FindingNote:
		default:
			return fmt.Errorf("unknown finding severity %q: %w", finding.Severity, ErrInvalid)
		}
	}
	switch v.Status {
	case ReviewPass:
		if blocking != 0 {
			return fmt.Errorf("passing verdict cannot carry blocking findings: %w", ErrInvalid)
		}
	case ReviewBlocked:
		if blocking == 0 {
			return fmt.Errorf("blocked verdict requires at least one blocking finding: %w", ErrInvalid)
		}
	default:
		return fmt.Errorf("unknown review status %q: %w", v.Status, ErrInvalid)
	}
	return nil
}

// ValidateReviewVerdictForOperation 把模型不能自证的 Revision、审阅范围、
// 终审 Intent 要求和用户要求核验绑定到启动 Operation，由所有 Executor 共用同一确定性校验。
func ValidateReviewVerdictForOperation(operation Operation, verdict ReviewVerdict) error {
	if operation.Kind != OperationReviewRange {
		return fmt.Errorf("%s operation cannot produce a review verdict: %w", operation.Kind, ErrInvalid)
	}
	if err := verdict.Validate(); err != nil {
		return err
	}
	if verdict.Revision != operation.Snapshot.BaseRevision {
		return fmt.Errorf("review verdict revision does not match operation snapshot: %w", ErrInvalid)
	}
	var input struct {
		Range struct {
			ChapterIDs []string `json:"chapter_ids"`
		} `json:"range"`
		VerifyIntent bool `json:"verify_intent"`
		Directives   []struct {
			ID string `json:"id"`
		} `json:"directives"`
	}
	if err := json.Unmarshal(operation.Input, &input); err != nil {
		return fmt.Errorf("decode review operation input: %w", err)
	}
	requested := make(map[string]struct{}, len(input.Range.ChapterIDs))
	for _, id := range input.Range.ChapterIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("review operation contains an empty chapter id: %w", ErrInvalid)
		}
		requested[id] = struct{}{}
	}
	if len(requested) == 0 || len(requested) != len(input.Range.ChapterIDs) {
		return fmt.Errorf("review operation chapter range is empty or duplicated: %w", ErrInvalid)
	}
	if len(requested) != len(verdict.ChapterIDs) {
		return fmt.Errorf("verdict does not exactly cover the requested chapter range: %w", ErrInvalid)
	}
	for _, id := range verdict.ChapterIDs {
		if _, ok := requested[id]; !ok {
			return fmt.Errorf("verdict chapter %q is outside the requested range: %w", id, ErrInvalid)
		}
	}
	for _, finding := range verdict.Findings {
		if _, ok := requested[finding.ChapterID]; !ok {
			return fmt.Errorf("finding chapter %q is outside the requested range: %w", finding.ChapterID, ErrInvalid)
		}
	}
	if input.VerifyIntent && verdict.Status == ReviewPass && !verdict.IntentSatisfied() {
		return fmt.Errorf(
			"final review pass requires all intent checks satisfied; report unmet intent as blocking findings: %w",
			ErrInvalid,
		)
	}
	// 要求核验（§4.9）：任务输入里的每条 Directive 都必须被恰好声明一次，
	// pass 要求全部满足——与章节范围同样不允许漏项或越界。
	expected := make(map[string]struct{}, len(input.Directives))
	for _, directive := range input.Directives {
		expected[directive.ID] = struct{}{}
	}
	if len(expected) != len(verdict.Directives) {
		return fmt.Errorf("verdict does not exactly cover the requested directives: %w", ErrInvalid)
	}
	for _, directive := range verdict.Directives {
		if _, ok := expected[directive.DirectiveID]; !ok {
			return fmt.Errorf("verdict directive %q is outside the requested directives: %w", directive.DirectiveID, ErrInvalid)
		}
	}
	if verdict.Status == ReviewPass && !verdict.DirectivesSatisfied() {
		return fmt.Errorf(
			"review pass requires all directive checks satisfied; report unmet directives as blocking findings: %w",
			ErrInvalid,
		)
	}
	return nil
}

// BlockingChapters 返回按发现顺序去重的阻塞章节。
func (v ReviewVerdict) BlockingChapters() []string {
	seen := make(map[string]struct{})
	var chapters []string
	for _, finding := range v.Findings {
		if finding.Severity != FindingBlocking {
			continue
		}
		if _, ok := seen[finding.ChapterID]; ok {
			continue
		}
		seen[finding.ChapterID] = struct{}{}
		chapters = append(chapters, finding.ChapterID)
	}
	return chapters
}
