package model

import (
	"encoding/json"
	"fmt"
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

// OperationKindSpec 是一种 Operation 的登记项：装配层按它判断执行族、Run 归属、
// 预算计数与进行中的呈现文案；新增种类只在这里登记一次。
type OperationKindSpec struct {
	Kind        OperationKind
	Executor    ExecutorFamily
	RequiresRun bool   // 影响故事内容，必须归属唯一 CreationRun（D27）
	Repair      bool   // 自动修订：消耗 RunStrategy 的修订预算
	Label       string // 进行中的创作语言
	NewInput    func() TaskInput
}

var operationKinds = []OperationKindSpec{
	{Kind: OperationInitializeProject, Executor: ExecutorLLM, RequiresRun: true, Label: "正在整理创作设定",
		NewInput: func() TaskInput { return &InitializeProjectInput{} }},
	{Kind: OperationDevelopPlan, Executor: ExecutorLLM, RequiresRun: true, Label: "正在规划故事蓝图",
		NewInput: func() TaskInput { return &DevelopPlanInput{} }},
	{Kind: OperationRevisePlan, Executor: ExecutorLLM, RequiresRun: true, Label: "正在规划故事蓝图",
		NewInput: func() TaskInput { return &RevisePlanInput{} }},
	{Kind: OperationReviseCanon, Executor: ExecutorLLM, RequiresRun: true, Label: "正在修订已确认事实",
		NewInput: func() TaskInput { return &ReviseCanonInput{} }},
	{Kind: OperationWriteChapter, Executor: ExecutorLLM, RequiresRun: true, Label: "正在落笔新章节",
		NewInput: func() TaskInput { return &WriteChapterInput{} }},
	{Kind: OperationRewriteChapter, Executor: ExecutorLLM, RequiresRun: true, Repair: true, Label: "正在按意见重写章节",
		NewInput: func() TaskInput { return &RewriteChapterInput{} }},
	{Kind: OperationRewriteAffected, Executor: ExecutorLLM, RequiresRun: true, Label: "正在同步修订受影响章节",
		NewInput: func() TaskInput { return &RewriteAffectedInput{} }},
	{Kind: OperationReviewRange, Executor: ExecutorLLM, RequiresRun: true, Label: "正在审阅已完成章节",
		NewInput: func() TaskInput { return &ReviewRangeInput{} }},
	{Kind: OperationGenerateAsset, Executor: ExecutorExternal, RequiresRun: true, Label: "正在生成衍生资产",
		NewInput: func() TaskInput { return &GenerateAssetInput{} }},
	{Kind: OperationInspectAsset, Executor: ExecutorExternal, RequiresRun: true, Label: "正在核验衍生资产",
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

type InitializeProjectInput struct {
	Intent string `json:"intent"`
	Goal   string `json:"goal,omitempty"`
}

func (v InitializeProjectInput) Validate() error {
	if strings.TrimSpace(v.Intent) == "" {
		return fmt.Errorf("intent is required: %w", ErrInvalid)
	}
	return nil
}

// DevelopPlanInput / RevisePlanInput 的 RequestedChapters 是滚动规划的窗口目标：
// 提交边界按它校验 chapter 节点数量（§6.3）。
type DevelopPlanInput struct {
	Intent            string `json:"intent"`
	TargetChapters    int    `json:"target_chapters"`
	RequestedChapters int    `json:"requested_chapters"`
	Goal              string `json:"goal,omitempty"`
}

func (v DevelopPlanInput) Validate() error {
	if strings.TrimSpace(v.Intent) == "" || v.TargetChapters <= 0 ||
		v.RequestedChapters <= 0 || v.RequestedChapters > v.TargetChapters {
		return fmt.Errorf("intent, positive target and requested chapters within target are required: %w", ErrInvalid)
	}
	return nil
}

// RevisePlanInput 的 Basis 是扩窗所依据的窗口审阅裁定基线（D51）：相干要求或正文
// 再变化时扩窗任务失效，先重审再扩。
type RevisePlanInput struct {
	Intent            string        `json:"intent"`
	TargetChapters    int           `json:"target_chapters"`
	ExistingChapters  int           `json:"existing_chapters"`
	RequestedChapters int           `json:"requested_chapters"`
	ReviewNotes       []string      `json:"review_notes,omitempty"`
	Basis             EvidenceBasis `json:"basis,omitzero"`
	Goal              string        `json:"goal,omitempty"`
}

func (v RevisePlanInput) Validate() error {
	if strings.TrimSpace(v.Intent) == "" || v.TargetChapters <= 0 || v.ExistingChapters < 0 ||
		v.RequestedChapters <= v.ExistingChapters || v.RequestedChapters > v.TargetChapters {
		return fmt.Errorf("intent, positive target and requested chapters beyond existing are required: %w", ErrInvalid)
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
// 不相干的用户专属变化（锁定、审批、其他章的要求）只让提案重定位。
type WriteChapterInput struct {
	ChapterPlanID string        `json:"chapter_plan_id"`
	ChapterNumber int           `json:"chapter_number"`
	Directives    []Directive   `json:"directives,omitempty"`
	Basis         EvidenceBasis `json:"basis,omitzero"`
	Goal          string        `json:"goal,omitempty"`
}

func (v WriteChapterInput) Validate() error {
	if strings.TrimSpace(v.ChapterPlanID) == "" || v.ChapterNumber <= 0 {
		return fmt.Errorf("chapter plan and positive chapter number are required: %w", ErrInvalid)
	}
	if err := validateTaskDirectives(v.Directives); err != nil {
		return err
	}
	return v.Basis.Validate()
}

type RewriteChapterInput struct {
	ChapterID     string        `json:"chapter_id"`
	ChapterPlanID string        `json:"chapter_plan_id"`
	ChapterNumber int           `json:"chapter_number"`
	Findings      []string      `json:"findings"`
	Directives    []Directive   `json:"directives,omitempty"`
	Basis         EvidenceBasis `json:"basis,omitzero"`
	Goal          string        `json:"goal,omitempty"`
}

func (v RewriteChapterInput) Validate() error {
	if strings.TrimSpace(v.ChapterID) == "" || strings.TrimSpace(v.ChapterPlanID) == "" ||
		v.ChapterNumber <= 0 || len(v.Findings) == 0 {
		return fmt.Errorf("chapter, plan, number and findings are required: %w", ErrInvalid)
	}
	if err := validateTaskDirectives(v.Directives); err != nil {
		return err
	}
	return v.Basis.Validate()
}

type RewriteAffectedInput struct {
	ChapterIDs           []string `json:"chapter_ids"`
	BaseRevision         Revision `json:"base_revision"`
	ResolutionProposalID string   `json:"resolution_proposal_id"`
	Reason               string   `json:"reason"`
}

func (v RewriteAffectedInput) Validate() error {
	if v.BaseRevision <= InitialRevision || strings.TrimSpace(v.ResolutionProposalID) == "" || strings.TrimSpace(v.Reason) == "" {
		return fmt.Errorf("base revision, resolution proposal and reason are required: %w", ErrInvalid)
	}
	return validateDistinctStrings("affected chapters", v.ChapterIDs)
}

// ReviewRangeInput 的 Basis 由装配层构造：裁定继承它，有效性按基线判定（D48）。
type ReviewRangeInput struct {
	ChapterIDs   []string      `json:"chapter_ids"`
	VerifyIntent bool          `json:"verify_intent"`
	Directives   []Directive   `json:"directives,omitempty"`
	Basis        EvidenceBasis `json:"basis"`
	Goal         string        `json:"goal,omitempty"`
}

func (v ReviewRangeInput) Validate() error {
	if len(v.ChapterIDs) == 0 {
		return fmt.Errorf("review range requires chapter ids: %w", ErrInvalid)
	}
	if err := validateDistinctStrings("review chapters", v.ChapterIDs); err != nil {
		return err
	}
	if err := validateTaskDirectives(v.Directives); err != nil {
		return err
	}
	return validateEvidenceBasis("review", v.Basis)
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

// TaskDirectives 返回任务输入携带的用户要求（§4.9）；不装配要求的种类返回空。
func TaskDirectives(input TaskInput) []Directive {
	switch value := input.(type) {
	case *WriteChapterInput:
		return value.Directives
	case *RewriteChapterInput:
		return value.Directives
	case *ReviewRangeInput:
		return value.Directives
	default:
		return nil
	}
}

// TaskBasis 返回任务输入携带的证据基线（D48/D51）：不带基线的种类返回零值，
// 零值基线在任何 Revision 上都成立。
func TaskBasis(input TaskInput) EvidenceBasis {
	switch value := input.(type) {
	case *WriteChapterInput:
		return value.Basis
	case *RewriteChapterInput:
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
	return TaskBasis(input), nil
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
