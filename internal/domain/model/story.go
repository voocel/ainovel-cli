package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type Intent struct {
	Premise           string   `json:"premise"`
	Audience          string   `json:"audience,omitempty"`
	DesiredExperience []string `json:"desired_experience,omitempty"`
	Required          []string `json:"required,omitempty"`
	Forbidden         []string `json:"forbidden,omitempty"`
	EndingDirection   string   `json:"ending_direction,omitempty"`
	TargetChapters    int      `json:"target_chapters,omitempty"`
}

func (v Intent) Validate() error {
	if strings.TrimSpace(v.Premise) == "" {
		return fmt.Errorf("intent premise is required: %w", ErrInvalid)
	}
	if v.TargetChapters < 0 {
		return fmt.Errorf("target chapters cannot be negative: %w", ErrInvalid)
	}
	return validateDistinctStrings("intent required", v.Required)
}

type PlanNodeKind string

const (
	PlanVolume  PlanNodeKind = "volume"
	PlanArc     PlanNodeKind = "arc"
	PlanChapter PlanNodeKind = "chapter"
	PlanBeat    PlanNodeKind = "beat"
)

type PlanNode struct {
	ID        string        `json:"id"`
	Kind      PlanNodeKind  `json:"kind"`
	ParentID  string        `json:"parent_id,omitempty"`
	Order     int           `json:"order"`
	Title     string        `json:"title"`
	Summary   string        `json:"summary"`
	DependsOn []DocumentRef `json:"depends_on,omitempty"`
}

// PlanAncestry 返回节点自身及其祖先 ID（章 → 弧 → 卷），供作用域匹配；
// 节点缺失时在断点处停止。
func PlanAncestry(plan []PlanNode, id string) []string {
	byID := make(map[string]PlanNode, len(plan))
	for _, node := range plan {
		byID[node.ID] = node
	}
	var path []string
	for current := id; current != "" && len(path) <= len(plan); {
		node, ok := byID[current]
		if !ok {
			break
		}
		path = append(path, current)
		current = node.ParentID
	}
	return path
}

func (v PlanNode) Validate() error {
	if strings.TrimSpace(v.ID) == "" {
		return fmt.Errorf("plan node id is required: %w", ErrInvalid)
	}
	switch v.Kind {
	case PlanVolume:
		if v.ParentID != "" {
			return fmt.Errorf("volume cannot have parent: %w", ErrInvalid)
		}
	case PlanArc, PlanChapter, PlanBeat:
		if strings.TrimSpace(v.ParentID) == "" {
			return fmt.Errorf("%s parent is required: %w", v.Kind, ErrInvalid)
		}
	default:
		return fmt.Errorf("unknown plan node kind %q: %w", v.Kind, ErrInvalid)
	}
	if v.Order < 0 {
		return fmt.Errorf("plan node order cannot be negative: %w", ErrInvalid)
	}
	if strings.TrimSpace(v.Title) == "" || strings.TrimSpace(v.Summary) == "" {
		return fmt.Errorf("plan node title and summary are required: %w", ErrInvalid)
	}
	return validateDocumentRefs(v.DependsOn)
}

type CanonFactKind string

const (
	CanonEvent        CanonFactKind = "event"
	CanonState        CanonFactKind = "state"
	CanonRelationship CanonFactKind = "relationship"
	CanonWorldRule    CanonFactKind = "world_rule"
	CanonForeshadow   CanonFactKind = "foreshadow"
)

// Entity 是独立权威节点（D35）：角色、地点、物品、组织。Canon 事实的主体引用
// 实体 ID，实体缺失即结构冲突；实体本身不承载事实。
type EntityKind string

const (
	EntityCharacter    EntityKind = "character"
	EntityLocation     EntityKind = "location"
	EntityItem         EntityKind = "item"
	EntityOrganization EntityKind = "organization"
)

type Entity struct {
	ID      string     `json:"id"`
	Kind    EntityKind `json:"kind"`
	Name    string     `json:"name"`
	Aliases []string   `json:"aliases,omitempty"`
}

func (v Entity) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Name) == "" {
		return fmt.Errorf("entity id and name are required: %w", ErrInvalid)
	}
	switch v.Kind {
	case EntityCharacter, EntityLocation, EntityItem, EntityOrganization:
	default:
		return fmt.Errorf("unknown entity kind %q: %w", v.Kind, ErrInvalid)
	}
	return validateDistinctStrings("entity aliases", v.Aliases)
}

type CanonFact struct {
	ID              string          `json:"id"`
	Kind            CanonFactKind   `json:"kind"`
	SubjectID       string          `json:"subject_id"`
	Predicate       string          `json:"predicate"`
	PreviousValue   json.RawMessage `json:"old_value,omitempty"`
	Value           json.RawMessage `json:"new_value"`
	SourceChapterID string          `json:"source_chapter_id,omitempty"`
	// EffectiveChapterID 是状态类事实在故事中的生效章节（D41）：插叙、回忆才与来源章
	// 不同，缺省即来源章；事件按来源章只追加，不用它。
	EffectiveChapterID string        `json:"effective_chapter_id,omitempty"`
	DependsOn          []DocumentRef `json:"depends_on,omitempty"`
}

// EffectiveChapter 是事实在故事中的生效章节，缺省为来源章；两者都空表示规划期事实。
func (v CanonFact) EffectiveChapter() string {
	if v.EffectiveChapterID != "" {
		return v.EffectiveChapterID
	}
	return v.SourceChapterID
}

func (v CanonFact) IsEvent() bool { return v.Kind == CanonEvent }

func (v CanonFact) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.SubjectID) == "" || strings.TrimSpace(v.Predicate) == "" {
		return fmt.Errorf("canon id, subject_id and predicate are required: %w", ErrInvalid)
	}
	var prefix string
	switch v.Kind {
	case CanonEvent:
		prefix = "event."
	case CanonState:
		prefix = "state."
	case CanonRelationship:
		prefix = "relation."
	case CanonWorldRule:
		prefix = "rule."
	case CanonForeshadow:
		prefix = "foreshadow."
	default:
		return fmt.Errorf("unknown canon kind %q: %w", v.Kind, ErrInvalid)
	}
	if !strings.HasPrefix(v.Predicate, prefix) || !validCanonKey(v.Predicate) {
		return fmt.Errorf("canon predicate %q must use the %q controlled namespace: %w", v.Predicate, prefix, ErrInvalid)
	}
	if len(v.PreviousValue) != 0 && !json.Valid(v.PreviousValue) {
		return fmt.Errorf("canon old_value must be valid JSON: %w", ErrInvalid)
	}
	if len(v.Value) == 0 || !json.Valid(v.Value) {
		return fmt.Errorf("canon new_value must be valid JSON: %w", ErrInvalid)
	}
	for _, id := range []string{v.SourceChapterID, v.EffectiveChapterID} {
		if id != "" && strings.TrimSpace(id) == "" {
			return fmt.Errorf("canon chapter references cannot be blank: %w", ErrInvalid)
		}
	}
	return validateDocumentRefs(v.DependsOn)
}

func validCanonKey(value string) bool {
	for index, char := range value {
		lowercase := char >= 'a' && char <= 'z'
		if index == 0 {
			if lowercase {
				continue
			}
			return false
		}
		digit := char >= '0' && char <= '9'
		if lowercase || digit || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

type ManuscriptBlock struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// ManuscriptChapter 记录章级作者（D34）：用户写的章默认锁定，AI 不得冒认用户
// 身份；DependsOn 是故事依赖（D40），由宿主在提交时按本章 Canon Delta 的实体写入。
type ManuscriptChapter struct {
	ID         string            `json:"id"`
	PlanNodeID string            `json:"plan_node_id"`
	Number     int               `json:"number"`
	Title      string            `json:"title"`
	Author     AuthorKind        `json:"author"`
	Blocks     []ManuscriptBlock `json:"blocks"`
	DependsOn  []DocumentRef     `json:"depends_on,omitempty"`
}

func (v ManuscriptChapter) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.PlanNodeID) == "" {
		return fmt.Errorf("chapter id and plan_node_id are required: %w", ErrInvalid)
	}
	switch v.Author {
	case AuthorUser, AuthorAI, AuthorExtension:
	default:
		return fmt.Errorf("chapter author must be user, ai or extension, got %q: %w", v.Author, ErrInvalid)
	}
	if v.Number <= 0 || strings.TrimSpace(v.Title) == "" || len(v.Blocks) == 0 {
		return fmt.Errorf("chapter number, title and blocks are required: %w", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(v.Blocks))
	for i, block := range v.Blocks {
		if strings.TrimSpace(block.ID) == "" || strings.TrimSpace(block.Text) == "" {
			return fmt.Errorf("chapter block %d requires id and text: %w", i, ErrInvalid)
		}
		if _, ok := seen[block.ID]; ok {
			return fmt.Errorf("duplicate chapter block id %q: %w", block.ID, ErrInvalid)
		}
		seen[block.ID] = struct{}{}
	}
	return validateDocumentRefs(v.DependsOn)
}

// ApprovalSetting 是本书当前审批策略的权威文档（单例 root，§6.3）：
// 控制权威只有 Project 最新已批准 Revision 一处，Operation 快照只是历史最低约束。
// custom 需要显式策略契约，不允许作为可存储的简单设置。
type ApprovalSetting struct {
	Policy ApprovalPolicy `json:"policy"`
}

func (v ApprovalSetting) Validate() error {
	switch v.Policy {
	case ApprovalAuto, ApprovalMilestone, ApprovalManual:
		return nil
	default:
		return fmt.Errorf("approval setting must be auto, milestone or manual, got %q: %w", v.Policy, ErrInvalid)
	}
}

// ProjectOverlay 是书级创作规则文档（§7.1）：真实可编辑的内容，随 Project
// Revision 版本化进入提示词；不是 Creator Profile 或 Pack 的别名。
type ProjectOverlay struct {
	Rules []string `json:"rules"`
}

func (v ProjectOverlay) Validate() error {
	if len(v.Rules) == 0 {
		return fmt.Errorf("project overlay requires at least one rule: %w", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(v.Rules))
	for i, rule := range v.Rules {
		if strings.TrimSpace(rule) == "" {
			return fmt.Errorf("project overlay rule %d is empty: %w", i, ErrInvalid)
		}
		if _, ok := seen[rule]; ok {
			return fmt.Errorf("duplicate project overlay rule %q: %w", rule, ErrInvalid)
		}
		seen[rule] = struct{}{}
	}
	return nil
}

// ProjectAssetRefs 是 Project 启用的跨作品资产固定引用（D31）：只保存引用与
// 固定 Revision，资产本体在各自独立的 Authority Stream；启用或升级必须用户确认。
type ProjectAssetRefs struct {
	Packs           []ProjectPackRef    `json:"packs,omitempty"`
	CreatorProfiles []ProjectProfileRef `json:"creator_profiles,omitempty"`
}

type ProjectPackRef struct {
	ID       string   `json:"id"`
	Revision Revision `json:"revision"`
}

type ProjectProfileRef struct {
	ID       string   `json:"id"`
	Scope    string   `json:"scope"`
	Revision Revision `json:"revision"`
}

func (v ProjectAssetRefs) Validate() error {
	if len(v.Packs) == 0 && len(v.CreatorProfiles) == 0 {
		return fmt.Errorf("project assets require at least one reference: %w", ErrInvalid)
	}
	seenPacks := make(map[string]struct{}, len(v.Packs))
	for i, ref := range v.Packs {
		if strings.TrimSpace(ref.ID) == "" || ref.Revision <= InitialRevision {
			return fmt.Errorf("pack reference %d requires id and pinned revision: %w", i, ErrInvalid)
		}
		if _, ok := seenPacks[ref.ID]; ok {
			return fmt.Errorf("duplicate pack reference %q: %w", ref.ID, ErrInvalid)
		}
		seenPacks[ref.ID] = struct{}{}
	}
	seenProfiles := make(map[string]struct{}, len(v.CreatorProfiles))
	for i, ref := range v.CreatorProfiles {
		if strings.TrimSpace(ref.ID) == "" || strings.TrimSpace(ref.Scope) == "" || ref.Revision <= InitialRevision {
			return fmt.Errorf("creator profile reference %d requires id, scope and pinned revision: %w", i, ErrInvalid)
		}
		key := ref.ID + "/" + ref.Scope
		if _, ok := seenProfiles[key]; ok {
			return fmt.Errorf("duplicate creator profile reference %q: %w", key, ErrInvalid)
		}
		seenProfiles[key] = struct{}{}
	}
	return nil
}

type OwnershipRule struct {
	Target   DocumentRef  `json:"target"`
	Control  ControlLevel `json:"control"`
	Guidance []string     `json:"guidance,omitempty"`
}

func (v OwnershipRule) Validate() error {
	if err := v.Target.Validate(); err != nil {
		return err
	}
	switch v.Control {
	case ControlLocked, ControlGuided, ControlOpen:
	default:
		return fmt.Errorf("unknown control level %q: %w", v.Control, ErrInvalid)
	}
	if v.Control == ControlGuided && len(v.Guidance) == 0 {
		return fmt.Errorf("guided ownership requires guidance: %w", ErrInvalid)
	}
	return validateDistinctStrings("ownership guidance", v.Guidance)
}

// ValidatePlanChapterTarget 在提交边界按 Proposal 应用后的结果验证固定章节目标。
// 它只判断结构覆盖，不判断文学内容；超量和缺量都必须显式修正，不能在完成时忽略。
func ValidatePlanChapterTarget(base []PlanNode, patches []Patch, expected int) error {
	if expected <= 0 {
		return fmt.Errorf("plan chapter target must be positive: %w", ErrInvalid)
	}
	nodes := make(map[string]PlanNode, len(base))
	for _, node := range base {
		if err := node.Validate(); err != nil {
			return err
		}
		nodes[node.ID] = node
	}
	for _, patch := range patches {
		if patch.Document.Kind != DocumentPlan {
			continue
		}
		switch patch.Operation {
		case PatchDelete:
			delete(nodes, patch.Document.ID)
		case PatchPut:
			if err := ValidateDocumentContent(patch.Document, patch.Content); err != nil {
				return err
			}
			var node PlanNode
			if err := json.Unmarshal(patch.Content, &node); err != nil {
				return fmt.Errorf("decode proposed plan node %q: %w", patch.Document.ID, err)
			}
			nodes[node.ID] = node
		default:
			return fmt.Errorf("unknown plan patch operation %q: %w", patch.Operation, ErrInvalid)
		}
	}
	chapters := 0
	for _, node := range nodes {
		if node.Kind == PlanChapter {
			chapters++
		}
	}
	if chapters != expected {
		return fmt.Errorf("plan has %d chapter nodes, requested exactly %d: %w", chapters, expected, ErrInvalid)
	}
	return nil
}

// PlanChapterTargetForOperation 解析滚动规划 Operation 请求的章节数；
// 非规划类 Operation 不受数量不变量约束（ok=false）。
func PlanChapterTargetForOperation(operation Operation) (int, bool, error) {
	switch operation.Kind {
	case OperationDevelopPlan:
		input, err := TaskInputAs[DevelopPlanInput](operation)
		return input.RequestedChapters, err == nil, err
	case OperationRevisePlan:
		input, err := TaskInputAs[RevisePlanInput](operation)
		return input.RequestedChapters, err == nil, err
	default:
		return 0, false, nil
	}
}

// BindChapterDependencies 由宿主写入章节的故事依赖（D40）：本章 Canon Delta 的
// 主体实体。模型自行声明的 depends_on 被覆盖，依赖只来自可验证的提交内容。
func BindChapterDependencies(patches []Patch) ([]Patch, error) {
	subjects := make(map[string][]DocumentRef)
	for _, patch := range patches {
		if patch.Document.Kind != DocumentCanon || patch.Operation != PatchPut {
			continue
		}
		var fact CanonFact
		if err := DecodeStrict(patch.Content, &fact); err != nil {
			return nil, fmt.Errorf("decode canon delta %q: %w", patch.Document.ID, err)
		}
		if fact.SourceChapterID != "" {
			subjects[fact.SourceChapterID] = append(subjects[fact.SourceChapterID], DocumentRef{Kind: DocumentEntity, ID: fact.SubjectID})
		}
	}
	bound := slices.Clone(patches)
	for i, patch := range bound {
		if patch.Document.Kind != DocumentManuscript || patch.Operation != PatchPut {
			continue
		}
		var chapter ManuscriptChapter
		if err := DecodeStrict(patch.Content, &chapter); err != nil {
			return nil, fmt.Errorf("decode chapter %q: %w", patch.Document.ID, err)
		}
		dependencies := subjects[chapter.ID]
		slices.SortFunc(dependencies, func(a, b DocumentRef) int { return strings.Compare(a.Key(), b.Key()) })
		chapter.DependsOn = slices.CompactFunc(dependencies, func(a, b DocumentRef) bool { return a.Key() == b.Key() })
		content, err := json.Marshal(chapter)
		if err != nil {
			return nil, fmt.Errorf("encode chapter %q: %w", patch.Document.ID, err)
		}
		bound[i].Content = content
	}
	return bound, nil
}

func validateDocumentRefs(refs []DocumentRef) error {
	seen := make(map[string]struct{}, len(refs))
	for i, ref := range refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("dependency %d: %w", i, err)
		}
		if _, ok := seen[ref.Key()]; ok {
			return fmt.Errorf("duplicate dependency %q: %w", ref.Key(), ErrInvalid)
		}
		seen[ref.Key()] = struct{}{}
	}
	return nil
}

func validateDistinctStrings(name string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s item %d is empty: %w", name, i, ErrInvalid)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s contains duplicate %q: %w", name, value, ErrInvalid)
		}
		seen[value] = struct{}{}
	}
	return nil
}
