package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ExecutorFamily 区分任务由谁执行：llm 走 Execution Profile 编译与模型循环，
// external 走外部服务的提交、恢复与工件发布。领取与身份核对只看 Snapshot.Executor。
type ExecutorFamily string

const (
	ExecutorLLM      ExecutorFamily = "llm"
	ExecutorExternal ExecutorFamily = "external"
)

// TaskInput 是各 Operation 种类的类型化任务输入：形状由种类登记决定，宿主各处
// 按类型解码而不是各写一份匿名结构。
type TaskInput interface {
	Validate() error
}

// OperationKindSpec 是一种 Operation 的登记项：装配层按它判断执行族与进行中的呈现文案；
// 新增种类只在这里登记一次。
type OperationKindSpec struct {
	Kind     OperationKind
	Executor ExecutorFamily
	Label    string // 进行中的创作语言
	NewInput func() TaskInput
}

var operationKinds = []OperationKindSpec{
	{Kind: OperationDevelopPlan, Executor: ExecutorLLM, Label: "正在规划故事蓝图",
		NewInput: func() TaskInput { return &DevelopPlanInput{} }},
	{Kind: OperationRevisePlan, Executor: ExecutorLLM, Label: "正在规划故事蓝图",
		NewInput: func() TaskInput { return &RevisePlanInput{} }},
	{Kind: OperationReviseCanon, Executor: ExecutorLLM, Label: "正在修订已确认事实",
		NewInput: func() TaskInput { return &ReviseCanonInput{} }},
	{Kind: OperationWriteChapter, Executor: ExecutorLLM, Label: "正在落笔新章节",
		NewInput: func() TaskInput { return &WriteChapterInput{} }},
	{Kind: OperationRewriteChapter, Executor: ExecutorLLM, Label: "正在按意见重写章节",
		NewInput: func() TaskInput { return &RewriteChapterInput{} }},
	{Kind: OperationReworkChapter, Executor: ExecutorLLM, Label: "正在按你的要求重写章节",
		NewInput: func() TaskInput { return &ReworkChapterInput{} }},
	{Kind: OperationReviewRange, Executor: ExecutorLLM, Label: "正在审阅已完成章节",
		NewInput: func() TaskInput { return &ReviewRangeInput{} }},
	{Kind: OperationGenerateAsset, Executor: ExecutorExternal, Label: "正在生成衍生资产",
		NewInput: func() TaskInput { return &GenerateAssetInput{} }},
	{Kind: OperationInspectAsset, Executor: ExecutorExternal, Label: "正在核验衍生资产",
		NewInput: func() TaskInput { return &InspectAssetInput{} }},
}

func KindSpec(kind OperationKind) (OperationKindSpec, error) {
	for _, spec := range operationKinds {
		if spec.Kind == kind {
			return spec, nil
		}
	}
	return OperationKindSpec{}, fmt.Errorf("unknown operation kind %q: %w", kind, ErrInvalid)
}

func OperationKinds() []OperationKindSpec {
	return append([]OperationKindSpec(nil), operationKinds...)
}

// DecodeTaskInput 按种类严格解码并校验任务输入。
func DecodeTaskInput(kind OperationKind, raw json.RawMessage) (TaskInput, error) {
	spec, err := KindSpec(kind)
	if err != nil {
		return nil, err
	}
	input := spec.NewInput()
	if err := DecodeStrict(raw, input); err != nil {
		return nil, fmt.Errorf("decode %s task input: %w", kind, err)
	}
	if err := input.Validate(); err != nil {
		return nil, fmt.Errorf("%s task input: %w", kind, err)
	}
	return input, nil
}

// TaskInputAs 解码 Operation 的任务输入为其登记类型。
func TaskInputAs[T any, PT interface {
	*T
	TaskInput
}](operation Operation) (T, error) {
	var zero T
	input, err := DecodeTaskInput(operation.Kind, operation.Input)
	if err != nil {
		return zero, err
	}
	typed, ok := input.(PT)
	if !ok {
		return zero, fmt.Errorf("operation %s input is %T, not %T: %w", operation.Kind, input, zero, ErrInvalid)
	}
	return *typed, nil
}

// DevelopPlanInput / RevisePlanInput 不指定章数：这次展开或追加多少章由规划者决定，
// 提交边界只守篇幅上界与进展（§6.3 D67）。FixedChapters 为 0 表示篇幅交给 AI，正数
// 表示用户固定的全书章数。PendingRequirements 是尚未兑现的要求原文（D62），供规划安排落点。
// 作品前提不在输入里：任务按基线读创作意图，前提改了不留旧抄本。
type DevelopPlanInput struct {
	FixedChapters       int      `json:"fixed_chapters,omitempty"`
	PendingRequirements []string `json:"pending_requirements,omitempty"`
	Goal                string   `json:"goal,omitempty"`
}

func (v DevelopPlanInput) Validate() error {
	if v.FixedChapters < 0 {
		return fmt.Errorf("fixed chapters %d must not be negative: %w", v.FixedChapters, ErrInvalid)
	}
	return nil
}

// RevisePlanInput 的 Basis 是扩窗所依据的窗口审阅裁定基线（D51）：相干要求或正文
// 再变化时扩窗任务失效，先重审再扩。
type RevisePlanInput struct {
	FixedChapters       int           `json:"fixed_chapters,omitempty"`
	ExistingChapters    int           `json:"existing_chapters"`
	ReviewNotes         []string      `json:"review_notes,omitempty"`
	PendingRequirements []string      `json:"pending_requirements,omitempty"`
	Basis               EvidenceBasis `json:"basis,omitzero"`
	Goal                string        `json:"goal,omitempty"`
}

func (v RevisePlanInput) Validate() error {
	if v.FixedChapters < 0 || v.ExistingChapters < 1 || (v.FixedChapters > 0 && v.ExistingChapters >= v.FixedChapters) {
		return fmt.Errorf("extending %d existing chapters requires room below fixed %d: %w", v.ExistingChapters, v.FixedChapters, ErrInvalid)
	}
	return v.Basis.Validate()
}

// ReviseCanonInput 核验来源于某章的事实（§4.4 D41 / §4.5）：FactIDs 是正文改动后待核验
// 的事实，逐条确认、更新或删除；为空表示该章事实未入账，需要补账。
type ReviseCanonInput struct {
	ChapterID string   `json:"chapter_id"`
	FactIDs   []string `json:"fact_ids,omitempty"`
	Reason    string   `json:"reason"`
	Goal      string   `json:"goal,omitempty"`
}

func (v ReviseCanonInput) Validate() error {
	if strings.TrimSpace(v.ChapterID) == "" || strings.TrimSpace(v.Reason) == "" {
		return fmt.Errorf("chapter and reason are required: %w", ErrInvalid)
	}
	return validateDistinctStrings("canon facts", v.FactIDs)
}

// 章节任务的 Basis 只钉住本章的要求作用域（D51）：相干要求变化使在途任务失效，
// 不相干的用户专属变化（锁定、审批、其他章的要求）只让提案重定位。章号是章节计划的位置
// （D66），不在任务输入里另存一份。
type WriteChapterInput struct {
	ChapterPlanID string        `json:"chapter_plan_id"`
	Directives    []Directive   `json:"directives,omitempty"`
	Basis         EvidenceBasis `json:"basis,omitzero"`
	Goal          string        `json:"goal,omitempty"`
}

func (v WriteChapterInput) Validate() error {
	if strings.TrimSpace(v.ChapterPlanID) == "" {
		return fmt.Errorf("chapter plan is required: %w", ErrInvalid)
	}
	if err := validateTaskDirectives(v.Directives); err != nil {
		return err
	}
	return v.Basis.Validate()
}

// RewriteChapterInput 是按审阅的阻塞意见修订一章：照章纲改正文，计入自动修订预算（D63）。
type RewriteChapterInput struct {
	ChapterID     string        `json:"chapter_id"`
	ChapterPlanID string        `json:"chapter_plan_id"`
	Findings      []string      `json:"findings"`
	Directives    []Directive   `json:"directives,omitempty"`
	Basis         EvidenceBasis `json:"basis,omitzero"`
	Goal          string        `json:"goal,omitempty"`
}

func (v RewriteChapterInput) Validate() error {
	if strings.TrimSpace(v.ChapterID) == "" || strings.TrimSpace(v.ChapterPlanID) == "" {
		return fmt.Errorf("chapter and plan are required: %w", ErrInvalid)
	}
	if len(v.Findings) == 0 {
		return fmt.Errorf("a rewrite follows review findings: %w", ErrInvalid)
	}
	if err := validateTaskDirectives(v.Directives); err != nil {
		return err
	}
	return v.Basis.Validate()
}

// ReworkChapterInput 是按用户点名的返工要求重写一章（D78）：Directives 里标了 rework 的
// 要求是用户对现有正文的意见。返工可能改变这一章写了什么，章题、章纲与转折随正文一起改（D87、D92）；
// 不是自动修订，不计入预算。
type ReworkChapterInput struct {
	ChapterID     string        `json:"chapter_id"`
	ChapterPlanID string        `json:"chapter_plan_id"`
	Directives    []Directive   `json:"directives"`
	Basis         EvidenceBasis `json:"basis,omitzero"`
	Goal          string        `json:"goal,omitempty"`
}

func (v ReworkChapterInput) Validate() error {
	if strings.TrimSpace(v.ChapterID) == "" || strings.TrimSpace(v.ChapterPlanID) == "" {
		return fmt.Errorf("chapter and plan are required: %w", ErrInvalid)
	}
	if !slices.ContainsFunc(v.Directives, func(directive Directive) bool { return directive.Rework }) {
		return fmt.Errorf("a rework follows a rework directive: %w", ErrInvalid)
	}
	if err := validateTaskDirectives(v.Directives); err != nil {
		return err
	}
	return v.Basis.Validate()
}

// ReviewRangeInput 的 Basis 由装配层构造：裁定继承它，有效性按基线判定（D48）。
// Requirements 是本窗口要逐项核验的要求（D62），裁定必须恰好逐项声明。
// Reviewed 是上一轮已审且正文未变的章（D68），只作上下文；PriorFindings 是其余章
// 上一轮的阻塞意见，复审逐条核对是否解决。
type ReviewRangeInput struct {
	ChapterIDs   []string      `json:"chapter_ids"`
	Requirements []Requirement `json:"requirements,omitempty"`
	Reviewed     []string      `json:"reviewed,omitempty"`
	// Satisfied 是 reviewed 章所在的上一份裁定已判满足的要求：正文与要求都没变，结论不重开，
	// 不能借它们阻塞 reviewed 章。
	Satisfied     []string        `json:"satisfied,omitempty"`
	PriorFindings []ReviewFinding `json:"prior_findings,omitempty"`
	Basis         EvidenceBasis   `json:"basis"`
	Goal          string          `json:"goal,omitempty"`
}

// Requirement 是审阅要核验的一项要求：用户要求或意图条目（D62）。Settle 表示本窗口
// 必须给出结论（satisfied 或 violated），不得 pending。
type Requirement struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Settle bool   `json:"settle,omitempty"`
}

func (v ReviewRangeInput) Validate() error {
	if len(v.ChapterIDs) == 0 {
		return fmt.Errorf("review range requires chapter ids: %w", ErrInvalid)
	}
	if err := validateDistinctStrings("review chapters", v.ChapterIDs); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(v.Requirements))
	for i, requirement := range v.Requirements {
		if strings.TrimSpace(requirement.ID) == "" || strings.TrimSpace(requirement.Text) == "" {
			return fmt.Errorf("review requirement %d requires id and text: %w", i, ErrInvalid)
		}
		if _, ok := seen[requirement.ID]; ok {
			return fmt.Errorf("duplicate review requirement %q: %w", requirement.ID, ErrInvalid)
		}
		seen[requirement.ID] = struct{}{}
	}
	if err := validateDistinctStrings("reviewed chapters", v.Reviewed); err != nil {
		return err
	}
	for _, id := range v.Reviewed {
		if !slices.Contains(v.ChapterIDs, id) {
			return fmt.Errorf("reviewed chapter %q is outside the review range: %w", id, ErrInvalid)
		}
	}
	if err := validateDistinctStrings("satisfied requirements", v.Satisfied); err != nil {
		return err
	}
	for _, id := range v.Satisfied {
		if _, ok := seen[id]; !ok || len(v.Reviewed) == 0 {
			return fmt.Errorf("satisfied requirement %q must be one of the requirements, with reviewed chapters: %w", id, ErrInvalid)
		}
	}
	for i, finding := range v.PriorFindings {
		if finding.Severity != FindingBlocking || strings.TrimSpace(finding.Note) == "" ||
			!slices.Contains(v.ChapterIDs, finding.ChapterID) || slices.Contains(v.Reviewed, finding.ChapterID) {
			return fmt.Errorf("prior finding %d must be a blocking note on a chapter under review: %w", i, ErrInvalid)
		}
	}
	return validateEvidenceBasis("review", v.Basis)
}

// AdmitsFinding 是审阅发现的落点规则：只能落在审阅范围内；已审且正文未变的章（D68）
// 只作上下文，阻塞发现必须链接它违反的要求，而且不是上一份裁定已判满足的要求。
func (v ReviewRangeInput) AdmitsFinding(finding ReviewFinding) error {
	if !slices.Contains(v.ChapterIDs, finding.ChapterID) {
		return fmt.Errorf("finding chapter %q is outside the requested range; attach cross-chapter issues to the most relevant chapter in range: %w",
			finding.ChapterID, ErrInvalid)
	}
	if finding.Requirement != "" && !slices.ContainsFunc(v.Requirements, func(r Requirement) bool { return r.ID == finding.Requirement }) {
		ids := make([]string, 0, len(v.Requirements))
		for _, r := range v.Requirements {
			ids = append(ids, r.ID)
		}
		return fmt.Errorf("requirement %q is not one of this review's requirements %q; link only those ids, and leave requirement empty for any other issue: %w",
			finding.Requirement, ids, ErrInvalid)
	}
	if finding.Severity != FindingBlocking || !slices.Contains(v.Reviewed, finding.ChapterID) {
		return nil
	}
	if finding.Requirement == "" {
		return fmt.Errorf("chapter %q was already reviewed and is unchanged, so this review reads it as context only: "+
			"record the issue with severity %q, or link the requirement it violates: %w", finding.ChapterID, FindingNote, ErrInvalid)
	}
	if slices.Contains(v.Satisfied, finding.Requirement) {
		return fmt.Errorf("chapter %q and requirement %q are both unchanged since the previous review found them satisfied, so that stands: "+
			"record the issue with severity %q, or put it on the changed chapter that breaks the requirement: %w",
			finding.ChapterID, finding.Requirement, FindingNote, ErrInvalid)
	}
	return nil
}

// GenerateAssetInput 是外部生成任务的输入：为 Target 生成 Role 角色的衍生工件，
// 工件继承 Basis。
type GenerateAssetInput struct {
	Target DocumentRef   `json:"target"`
	Role   string        `json:"role"`
	Basis  EvidenceBasis `json:"basis"`
}

func (v GenerateAssetInput) Validate() error {
	if err := v.Target.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(v.Role) == "" {
		return fmt.Errorf("asset role is required: %w", ErrInvalid)
	}
	return validateEvidenceBasis("asset", v.Basis)
}

// InspectAssetInput 指定需要检查的不可变产物及来源基线，检查结果不提交作品变更。
type InspectAssetInput struct {
	Artifact ArtifactRef   `json:"artifact"`
	Basis    EvidenceBasis `json:"basis"`
}

func (v InspectAssetInput) Validate() error {
	if err := v.Artifact.Validate(); err != nil {
		return err
	}
	if err := validateEvidenceBasis("asset inspection", v.Basis); err != nil {
		return err
	}
	for _, ref := range v.Basis.Artifacts {
		if ref == v.Artifact {
			return nil
		}
	}
	return fmt.Errorf("asset inspection basis must include the inspected artifact: %w", ErrInvalid)
}

// validateEvidenceBasis 要求产出证据的任务带有非空文档基线：没有基线的证据永不失效。
func validateEvidenceBasis(name string, basis EvidenceBasis) error {
	if len(basis.Documents) == 0 {
		return fmt.Errorf("%s task requires a document basis: %w", name, ErrInvalid)
	}
	return basis.Validate()
}

// taskDirectives 返回任务输入携带的用户要求（§4.9）；不装配要求的种类返回空。
func taskDirectives(input TaskInput) []Directive {
	switch value := input.(type) {
	case *WriteChapterInput:
		return value.Directives
	case *RewriteChapterInput:
		return value.Directives
	case *ReworkChapterInput:
		return value.Directives
	default:
		return nil
	}
}

// taskBasis 返回任务输入携带的证据基线（D48/D51）：不带基线的种类返回零值，
// 零值基线在任何 Revision 上都成立。
func taskBasis(input TaskInput) EvidenceBasis {
	switch value := input.(type) {
	case *WriteChapterInput:
		return value.Basis
	case *RewriteChapterInput:
		return value.Basis
	case *ReworkChapterInput:
		return value.Basis
	case *RevisePlanInput:
		return value.Basis
	case *ReviewRangeInput:
		return value.Basis
	case *GenerateAssetInput:
		return value.Basis
	case *InspectAssetInput:
		return value.Basis
	default:
		return EvidenceBasis{}
	}
}

// OperationBasis 解码 Operation 的任务输入并取其基线。
func OperationBasis(operation Operation) (EvidenceBasis, error) {
	input, err := DecodeTaskInput(operation.Kind, operation.Input)
	if err != nil {
		return EvidenceBasis{}, err
	}
	return taskBasis(input), nil
}

func validateTaskDirectives(directives []Directive) error {
	seen := make(map[string]struct{}, len(directives))
	for i, directive := range directives {
		if err := directive.Validate(); err != nil {
			return fmt.Errorf("directive %d: %w", i, err)
		}
		if _, ok := seen[directive.ID]; ok {
			return fmt.Errorf("duplicate directive %q: %w", directive.ID, ErrInvalid)
		}
		seen[directive.ID] = struct{}{}
	}
	return nil
}
