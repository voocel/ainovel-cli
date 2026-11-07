package derive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
)

// ViewKind 是模型视图的显式 schema 版本：视图结构或故事语言渲染演进时必须升版，
// 旧 Execution Profile 仍按其记录的版本解释，不做静默兼容。
const ViewKind = "model_view.v3" // v3：规划任务先装卷弧结构（D95），罗盘不带篇幅数字（D96）

// contextPolicy 是有界装配参数（§6.5）。它进入 ViewKey，改参数即换缓存键。
type contextPolicy struct {
	Window    int `json:"window"`    // 近 N 章：章节计划与来源于这些章的事件
	Lookahead int `json:"lookahead"` // 后 N 章章节计划
	Budget    int `json:"budget"`    // 故事上下文与任务合计的字符（rune）预算
}

const contextBudget = 32000

// 以下两项从同一上下文预算划出，供推导层切分任务输入（D67）：
//   - ReviewTextBudget 是一次审阅的窗口正文上限：窗口正文与衔接章进入固定段、不受
//     裁剪，余量留给相关事实、近期计划与约束。
//   - PlanNotesBudget 是扩窗携带的审阅意见上限。
const (
	ReviewTextBudget = contextBudget * 3 / 8
	PlanNotesBudget  = contextBudget / 8
)

var defaultPolicy = contextPolicy{Window: 5, Lookahead: 3, Budget: contextBudget}

func ViewKey(kind model.OperationKind, task json.RawMessage) (string, error) {
	if kind == "" || len(task) == 0 || !json.Valid(task) {
		return "", fmt.Errorf("context operation kind and task are required: %w", model.ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(task))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", fmt.Errorf("context task must contain exactly one JSON value: %w", model.ErrInvalid)
	}
	return model.DigestJSON(struct {
		Version string              `json:"version"`
		Policy  contextPolicy       `json:"policy"`
		Kind    model.OperationKind `json:"kind"`
		Task    any                 `json:"task"`
	}{Version: ViewKind, Policy: defaultPolicy, Kind: kind, Task: value})
}

type ProjectContent struct {
	ID         string
	Revision   model.Revision
	Compass    *model.Compass
	Plan       []model.PlanNode
	Entities   []model.Entity
	Canon      []model.CanonFact
	Manuscript []model.ManuscriptChapter
	Ownership  []model.OwnershipRule
}

func (c ProjectContent) story() *narrative.Story {
	return narrative.New(narrative.Content{Compass: c.Compass, Plan: c.Plan, Entities: c.Entities, Canon: c.Canon, Manuscript: c.Manuscript})
}

// ContextDocument 是装配选中的一份权威文档；它只在选择阶段存在，渲染后模型看不到 ID。
type ContextDocument struct {
	Ref     model.DocumentRef
	Content json.RawMessage
}

// ContextBudget 让裁剪可见：Used 超过 Limit 表示必选内容本身超预算；Omitted 是各类
// 文档未装配的数量。
type ContextBudget struct {
	Limit   int
	Used    int
	Omitted map[model.DocumentKind]int
}

// selection 是有界装配的结果：选中的文档与预算，再由 render 渲染成故事语言。
type selection struct {
	Documents []ContextDocument
	Budget    ContextBudget
}

// StoryContext 是按预算装配、用故事语言渲染的有界视图（D66）：卷弧章大纲、实体、
// 事实与正文，外加全书规模。Intent 与 Ownership 在 project_rules 层，不重复。
type StoryContext struct {
	Compass  *narrative.CompassView  `json:"compass,omitempty"`
	Totals   narrative.Totals        `json:"totals"`
	Outline  []narrative.VolumeView  `json:"outline,omitempty"`
	Entities []narrative.EntityView  `json:"entities,omitempty"`
	Facts    []narrative.FactView    `json:"facts,omitempty"`
	Chapters []narrative.ChapterText `json:"chapters,omitempty"`
	// Omitted 是各类内容未装配的数量，模型按章号或名称用 authority_read 回查。
	Omitted map[string]int `json:"omitted,omitempty"`
}

// ModelView 是一次任务里模型看到的全部作品内容（D66）：规则、上下文与任务都用故事
// 语言。它整体作为一个派生文档缓存，同一 Revision 与任务的重编译结果逐字不变。
type ModelView struct {
	Ownership []narrative.RuleView `json:"ownership"`
	Context   StoryContext         `json:"story_context"`
	Task      json.RawMessage      `json:"task"`
}

func BuildModelView(content ProjectContent, kind model.OperationKind, task json.RawMessage) (ModelView, error) {
	selected, err := buildStoryContext(content, kind, task, defaultPolicy)
	if err != nil {
		return ModelView{}, err
	}
	story := content.story()
	ownership := story.Ownership(content.Ownership)
	rendered, err := story.Task(kind, task)
	if err != nil {
		return ModelView{}, err
	}
	return ModelView{Ownership: ownership, Context: selected.render(story), Task: rendered}, nil
}

// omittedNames 是未装配计数在模型视图里的名字。
var omittedNames = map[model.DocumentKind]string{
	model.DocumentPlan: "outline", model.DocumentEntity: "entities",
	model.DocumentCanon: "facts", model.DocumentManuscript: "chapters",
}

func (s selection) render(story *narrative.Story) StoryContext {
	byKind := make(map[model.DocumentKind][]string)
	for _, document := range s.Documents {
		byKind[document.Ref.Kind] = append(byKind[document.Ref.Kind], document.Ref.ID)
	}
	context := StoryContext{
		Totals: story.Totals(), Outline: story.Outline(byKind[model.DocumentPlan]),
		Entities: story.Entities(byKind[model.DocumentEntity]), Facts: story.Facts(byKind[model.DocumentCanon]),
		Chapters: story.Texts(byKind[model.DocumentManuscript]),
	}
	if len(byKind[model.DocumentCompass]) > 0 {
		context.Compass = story.Compass()
	}
	for kind, count := range s.Budget.Omitted {
		if name, ok := omittedNames[kind]; ok {
			if context.Omitted == nil {
				context.Omitted = make(map[string]int)
			}
			context.Omitted[name] = count
		}
	}
	return context
}

// buildStoryContext 分两段装配（§6.5）：固定段大小与章数无关，照装；随作品增长的
// 当前事实与卷弧结构按优先级排序，吃剩余预算。整个流程只在一处截断。
func buildStoryContext(content ProjectContent, kind model.OperationKind, task json.RawMessage, policy contextPolicy) (selection, error) {
	if strings.TrimSpace(content.ID) == "" || content.Revision <= model.InitialRevision || len(task) == 0 || !json.Valid(task) {
		return selection{}, fmt.Errorf("context project, positive revision and task are required: %w", model.ErrInvalid)
	}
	for _, rule := range content.Ownership {
		if err := rule.Validate(); err != nil {
			return selection{}, err
		}
	}
	input, err := model.DecodeTaskInput(kind, task)
	if err != nil {
		return selection{}, err
	}
	s, err := newSelector(content, max(0, policy.Budget-utf8.RuneCount(task)))
	if err != nil {
		return selection{}, err
	}
	switch input := input.(type) {
	case *model.DevelopPlanInput, *model.RevisePlanInput:
		err = s.assemble(focus{anchor: s.firstUnwritten(), planning: true}, policy)
	case *model.WriteChapterInput:
		err = s.writing(input.ChapterPlanID, "", policy)
	case *model.RewriteChapterInput:
		err = s.writing(input.ChapterPlanID, input.ChapterID, policy)
	case *model.ReworkChapterInput:
		err = s.writing(input.ChapterPlanID, input.ChapterID, policy)
	case *model.ReviseCanonInput:
		// 事实核验（D41）：本章正文与来源于它的事实，对着正文逐条核对。
		chapter, ok := s.manuscripts[input.ChapterID]
		if !ok {
			return selection{}, fmt.Errorf("context dependency %q does not exist: %w", manuscriptRef(input.ChapterID).Key(), model.ErrInvalid)
		}
		err = s.assemble(focus{anchor: s.position[chapter.PlanNodeID], targets: s.chapterTargets(chapter.ID)}, policy)
	case *model.ReviewRangeInput:
		// 窗口审阅（D62）：窗口各章正文与来源事实是目标，锚在首章；衔接章、近期计划与
		// 事件随固定段进入，相关实体的当前事实按预算排序。
		anchor := len(s.chapters)
		var targets []model.DocumentRef
		for _, id := range input.ChapterIDs {
			if _, ok := s.manuscripts[id]; !ok {
				return selection{}, fmt.Errorf("context dependency %q does not exist: %w", manuscriptRef(id).Key(), model.ErrInvalid)
			}
			anchor = min(anchor, s.chapterPosition(id))
			targets = append(targets, s.chapterTargets(id)...)
		}
		err = s.assemble(focus{anchor: anchor, targets: targets, previous: true}, policy)
	default:
		return selection{}, fmt.Errorf("operation %s has no story context contract: %w", kind, model.ErrInvalid)
	}
	if err != nil {
		return selection{}, err
	}
	return s.result(), nil
}

// focus 描述锚定任务：锚点是章节计划有序下标，targets 是任务本身必须看到的文档。
type focus struct {
	anchor   int
	targets  []model.DocumentRef
	previous bool // 写作需要上一章正文衔接
	planning bool // 规划任务：锚点起的全部章节计划都是工作对象
}

type selector struct {
	documents   map[string]ContextDocument
	facts       map[string]model.CanonFact
	sourced     map[string][]string // 来源章 → 事实 ID（升序）
	entities    []model.Entity
	volumes     []model.PlanNode // 按卷号
	arcs        []model.PlanNode // 按故事弧号
	chapters    []model.PlanNode // 章节计划，按唯一排序口径
	position    map[string]int   // 章节计划 ID → 下标
	written     map[string]model.ManuscriptChapter
	manuscripts map[string]model.ManuscriptChapter
	ownership   []model.OwnershipRule
	selected    map[string]ContextDocument
	used, limit int
}

func newSelector(content ProjectContent, limit int) (*selector, error) {
	s := &selector{
		documents: make(map[string]ContextDocument), facts: make(map[string]model.CanonFact),
		sourced: make(map[string][]string), entities: content.Entities,
		volumes: model.PlanNodesInOrder(content.Plan, model.PlanVolume), arcs: model.PlanNodesInOrder(content.Plan, model.PlanArc),
		chapters: model.ChapterPlansInOrder(content.Plan), position: make(map[string]int),
		written: make(map[string]model.ManuscriptChapter), manuscripts: make(map[string]model.ManuscriptChapter),
		ownership: content.Ownership, selected: make(map[string]ContextDocument), limit: limit,
	}
	add := func(ref model.DocumentRef, value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode context document %q: %w", ref.Key(), err)
		}
		s.documents[ref.Key()] = ContextDocument{Ref: ref, Content: payload}
		return nil
	}
	if content.Compass != nil {
		if err := add(compassRef, *content.Compass); err != nil {
			return nil, err
		}
	}
	for _, node := range content.Plan {
		if err := add(model.DocumentRef{Kind: model.DocumentPlan, ID: node.ID}, node); err != nil {
			return nil, err
		}
	}
	for index, node := range s.chapters {
		s.position[node.ID] = index
	}
	for _, entity := range content.Entities {
		if err := add(model.DocumentRef{Kind: model.DocumentEntity, ID: entity.ID}, entity); err != nil {
			return nil, err
		}
	}
	for _, fact := range content.Canon {
		if err := add(canonRef(fact.ID), fact); err != nil {
			return nil, err
		}
		s.facts[fact.ID] = fact
		if fact.SourceChapterID != "" {
			s.sourced[fact.SourceChapterID] = append(s.sourced[fact.SourceChapterID], fact.ID)
		}
	}
	for _, ids := range s.sourced {
		slices.Sort(ids)
	}
	for _, chapter := range content.Manuscript {
		if err := add(manuscriptRef(chapter.ID), chapter); err != nil {
			return nil, err
		}
		s.manuscripts[chapter.ID] = chapter
		s.written[chapter.PlanNodeID] = chapter
	}
	return s, nil
}

// writing 装配写作与重写：目标章计划、上一章正文，重写时再加本章正文与来源事实，
// 以及已写的下一章正文——改动要与后文衔接（D68）。
func (s *selector) writing(planID, chapterID string, policy contextPolicy) error {
	anchor, ok := s.position[planID]
	if !ok {
		return fmt.Errorf("context dependency %q does not exist: %w", planRef(planID).Key(), model.ErrInvalid)
	}
	targets := []model.DocumentRef{planRef(planID)}
	if chapterID != "" {
		targets = append(targets, s.chapterTargets(chapterID)...)
		if anchor+1 < len(s.chapters) {
			if next, ok := s.written[s.chapters[anchor+1].ID]; ok {
				targets = append(targets, manuscriptRef(next.ID))
			}
		}
	}
	return s.assemble(focus{anchor: anchor, targets: targets, previous: true}, policy)
}

// chapterTargets 是改动某章时必须看到的内容：正文与来源于它的全部事实——重写要
// 重申报这些事实（D41），看不到就无从确认。
func (s *selector) chapterTargets(chapterID string) []model.DocumentRef {
	refs := []model.DocumentRef{manuscriptRef(chapterID)}
	for _, id := range s.sourced[chapterID] {
		refs = append(refs, canonRef(id))
	}
	return refs
}

// assemble 先照装固定段（罗盘、目标、上一章、近期窗口、后续计划），再按优先级装当前事实。
func (s *selector) assemble(f focus, policy contextPolicy) error {
	required := slices.Clone(f.targets)
	if _, ok := s.documents[compassRef.Key()]; ok {
		required = append(required, compassRef) // 篇幅、终局与长线（D63、D91），各任务都要知道故事往哪收
	}
	if f.previous {
		for i := f.anchor - 1; i >= 0; i-- {
			if chapter, ok := s.written[s.chapters[i].ID]; ok {
				required = append(required, manuscriptRef(chapter.ID))
				break
			}
		}
	}
	for i := max(0, f.anchor-policy.Window); i < min(f.anchor, len(s.chapters)); i++ {
		required = append(required, planRef(s.chapters[i].ID))
		if chapter, ok := s.written[s.chapters[i].ID]; ok {
			for _, id := range s.sourced[chapter.ID] {
				if s.facts[id].IsEvent() {
					required = append(required, canonRef(id))
				}
			}
		}
	}
	start, end := f.anchor+1, min(len(s.chapters), f.anchor+1+policy.Lookahead)
	if f.planning {
		start, end = f.anchor, len(s.chapters)
	}
	for i := start; i < end; i++ {
		required = append(required, planRef(s.chapters[i].ID))
	}
	for _, ref := range required {
		if err := s.require(ref); err != nil {
			return err
		}
	}
	return s.rank(f.planning)
}

// rank 是唯一的裁剪点，优先级依次为：
//  0. 约束——受保护事实、世界规则、未回收伏笔，由早到晚（越早立下越要记住）；
//  1. 相关实体的当前事实，由近到远；
//  2. 其余当前事实，由近到远；
//  3. 卷，由近到远；
//  4. 故事弧，由近到远。
//
// 规划任务的工作对象是全书走向，卷弧结构提到约束之后、事实之前（D95）：先有全书各卷，
// 再由近到远补故事弧。事件与已回收伏笔只经窗口或重写目标进入。
func (s *selector) rank(planning bool) error {
	factRank, structureRank := 1, 3
	if planning {
		factRank, structureRank = 3, 1
	}
	relevant := s.relevantEntities()
	guarded := make(map[string]bool)
	for _, rule := range s.ownership {
		if rule.Control == model.ControlLocked || rule.Control == model.ControlGuided {
			guarded[rule.Target.Key()] = true
		}
	}
	type candidate struct {
		ref            model.DocumentRef
		rank, position int
	}
	var candidates []candidate
	for id, fact := range s.facts {
		ref := canonRef(id)
		if _, done := s.selected[ref.Key()]; done || fact.IsEvent() || fact.Resolved {
			continue
		}
		rank := factRank + 1
		switch {
		case guarded[ref.Key()] || fact.Kind == model.CanonWorldRule || fact.Kind == model.CanonForeshadow:
			rank = 0
		case relevant[fact.SubjectID]:
			rank = factRank
		}
		candidates = append(candidates, candidate{ref: ref, rank: rank, position: s.chapterPosition(fact.EffectiveChapter())})
	}
	for offset, nodes := range [][]model.PlanNode{s.volumes, s.arcs} {
		for index, node := range nodes {
			if _, done := s.selected[planRef(node.ID).Key()]; !done {
				candidates = append(candidates, candidate{ref: planRef(node.ID), rank: structureRank + offset, position: index})
			}
		}
	}
	slices.SortFunc(candidates, func(left, right candidate) int {
		if left.rank != right.rank {
			return left.rank - right.rank
		}
		if left.position != right.position {
			if left.rank == 0 {
				return left.position - right.position
			}
			return right.position - left.position
		}
		return strings.Compare(left.ref.Key(), right.ref.Key())
	})
	for _, candidate := range candidates {
		additions, err := s.closure(candidate.ref)
		if err != nil {
			return err
		}
		if s.used+cost(additions) <= s.limit {
			s.commit(additions)
		}
	}
	return nil
}

// relevantEntities 是本次任务涉及的实体：已选正文由宿主绑定的依赖（该章 Canon Delta
// 的主体），以及名称或别名出现在已选计划与正文里的实体。
func (s *selector) relevantEntities() map[string]bool {
	relevant := make(map[string]bool)
	var text strings.Builder
	for _, document := range s.selected {
		switch document.Ref.Kind {
		case model.DocumentManuscript:
			chapter := s.manuscripts[document.Ref.ID]
			for _, dependency := range chapter.DependsOn {
				if dependency.Kind == model.DocumentEntity {
					relevant[dependency.ID] = true
				}
			}
			for _, block := range chapter.Blocks {
				text.WriteString(block.Text)
			}
		case model.DocumentPlan:
			text.Write(document.Content)
		}
	}
	corpus := text.String()
	for _, entity := range s.entities {
		if entity.MentionedIn(corpus) {
			relevant[entity.ID] = true
		}
	}
	return relevant
}

func (s *selector) require(ref model.DocumentRef) error {
	additions, err := s.closure(ref)
	if err != nil {
		return err
	}
	s.commit(additions)
	return nil
}

// closure 返回装入 ref 需要新增的文档：它自身与尚未装入的结构依赖（计划祖先、事实主体等）。
func (s *selector) closure(ref model.DocumentRef) ([]ContextDocument, error) {
	var additions []ContextDocument
	pending := make(map[string]bool)
	var visit func(model.DocumentRef) error
	visit = func(ref model.DocumentRef) error {
		if ref.Kind == model.DocumentIntent || ref.Kind == model.DocumentOwnership {
			return nil
		}
		key := ref.Key()
		if _, done := s.selected[key]; done || pending[key] {
			return nil
		}
		document, ok := s.documents[key]
		if !ok {
			return fmt.Errorf("context dependency %q does not exist: %w", key, model.ErrInvalid)
		}
		pending[key] = true
		additions = append(additions, document)
		dependencies, err := model.DocumentDependencies(ref, document.Content)
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	return additions, visit(ref)
}

func (s *selector) commit(additions []ContextDocument) {
	for _, document := range additions {
		s.selected[document.Ref.Key()] = document
	}
	s.used += cost(additions)
}

func cost(documents []ContextDocument) int {
	total := 0
	for _, document := range documents {
		total += utf8.RuneCount(document.Content)
	}
	return total
}

// firstUnwritten 是规划任务的锚点：第一个还没有正文的章节计划。
func (s *selector) firstUnwritten() int {
	for index, node := range s.chapters {
		if _, ok := s.written[node.ID]; !ok {
			return index
		}
	}
	return len(s.chapters)
}

// chapterPosition 把正文章节换算为章节计划下标；规划期事实没有章节，视为最早。
func (s *selector) chapterPosition(chapterID string) int {
	if chapter, ok := s.manuscripts[chapterID]; ok {
		if index, ok := s.position[chapter.PlanNodeID]; ok {
			return index
		}
	}
	return -1
}

func (s *selector) result() selection {
	result := selection{Budget: ContextBudget{Limit: s.limit, Used: s.used}}
	selectedKinds := make(map[model.DocumentKind]int)
	for _, document := range s.selected {
		result.Documents = append(result.Documents, document)
		selectedKinds[document.Ref.Kind]++
	}
	slices.SortFunc(result.Documents, func(left, right ContextDocument) int {
		return strings.Compare(left.Ref.Key(), right.Ref.Key())
	})
	totalKinds := make(map[model.DocumentKind]int)
	for _, document := range s.documents {
		totalKinds[document.Ref.Kind]++
	}
	for kind, total := range totalKinds {
		if omitted := total - selectedKinds[kind]; omitted > 0 {
			if result.Budget.Omitted == nil {
				result.Budget.Omitted = make(map[model.DocumentKind]int)
			}
			result.Budget.Omitted[kind] = omitted
		}
	}
	return result
}

var compassRef = model.DocumentRef{Kind: model.DocumentCompass, ID: model.SingletonDocumentID}

func planRef(id string) model.DocumentRef { return model.DocumentRef{Kind: model.DocumentPlan, ID: id} }
func canonRef(id string) model.DocumentRef {
	return model.DocumentRef{Kind: model.DocumentCanon, ID: id}
}
func manuscriptRef(id string) model.DocumentRef {
	return model.DocumentRef{Kind: model.DocumentManuscript, ID: id}
}
