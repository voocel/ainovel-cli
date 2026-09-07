package domain

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

type CanonFact struct {
	ID              string          `json:"id"`
	Kind            CanonFactKind   `json:"kind"`
	SubjectID       string          `json:"subject_id"`
	Predicate       string          `json:"predicate"`
	PreviousValue   json.RawMessage `json:"old_value,omitempty"`
	Value           json.RawMessage `json:"new_value"`
	SourceChapterID string          `json:"source_chapter_id,omitempty"`
	DependsOn       []DocumentRef   `json:"depends_on,omitempty"`
}

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
	if v.SourceChapterID != "" && strings.TrimSpace(v.SourceChapterID) == "" {
		return fmt.Errorf("canon source_chapter_id cannot be blank: %w", ErrInvalid)
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

type ManuscriptChapter struct {
	ID         string            `json:"id"`
	PlanNodeID string            `json:"plan_node_id"`
	Number     int               `json:"number"`
	Title      string            `json:"title"`
	Blocks     []ManuscriptBlock `json:"blocks"`
	DependsOn  []DocumentRef     `json:"depends_on,omitempty"`
}

func (v ManuscriptChapter) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.PlanNodeID) == "" {
		return fmt.Errorf("chapter id and plan_node_id are required: %w", ErrInvalid)
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
// 非规划类或未声明数量的 Operation 不受数量不变量约束（ok=false）。
func PlanChapterTargetForOperation(operation Operation) (int, bool, error) {
	if operation.Kind != OperationDevelopPlan && operation.Kind != OperationRevisePlan {
		return 0, false, nil
	}
	var input struct {
		RequestedChapters int `json:"requested_chapters"`
	}
	if err := json.Unmarshal(operation.Input, &input); err != nil {
		return 0, false, fmt.Errorf("decode plan operation input: %w", err)
	}
	if input.RequestedChapters == 0 {
		return 0, false, nil
	}
	return input.RequestedChapters, true, nil
}

type CreatorProfile struct {
	ID                   string                `json:"id"`
	Scope                string                `json:"scope"`
	ExplicitRules        []string              `json:"explicit_rules,omitempty"`
	PositiveExamples     []string              `json:"positive_examples,omitempty"`
	NegativeExamples     []string              `json:"negative_examples,omitempty"`
	Preferences          map[string]string     `json:"style_preferences,omitempty"`
	PreferenceCandidates []PreferenceCandidate `json:"preference_candidates,omitempty"`
}

type PreferenceCandidate struct {
	ID                  string            `json:"id"`
	Summary             string            `json:"summary"`
	Evidence            []string          `json:"evidence"`
	ProposedRules       []string          `json:"proposed_rules,omitempty"`
	ProposedPreferences map[string]string `json:"proposed_style_preferences,omitempty"`
	SourceProjectID     string            `json:"source_project_id"`
	SourceRevision      Revision          `json:"source_revision"`
}

type ManuscriptEditEvidence struct {
	ChapterID string `json:"chapter_id"`
	BlockID   string `json:"block_id"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
}

type PreferenceLearningInput struct {
	CandidateID     string                   `json:"candidate_id"`
	ProfileID       string                   `json:"profile_id"`
	Scope           string                   `json:"scope"`
	ProjectID       string                   `json:"project_id"`
	FromRevision    Revision                 `json:"from_revision"`
	ToRevision      Revision                 `json:"to_revision"`
	ManuscriptEdits []ManuscriptEditEvidence `json:"manuscript_edits"`
}

func (v PreferenceLearningInput) Validate() error {
	if strings.TrimSpace(v.CandidateID) == "" || strings.TrimSpace(v.ProfileID) == "" ||
		strings.TrimSpace(v.Scope) == "" || strings.TrimSpace(v.ProjectID) == "" ||
		v.FromRevision <= InitialRevision || v.ToRevision <= v.FromRevision || len(v.ManuscriptEdits) == 0 {
		return fmt.Errorf("preference learning identity, revision range and edits are required: %w", ErrInvalid)
	}
	for index, edit := range v.ManuscriptEdits {
		if strings.TrimSpace(edit.ChapterID) == "" || strings.TrimSpace(edit.BlockID) == "" || edit.Before == edit.After {
			return fmt.Errorf("manuscript edit %d is invalid: %w", index, ErrInvalid)
		}
	}
	return nil
}

func (v PreferenceCandidate) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Summary) == "" ||
		strings.TrimSpace(v.SourceProjectID) == "" || v.SourceRevision <= InitialRevision || len(v.Evidence) == 0 {
		return fmt.Errorf("preference candidate is invalid: %w", ErrInvalid)
	}
	if err := validateDistinctStrings("candidate evidence", v.Evidence); err != nil {
		return err
	}
	if err := validateDistinctStrings("candidate rules", v.ProposedRules); err != nil {
		return err
	}
	if len(v.ProposedRules) == 0 && len(v.ProposedPreferences) == 0 {
		return fmt.Errorf("preference candidate requires at least one proposed rule or preference: %w", ErrInvalid)
	}
	for key := range v.ProposedPreferences {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("candidate preference key is required: %w", ErrInvalid)
		}
	}
	return nil
}

func (v CreatorProfile) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Scope) == "" {
		return fmt.Errorf("creator profile id and scope are required: %w", ErrInvalid)
	}
	if err := validateDistinctStrings("profile rules", v.ExplicitRules); err != nil {
		return err
	}
	for key := range v.Preferences {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("profile preference key is required: %w", ErrInvalid)
		}
	}
	seenCandidates := make(map[string]struct{}, len(v.PreferenceCandidates))
	for i, candidate := range v.PreferenceCandidates {
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("profile preference candidate %d: %w", i, err)
		}
		if _, ok := seenCandidates[candidate.ID]; ok {
			return fmt.Errorf("duplicate preference candidate %q: %w", candidate.ID, ErrInvalid)
		}
		seenCandidates[candidate.ID] = struct{}{}
	}
	return nil
}

type PackManifest struct {
	ID             string            `json:"id"`
	Version        string            `json:"version"`
	Name           string            `json:"name"`
	PromptOverlays map[string]string `json:"prompt_overlays,omitempty"`
	Rules          []string          `json:"rules,omitempty"`
	References     []string          `json:"references,omitempty"`
	ReferenceData  map[string]string `json:"reference_data,omitempty"`
	Templates      []string          `json:"templates,omitempty"`
	TemplateData   map[string]string `json:"template_data,omitempty"`
	Evals          []string          `json:"evals,omitempty"`
	EvalData       map[string]string `json:"eval_data,omitempty"`
}

func (v PackManifest) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Version) == "" || strings.TrimSpace(v.Name) == "" {
		return fmt.Errorf("pack id, version and name are required: %w", ErrInvalid)
	}
	for slot := range v.PromptOverlays {
		if strings.TrimSpace(slot) == "" {
			return fmt.Errorf("prompt overlay slot is required: %w", ErrInvalid)
		}
	}
	if err := validateDistinctStrings("pack rules", v.Rules); err != nil {
		return err
	}
	for _, assets := range []struct {
		name string
		keys []string
		data map[string]string
	}{
		{"references", v.References, v.ReferenceData},
		{"templates", v.Templates, v.TemplateData},
		{"evals", v.Evals, v.EvalData},
	} {
		if err := validateDistinctStrings("pack "+assets.name, assets.keys); err != nil {
			return err
		}
		if len(assets.keys) != len(assets.data) {
			return fmt.Errorf("pack %s index and data differ: %w", assets.name, ErrInvalid)
		}
		for _, key := range assets.keys {
			if _, ok := assets.data[key]; !ok {
				return fmt.Errorf("pack %s data %q is missing: %w", assets.name, key, ErrInvalid)
			}
		}
	}
	return nil
}

func ValidateDocumentContent(ref DocumentRef, content json.RawMessage) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	switch ref.Kind {
	case DocumentIntent:
		value, err := decodeStrict[Intent](content)
		if err != nil {
			return fmt.Errorf("decode intent: %w", err)
		}
		return value.Validate()
	case DocumentPlan:
		value, err := decodeStrict[PlanNode](content)
		if err != nil {
			return fmt.Errorf("decode plan node: %w", err)
		}
		if value.ID != ref.ID {
			return fmt.Errorf("plan node id %q does not match document id %q: %w", value.ID, ref.ID, ErrInvalid)
		}
		return value.Validate()
	case DocumentCanon:
		value, err := decodeStrict[CanonFact](content)
		if err != nil {
			return fmt.Errorf("decode canon fact: %w", err)
		}
		if value.ID != ref.ID {
			return fmt.Errorf("canon fact id %q does not match document id %q: %w", value.ID, ref.ID, ErrInvalid)
		}
		return value.Validate()
	case DocumentManuscript:
		value, err := decodeStrict[ManuscriptChapter](content)
		if err != nil {
			return fmt.Errorf("decode manuscript: %w", err)
		}
		if value.ID != ref.ID {
			return fmt.Errorf("chapter id %q does not match document id %q: %w", value.ID, ref.ID, ErrInvalid)
		}
		return value.Validate()
	case DocumentOwnership:
		value, err := decodeStrict[OwnershipRule](content)
		if err != nil {
			return fmt.Errorf("decode ownership: %w", err)
		}
		if value.Target.Key() != ref.ID {
			return fmt.Errorf("ownership target %q does not match document id %q: %w", value.Target.Key(), ref.ID, ErrInvalid)
		}
		return value.Validate()
	case DocumentApproval:
		value, err := decodeStrict[ApprovalSetting](content)
		if err != nil {
			return fmt.Errorf("decode approval setting: %w", err)
		}
		if ref.ID != "root" {
			return fmt.Errorf("approval document id must be root: %w", ErrInvalid)
		}
		return value.Validate()
	case DocumentOverlay:
		value, err := decodeStrict[ProjectOverlay](content)
		if err != nil {
			return fmt.Errorf("decode project overlay: %w", err)
		}
		if ref.ID != "root" {
			return fmt.Errorf("overlay document id must be root: %w", ErrInvalid)
		}
		return value.Validate()
	case DocumentAssets:
		value, err := decodeStrict[ProjectAssetRefs](content)
		if err != nil {
			return fmt.Errorf("decode project asset refs: %w", err)
		}
		if ref.ID != "root" {
			return fmt.Errorf("assets document id must be root: %w", ErrInvalid)
		}
		return value.Validate()
	case DocumentDirective:
		value, err := decodeStrict[Directive](content)
		if err != nil {
			return fmt.Errorf("decode directive: %w", err)
		}
		if value.ID != ref.ID {
			return fmt.Errorf("directive id %q does not match document id %q: %w", value.ID, ref.ID, ErrInvalid)
		}
		return value.Validate()
	case DocumentCreatorProfile:
		value, err := decodeStrict[CreatorProfile](content)
		if err != nil {
			return fmt.Errorf("decode creator profile: %w", err)
		}
		if value.ID != ref.ID {
			return fmt.Errorf("profile id %q does not match document id %q: %w", value.ID, ref.ID, ErrInvalid)
		}
		return value.Validate()
	case DocumentPack:
		value, err := decodeStrict[PackManifest](content)
		if err != nil {
			return fmt.Errorf("decode pack manifest: %w", err)
		}
		if value.ID != ref.ID {
			return fmt.Errorf("pack id %q does not match document id %q: %w", value.ID, ref.ID, ErrInvalid)
		}
		return value.Validate()
	default:
		return fmt.Errorf("unsupported document kind %q: %w", ref.Kind, ErrInvalid)
	}
}

func DocumentDependencies(ref DocumentRef, content json.RawMessage) ([]DocumentRef, error) {
	if err := ValidateDocumentContent(ref, content); err != nil {
		return nil, err
	}
	var dependencies []DocumentRef
	switch ref.Kind {
	case DocumentPlan:
		value, err := decodeStrict[PlanNode](content)
		if err != nil {
			return nil, err
		}
		dependencies = append(dependencies, value.DependsOn...)
		if value.ParentID != "" {
			dependencies = append(dependencies, DocumentRef{Kind: DocumentPlan, ID: value.ParentID})
		}
	case DocumentCanon:
		value, err := decodeStrict[CanonFact](content)
		if err != nil {
			return nil, err
		}
		dependencies = append(dependencies, value.DependsOn...)
	case DocumentManuscript:
		value, err := decodeStrict[ManuscriptChapter](content)
		if err != nil {
			return nil, err
		}
		dependencies = append(dependencies, value.DependsOn...)
		dependencies = append(dependencies, DocumentRef{Kind: DocumentPlan, ID: value.PlanNodeID})
	case DocumentOwnership:
		value, err := decodeStrict[OwnershipRule](content)
		if err != nil {
			return nil, err
		}
		dependencies = append(dependencies, value.Target)
	}
	slices.SortFunc(dependencies, func(a, b DocumentRef) int { return strings.Compare(a.Key(), b.Key()) })
	dependencies = slices.CompactFunc(dependencies, func(a, b DocumentRef) bool { return a.Key() == b.Key() })
	return dependencies, nil
}

func decodeStrict[T any](content json.RawMessage) (T, error) {
	var value T
	err := DecodeStrict(content, &value)
	return value, err
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
