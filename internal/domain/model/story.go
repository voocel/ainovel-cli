package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

type Intent struct {
	Premise           string   `json:"premise"`
	Audience          string   `json:"audience,omitempty"`
	DesiredExperience []string `json:"desired_experience,omitempty"`
	Required          []string `json:"required,omitempty"`
	Forbidden         []string `json:"forbidden,omitempty"`
	EndingDirection   string   `json:"ending_direction,omitempty"`
}

func (v Intent) Validate() error {
	if strings.TrimSpace(v.Premise) == "" {
		return fmt.Errorf("intent premise is required: %w", ErrInvalid)
	}
	return validateDistinctStrings("intent required", v.Required)
}

// Compass 是故事罗盘（D63）：AI 在蓝图里给出的终局、长线与篇幅，随滚动规划修订，只有
// 规划任务能写。终局方向总由 AI 定；篇幅只在交给 AI 时才有（D70）：Final 是收官承诺
// （全书章数，0 表示尚未收官），ScaleMax 是超过护栏的篇幅上限（D96）；用户固定篇幅时二者为 0。
type Compass struct {
	// ScaleMax 是 AI 申请、经用户认可的篇幅上限，只在故事需要超过护栏时才有（D96）；
	// 篇幅不预计，0 表示上限就是护栏。
	ScaleMax int    `json:"scale_max,omitempty"`
	Ending   string `json:"ending"` // 终局方向
	Final    int    `json:"final,omitempty"`
	// Threads 是长线（D91）：全书在追问、终局要回答的几个大问题，章节计划引用它们。
	Threads []StoryThread `json:"threads,omitempty"`
}

// StoryThread 是一条长线（D91）：驱动全书的一个问题，与伏笔（埋下待回收的具体线索）不同。
// 章节计划按 ID 引用它推进的长线；Answer 是作答那一章的计划 ID，未定为空。
type StoryThread struct {
	ID       string `json:"id"`
	Name     string `json:"name"`     // 短名，故事语言里按它指称
	Question string `json:"question"` // 全书追问的问题
	Answer   string `json:"answer,omitempty"`
}

// Thread 按 ID 取长线。
func (v Compass) Thread(id string) (StoryThread, bool) {
	for _, thread := range v.Threads {
		if thread.ID == id {
			return thread, true
		}
	}
	return StoryThread{}, false
}

// CompassAutonomyCeiling 是无人值守的篇幅护栏（D63/D96）：未收官的蓝图超过它，AI 须经用户裁决。
const CompassAutonomyCeiling = 300

// Limit 是 AI 不经用户就能规划到的章数（D96）：护栏，或用户已经认可的更高上限。提交边界与
// 审批判定共用这一条规则。
func (v Compass) Limit() int {
	return max(CompassAutonomyCeiling, v.ScaleMax)
}

func (v Compass) Validate() error {
	if strings.TrimSpace(v.Ending) == "" {
		return fmt.Errorf("compass requires an ending: %w", ErrInvalid)
	}
	if v.ScaleMax < 0 || v.Final < 0 || v.Final > v.Limit() {
		return fmt.Errorf("compass final %d must be within the length limit %d (a higher scale_max raises it, with the user's approval): %w", v.Final, v.Limit(), ErrInvalid)
	}
	ids, names := make([]string, len(v.Threads)), make([]string, len(v.Threads))
	for i, thread := range v.Threads {
		if strings.TrimSpace(thread.Question) == "" {
			return fmt.Errorf("compass thread %q requires a question: %w", thread.ID, ErrInvalid)
		}
		ids[i], names[i] = thread.ID, thread.Name
	}
	if err := validateDistinctStrings("compass thread ids", ids); err != nil {
		return err
	}
	return validateDistinctStrings("compass thread names", names)
}

type PlanNodeKind string

const (
	PlanVolume  PlanNodeKind = "volume"
	PlanArc     PlanNodeKind = "arc"
	PlanChapter PlanNodeKind = "chapter"
	PlanBeat    PlanNodeKind = "beat"
)

type PlanNode struct {
	ID       string       `json:"id"`
	Kind     PlanNodeKind `json:"kind"`
	ParentID string       `json:"parent_id,omitempty"`
	Order    int          `json:"order"`
	Title    string       `json:"title"`
	Summary  string       `json:"summary"`
	// Turn 是这一章的转折（D92）：读完这一章，读者在它推进的长线上多知道的那件具体的事，或
	// 局面的变化。章节计划必有，其余节点没有。
	Turn string `json:"turn,omitempty"`
	// Threads 是这一章推进的长线（罗盘里的 ID，D91），只有章节计划有。
	Threads   []string      `json:"threads,omitempty"`
	DependsOn []DocumentRef `json:"depends_on,omitempty"`
}

// ChapterPlansInOrder 是章节计划的唯一排序口径：按 Order、再按 ID；章号即下标 +1。
func ChapterPlansInOrder(plan []PlanNode) []PlanNode {
	return PlanNodesInOrder(plan, PlanChapter)
}

// PlanNodesInOrder 按同一口径排列某一种计划节点：卷号、故事弧号与章号都是下标 +1（D66）。
func PlanNodesInOrder(plan []PlanNode, kind PlanNodeKind) []PlanNode {
	nodes := make([]PlanNode, 0, len(plan))
	for _, node := range plan {
		if node.Kind == kind {
			nodes = append(nodes, node)
		}
	}
	slices.SortFunc(nodes, func(left, right PlanNode) int {
		if left.Order != right.Order {
			return left.Order - right.Order
		}
		return strings.Compare(left.ID, right.ID)
	})
	return nodes
}

// PlanIndex 按 ID 索引计划节点：建一次，反复沿父链查祖先。
type PlanIndex map[string]PlanNode

func IndexPlan(plan []PlanNode) PlanIndex {
	index := make(PlanIndex, len(plan))
	for _, node := range plan {
		index[node.ID] = node
	}
	return index
}

// Ancestry 返回节点自身及其祖先 ID（章 → 弧 → 卷），供作用域匹配；节点缺失时在断点处停止。
func (index PlanIndex) Ancestry(id string) []string {
	var path []string
	for current := id; current != "" && len(path) <= len(index); {
		node, ok := index[current]
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
	if (strings.TrimSpace(v.Turn) != "") != (v.Kind == PlanChapter) {
		return fmt.Errorf("chapter plans and only chapter plans carry a turn (%s %q): %w", v.Kind, v.ID, ErrInvalid)
	}
	if len(v.Threads) > 0 && v.Kind != PlanChapter {
		return fmt.Errorf("only chapter plans advance threads, not %s %q: %w", v.Kind, v.ID, ErrInvalid)
	}
	if err := validateDistinctStrings("plan node threads", v.Threads); err != nil {
		return err
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

// Noun 是实体种类的故事语言名称。
func (k EntityKind) Noun() string {
	switch k {
	case EntityCharacter:
		return "人物"
	case EntityLocation:
		return "地点"
	case EntityItem:
		return "物品"
	case EntityOrganization:
		return "组织"
	default:
		return string(k)
	}
}

type Entity struct {
	ID      string     `json:"id"`
	Kind    EntityKind `json:"kind"`
	Name    string     `json:"name"`
	Aliases []string   `json:"aliases,omitempty"`
}

// MentionedIn 判断 text 是否点到这个实体：本名或别名出现在其中。单字名太容易误中，不算。
func (v Entity) MentionedIn(text string) bool {
	for _, name := range append([]string{v.Name}, v.Aliases...) {
		if utf8.RuneCountInString(name) >= 2 && strings.Contains(text, name) {
			return true
		}
	}
	return false
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
	// Resolved 标记伏笔已回收（D61）：只用于 foreshadow，回收后不再进入创作上下文。
	Resolved bool `json:"resolved,omitempty"`
}

// CanonKey 是非事件事实的概念身份（D61）：同一主体的同一谓词只有一个节点，按
// old/new 演进。事件跨章只追加；关系的对象写在值里，二者都不按键归并。
type CanonKey struct {
	SubjectID string
	Predicate string
}

func (k CanonKey) String() string { return k.SubjectID + "/" + k.Predicate }

func (v CanonFact) ConceptKey() (CanonKey, bool) {
	switch v.Kind {
	case CanonState, CanonWorldRule, CanonForeshadow:
		return CanonKey{SubjectID: v.SubjectID, Predicate: v.Predicate}, true
	}
	return CanonKey{}, false
}

// EffectiveChapter 是事实在故事中的生效章节，缺省为来源章；两者都空表示规划期事实。
func (v CanonFact) EffectiveChapter() string {
	if v.EffectiveChapterID != "" {
		return v.EffectiveChapterID
	}
	return v.SourceChapterID
}

func (v CanonFact) IsEvent() bool { return v.Kind == CanonEvent }

// canonPrefixes 是事实种类与谓词受控前缀的唯一对照：种类由前缀推出（D66）。
var canonPrefixes = map[CanonFactKind]string{
	CanonEvent: "event.", CanonState: "state.", CanonRelationship: "relation.",
	CanonWorldRule: "rule.", CanonForeshadow: "foreshadow.",
}

// CanonKindOf 按谓词前缀返回事实种类；前缀不在受控命名空间内时 ok=false。
func CanonKindOf(predicate string) (CanonFactKind, bool) {
	for kind, prefix := range canonPrefixes {
		if strings.HasPrefix(predicate, prefix) {
			return kind, true
		}
	}
	return "", false
}

func (v CanonFact) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.SubjectID) == "" || strings.TrimSpace(v.Predicate) == "" {
		return fmt.Errorf("canon id, subject_id and predicate are required: %w", ErrInvalid)
	}
	prefix, ok := canonPrefixes[v.Kind]
	if !ok {
		return fmt.Errorf("unknown canon kind %q: %w", v.Kind, ErrInvalid)
	}
	if !strings.HasPrefix(v.Predicate, prefix) {
		return fmt.Errorf("canon predicate %q must use the %q controlled namespace: %w", v.Predicate, prefix, ErrInvalid)
	}
	if !validCanonKey(v.Predicate) {
		return fmt.Errorf("canon predicate %q may only use lowercase ASCII letters, digits and _ after %q (no Chinese, no capitals); the value carries the wording: %w", v.Predicate, prefix, ErrInvalid)
	}
	if v.Resolved && v.Kind != CanonForeshadow {
		return fmt.Errorf("only foreshadow canon can be resolved, got %s: %w", v.Kind, ErrInvalid)
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
// 标题只在章节计划上（D77）：改标题是一次计划修订，不动正文。
type ManuscriptChapter struct {
	ID         string            `json:"id"`
	PlanNodeID string            `json:"plan_node_id"`
	Number     int               `json:"number"`
	Author     AuthorKind        `json:"author"`
	Blocks     []ManuscriptBlock `json:"blocks"`
	DependsOn  []DocumentRef     `json:"depends_on,omitempty"`
}

// Runes 是正文字符数：各 block 正文累加。
func (v ManuscriptChapter) Runes() int {
	count := 0
	for _, block := range v.Blocks {
		count += utf8.RuneCountInString(block.Text)
	}
	return count
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
	if v.Number <= 0 || len(v.Blocks) == 0 {
		return fmt.Errorf("chapter number and blocks are required: %w", ErrInvalid)
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

func validateDocumentRefs(refs []DocumentRef) error {
	seen := make(map[string]struct{}, len(refs))
	for i, ref := range refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("dependency %d: %w", i, err)
		}
		if ref.Kind == DocumentCompass {
			return fmt.Errorf("dependency %d: the compass cannot be a dependency: %w", i, ErrInvalid)
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
