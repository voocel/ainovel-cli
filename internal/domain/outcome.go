package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// OperationOutcome 是 Operation 的统一产出（D30/D47）：需要改变 Authority 时提交
// Proposal，审阅/校验类产出结构化 Verdict，二者互斥；Artifacts 可单独产出，也可
// 伴随 Proposal（附件引用它们）。审阅通过不制造空 Patch。
type OperationOutcome struct {
	Proposal  *Proposal       `json:"proposal,omitempty"`
	Verdict   json.RawMessage `json:"verdict,omitempty"`
	Artifacts []Artifact      `json:"artifacts,omitempty"`
}

func (o OperationOutcome) Validate() error {
	hasProposal := o.Proposal != nil
	hasVerdict := len(o.Verdict) != 0
	if hasProposal && hasVerdict {
		return fmt.Errorf("operation outcome cannot carry both proposal and verdict: %w", ErrInvalid)
	}
	if !hasProposal && !hasVerdict && len(o.Artifacts) == 0 {
		return fmt.Errorf("operation outcome requires a proposal, a verdict or artifacts: %w", ErrInvalid)
	}
	if hasVerdict && !json.Valid(o.Verdict) {
		return fmt.Errorf("operation verdict must be valid JSON: %w", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(o.Artifacts))
	for i, artifact := range o.Artifacts {
		if err := artifact.Validate(); err != nil {
			return fmt.Errorf("artifact %d: %w", i, err)
		}
		if _, ok := seen[artifact.ID]; ok {
			return fmt.Errorf("duplicate artifact %q: %w", artifact.ID, ErrInvalid)
		}
		seen[artifact.ID] = struct{}{}
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

// ReviewFinding 是一条审阅发现。阻塞发现可通过 DirectiveID / Intent 链接到它证明
// 未满足的核验项（§6.4）：未满足项与阻塞发现一一可追溯，用户接受发现即接受该项（D43）。
type ReviewFinding struct {
	ChapterID   string `json:"chapter_id"`
	Severity    string `json:"severity"`
	Note        string `json:"note"`
	DirectiveID string `json:"directive_id,omitempty"`
	Intent      string `json:"intent,omitempty"`
}

// Intent 核验的三个维度名，供发现链接与裁决引用。
const (
	IntentRequiredPresent  = "required_present"
	IntentForbiddenAbsent  = "forbidden_absent"
	IntentEndingConsistent = "ending_consistent"
)

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

// Unmet 按固定顺序列出未满足的维度。
func (v IntentVerification) Unmet() []string {
	var unmet []string
	for _, dimension := range []struct {
		name string
		ok   bool
	}{{IntentRequiredPresent, v.RequiredPresent}, {IntentForbiddenAbsent, v.ForbiddenAbsent}, {IntentEndingConsistent, v.EndingConsistent}} {
		if !dimension.ok {
			unmet = append(unmet, dimension.name)
		}
	}
	return unmet
}

func (v *IntentVerification) satisfy(dimension string) {
	switch dimension {
	case IntentRequiredPresent:
		v.RequiredPresent = true
	case IntentForbiddenAbsent:
		v.ForbiddenAbsent = true
	case IntentEndingConsistent:
		v.EndingConsistent = true
	}
}

func validIntentDimension(dimension string) bool {
	return dimension == IntentRequiredPresent || dimension == IntentForbiddenAbsent || dimension == IntentEndingConsistent
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
// Revision 是产出裁定的基线 Revision；有效性按 Basis 判定（D48），不按 Revision 相等。
type ReviewVerdict struct {
	Status     string                  `json:"status"`
	Revision   Revision                `json:"revision"`
	ChapterIDs []string                `json:"chapter_ids"`
	ReviewKey  string                  `json:"review_key"`
	Basis      EvidenceBasis           `json:"basis"`
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
	if err := validateEvidenceBasis("review verdict", v.Basis); err != nil {
		return err
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
			if finding.DirectiveID != "" || finding.Intent != "" {
				return fmt.Errorf("review finding %d links a verification item but is not blocking: %w", i, ErrInvalid)
			}
		default:
			return fmt.Errorf("unknown finding severity %q: %w", finding.Severity, ErrInvalid)
		}
		if finding.DirectiveID != "" && finding.Intent != "" {
			return fmt.Errorf("review finding %d cannot link both a directive and an intent dimension: %w", i, ErrInvalid)
		}
		if finding.Intent != "" && !validIntentDimension(finding.Intent) {
			return fmt.Errorf("review finding %d links unknown intent dimension %q: %w", i, finding.Intent, ErrInvalid)
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
	input, err := TaskInputAs[ReviewRangeInput](operation)
	if err != nil {
		return err
	}
	if !verdict.Basis.Equal(input.Basis) {
		return fmt.Errorf("review verdict basis does not match the task basis: %w", ErrInvalid)
	}
	requested := make(map[string]struct{}, len(input.ChapterIDs))
	for _, id := range input.ChapterIDs {
		requested[id] = struct{}{}
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
	return validateFindingLinks(input, verdict)
}

// validateFindingLinks 是链接门（§6.4/D43）：终审必须声明意图核验；每个未满足的
// 核验项至少有一条阻塞发现链接到它，链接只能指向本裁定声明为未满足的项。
func validateFindingLinks(input ReviewRangeInput, verdict ReviewVerdict) error {
	if input.VerifyIntent && verdict.Intent == nil {
		return fmt.Errorf("final review requires an intent verification declaration: %w", ErrInvalid)
	}
	linkedDirectives := make(map[string]bool)
	for _, directive := range verdict.Directives {
		if !directive.Satisfied {
			linkedDirectives[directive.DirectiveID] = false
		}
	}
	linkedIntent := make(map[string]bool)
	if verdict.Intent != nil {
		for _, dimension := range verdict.Intent.Unmet() {
			linkedIntent[dimension] = false
		}
	}
	for i, finding := range verdict.Findings {
		if finding.DirectiveID != "" {
			if _, unmet := linkedDirectives[finding.DirectiveID]; !unmet {
				return fmt.Errorf("finding %d links directive %q that is not declared unmet: %w", i, finding.DirectiveID, ErrInvalid)
			}
			linkedDirectives[finding.DirectiveID] = true
		}
		if finding.Intent != "" {
			if _, unmet := linkedIntent[finding.Intent]; !unmet {
				return fmt.Errorf("finding %d links intent %q that is not declared unmet: %w", i, finding.Intent, ErrInvalid)
			}
			linkedIntent[finding.Intent] = true
		}
	}
	for _, directive := range verdict.Directives {
		if linked, unmet := linkedDirectives[directive.DirectiveID]; unmet && !linked {
			return fmt.Errorf("unmet directive %q requires a blocking finding linked to it: %w", directive.DirectiveID, ErrInvalid)
		}
	}
	if verdict.Intent != nil {
		for _, dimension := range verdict.Intent.Unmet() {
			if !linkedIntent[dimension] {
				return fmt.Errorf("unmet intent %q requires a blocking finding linked to it: %w", dimension, ErrInvalid)
			}
		}
	}
	return nil
}

// Adjudicated 套用用户裁决（D43）得到生效裁定：被接受的阻塞发现移除；核验项在没有
// 剩余阻塞发现链接它时视为满足；不再有阻塞发现即生效为 pass。原裁定不变。
func (v ReviewVerdict) Adjudicated(operationID string, accepted map[string]struct{}) ReviewVerdict {
	if len(accepted) == 0 {
		return v
	}
	effective := v
	effective.Findings = make([]ReviewFinding, 0, len(v.Findings))
	linkedDirectives, linkedIntent := make(map[string]struct{}), make(map[string]struct{})
	removed := false
	for i, finding := range v.Findings {
		if _, ok := accepted[FindingID(operationID, i)]; ok && finding.Severity == FindingBlocking {
			removed = true
			continue
		}
		effective.Findings = append(effective.Findings, finding)
		if finding.DirectiveID != "" {
			linkedDirectives[finding.DirectiveID] = struct{}{}
		}
		if finding.Intent != "" {
			linkedIntent[finding.Intent] = struct{}{}
		}
	}
	if !removed {
		return v
	}
	effective.Directives = slices.Clone(v.Directives)
	for i := range effective.Directives {
		if _, linked := linkedDirectives[effective.Directives[i].DirectiveID]; !linked {
			effective.Directives[i].Satisfied = true
		}
	}
	if v.Intent != nil {
		intent := *v.Intent
		for _, dimension := range v.Intent.Unmet() {
			if _, linked := linkedIntent[dimension]; !linked {
				intent.satisfy(dimension)
			}
		}
		effective.Intent = &intent
	}
	if len(effective.BlockingChapters()) == 0 {
		effective.Status = ReviewPass
	}
	return effective
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
