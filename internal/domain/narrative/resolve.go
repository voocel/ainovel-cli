package narrative

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Submission 是模型用故事语言写的提交（D66）：不含任何文档 ID。正文不在这里，
// 由宿主按工作稿键与版本装配后随 drafts 传入。
type Submission struct {
	Compass  *CompassEdit  `json:"compass,omitempty"`
	Threads  []ThreadEdit  `json:"threads,omitempty"`
	Volumes  []VolumeEdit  `json:"volumes,omitempty"`
	Arcs     []ArcEdit     `json:"arcs,omitempty"`
	Chapters []ChapterEdit `json:"chapters,omitempty"`
	Entities []EntityEdit  `json:"entities,omitempty"`
	Facts    []FactEdit    `json:"facts,omitempty"`
	Confirm  []FactRef     `json:"confirm_facts,omitempty"`
	Remove   []FactRef     `json:"remove_facts,omitempty"`
	// Outline 是返工重申报的本章章题、章纲与转折（D87、D92）。
	Outline *OutlineEdit `json:"outline,omitempty"`
}

// CompassEdit 替换罗盘的终局与收官承诺；ScaleMax 只在申请超过上限时给出，省略保持原样
// （D96）。长线另按 ThreadEdit 编辑。
type CompassEdit struct {
	ScaleMax int    `json:"scale_max,omitempty"`
	Ending   string `json:"ending"`
	Final    int    `json:"final,omitempty"`
}

// ThreadEdit 按短名定位长线（D91）：已有的就地修改，否则新立；Answer 是作答的章号。
type ThreadEdit struct {
	Name     string `json:"name"`
	Question string `json:"question,omitempty"`
	Answer   int    `json:"answer,omitempty"`
}

// OutlineEdit 是一章的章题、章纲与转折。
type OutlineEdit struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Turn    string `json:"turn"`
}

// 计划编辑按序号定位：已有序号是修改（空字段保持原值），紧接末尾的序号是新增。
type VolumeEdit struct {
	Volume  int    `json:"volume"`
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type ArcEdit struct {
	Arc     int    `json:"arc"`
	Volume  int    `json:"volume,omitempty"`
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type ChapterEdit struct {
	Chapter int    `json:"chapter"`
	Arc     int    `json:"arc,omitempty"`
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Turn 是本章的转折（D92），新增的章必填。
	Turn string `json:"turn,omitempty"`
	// Threads 是本章推进的长线短名：给出即整体替换，省略保持原样。
	Threads []string `json:"threads,omitempty"`
}

// EntityEdit 按本名定位：已有实体合并别名，否则新建。Rename 把已有实体改叫别的本名（用户
// 说它其实叫别的、其实是别的东西）；这次修改里新旧两个名字都指它。
type EntityEdit struct {
	Name    string           `json:"name"`
	Kind    model.EntityKind `json:"kind"`
	Aliases []string         `json:"aliases,omitempty"`
	Rename  string           `json:"rename,omitempty"`
}

// FactEdit 写入一条事实；种类由谓词前缀决定。Chapter 是来源章，单章任务由宿主填。
type FactEdit struct {
	Subject          string          `json:"subject"`
	Predicate        string          `json:"predicate"`
	Value            json.RawMessage `json:"value"`
	Chapter          int             `json:"chapter,omitempty"`
	EffectiveChapter int             `json:"effective_chapter,omitempty"`
	Resolved         bool            `json:"resolved,omitempty"`
}

// FactRef 指称一条已有事实：主体+谓词；事件与关系另按来源章区分。
type FactRef struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Chapter   int    `json:"chapter,omitempty"`
}

// Resolve 把故事语言提交解析成补丁：新文档由宿主分配 ID，已有文档按自然键定位，
// 更新事实的 old_value 一律取自基线。跨文档不变量仍由 model.ValidateChange 执行。
func (s *Story) Resolve(input model.TaskInput, submission Submission, drafts []model.ManuscriptChapter) ([]model.Patch, error) {
	r := s.resolver(false)
	if err := r.submission(input, submission, drafts); err != nil {
		return nil, err
	}
	return r.patches, nil
}

func (s *Story) resolver(direct bool) *resolver {
	r := &resolver{
		story: s, ids: s.allocator(), created: make(map[model.PlanNodeKind][]string),
		names: make(map[string]string), touched: make(map[string]bool), chapters: make(map[int]string), direct: direct,
	}
	if s.content.Compass != nil {
		compass := *s.content.Compass
		compass.Threads = slices.Clone(compass.Threads)
		r.compass = &compass
	}
	return r
}

func (r *resolver) submission(input model.TaskInput, submission Submission, drafts []model.ManuscriptChapter) error {
	for _, chapter := range drafts {
		r.scope = append(r.scope, chapter.ID)
		r.chapters[chapter.Number] = chapter.ID
		if err := r.put(model.DocumentManuscript, chapter.ID, chapter); err != nil {
			return err
		}
	}
	if canon, ok := input.(*model.ReviseCanonInput); ok {
		r.scope = append(r.scope, canon.ChapterID)
	}
	if err := r.outline(input, submission.Outline); err != nil {
		return err
	}
	// 长线先立后用：章节编辑按短名引用它们，作答章在章节编辑之后解析，新增的章也能作答。
	steps := []func() error{
		func() error { return r.defineThreads(submission.Compass, submission.Threads) },
		func() error { return r.plan(model.PlanVolume, "", volumeEdits(submission.Volumes)) },
		func() error { return r.plan(model.PlanArc, model.PlanVolume, arcEdits(submission.Arcs)) },
		func() error { return r.plan(model.PlanChapter, model.PlanArc, chapterEdits(submission.Chapters)) },
		func() error { return r.answerThreads(submission.Threads) },
		func() error { return r.entities(submission.Entities) },
		func() error { return r.facts(submission.Facts) },
		func() error { return r.references(submission.Confirm, true) },
		func() error { return r.references(submission.Remove, false) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// outline 处理返工重申报的章题、章纲与转折（D87、D92）：返工的章写了什么由重写后的正文
// 说了算，它们随正文走，必须重申报；变了才写进提交。其余任务照大纲写，不提交它。
func (r *resolver) outline(input model.TaskInput, outline *OutlineEdit) error {
	rework, ok := input.(*model.ReworkChapterInput)
	switch {
	case !ok && outline == nil:
		return nil
	case !ok:
		return fmt.Errorf("只有返工能随正文改本章的章题、章纲与转折: %w", model.ErrInvalid)
	case outline == nil || strings.TrimSpace(outline.Title) == "" || strings.TrimSpace(outline.Summary) == "" || strings.TrimSpace(outline.Turn) == "":
		return fmt.Errorf("返工要重申报本章的章题、章纲与转折（outline 的 title、summary 与 turn）：照重写后的正文给出，没变就原样给出: %w", model.ErrInvalid)
	}
	node := r.story.plans[rework.ChapterPlanID]
	if node.Title == outline.Title && node.Summary == outline.Summary && node.Turn == outline.Turn {
		return nil
	}
	node.Title, node.Summary, node.Turn = outline.Title, outline.Summary, outline.Turn
	return r.put(model.DocumentPlan, node.ID, node)
}

type resolver struct {
	story   *Story
	ids     *idAllocator
	patches []model.Patch
	// compass 是解析中的罗盘：基线的副本加本次的编辑，尚未规划时为 nil。
	compass  *model.Compass
	created  map[model.PlanNodeKind][]string // 本次新增的计划节点，按序号
	names    map[string]string               // 本次提交的实体名称与别名 → ID
	touched  map[string]bool                 // 本次已处理的事实
	scope    []string                        // 事实可以来源的正文章节
	chapters map[int]string                  // 本次工作稿的章号 → 正文 ID
	// direct 是不属于任务的修改（主编，D77）：事实可以来源于任何已写的章，不给来源章时
	// 沿用原有事实的来源。
	direct bool
}

func (r *resolver) put(kind model.DocumentKind, id string, value any) error {
	content, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s %q: %w", kind, id, err)
	}
	r.patches = append(r.patches, model.Patch{Document: model.DocumentRef{Kind: kind, ID: id}, Operation: model.PatchPut, Content: content})
	return nil
}

type planEdit struct {
	number, parent int
	title, summary string
	turn           string   // 仅章
	threads        []string // 长线短名；nil 保持原样
}

func volumeEdits(edits []VolumeEdit) []planEdit {
	result := make([]planEdit, len(edits))
	for i, edit := range edits {
		result[i] = planEdit{number: edit.Volume, title: edit.Title, summary: edit.Summary}
	}
	return result
}

func arcEdits(edits []ArcEdit) []planEdit {
	result := make([]planEdit, len(edits))
	for i, edit := range edits {
		result[i] = planEdit{number: edit.Arc, parent: edit.Volume, title: edit.Title, summary: edit.Summary}
	}
	return result
}

func chapterEdits(edits []ChapterEdit) []planEdit {
	result := make([]planEdit, len(edits))
	for i, edit := range edits {
		result[i] = planEdit{number: edit.Chapter, parent: edit.Arc, title: edit.Title, summary: edit.Summary, turn: edit.Turn, threads: edit.Threads}
	}
	return result
}

// plan 处理一种计划节点：已有序号就地修改，新序号必须紧接末尾连续编号，排在已有节点之后。
func (r *resolver) plan(kind, parentKind model.PlanNodeKind, edits []planEdit) error {
	existing := r.story.ordered[kind]
	slices.SortFunc(edits, func(left, right planEdit) int { return left.number - right.number })
	order := 0
	for _, node := range existing {
		order = max(order, node.Order)
	}
	name := kindName(kind)
	for i, edit := range edits {
		if i > 0 && edits[i-1].number == edit.number {
			return fmt.Errorf("第 %d 个%s提交了两次: %w", edit.number, name, model.ErrInvalid)
		}
		var node model.PlanNode
		switch next := len(existing) + len(r.created[kind]) + 1; {
		case edit.number >= 1 && edit.number <= len(existing):
			node = existing[edit.number-1]
			node.Title, node.Summary, node.Turn = cmp.Or(edit.title, node.Title), cmp.Or(edit.summary, node.Summary), cmp.Or(edit.turn, node.Turn)
		case edit.number == next:
			if strings.TrimSpace(edit.title) == "" || strings.TrimSpace(edit.summary) == "" || (parentKind != "" && edit.parent == 0) {
				return fmt.Errorf("新增的第 %d 个%s需要 title、summary 和所属的%s序号: %w", edit.number, name, kindName(parentKind), model.ErrInvalid)
			}
			if kind == model.PlanChapter && strings.TrimSpace(edit.turn) == "" {
				return fmt.Errorf("新增的第 %d 章需要 turn：读完这一章，读者在它推进的长线上多知道了哪件具体的事，或局面变成了什么: %w", edit.number, model.ErrInvalid)
			}
			node = model.PlanNode{ID: r.ids.allocate(planPrefix(kind)), Kind: kind, Order: order + edit.number - len(existing), Title: edit.title, Summary: edit.summary, Turn: edit.turn}
			r.created[kind] = append(r.created[kind], node.ID)
		default:
			return fmt.Errorf("第 %d 个%s不存在；已有 %d 个，新增的%s从第 %d 个起连续编号: %w",
				edit.number, name, len(existing), name, next, model.ErrInvalid)
		}
		if edit.parent != 0 {
			parent, err := r.planID(parentKind, edit.parent)
			if err != nil {
				return err
			}
			node.ParentID = parent
		}
		if edit.threads != nil {
			threads, err := r.threadIDs(edit.number, edit.threads)
			if err != nil {
				return err
			}
			node.Threads = threads
		}
		if err := r.put(model.DocumentPlan, node.ID, node); err != nil {
			return err
		}
	}
	return nil
}

// defineThreads 处理罗盘（D91）：终局与收官承诺随 compass 替换，篇幅上限只在给出时改；
// 长线按短名定位，已有的改问题，新的分配 ID。罗盘在全部编辑之后写一次。
func (r *resolver) defineThreads(edit *CompassEdit, threads []ThreadEdit) error {
	if edit == nil && len(threads) == 0 {
		return nil
	}
	if r.compass == nil {
		r.compass = new(model.Compass)
	}
	if edit != nil {
		// 上限只为超过护栏而设（D96），护栏以内的数字就是预计篇幅，不收。
		if edit.ScaleMax != 0 && edit.ScaleMax <= model.CompassAutonomyCeiling {
			return fmt.Errorf("scale_max 只在故事需要超过 %d 章时给出，省略即可: %w", model.CompassAutonomyCeiling, model.ErrInvalid)
		}
		if edit.ScaleMax != 0 {
			r.compass.ScaleMax = edit.ScaleMax
		}
		r.compass.Ending, r.compass.Final = edit.Ending, edit.Final
	}
	seen := make(map[string]bool)
	for _, thread := range threads {
		name := strings.TrimSpace(thread.Name)
		switch {
		case name == "":
			return fmt.Errorf("长线需要 name: %w", model.ErrInvalid)
		case seen[name]:
			return fmt.Errorf("长线「%s」提交了两次: %w", name, model.ErrInvalid)
		}
		seen[name] = true
		index := slices.IndexFunc(r.compass.Threads, func(t model.StoryThread) bool { return t.Name == name })
		switch {
		case index >= 0 && strings.TrimSpace(thread.Question) != "":
			r.compass.Threads[index].Question = thread.Question
		case index >= 0:
		case strings.TrimSpace(thread.Question) == "":
			return fmt.Errorf("新立的长线「%s」需要 question：全书在追问的那个问题: %w", name, model.ErrInvalid)
		default:
			r.compass.Threads = append(r.compass.Threads, model.StoryThread{ID: r.ids.allocate(threadPrefix), Name: name, Question: thread.Question})
		}
	}
	return nil
}

// answerThreads 记下长线的作答章，罗盘有变化就写入。
func (r *resolver) answerThreads(threads []ThreadEdit) error {
	if r.compass == nil {
		return nil
	}
	for _, thread := range threads {
		if thread.Answer == 0 {
			continue
		}
		id, err := r.planID(model.PlanChapter, thread.Answer)
		if err != nil {
			return fmt.Errorf("长线「%s」的作答章第 %d 章还没有规划：先不写 answer，规划到那一章时再写: %w", thread.Name, thread.Answer, model.ErrInvalid)
		}
		index := slices.IndexFunc(r.compass.Threads, func(t model.StoryThread) bool { return t.Name == strings.TrimSpace(thread.Name) })
		r.compass.Threads[index].Answer = id
	}
	if !r.compassEdited() {
		return nil
	}
	return r.put(model.DocumentCompass, model.SingletonDocumentID, r.compass)
}

// compassEdited 是本次是否编辑了罗盘：没有基线的罗盘被新建，或与基线不同。
func (r *resolver) compassEdited() bool {
	base := r.story.content.Compass
	if base == nil {
		return r.compass != nil
	}
	return base.ScaleMax != r.compass.ScaleMax || base.Ending != r.compass.Ending || base.Final != r.compass.Final ||
		!slices.Equal(base.Threads, r.compass.Threads)
}

// threadIDs 把一章推进的长线短名解析成罗盘里的 ID。
func (r *resolver) threadIDs(chapter int, names []string) ([]string, error) {
	ids := make([]string, 0, len(names))
	for _, name := range names {
		var thread model.StoryThread
		found := false
		if r.compass != nil {
			for _, candidate := range r.compass.Threads {
				if candidate.Name == strings.TrimSpace(name) {
					thread, found = candidate, true
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("第 %d 章推进的长线「%s」不存在；新的长线先在 threads 里立起来: %w", chapter, name, model.ErrInvalid)
		}
		if slices.Contains(ids, thread.ID) {
			return nil, fmt.Errorf("第 %d 章把长线「%s」列了两次: %w", chapter, name, model.ErrInvalid)
		}
		ids = append(ids, thread.ID)
	}
	return ids, nil
}

// planID 按序号找计划节点：已有的与本次新增的。
func (r *resolver) planID(kind model.PlanNodeKind, number int) (string, error) {
	existing := r.story.ordered[kind]
	switch {
	case number >= 1 && number <= len(existing):
		return existing[number-1].ID, nil
	case number > len(existing) && number <= len(existing)+len(r.created[kind]):
		return r.created[kind][number-len(existing)-1], nil
	}
	return "", fmt.Errorf("第 %d 个%s不存在: %w", number, kindName(kind), model.ErrInvalid)
}

func (r *resolver) entities(edits []EntityEdit) error {
	for _, edit := range edits {
		name := strings.TrimSpace(edit.Name)
		if name == "" {
			return fmt.Errorf("实体需要 name: %w", model.ErrInvalid)
		}
		if _, dup := r.names[name]; dup {
			return fmt.Errorf("实体「%s」提交了两次: %w", name, model.ErrInvalid)
		}
		entity, err := r.story.entity(name)
		rename := strings.TrimSpace(edit.Rename)
		changed := true
		switch {
		case err == nil && entity.Name != name:
			return fmt.Errorf("「%s」是「%s」的别名；修改已有实体时用它的本名: %w", name, entity.Name, model.ErrInvalid)
		case err == nil:
			// 改名是它其实叫别的、其实是别的东西：旧的本名与别名都不再指它，还要用的列进 aliases。
			next := entity
			next.Kind, next.Aliases = edit.Kind, slices.Clone(entity.Aliases)
			if rename != "" {
				next.Name, next.Aliases = rename, nil
			}
			for _, alias := range edit.Aliases {
				if alias != next.Name && !slices.Contains(next.Aliases, alias) {
					next.Aliases = append(next.Aliases, alias)
				}
			}
			changed = next.Kind != entity.Kind || next.Name != entity.Name || !slices.Equal(next.Aliases, entity.Aliases)
			entity = next
		case errors.Is(err, model.ErrNotFound) && rename != "":
			return fmt.Errorf("实体「%s」不存在，无从改名: %w", name, model.ErrInvalid)
		case errors.Is(err, model.ErrNotFound):
			entity = model.Entity{ID: r.ids.allocate(entityPrefix), Kind: edit.Kind, Name: name, Aliases: edit.Aliases}
		default:
			return err
		}
		// 一个名字只指一个实体：本名与别名不得是别的实体的名字。
		for _, known := range append([]string{entity.Name}, entity.Aliases...) {
			if owner, ok := r.names[known]; ok && owner != entity.ID {
				return fmt.Errorf("「%s」在这次修改里已指别的实体: %w", known, model.ErrInvalid)
			}
			if other, err := r.story.entity(known); err == nil && other.ID != entity.ID {
				return fmt.Errorf("「%s」已是实体「%s」的名字；要让「%s」改叫别的，用 rename: %w", known, other.Name, other.Name, model.ErrInvalid)
			}
		}
		for _, known := range append([]string{name, entity.Name}, entity.Aliases...) {
			r.names[known] = entity.ID
		}
		// 没有变化就不写：锁定的实体被原样引用不算改动。
		if changed {
			if err := r.put(model.DocumentEntity, entity.ID, entity); err != nil {
				return err
			}
		}
	}
	return nil
}

// subject 解析事实主体：本次提交的实体优先，其次故事里已有的实体。
func (r *resolver) subject(name string) (string, error) {
	if id, ok := r.names[name]; ok {
		return id, nil
	}
	entity, err := r.story.entity(name)
	if errors.Is(err, model.ErrNotFound) {
		return "", fmt.Errorf("实体「%s」不存在；新的人物、地点、物品或组织先在 entities 里建立: %w", name, model.ErrInvalid)
	}
	return entity.ID, err
}

// source 解析来源章：给了章号必须是本次改写的章，没给且只有一章时就是它；规划期事实没有来源章。
// 直接修改的来源章可以是任何已写的章。
func (r *resolver) source(number int) (string, error) {
	if r.direct {
		if number == 0 {
			return "", nil
		}
		chapter, err := r.story.manuscript(number)
		return chapter.ID, err
	}
	if number != 0 {
		for _, id := range r.scope {
			if r.chapterNumber(id) == number {
				return id, nil
			}
		}
		return "", fmt.Errorf("事实只能来源于本次提交的章节 %v，不能是第 %d 章: %w", r.scopeNumbers(), number, model.ErrInvalid)
	}
	switch len(r.scope) {
	case 0:
		return "", nil
	case 1:
		return r.scope[0], nil
	default:
		return "", fmt.Errorf("本次改写了多章 %v，每条事实都要用 chapter 指明来源章: %w", r.scopeNumbers(), model.ErrInvalid)
	}
}

func (r *resolver) chapterNumber(id string) int {
	for number, chapterID := range r.chapters {
		if chapterID == id {
			return number
		}
	}
	return r.story.chapterNumberOf(id)
}

func (r *resolver) scopeNumbers() []int {
	numbers := make([]int, 0, len(r.scope))
	for _, id := range r.scope {
		numbers = append(numbers, r.chapterNumber(id))
	}
	slices.Sort(numbers)
	return numbers
}

// chapterID 按章号找正文：本次工作稿优先，其次已写正文。
func (r *resolver) chapterID(number int) (string, error) {
	if id, ok := r.chapters[number]; ok {
		return id, nil
	}
	chapter, err := r.story.manuscript(number)
	return chapter.ID, err
}

func (r *resolver) facts(edits []FactEdit) error {
	for _, edit := range edits {
		kind, ok := model.CanonKindOf(edit.Predicate)
		if !ok {
			return fmt.Errorf("谓词「%s」必须以 event.、state.、relation.、rule. 或 foreshadow. 开头: %w", edit.Predicate, model.ErrInvalid)
		}
		if len(edit.Value) == 0 {
			return fmt.Errorf("事实「%s」%s 缺少 value: %w", edit.Subject, edit.Predicate, model.ErrInvalid)
		}
		subject, err := r.subject(edit.Subject)
		if err != nil {
			return err
		}
		source, err := r.source(edit.Chapter)
		if err != nil {
			return err
		}
		fact := model.CanonFact{Kind: kind, SubjectID: subject, Predicate: edit.Predicate, Value: edit.Value, Resolved: edit.Resolved}
		previous, exists := r.story.fact(subject, edit.Predicate, source)
		if exists && r.direct && edit.Chapter == 0 {
			source = previous.SourceChapterID
		}
		fact.SourceChapterID = source
		if edit.EffectiveChapter != 0 {
			if fact.EffectiveChapterID, err = r.chapterID(edit.EffectiveChapter); err != nil {
				return fmt.Errorf("事实「%s」%s 的 effective_chapter 只能指向已写的章（插叙、回忆），第 %d 章还没写；以后才发生的变化写进那一章的梗概，写到时再记: %w",
					edit.Subject, edit.Predicate, edit.EffectiveChapter, model.ErrInvalid)
			}
			if fact.EffectiveChapterID == source {
				fact.EffectiveChapterID = ""
			}
		}
		if exists {
			fact.ID, fact.PreviousValue, fact.DependsOn = previous.ID, previous.Value, previous.DependsOn
		} else {
			fact.ID = r.ids.allocate(factPrefix)
		}
		if err := r.touch(fact, edit.Subject); err != nil {
			return err
		}
		if err := r.put(model.DocumentCanon, fact.ID, fact); err != nil {
			return err
		}
	}
	return nil
}

// references 处理确认（原样重申报）与删除。
func (r *resolver) references(refs []FactRef, confirm bool) error {
	for _, ref := range refs {
		subject, err := r.subject(ref.Subject)
		if err != nil {
			return err
		}
		source, err := r.source(ref.Chapter)
		if err != nil {
			return err
		}
		fact, exists := r.story.fact(subject, ref.Predicate, source)
		if !exists {
			return fmt.Errorf("没有这条事实：「%s」%s；%s: %w", ref.Subject, ref.Predicate, r.story.namesake(ref.Predicate), model.ErrInvalid)
		}
		if err := r.touch(fact, ref.Subject); err != nil {
			return err
		}
		if !confirm {
			r.patches = append(r.patches, model.Patch{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: fact.ID}, Operation: model.PatchDelete})
			continue
		}
		fact.PreviousValue = fact.Value
		if err := r.put(model.DocumentCanon, fact.ID, fact); err != nil {
			return err
		}
	}
	return nil
}

func (r *resolver) touch(fact model.CanonFact, subject string) error {
	if r.touched[fact.ID] {
		return fmt.Errorf("事实「%s」%s 在一次提交里只能出现一次（facts、confirm_facts、remove_facts 合计，改名后新旧名字指同一个）；"+
			"同一章里同一主体的同一谓词只记一条，要改它的值就只在 facts 里给新值: %w", subject, fact.Predicate, model.ErrInvalid)
	}
	r.touched[fact.ID] = true
	return nil
}
