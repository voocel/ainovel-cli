package chief

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/decision"
	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

const (
	toolOutline      = "book_outline"
	toolRead         = "book_read"
	toolRequirements = "book_requirements"
	toolReviews      = "book_reviews"
	toolDraft        = "book_draft"
	toolNote         = "note_requirement"
	toolRetire       = "retire_requirement"
	toolReject       = "reject_draft"
	toolLength       = "set_length"
	toolPause        = "pause_creation"
	toolResume       = "resume_creation"
	toolChange       = "propose_change"
	toolHistory      = "book_history"
	toolRevert       = "revert_change"
)

// changes 是改动作品或创作的工具：它们的结果就是给用户看的回执。
var changes = map[string]bool{
	toolNote: true, toolRetire: true, toolReject: true, toolLength: true, toolPause: true, toolResume: true, toolChange: true, toolRevert: true,
}

type tool struct {
	schema prompt.ToolSchema
	run    func(ctx context.Context, args json.RawMessage) (string, error)
}

func define[A any](name, description, schema string, run func(ctx context.Context, args A) (string, error)) tool {
	return tool{
		schema: prompt.ToolSchema{Name: name, Description: description, InputSchema: json.RawMessage(schema)},
		run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args A
			if err := model.DecodeStrict(raw, &args); err != nil {
				return "", fmt.Errorf("参数不合法：%w", err)
			}
			return run(ctx, args)
		},
	}
}

const empty = `{"type":"object","properties":{},"additionalProperties":false}`

// tools 是主编的固定工具集（§7.4：会话内不增删）。读的工具读此刻的作品；改的工具经各自的
// 用例落成一等对象，没有旁路。
func (s *Session) tools() []tool {
	return []tool{
		define(toolOutline, "看全书的梗概、篇幅、卷→故事弧→章的大纲（每章是否已入稿），与书里所有的人物、地点、物品、组织", empty, s.outline),
		define(toolRead, "读这本书：chapter 给该章大纲、正文与来源于它的事实；arc、volume 给故事弧或卷；entity 给人物、地点、物品、组织的设定与事实。四选一", prompt.StoryReadSchema, s.read),
		define(toolRequirements, "看生效中的要求：编号、作用域、原话与量化约束", empty, s.requirements),
		define(toolReviews, "看某章当前的审阅意见（阻塞的要改，参考的仅供参考）", `{
			"type":"object","properties":{"chapter":{"type":"integer","minimum":1}},
			"required":["chapter"],"additionalProperties":false}`, s.reviews),
		define(toolDraft, "看等用户决定的稿件：为什么等用户、改了什么，候选章节通过后的规划、全文与设定", empty, s.draft),
		define(toolNote, "把用户对怎么写的要求记下来：之后作用域内的写作都会看到它，审阅逐条核验。"+
			"text 逐字摘自用户的话；scope 省略即全书。用户说已入稿的章不满足、要按这条重写时，rework 为 true，作用域里已入稿的章会按它重写；等用户决定的稿件不满意用 reject_draft。"+
			"改大纲、设定这类对已有内容的修改用 propose_change，不记成要求", `{
			"type":"object",
			"properties":{
				"text":{"type":"string","minLength":1,"description":"用户的原话，逐字摘录，可以只摘与这条要求有关的部分"},
				"scope":{"type":"object","description":"作用于哪里，给一种：chapter 单章；from 与 to 章号区间；只给 from 是自该章起；volume 或 arc 是某卷或某故事弧",
					"properties":{
						"chapter":{"type":"integer","minimum":1},
						"from":{"type":"integer","minimum":1},
						"to":{"type":"integer","minimum":1},
						"volume":{"type":"integer","minimum":1},
						"arc":{"type":"integer","minimum":1}
					},
					"additionalProperties":false},
				"target_words":{"type":"integer","minimum":1,"description":"每章字数目标（用户提到字数时）"},
				"min_words":{"type":"integer","minimum":1},
				"max_words":{"type":"integer","minimum":1},
				"rework":{"type":"boolean","description":"用户明确要已入稿的正文按这条要求重写时为 true"}
			},
			"required":["text"],"additionalProperties":false}`, s.note),
		define(toolRetire, "退掉一条要求（用户说不要了或记错了）；编号见 book_requirements", `{
			"type":"object","properties":{"id":{"type":"string","minLength":1}},
			"required":["id"],"additionalProperties":false}`, s.retire),
		define(toolReject, "退回等用户决定的稿件或规划方案，理由交给写作者、规划者按它重做，之后接着创作。reason 逐字摘自用户的话", `{
			"type":"object","properties":{"reason":{"type":"string","minLength":1}},
			"required":["reason"],"additionalProperties":false}`, s.reject),
		define(toolLength, "调整全书篇幅并继续创作：chapters 为全书章数；0 表示交给 AI（完本的书即为续写）", `{
			"type":"object","properties":{"chapters":{"type":"integer","minimum":0}},
			"required":["chapters"],"additionalProperties":false}`, s.length),
		define(toolPause, "暂停创作（用户要停下时）", empty, s.pauseTool),
		define(toolResume, "继续创作", empty, s.resume),
		define(toolChange, "按用户的意思改这本书：大纲（卷、故事弧、章的标题与梗概）、人物地点、设定、作品方向、锁定与审批方式。"+
			"已有的按序号、名称、主体+谓词定位，给出的字段改成新的，没给的不变。能否直接生效由审批决定；"+
			"改设定、人物地点时，还没写成正文的规划里点到它的——没写的章纲与卷、故事弧梗概，规划时定下的设定（不带章号的事实），作品前提——在同一次修改里一并改成新的样子，对不上会被退回；已写章的章题、章纲与事实随正文走，不在这里改。"+
			"与已写章节有出入时等用户选怎么处理。还有一份等确认的修改时，新的替换它。正文不在这里改；等用户决定的稿件、规划方案还不在书里，要改它们用 reject_draft", string(prompt.ChangeSchema()), s.proposeChange),
		define(toolHistory, "看这本书最近的修改：谁、何时、改了什么、能否撤销（编号给 revert_change），以及等用户确认的那份", empty, s.history),
		define(toolRevert, "撤销一次修改（编号见 book_history）：已生效的恢复原样，同样按审批放行；还在等确认的直接撤回", `{
			"type":"object","properties":{"id":{"type":"string","minLength":1}},
			"required":["id"],"additionalProperties":false}`, s.revert),
	}
}

type none struct{}

func (s *Session) project(ctx context.Context) (projectdoc.Snapshot, *narrative.Story, error) {
	project, err := s.service.projects.Project(ctx, s.projectID, model.InitialRevision)
	if err != nil {
		return projectdoc.Snapshot{}, nil, err
	}
	story := narrative.New(narrative.Content{Compass: project.Compass, Plan: project.Plan, Entities: project.Entities, Canon: project.Canon, Manuscript: project.Manuscript})
	return project, story, nil
}

func encode(value any) (string, error) {
	payload, err := json.Marshal(value)
	return string(payload), err
}

func (s *Session) outline(ctx context.Context, _ none) (string, error) {
	project, story, err := s.project(ctx)
	if err != nil {
		return "", err
	}
	var chapters, entities []string
	for _, node := range project.Plan {
		if node.Kind == model.PlanChapter {
			chapters = append(chapters, node.ID)
		}
	}
	for _, entity := range project.Entities {
		entities = append(entities, entity.ID)
	}
	return encode(struct {
		Intent   model.Intent           `json:"intent"`
		Compass  *narrative.CompassView `json:"compass,omitempty"`
		Totals   narrative.Totals       `json:"totals"`
		Outline  []narrative.VolumeView `json:"outline"`
		Entities []narrative.EntityView `json:"entities"`
	}{project.Intent, story.Compass(), story.Totals(), story.Outline(chapters), story.Entities(entities)})
}

func (s *Session) read(ctx context.Context, query narrative.Query) (string, error) {
	_, story, err := s.project(ctx)
	if err != nil {
		return "", err
	}
	view, err := story.Read(query)
	if err != nil {
		return "", err
	}
	return encode(view)
}

func (s *Session) requirements(ctx context.Context, _ none) (string, error) {
	project, story, err := s.project(ctx)
	if err != nil {
		return "", err
	}
	type requirement struct {
		ID          string                      `json:"id"`
		Scope       string                      `json:"scope"`
		Text        string                      `json:"text"`
		Constraints *model.DirectiveConstraints `json:"constraints,omitempty"`
		Rework      bool                        `json:"rework,omitempty"`
	}
	list := []requirement{}
	for _, directive := range model.ActiveDirectives(project.Directives) {
		list = append(list, requirement{directive.ID, story.DescribeScope(directive), directive.Text, directive.Constraints, directive.Rework})
	}
	return encode(list)
}

func (s *Session) reviews(ctx context.Context, args struct {
	Chapter int `json:"chapter"`
}) (string, error) {
	snap, err := s.service.bench.WorkbenchSnapshot(ctx, s.projectID)
	if err != nil {
		return "", err
	}
	chapterID := ""
	for _, chapter := range snap.Manuscript {
		if chapter.Number == args.Chapter {
			chapterID = chapter.ID
		}
	}
	if chapterID == "" {
		return "", fmt.Errorf("第 %d 章还没有入稿的正文", args.Chapter)
	}
	requirements := make(map[string]string, len(snap.Directives))
	for _, directive := range snap.Directives {
		requirements[directive.ID] = directive.Text
	}
	type finding struct {
		Blocking    bool   `json:"blocking"`
		Note        string `json:"note"`
		Requirement string `json:"requirement,omitempty"`
	}
	list := []finding{}
	for _, item := range snap.Findings {
		if item.ChapterID == chapterID {
			list = append(list, finding{item.Severity == "blocking", item.Note, requirements[item.Requirement]})
		}
	}
	return encode(list)
}

func (s *Session) draft(ctx context.Context, _ none) (string, error) {
	snap, err := s.service.bench.WorkbenchSnapshot(ctx, s.projectID)
	if err != nil {
		return "", err
	}
	decision := snap.Decision
	if decision == nil || !decision.HasProposal {
		return "", errors.New("现在没有等用户决定的稿件")
	}
	_, story, err := s.project(ctx)
	if err != nil {
		return "", err
	}
	next, err := story.Apply(decision.Proposal.Patches)
	if err != nil {
		return "", err
	}
	chapters := []any{}
	for _, patch := range decision.Proposal.Patches {
		if patch.Document.Kind != model.DocumentManuscript || patch.Operation != model.PatchPut {
			continue
		}
		var chapter model.ManuscriptChapter
		if err := json.Unmarshal(patch.Content, &chapter); err != nil {
			return "", fmt.Errorf("读取候选章节：%w", err)
		}
		view, err := next.Read(narrative.Query{Chapter: chapter.Number})
		if err != nil {
			return "", err
		}
		chapters = append(chapters, view)
	}
	return encode(struct {
		Reason   string                 `json:"reason"`
		Stale    bool                   `json:"stale,omitempty"`
		Changes  workbench.ProposalView `json:"changes"`
		Chapters []any                  `json:"chapters"`
	}{decision.Reason, decision.Stale, decision.View, chapters})
}

// quote 校验 text 逐字出自用户说过的话（D74：引用即署名），返回用户的原文与最近那句话的
// 来源引用。模型摘录时常把全角标点写成半角，比对不计标点的全半角，入账的是用户的写法。
func (s *Session) quote(text string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.said) - 1; i >= 0; i-- {
		if original, ok := excerpt(s.said[i].words, text); ok {
			return original, model.ConversationSource(s.said[i].sequence), nil
		}
	}
	return "", "", fmt.Errorf("「%s」不是用户说过的原话：以用户名义写入的文字必须逐字摘自用户的消息", text)
}

// excerpt 在 words 里找 text（标点不计全半角），返回 words 里的那一段。
func excerpt(words, text string) (string, bool) {
	w, t := []rune(words), []rune(text)
	for start := 0; start+len(t) <= len(w); start++ {
		if slices.EqualFunc(w[start:start+len(t)], t, func(a, b rune) bool { return halfWidth(a) == halfWidth(b) }) {
			return string(w[start : start+len(t)]), true
		}
	}
	return "", false
}

// halfWidth 把全角字符与中文引号、句号归成半角，只用于比对。
func halfWidth(r rune) rune {
	switch {
	case r >= '！' && r <= '～':
		return r - 0xFEE0
	case r == '。':
		return '.'
	case r == '“' || r == '”':
		return '"'
	case r == '‘' || r == '’':
		return '\''
	}
	return r
}

type scopeArgs struct {
	Chapter int `json:"chapter"`
	From    int `json:"from"`
	To      int `json:"to"`
	Volume  int `json:"volume"`
	Arc     int `json:"arc"`
}

func (a *scopeArgs) resolve(story *narrative.Story) (string, error) {
	if a == nil {
		return model.DirectiveScopeProject, nil
	}
	switch {
	case a.Chapter > 0 && a.From+a.To+a.Volume+a.Arc == 0:
		return model.DirectiveScopeChapters(a.Chapter, a.Chapter), nil
	case a.From > 0 && a.Chapter+a.Volume+a.Arc == 0:
		if a.To == 0 {
			return model.DirectiveScopeFromChapter(a.From), nil
		}
		if a.To < a.From {
			return "", fmt.Errorf("章号区间 %d–%d 不成立", a.From, a.To)
		}
		return model.DirectiveScopeChapters(a.From, a.To), nil
	case a.Volume > 0 && a.Chapter+a.From+a.To+a.Arc == 0:
		node, err := story.PlanNode(model.PlanVolume, a.Volume)
		return model.DirectiveScopePlanNode(node.ID), err
	case a.Arc > 0 && a.Chapter+a.From+a.To+a.Volume == 0:
		node, err := story.PlanNode(model.PlanArc, a.Arc)
		return model.DirectiveScopePlanNode(node.ID), err
	case a.Chapter+a.From+a.To+a.Volume+a.Arc == 0:
		return model.DirectiveScopeProject, nil
	}
	return "", errors.New("scope 只能给一种：chapter、from（可带 to）、volume 或 arc")
}

func (s *Session) note(ctx context.Context, args struct {
	Text        string     `json:"text"`
	Scope       *scopeArgs `json:"scope"`
	TargetWords int        `json:"target_words"`
	MinWords    int        `json:"min_words"`
	MaxWords    int        `json:"max_words"`
	Rework      bool       `json:"rework"`
}) (string, error) {
	text, source, err := s.quote(strings.TrimSpace(args.Text))
	if err != nil {
		return "", err
	}
	project, story, err := s.project(ctx)
	if err != nil {
		return "", err
	}
	scope, err := args.Scope.resolve(story)
	if err != nil {
		return "", err
	}
	reworked := 0
	if args.Rework {
		plans, directive := model.IndexPlan(project.Plan), model.Directive{Scope: scope}
		for _, chapter := range project.Manuscript {
			if directive.Covers(model.DirectiveTarget{ChapterNumber: chapter.Number, PlanNodeIDs: plans.Ancestry(chapter.PlanNodeID)}) {
				reworked++
			}
		}
		if reworked == 0 {
			return "", errors.New("作用域里还没有入稿的章，不用返工：等用户决定的稿件不满意用 reject_draft 退回，其余去掉 rework 记成之后写作的要求")
		}
	}
	var constraints *model.DirectiveConstraints
	if args.TargetWords+args.MinWords+args.MaxWords > 0 {
		constraints = &model.DirectiveConstraints{TargetWords: args.TargetWords, MinWords: args.MinWords, MaxWords: args.MaxWords}
	}
	now := time.Now().UTC()
	result, err := s.service.projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
		ProjectID: s.projectID, ChangeID: projectdoc.NewID("directive", now), UserID: s.userID,
		Scope: scope, Text: text, Constraints: constraints, Source: source, Rework: args.Rework, Reason: "主编记下用户的要求", CreatedAt: now,
	})
	if err != nil {
		return "", err
	}
	receipt := fmt.Sprintf("记下要求 · %s ·「%s」", story.DescribeScope(result.Directive), text)
	if reworked > 0 {
		receipt += fmt.Sprintf(" · 已写的 %d 章按它重写", reworked)
	}
	return s.settled(ctx, receipt, reworked > 0)
}

func (s *Session) retire(ctx context.Context, args struct {
	ID string `json:"id"`
}) (string, error) {
	project, _, err := s.project(ctx)
	if err != nil {
		return "", err
	}
	for _, directive := range model.ActiveDirectives(project.Directives) {
		if directive.ID != args.ID {
			continue
		}
		now := time.Now().UTC()
		if _, err := s.service.projects.RetireDirective(ctx, projectdoc.RetireDirectiveCommand{
			ProjectID: s.projectID, ChangeID: projectdoc.NewID("directive-retire", now), UserID: s.userID,
			DirectiveID: directive.ID, Reason: "主编按用户的话退掉要求", CreatedAt: now,
		}); err != nil {
			return "", err
		}
		return s.settled(ctx, "退掉要求 ·「"+directive.Text+"」", false)
	}
	return "", fmt.Errorf("没有生效中的要求 %q，先用 book_requirements 查编号", args.ID)
}

func (s *Session) reject(ctx context.Context, args struct {
	Reason string `json:"reason"`
}) (string, error) {
	reason, source, err := s.quote(strings.TrimSpace(args.Reason))
	if err != nil {
		return "", err
	}
	snap, err := s.service.bench.WorkbenchSnapshot(ctx, s.projectID)
	if err != nil {
		return "", err
	}
	if snap.Decision == nil || !snap.Decision.HasProposal {
		return "", errors.New("现在没有等用户决定的稿件")
	}
	if _, err := s.service.decisions.Reject(ctx, decision.RejectCommand{
		ProposalID: snap.Decision.Proposal.ID, UserID: s.userID, Reason: reason, Source: source, At: time.Now().UTC(),
	}); err != nil {
		return "", err
	}
	// 用户自己的草案（导入、回滚）没有写作者可以重做：退回即作罢。
	if snap.Decision.Proposal.OperationID == "" {
		return "退回草案 ·「" + reason + "」", nil
	}
	s.Write(WriteParams{Chapters: novel.KeepChapters})
	return "退回稿件 ·「" + reason + "」· 按这条意见重写", nil
}

func (s *Session) length(ctx context.Context, args struct {
	Chapters int `json:"chapters"`
}) (string, error) {
	snap, err := s.service.bench.WorkbenchSnapshot(ctx, s.projectID)
	if err != nil {
		return "", err
	}
	if snap.Decision != nil && snap.Decision.HasProposal {
		return "", errors.New("有稿件等用户决定，先让用户通过或退回")
	}
	completed := snap.Run != nil && snap.Run.State == model.RunCompleted
	s.restart(WriteParams{Chapters: args.Chapters, Extend: completed && args.Chapters == 0})
	if args.Chapters == 0 {
		return "篇幅交给 AI · 继续创作", nil
	}
	return fmt.Sprintf("篇幅定为 %d 章 · 继续创作", args.Chapters), nil
}

func (s *Session) pauseTool(ctx context.Context, _ none) (string, error) {
	if err := s.pause(ctx); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.paused = false
	s.mu.Unlock()
	return "暂停创作", nil
}

func (s *Session) resume(ctx context.Context, _ none) (string, error) {
	snap, err := s.service.bench.WorkbenchSnapshot(ctx, s.projectID)
	if err != nil {
		return "", err
	}
	switch {
	case snap.Decision != nil && snap.Decision.HasProposal:
		return "", errors.New("有稿件等用户决定，先让用户通过或退回")
	case snap.Run != nil && snap.Run.State == model.RunCompleted:
		return "", errors.New("全书已经完成；要续写就调整篇幅")
	case snap.Decision != nil && snap.Decision.Held != "":
		// 照原样续跑还会停在这一章：要改得先有新的说法，返工后才接着写。
		return "", errors.New("卡住的那一章修订已用尽仍没做到用户的要求：让用户按 y 接受现状；要改就问清楚怎么改，用 note_requirement 记下并设 rework，再继续创作")
	}
	// 已经在写就是用户要的状态：照实回执，不算失败。
	if !s.Write(WriteParams{Chapters: novel.KeepChapters}) {
		return "已经在写了", nil
	}
	return "继续创作", nil
}
