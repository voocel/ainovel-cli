package narrative

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 视图是模型读到的故事形状：只有章号、序号、名称与内容，没有任何文档 ID。

type Totals struct {
	Volumes  int `json:"volumes"`
	Arcs     int `json:"arcs"`
	Chapters int `json:"chapters"`
	Written  int `json:"written"`
}

type VolumeView struct {
	Volume  int       `json:"volume"`
	Title   string    `json:"title"`
	Summary string    `json:"summary"`
	Arcs    []ArcView `json:"arcs,omitempty"`
}

type ArcView struct {
	Arc      int               `json:"arc"`
	Title    string            `json:"title"`
	Summary  string            `json:"summary"`
	Chapters []ChapterPlanView `json:"chapters,omitempty"`
}

type ChapterPlanView struct {
	Chapter int    `json:"chapter"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Written bool   `json:"written,omitempty"`
}

type EntityView struct {
	Name    string           `json:"name"`
	Kind    model.EntityKind `json:"kind"`
	Aliases []string         `json:"aliases,omitempty"`
}

// FactView 的种类由谓词前缀表达；Chapter 是来源章，规划期事实为 0 省略。
type FactView struct {
	Subject          string          `json:"subject"`
	Predicate        string          `json:"predicate"`
	Value            json.RawMessage `json:"value"`
	Chapter          int             `json:"chapter,omitempty"`
	EffectiveChapter int             `json:"effective_chapter,omitempty"`
	Resolved         bool            `json:"resolved,omitempty"`
}

type ChapterText struct {
	Chapter int    `json:"chapter"`
	Title   string `json:"title"`
	Text    string `json:"text"`
}

func (s *Story) Totals() Totals {
	return Totals{
		Volumes: len(s.ordered[model.PlanVolume]), Arcs: len(s.ordered[model.PlanArc]),
		Chapters: len(s.ordered[model.PlanChapter]), Written: len(s.written),
	}
}

// Outline 把计划节点渲染成卷→弧→章的树；祖先自动补齐，节拍不进入模型视图。
func (s *Story) Outline(ids []string) []VolumeView {
	include := make(map[string]bool)
	for _, id := range ids {
		for _, ancestor := range model.PlanAncestry(s.content.Plan, id) {
			include[ancestor] = true
		}
	}
	var volumes []VolumeView
	for _, volume := range s.ordered[model.PlanVolume] {
		if !include[volume.ID] {
			continue
		}
		view := VolumeView{Volume: s.number[volume.ID], Title: volume.Title, Summary: volume.Summary}
		for _, arc := range s.ordered[model.PlanArc] {
			if !include[arc.ID] || arc.ParentID != volume.ID {
				continue
			}
			arcView := ArcView{Arc: s.number[arc.ID], Title: arc.Title, Summary: arc.Summary}
			for _, chapter := range s.ordered[model.PlanChapter] {
				if include[chapter.ID] && chapter.ParentID == arc.ID {
					arcView.Chapters = append(arcView.Chapters, s.chapterPlanView(chapter))
				}
			}
			view.Arcs = append(view.Arcs, arcView)
		}
		volumes = append(volumes, view)
	}
	return volumes
}

func (s *Story) chapterPlanView(node model.PlanNode) ChapterPlanView {
	_, written := s.written[node.ID]
	return ChapterPlanView{Chapter: s.number[node.ID], Title: node.Title, Summary: node.Summary, Written: written}
}

func (s *Story) Entities(ids []string) []EntityView {
	views := make([]EntityView, 0, len(ids))
	for _, id := range ids {
		if entity, ok := s.entities[id]; ok {
			views = append(views, EntityView{Name: entity.Name, Kind: entity.Kind, Aliases: entity.Aliases})
		}
	}
	slices.SortFunc(views, func(left, right EntityView) int { return strings.Compare(left.Name, right.Name) })
	return views
}

// Facts 按来源章、主体、谓词排序；规划期事实在前。
func (s *Story) Facts(ids []string) []FactView {
	views := make([]FactView, 0, len(ids))
	for _, id := range ids {
		if fact, ok := s.facts[id]; ok {
			views = append(views, s.factView(fact))
		}
	}
	slices.SortFunc(views, func(left, right FactView) int {
		return cmp.Or(left.Chapter-right.Chapter, strings.Compare(left.Subject, right.Subject), strings.Compare(left.Predicate, right.Predicate))
	})
	return views
}

func (s *Story) factView(fact model.CanonFact) FactView {
	view := FactView{
		Subject: s.subjectName(fact.SubjectID), Predicate: fact.Predicate, Value: fact.Value,
		Chapter: s.chapterNumberOf(fact.SourceChapterID), Resolved: fact.Resolved,
	}
	if fact.EffectiveChapterID != "" && fact.EffectiveChapterID != fact.SourceChapterID {
		view.EffectiveChapter = s.chapterNumberOf(fact.EffectiveChapterID)
	}
	return view
}

// subjectName：事实主体是依赖（D35），存在性由 Change Engine 保证；被删除的实体回退到基线。
func (s *Story) subjectName(id string) string {
	for story := s; story != nil; story = story.parent {
		if entity, ok := story.entities[id]; ok {
			return entity.Name
		}
	}
	return id
}

// Texts 渲染正文：段落以换行分隔，按章号排序。
func (s *Story) Texts(ids []string) []ChapterText {
	texts := make([]ChapterText, 0, len(ids))
	for _, id := range ids {
		if chapter, ok := s.manuscripts[id]; ok {
			texts = append(texts, ChapterText{Chapter: s.chapterNumberOf(id), Title: chapter.Title, Text: joinBlocks(chapter.Blocks)})
		}
	}
	slices.SortFunc(texts, func(left, right ChapterText) int { return left.Chapter - right.Chapter })
	return texts
}

func joinBlocks(blocks []model.ManuscriptBlock) string {
	paragraphs := make([]string, len(blocks))
	for i, block := range blocks {
		paragraphs[i] = block.Text
	}
	return strings.Join(paragraphs, "\n")
}

// Query 是按故事语言回查的请求：四选一。
type Query struct {
	Chapter int    `json:"chapter,omitempty"`
	Arc     int    `json:"arc,omitempty"`
	Volume  int    `json:"volume,omitempty"`
	Entity  string `json:"entity,omitempty"`
}

type chapterDetail struct {
	ChapterPlanView
	Arc   int        `json:"arc"`
	Text  string     `json:"text,omitempty"`
	Facts []FactView `json:"facts,omitempty"`
}

type arcDetail struct {
	ArcView
	Volume int `json:"volume"`
}

type entityDetail struct {
	EntityView
	Facts         []FactView `json:"facts,omitempty"`
	EarlierEvents int        `json:"earlier_events,omitempty"`
}

// recentEvents 是实体回查携带的事件上限：主角的事件随章数增长，只给最近的。
const recentEvents = 30

// Read 执行一次回查。
func (s *Story) Read(query Query) (any, error) {
	set := 0
	for _, given := range []bool{query.Chapter != 0, query.Arc != 0, query.Volume != 0, query.Entity != ""} {
		if given {
			set++
		}
	}
	if set != 1 {
		return nil, fmt.Errorf("chapter、arc、volume、entity 恰好给一个: %w", model.ErrInvalid)
	}
	switch {
	case query.Chapter != 0:
		plan, err := s.chapterPlan(query.Chapter)
		if err != nil {
			return nil, err
		}
		detail := chapterDetail{ChapterPlanView: s.chapterPlanView(plan), Arc: s.number[plan.ParentID]}
		if chapter, ok := s.written[plan.ID]; ok {
			detail.Text = joinBlocks(chapter.Blocks)
			detail.Facts = s.Facts(s.sourcedFrom(chapter.ID))
		}
		return detail, nil
	case query.Arc != 0:
		arc, err := s.planNode(model.PlanArc, query.Arc)
		if err != nil {
			return nil, err
		}
		detail := arcDetail{ArcView: ArcView{Arc: query.Arc, Title: arc.Title, Summary: arc.Summary}, Volume: s.number[arc.ParentID]}
		for _, chapter := range s.ordered[model.PlanChapter] {
			if chapter.ParentID == arc.ID {
				detail.Chapters = append(detail.Chapters, s.chapterPlanView(chapter))
			}
		}
		return detail, nil
	case query.Volume != 0:
		volume, err := s.planNode(model.PlanVolume, query.Volume)
		if err != nil {
			return nil, err
		}
		view := VolumeView{Volume: query.Volume, Title: volume.Title, Summary: volume.Summary}
		for _, arc := range s.ordered[model.PlanArc] {
			if arc.ParentID == volume.ID {
				view.Arcs = append(view.Arcs, ArcView{Arc: s.number[arc.ID], Title: arc.Title, Summary: arc.Summary})
			}
		}
		return view, nil
	default:
		entity, err := s.entity(query.Entity)
		if err != nil {
			return nil, err
		}
		detail := entityDetail{EntityView: EntityView{Name: entity.Name, Kind: entity.Kind, Aliases: entity.Aliases}}
		var current, events []string
		for _, fact := range s.content.Canon {
			if fact.SubjectID != entity.ID {
				continue
			}
			if fact.IsEvent() {
				events = append(events, fact.ID)
			} else {
				current = append(current, fact.ID)
			}
		}
		facts := s.Facts(events)
		if len(facts) > recentEvents {
			detail.EarlierEvents = len(facts) - recentEvents
			facts = facts[len(facts)-recentEvents:]
		}
		detail.Facts = append(s.Facts(current), facts...)
		return detail, nil
	}
}

func (s *Story) planNode(kind model.PlanNodeKind, number int) (model.PlanNode, error) {
	nodes := s.ordered[kind]
	if number < 1 || number > len(nodes) {
		return model.PlanNode{}, fmt.Errorf("第 %d 个%s不存在（共 %d 个）: %w", number, kindName(kind), len(nodes), model.ErrNotFound)
	}
	return nodes[number-1], nil
}

// sourcedFrom 是来源于某章的全部事实 ID。
func (s *Story) sourcedFrom(chapterID string) []string {
	var ids []string
	for _, fact := range s.content.Canon {
		if fact.SourceChapterID == chapterID {
			ids = append(ids, fact.ID)
		}
	}
	return ids
}

func kindName(kind model.PlanNodeKind) string {
	switch kind {
	case model.PlanVolume:
		return "卷"
	case model.PlanArc:
		return "故事弧"
	default:
		return "章"
	}
}

// Label 用故事语言指称一份文档；故事里不存在的文档返回 false。
func (s *Story) Label(ref model.DocumentRef) (string, bool) {
	if s == nil {
		return "", false
	}
	switch ref.Kind {
	case model.DocumentIntent:
		return "创作意图", true
	case model.DocumentCompass:
		return "故事罗盘", true
	case model.DocumentPlan:
		if node, ok := s.plans[ref.ID]; ok {
			if node.Kind == model.PlanChapter {
				return s.planLabel(node) + "的大纲", true
			}
			return s.planLabel(node), true
		}
	case model.DocumentManuscript:
		if chapter, ok := s.manuscripts[ref.ID]; ok {
			return fmt.Sprintf("第 %d 章《%s》的正文", s.chapterNumberOf(ref.ID), chapter.Title), true
		}
	case model.DocumentEntity:
		if entity, ok := s.entities[ref.ID]; ok {
			return "「" + entity.Name + "」", true
		}
	case model.DocumentCanon:
		if fact, ok := s.facts[ref.ID]; ok {
			return s.factLabel(fact), true
		}
	}
	return s.parent.Label(ref)
}

func (s *Story) planLabel(node model.PlanNode) string {
	switch node.Kind {
	case model.PlanVolume:
		return fmt.Sprintf("第 %d 卷《%s》", s.number[node.ID], node.Title)
	case model.PlanArc:
		return fmt.Sprintf("第 %d 个故事弧《%s》", s.number[node.ID], node.Title)
	case model.PlanChapter:
		return fmt.Sprintf("第 %d 章《%s》", s.number[node.ID], node.Title)
	default:
		return "《" + node.Title + "》"
	}
}

// planLabelByID 用故事语言指称计划节点，供要求作用域等文案使用。
func (s *Story) planLabelByID(id string) string {
	if node, ok := s.plans[id]; ok {
		return s.planLabel(node)
	}
	if s.parent != nil {
		return s.parent.planLabelByID(id)
	}
	return "大纲中尚不存在的节点"
}

// FactSummary 是事实的一句话描述：标签加内容，供要求文本这类纯文字场合使用。
func (s *Story) FactSummary(fact model.CanonFact) string {
	return s.factLabel(fact) + "：" + factText(fact)
}

// FactNote 是给读者看的事实：主体名与内容。谓词键（state.mood 这类）是给模型与程序的
// 受控命名空间，不给读者看；种类由入口按 fact.Kind 写成读者用语。
func (s *Story) FactNote(fact model.CanonFact) (subject, text string) {
	return s.subjectName(fact.SubjectID), factText(fact)
}

// factText 事实的内容：字符串值取原文，其余照录 JSON。
func factText(fact model.CanonFact) string {
	var text string
	if json.Unmarshal(fact.Value, &text) != nil {
		text = string(fact.Value)
	}
	return text
}

// FactLabel 形如「主体」谓词；事件与关系按章归属，附来源章。
func (s *Story) factLabel(fact model.CanonFact) string {
	label := "「" + s.subjectName(fact.SubjectID) + "」" + fact.Predicate
	if _, keyed := fact.ConceptKey(); !keyed {
		if number := s.chapterNumberOf(fact.SourceChapterID); number > 0 {
			label += fmt.Sprintf("（第 %d 章）", number)
		}
	}
	return label
}
