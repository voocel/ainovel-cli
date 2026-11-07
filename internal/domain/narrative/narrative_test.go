package narrative

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func fixture() *Story {
	chapter := func(id string, number int) model.ManuscriptChapter {
		return model.ManuscriptChapter{ID: id, PlanNodeID: id, Number: number, Author: model.AuthorAI,
			Blocks: []model.ManuscriptBlock{{ID: "b1", Text: "山风很冷。"}, {ID: "b2", Text: "他走进山门。"}}}
	}
	return New(Content{
		Intent: model.Intent{Premise: "林凡拜入宗门，与苏晴同修"},
		Plan: []model.PlanNode{
			{ID: "v1", Kind: model.PlanVolume, Order: 1, Title: "入门", Summary: "进入宗门"},
			{ID: "a1", Kind: model.PlanArc, ParentID: "v1", Order: 1, Title: "试炼", Summary: "外门试炼"},
			{ID: "c1", Kind: model.PlanChapter, Turn: "转折", ParentID: "a1", Order: 1, Title: "山门", Summary: "拜师"},
			{ID: "c2", Kind: model.PlanChapter, Turn: "转折", ParentID: "a1", Order: 2, Title: "立誓", Summary: "立誓"},
			{ID: "c3", Kind: model.PlanChapter, Turn: "转折", ParentID: "a1", Order: 3, Title: "下山", Summary: "下山"},
		},
		Entities: []model.Entity{
			{ID: "e1", Kind: model.EntityCharacter, Name: "林凡", Aliases: []string{"小凡"}},
			{ID: "e2", Kind: model.EntityCharacter, Name: "苏晴"},
		},
		Canon: []model.CanonFact{
			{ID: "f1", Kind: model.CanonState, SubjectID: "e1", Predicate: "state.realm", Value: json.RawMessage(`"炼气"`), SourceChapterID: "c1"},
			{ID: "f2", Kind: model.CanonEvent, SubjectID: "e1", Predicate: "event.oath", Value: json.RawMessage(`"立誓入宗"`), SourceChapterID: "c2"},
			{ID: "f3", Kind: model.CanonWorldRule, SubjectID: "e2", Predicate: "rule.spirit_cost", Value: json.RawMessage(`"施法耗灵"`)},
		},
		Manuscript: []model.ManuscriptChapter{chapter("c1", 1), chapter("c2", 2)},
	})
}

// internalID 匹配本测试夹具与宿主分配的内部 ID 形状。
var internalID = regexp.MustCompile(`"[vacef]\d+"`)

func patchIDs(patches []model.Patch) []string {
	ids := make([]string, 0, len(patches))
	for _, patch := range patches {
		ids = append(ids, patch.Document.Key()+"/"+string(patch.Operation))
	}
	return ids
}

func decodePatch[T any](t *testing.T, patches []model.Patch, key string) T {
	t.Helper()
	var value T
	for _, patch := range patches {
		if patch.Document.Key() == key {
			if err := json.Unmarshal(patch.Content, &value); err != nil {
				t.Fatalf("decode %s: %v", key, err)
			}
			return value
		}
	}
	t.Fatalf("missing patch %s in %v", key, patchIDs(patches))
	return value
}

// 规划：已有序号就地修改，新增序号紧接末尾，ID 与顺序由宿主给。
func TestResolvePlanEditsAssignIDsAndKeepNumbering(t *testing.T) {
	story := fixture()
	patches, err := story.Resolve(&model.RevisePlanInput{}, Submission{
		Arcs: []ArcEdit{{Arc: 2, Volume: 1, Title: "下山", Summary: "历练"}},
		Chapters: []ChapterEdit{
			{Chapter: 5, Arc: 2, Title: "夜雨", Summary: "遇袭", Turn: "刺客认得林凡"},
			{Chapter: 4, Arc: 2, Title: "出山", Summary: "告别", Turn: "师父交出旧剑"},
			{Chapter: 3, Summary: "改为悄然下山"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	arc := decodePatch[model.PlanNode](t, patches, "plan:a2")
	fourth := decodePatch[model.PlanNode](t, patches, "plan:c4")
	fifth := decodePatch[model.PlanNode](t, patches, "plan:c5")
	third := decodePatch[model.PlanNode](t, patches, "plan:c3")
	if arc.ParentID != "v1" || fourth.ParentID != "a2" || fourth.Order != 4 || fifth.Order != 5 || fourth.Title != "出山" {
		t.Fatalf("new nodes = %+v %+v %+v", arc, fourth, fifth)
	}
	if fourth.Turn != "师父交出旧剑" || fifth.Turn != "刺客认得林凡" {
		t.Fatalf("new chapters carry their turns: %+v %+v", fourth, fifth)
	}
	if third.Title != "下山" || third.Summary != "改为悄然下山" || third.Turn != "转折" || third.ParentID != "a1" {
		t.Fatalf("update must keep unspecified fields: %+v", third)
	}
	projected, err := story.Apply(patches)
	if err != nil || projected.chapterNumberOf("c5") != 5 {
		t.Fatalf("projected numbering = %d, %v", projected.chapterNumberOf("c5"), err)
	}

	for name, edits := range map[string][]ChapterEdit{
		"gap":       {{Chapter: 5, Arc: 1, Title: "t", Summary: "s"}},
		"duplicate": {{Chapter: 3, Title: "a"}, {Chapter: 3, Title: "b"}},
		"no parent": {{Chapter: 4, Title: "t", Summary: "s", Turn: "r"}},
		"no turn":   {{Chapter: 4, Arc: 1, Title: "t", Summary: "s"}},
	} {
		if _, err := story.Resolve(&model.RevisePlanInput{}, Submission{Chapters: edits}, nil); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

// 篇幅上限只为超过护栏而设（D96）：规划改终局与收官时省略它，用户认可的上限不丢；护栏以内的
// 数字是预计篇幅，不收；故事需要更长时给出更高的上限。
func TestCompassEditKeepsTheLimitUnlessRaised(t *testing.T) {
	content := fixture().content
	content.Compass = &model.Compass{ScaleMax: 450, Ending: "林凡入内门"}
	story := New(content)
	patches, err := story.Resolve(&model.RevisePlanInput{}, Submission{Compass: &CompassEdit{Ending: "林凡入内门", Final: 3}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if compass := decodePatch[model.Compass](t, patches, "compass:root"); compass.ScaleMax != 450 || compass.Final != 3 {
		t.Fatalf("omitted scale_max dropped the approved limit: %+v", compass)
	}
	if _, err := story.Resolve(&model.RevisePlanInput{}, Submission{Compass: &CompassEdit{ScaleMax: 280, Ending: "林凡入内门"}}, nil); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("an estimate within the guardrail = %v", err)
	}
	patches, err = story.Resolve(&model.RevisePlanInput{}, Submission{Compass: &CompassEdit{ScaleMax: 600, Ending: "林凡入内门"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if compass := decodePatch[model.Compass](t, patches, "compass:root"); compass.ScaleMax != 600 {
		t.Fatalf("raised limit = %+v", compass)
	}
}

// 提前收官时删去写不到的卷与故事弧（D97）：只能删下面还没有节点的，删去的不能再挂节点。
func TestRemoveUnexpandedVolumesAndArcs(t *testing.T) {
	content := fixture().content
	content.Plan = append(content.Plan,
		model.PlanNode{ID: "a2", Kind: model.PlanArc, ParentID: "v1", Order: 2, Title: "历练", Summary: "下山历练"},
		model.PlanNode{ID: "v2", Kind: model.PlanVolume, Order: 2, Title: "内门", Summary: "内门争锋"})
	story := New(content)
	patches, err := story.Resolve(&model.RevisePlanInput{}, Submission{
		Volumes: []VolumeEdit{{Volume: 2, Remove: true}}, Arcs: []ArcEdit{{Arc: 2, Remove: true}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 2 || patches[0].Document.ID != "v2" || patches[1].Document.ID != "a2" ||
		patches[0].Operation != model.PatchDelete || patches[1].Operation != model.PatchDelete {
		t.Fatalf("patches = %+v", patches)
	}
	for _, c := range []struct {
		name       string
		submission Submission
	}{
		{"a volume with arcs", Submission{Volumes: []VolumeEdit{{Volume: 1, Remove: true}}}},
		{"an arc with chapters", Submission{Arcs: []ArcEdit{{Arc: 1, Remove: true}}}},
		{"a missing volume", Submission{Volumes: []VolumeEdit{{Volume: 3, Remove: true}}}},
		{"an arc under a removed volume", Submission{Volumes: []VolumeEdit{{Volume: 2, Remove: true}},
			Arcs: []ArcEdit{{Arc: 3, Volume: 2, Title: "争锋", Summary: "内门大比"}}}},
	} {
		if _, err := story.Resolve(&model.RevisePlanInput{}, c.submission, nil); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("%s = %v", c.name, err)
		}
	}
}

// 长线（D91）：按短名新立、引用与作答，ID 由宿主给；模型视图只见短名，统计每条长线最近推进
// 的已写章、此后停了几章与下一次的安排。
func TestResolveThreadsByNameAndCountTheirQuiet(t *testing.T) {
	story := fixture()
	patches, err := story.Resolve(&model.RevisePlanInput{}, Submission{
		Compass: &CompassEdit{Ending: "林凡入内门"},
		Threads: []ThreadEdit{{Name: "内门", Question: "林凡能否入内门", Answer: 4}, {Name: "苏晴", Question: "苏晴为何入宗"}},
		Chapters: []ChapterEdit{
			{Chapter: 3, Threads: []string{"苏晴"}},
			{Chapter: 4, Arc: 1, Title: "内门", Summary: "入内门", Turn: "林凡过了内门考校", Threads: []string{"内门", "苏晴"}},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	compass := decodePatch[model.Compass](t, patches, "compass:root")
	if len(compass.Threads) != 2 || compass.Threads[0] != (model.StoryThread{ID: "t1", Name: "内门", Question: "林凡能否入内门", Answer: "c4"}) ||
		compass.Threads[1].ID != "t2" || compass.Threads[1].Answer != "" || compass.ScaleMax != 0 {
		t.Fatalf("compass = %+v", compass)
	}
	if third := decodePatch[model.PlanNode](t, patches, "plan:c3"); !slices.Equal(third.Threads, []string{"t2"}) || third.Summary != "下山" {
		t.Fatalf("chapter 3 = %+v", third)
	}
	projected, err := story.Apply(patches)
	if err != nil {
		t.Fatal(err)
	}
	view := projected.Compass()
	want := []ThreadView{
		{Name: "内门", Question: "林凡能否入内门", Quiet: 2, Next: 4, Answer: 4},
		{Name: "苏晴", Question: "苏晴为何入宗", Quiet: 2, Next: 3},
	}
	if !slices.Equal(view.Threads, want) {
		t.Fatalf("threads = %+v", view.Threads)
	}
	content := projected.content
	content.Plan = slices.Clone(content.Plan)
	content.Plan[slices.IndexFunc(content.Plan, func(node model.PlanNode) bool { return node.ID == "c1" })].Threads = []string{"t1"}
	if advanced := New(content).Compass().Threads[0]; advanced.Advanced != 1 || advanced.Quiet != 1 || advanced.Next != 4 {
		t.Fatalf("advanced in chapter 1 = %+v", advanced)
	}
	content.Compass = &model.Compass{Ending: "林凡入内门", Threads: []model.StoryThread{{ID: "t1", Name: "内门", Question: "林凡能否入内门", Answer: "c2"}}}
	if answered := New(content).Compass().Threads[0]; !answered.Answered || answered.Advanced != 2 || answered.Quiet != 0 {
		t.Fatalf("answered in chapter 2 = %+v", answered)
	}
	if err := projected.Humanize(fmt.Errorf(`thread "t1": %w`, model.ErrInvalid)); !strings.Contains(err.Error(), "长线「内门」") {
		t.Fatalf("humanized = %v", err)
	}

	// 已有长线按短名定位：省略 question 保持原样，作答章随后可改；章的长线省略保持原样。
	again, err := projected.Resolve(&model.RevisePlanInput{}, Submission{
		Threads:  []ThreadEdit{{Name: "苏晴", Answer: 5}},
		Chapters: []ChapterEdit{{Chapter: 5, Arc: 1, Title: "真相", Summary: "苏晴道出来意", Turn: "苏晴是来寻仇的"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	compass = decodePatch[model.Compass](t, again, "compass:root")
	if compass.Threads[1] != (model.StoryThread{ID: "t2", Name: "苏晴", Question: "苏晴为何入宗", Answer: "c5"}) || compass.Ending != "林凡入内门" {
		t.Fatalf("answered compass = %+v", compass)
	}
	if fifth := decodePatch[model.PlanNode](t, again, "plan:c5"); fifth.Threads != nil {
		t.Fatalf("chapter 5 threads = %v", fifth.Threads)
	}
	if untouched, err := projected.Resolve(&model.RevisePlanInput{}, Submission{Chapters: []ChapterEdit{{Chapter: 3, Summary: "改道下山"}}}, nil); err != nil ||
		slices.ContainsFunc(untouched, func(patch model.Patch) bool { return patch.Document.Kind == model.DocumentCompass }) {
		t.Fatalf("an untouched compass must not be rewritten: %v, %v", patchIDs(untouched), err)
	}

	for name, submission := range map[string]Submission{
		"unknown thread":       {Chapters: []ChapterEdit{{Chapter: 3, Threads: []string{"师门"}}}},
		"new without question": {Threads: []ThreadEdit{{Name: "师门"}}},
		"submitted twice":      {Threads: []ThreadEdit{{Name: "师门", Question: "师门何在"}, {Name: "师门", Question: "师门何在"}}},
		"unnamed":              {Threads: []ThreadEdit{{Question: "师门何在"}}},
		"listed twice":         {Chapters: []ChapterEdit{{Chapter: 3, Threads: []string{"内门", "内门"}}}},
		"answer nowhere":       {Threads: []ThreadEdit{{Name: "内门", Answer: 9}}},
	} {
		if _, err := projected.Resolve(&model.RevisePlanInput{}, submission, nil); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
	// 作答章还没规划到（只看清近处）：说清先不写，规划到那一章再写。
	if _, err := projected.Resolve(&model.RevisePlanInput{}, Submission{Threads: []ThreadEdit{{Name: "内门", Answer: 9}}}, nil); err == nil ||
		!strings.Contains(err.Error(), "长线「内门」的作答章第 9 章还没有规划") {
		t.Fatalf("unplanned answer = %v", err)
	}
}

// 写新章：事实按自然键并入已有节点，old_value 取自基线；事件按章追加；新实体可当场引用。
func TestResolveChapterSubmissionUsesNaturalKeys(t *testing.T) {
	story := fixture()
	input := &model.WriteChapterInput{ChapterPlanID: "c3"}
	draft, err := story.Draft(input, ChapterDraft{Blocks: []model.ManuscriptBlock{{ID: "b1", Text: "下山了。"}}})
	if err != nil || draft.ID != "c3" || draft.PlanNodeID != "c3" || draft.Number != 3 || draft.Author != model.AuthorAI {
		t.Fatalf("draft identity = %+v, %v", draft, err)
	}
	patches, err := story.Resolve(input, Submission{
		Entities: []EntityEdit{{Name: "黑风寨", Kind: model.EntityOrganization}},
		Facts: []FactEdit{
			{Subject: "小凡", Predicate: "state.realm", Value: json.RawMessage(`"筑基"`)},
			{Subject: "林凡", Predicate: "event.oath", Value: json.RawMessage(`"再次立誓"`)},
			{Subject: "黑风寨", Predicate: "state.hostile", Value: json.RawMessage(`true`), EffectiveChapter: 2},
		},
	}, []model.ManuscriptChapter{draft})
	if err != nil {
		t.Fatal(err)
	}
	realm := decodePatch[model.CanonFact](t, patches, "canon:f1")
	if realm.SourceChapterID != "c3" || string(realm.PreviousValue) != `"炼气"` || realm.Kind != model.CanonState {
		t.Fatalf("keyed update = %+v", realm)
	}
	oath := decodePatch[model.CanonFact](t, patches, "canon:f4")
	if oath.SourceChapterID != "c3" || len(oath.PreviousValue) != 0 || oath.Kind != model.CanonEvent {
		t.Fatalf("events append per chapter: %+v", oath)
	}
	hostile := decodePatch[model.CanonFact](t, patches, "canon:f5")
	if hostile.SubjectID != "e3" || hostile.EffectiveChapterID != "c2" {
		t.Fatalf("new entity fact = %+v", hostile)
	}
	decodePatch[model.ManuscriptChapter](t, patches, "manuscript:c3")

	for name, submission := range map[string]Submission{
		"unknown subject": {Facts: []FactEdit{{Subject: "路人", Predicate: "state.mood", Value: json.RawMessage(`"好"`)}}},
		"bad predicate":   {Facts: []FactEdit{{Subject: "林凡", Predicate: "mood", Value: json.RawMessage(`"好"`)}}},
		"twice":           {Facts: []FactEdit{{Subject: "林凡", Predicate: "state.realm", Value: json.RawMessage(`"a"`)}, {Subject: "小凡", Predicate: "state.realm", Value: json.RawMessage(`"b"`)}}},
		"foreign chapter": {Facts: []FactEdit{{Subject: "林凡", Predicate: "state.realm", Value: json.RawMessage(`"a"`), Chapter: 1}}},
		"alias rename":    {Entities: []EntityEdit{{Name: "小凡", Kind: model.EntityCharacter}}},
	} {
		if _, err := story.Resolve(input, submission, []model.ManuscriptChapter{draft}); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

// 重写：确认复制基线、删除按自然键定位，同一事实不得既确认又改。
func TestResolveRedeclarationByNaturalKey(t *testing.T) {
	story := fixture()
	input := &model.RewriteChapterInput{ChapterID: "c2", ChapterPlanID: "c2", Findings: []string{"节奏拖沓"}}
	draft, err := story.Draft(input, ChapterDraft{Blocks: []model.ManuscriptBlock{{ID: "b1", Text: "他立誓。"}}})
	if err != nil {
		t.Fatal(err)
	}
	patches, err := story.Resolve(input, Submission{
		Confirm: []FactRef{{Subject: "林凡", Predicate: "event.oath"}},
		Remove:  []FactRef{{Subject: "苏晴", Predicate: "rule.spirit_cost"}},
	}, []model.ManuscriptChapter{draft})
	if err != nil {
		t.Fatal(err)
	}
	if got := patchIDs(patches); !slices.Equal(got, []string{"manuscript:c2/put", "canon:f2/put", "canon:f3/delete"}) {
		t.Fatalf("patches = %v", got)
	}
	if oath := decodePatch[model.CanonFact](t, patches, "canon:f2"); string(oath.PreviousValue) != string(oath.Value) {
		t.Fatalf("confirmation must copy the baseline: %+v", oath)
	}
	if _, err := story.Resolve(input, Submission{
		Facts:   []FactEdit{{Subject: "林凡", Predicate: "event.oath", Value: json.RawMessage(`"改"`)}},
		Confirm: []FactRef{{Subject: "林凡", Predicate: "event.oath"}},
	}, []model.ManuscriptChapter{draft}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("confirm and update of one fact: %v", err)
	}
	if _, err := story.Resolve(input, Submission{Confirm: []FactRef{{Subject: "林凡", Predicate: "event.missing"}}}, []model.ManuscriptChapter{draft}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("missing fact: %v", err)
	}
}

// 返工重申报本章的章题与章纲（D87）：必须给出，变了才入账；别的任务提交不了。
func TestReworkRedeclaresItsChapterOutline(t *testing.T) {
	story := fixture()
	rework := &model.ReworkChapterInput{ChapterID: "c2", ChapterPlanID: "c2", Directives: []model.Directive{{
		ID: "d1", Scope: model.DirectiveScopePlanNode("c2"), Text: "改成拒绝立誓", Status: model.DirectiveActive, Rework: true}}}
	draft, err := story.Draft(rework, ChapterDraft{Blocks: []model.ManuscriptBlock{{ID: "b1", Text: "他没有立誓。"}}})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(input model.TaskInput, outline *OutlineEdit) ([]model.Patch, error) {
		return story.Resolve(input, Submission{Outline: outline, Remove: []FactRef{{Subject: "林凡", Predicate: "event.oath"}}}, []model.ManuscriptChapter{draft})
	}
	if _, err := resolve(rework, nil); !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "重申报本章的章题、章纲与转折") {
		t.Fatalf("rework without its outline: %v", err)
	}
	if _, err := resolve(rework, &OutlineEdit{Title: "拒誓", Summary: "林凡拒绝立誓"}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("rework without its turn: %v", err)
	}
	patches, err := resolve(rework, &OutlineEdit{Title: "拒誓", Summary: "林凡拒绝立誓", Turn: "林凡与宗门决裂"})
	if err != nil {
		t.Fatal(err)
	}
	if node := decodePatch[model.PlanNode](t, patches, "plan:c2"); node.Title != "拒誓" || node.Summary != "林凡拒绝立誓" || node.Turn != "林凡与宗门决裂" || node.ParentID != "a1" {
		t.Fatalf("outline = %+v", node)
	}
	if patches, err = resolve(rework, &OutlineEdit{Title: "立誓", Summary: "立誓", Turn: "转折"}); err != nil || slices.Contains(patchIDs(patches), "plan:c2/put") {
		t.Fatalf("unchanged outline = %v, %v", patchIDs(patches), err)
	}
	rewrite := &model.RewriteChapterInput{ChapterID: "c2", ChapterPlanID: "c2", Findings: []string{"节奏拖沓"}}
	if _, err := resolve(rewrite, &OutlineEdit{Title: "拒誓", Summary: "林凡拒绝立誓", Turn: "林凡与宗门决裂"}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("a review rewrite edited its outline: %v", err)
	}
}

// 规划核对逐处下结论（D87）：要核对的是改动碰到或牵连的没写成正文的规划——没写的大纲（按
// 改后的故事称呼，这次新增的章也有章号）、规划时定下的事实、作品前提；已写章的章纲与事实随
// 正文走，改动自己写下的事实是设定本身，都不在其中。每一处都要有结论，漏了或多了是分析器
// 出错；还按改前设定写的落在那一处文档上。
func TestPlanningCheckJudgesEachUnwrittenPlan(t *testing.T) {
	plan := func(node model.PlanNode) model.Patch {
		content, err := json.Marshal(node)
		if err != nil {
			t.Fatal(err)
		}
		return model.Patch{Document: model.DocumentRef{Kind: model.DocumentPlan, ID: node.ID}, Operation: model.PatchPut, Content: content}
	}
	ref := func(kind model.DocumentKind, id string) model.DocumentRef {
		return model.DocumentRef{Kind: kind, ID: id}
	}
	premise, rule := ref(model.DocumentIntent, model.SingletonDocumentID), ref(model.DocumentCanon, "f3")
	patches := []model.Patch{
		plan(model.PlanNode{ID: "c3", Kind: model.PlanChapter, Turn: "转折", ParentID: "a1", Order: 3, Title: "下山", Summary: "林凡独自下山"}),
		plan(model.PlanNode{ID: "c4", Kind: model.PlanChapter, Turn: "转折", ParentID: "a1", Order: 4, Title: "归来", Summary: "归来"}),
	}
	affected := []model.DocumentRef{ref(model.DocumentManuscript, "c2"), ref(model.DocumentPlan, "c2"), ref(model.DocumentPlan, "a1"),
		rule, ref(model.DocumentCanon, "f1"), premise}
	_, planning, err := fixture().Impact("改下山", patches, []model.DocumentRef{ref(model.DocumentPlan, "c3"), ref(model.DocumentPlan, "c4")}, affected)
	if err != nil {
		t.Fatal(err)
	}
	if checks := planning.Checks(); !slices.Equal(checks, []string{"第 1 个故事弧", "第 3 章", "第 4 章", "「苏晴」rule.spirit_cost", "作品前提"}) {
		t.Fatalf("checks = %v", checks)
	}
	if len(planning.Facts) != 1 || planning.Facts[0].Predicate != "rule.spirit_cost" || planning.Premise != "林凡拜入宗门，与苏晴同修" {
		t.Fatalf("planning = %+v", planning)
	}
	verdicts := map[string]PlanningVerdict{
		"第 1 个故事弧":             {Explanation: "没写到下山"},
		"第 3 章":                {Explanation: "还写着结伴下山", Stale: true},
		"第 4 章":                {Explanation: "对得上"},
		"「苏晴」rule.spirit_cost": {Explanation: "对得上"},
		"作品前提":                 {Explanation: "前提还写着同修", Stale: true},
	}
	stale, err := planning.Stale(verdicts)
	if err != nil || len(stale) != 2 || *stale[0].Document != ref(model.DocumentPlan, "c3") || *stale[1].Document != premise {
		t.Fatalf("stale = %+v, %v", stale, err)
	}
	delete(verdicts, "第 4 章")
	if _, err := planning.Stale(verdicts); !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "漏了第 4 章") {
		t.Fatalf("a missing verdict was accepted: %v", err)
	}
	verdicts["第 2 章"] = PlanningVerdict{Explanation: "已经写了"}
	if _, err := planning.Stale(verdicts); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("a verdict on a written chapter was accepted: %v", err)
	}
	// 改动自己写下的事实是设定本身，拿来对照，不再核对它。
	_, planning, err = fixture().Impact("改施法", nil, []model.DocumentRef{rule}, []model.DocumentRef{rule, premise})
	if err != nil {
		t.Fatal(err)
	}
	if checks := planning.Checks(); !slices.Equal(checks, []string{"作品前提"}) || len(planning.After.Facts) != 1 {
		t.Fatalf("checks = %v, after = %+v", checks, planning.After)
	}
}

// 模型读到的任务、上下文片段与回查结果里没有任何内部 ID。
func TestModelFacingViewsCarryNoInternalIDs(t *testing.T) {
	story := fixture()
	directive := model.Directive{ID: "d1", Scope: model.DirectiveScopePlanNode("a1"), Text: "多写心理", Status: model.DirectiveActive}
	tasks := map[model.OperationKind]any{
		model.OperationRewriteChapter: model.RewriteChapterInput{ChapterID: "c2", ChapterPlanID: "c2", Findings: []string{"拖沓"}, Directives: []model.Directive{directive}},
		model.OperationReviseCanon:    model.ReviseCanonInput{ChapterID: "c2", FactIDs: []string{"f2"}, Reason: "核验"},
		model.OperationReviewRange: model.ReviewRangeInput{ChapterIDs: []string{"c2", "c1"}, Reviewed: []string{"c1"},
			PriorFindings: []model.ReviewFinding{{ChapterID: "c2", Severity: model.FindingBlocking, Note: "拖沓"}},
			Basis: model.EvidenceBasis{Documents: []model.DocumentBasis{
				{Ref: model.DocumentRef{Kind: model.DocumentManuscript, ID: "c1"}, Revision: 2},
			}}},
	}
	var rendered []string
	for kind, input := range tasks {
		raw, _ := json.Marshal(input)
		view, err := story.Task(kind, raw)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if kind == model.OperationReviewRange &&
			!strings.Contains(string(view), `"reviewed":[1],"prior_findings":[{"chapter":2,"severity":"blocking","note":"拖沓"}]`) {
			t.Fatalf("review view = %s", view)
		}
		rendered = append(rendered, string(view))
	}
	ownership := story.Ownership([]model.OwnershipRule{
		{Target: model.DocumentRef{Kind: model.DocumentManuscript, ID: "c1"}, Control: model.ControlLocked},
		{Target: model.DocumentRef{Kind: model.DocumentEntity, ID: "e1"}, Control: model.ControlGuided, Guidance: []string{"保持隐忍"}},
		{Target: model.DocumentRef{Kind: model.DocumentPlan, ID: "gone"}, Control: model.ControlLocked},
	})
	if len(ownership) != 2 {
		t.Fatalf("ownership = %+v", ownership)
	}
	for _, query := range []Query{{Chapter: 2}, {Arc: 1}, {Volume: 1}, {Entity: "小凡"}} {
		view, err := story.Read(query)
		if err != nil {
			t.Fatalf("read %+v: %v", query, err)
		}
		payload, _ := json.Marshal(view)
		rendered = append(rendered, string(payload))
	}
	for _, value := range []any{ownership, story.Outline([]string{"c2"}), story.Facts([]string{"f1", "f2", "f3"}), story.Texts([]string{"c1"})} {
		payload, _ := json.Marshal(value)
		rendered = append(rendered, string(payload))
	}
	all := strings.Join(rendered, "\n")
	if leaked := internalID.FindAllString(all, -1); len(leaked) > 0 {
		t.Fatalf("internal ids leaked %v in:\n%s", leaked, all)
	}
	for _, want := range []string{`"chapter":2`, `"chapters":[1,2]`, `第 1 个故事弧《试炼》`, `第 1 章《山门》的正文`, `"pending_facts"`} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %s in:\n%s", want, all)
		}
	}
}

// 错误里的内部 ID 换成故事标签，错误链不变；新提交的文档也认得。
func TestHumanizeTranslatesIDsAndKeepsErrorChain(t *testing.T) {
	story := fixture()
	projected, err := story.Apply([]model.Patch{{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: "f2"}, Operation: model.PatchDelete}})
	if err != nil {
		t.Fatal(err)
	}
	cause := fmt.Errorf(`rewritten chapter "c2" must redeclare canon "f2", see "entity:e1" and "intent:root": %w`, model.ErrStructuralConflict)
	got := projected.Humanize(cause)
	if !errors.Is(got, model.ErrStructuralConflict) || internalID.MatchString(got.Error()) || strings.Contains(got.Error(), "root") ||
		!strings.Contains(got.Error(), "第 2 章《立誓》") || !strings.Contains(got.Error(), "「林凡」event.oath（第 2 章）") {
		t.Fatalf("humanized = %v", got)
	}
}

func TestFindingsLandOnChaptersInRange(t *testing.T) {
	story := fixture()
	input := model.ReviewRangeInput{ChapterIDs: []string{"c2"}}
	findings, err := story.Findings(input, []Finding{{Chapter: 2, Severity: "note", Note: "好"}})
	if err != nil || findings[0].ChapterID != "c2" {
		t.Fatalf("findings = %+v, %v", findings, err)
	}
	if _, err := story.Findings(input, []Finding{{Chapter: 1, Severity: "note", Note: "越界"}}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("out of range finding: %v", err)
	}
	// 已审且正文未变的章只作上下文（D68）：阻塞必须链接被违反的要求，报错用故事语言。
	recheck := model.ReviewRangeInput{ChapterIDs: []string{"c1", "c2"}, Reviewed: []string{"c1"}, Requirements: []model.Requirement{{ID: "directive:d1", Text: "立誓要郑重"}}}
	_, err = story.Findings(recheck, []Finding{{Chapter: 1, Severity: "blocking", Note: "旧问题"}})
	if humanized := story.Humanize(err); !errors.Is(err, model.ErrInvalid) || internalID.MatchString(humanized.Error()) || !strings.Contains(humanized.Error(), "第 1 章") {
		t.Fatalf("blocking on reviewed chapter: %v", humanized)
	}
	for _, finding := range []Finding{
		{Chapter: 1, Severity: "note", Note: "旧问题"},
		{Chapter: 1, Severity: "blocking", Note: "违反要求", Requirement: "directive:d1"},
		{Chapter: 2, Severity: "blocking", Note: "改动章的问题"},
	} {
		if _, err := story.Findings(recheck, []Finding{finding}); err != nil {
			t.Fatalf("finding %+v: %v", finding, err)
		}
	}
	// requirement 只能链接这次审阅的要求：把问题类别填进去当场被拒，报错说出能链接的。
	if _, err := story.Findings(recheck, []Finding{{Chapter: 2, Severity: "blocking", Note: "前后矛盾", Requirement: "与已确立的事实矛盾"}}); err == nil || !strings.Contains(err.Error(), `"directive:d1"`) {
		t.Fatalf("finding linking a made-up requirement: %v", err)
	}
}

// “苏晴其实叫苏雨”：改名后新旧两个名字在这次修改里都指她，事实照旧挂在她身上。一个名字
// 只指一个实体：新实体不能把已有实体的名字占作别名，那样后面按这个名字的引用会指错。
func TestEntityRenameKeepsOneNamePerEntity(t *testing.T) {
	story := fixture()
	patches, _, err := story.ResolveEdit(Edit{
		Entities: []EntityEdit{{Name: "苏晴", Kind: model.EntityCharacter, Rename: "苏雨"}},
		Remove:   []FactRef{{Subject: "苏晴", Predicate: "rule.spirit_cost"}},
	}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if renamed := decodePatch[model.Entity](t, patches, "entity:e2"); renamed.Name != "苏雨" || len(renamed.Aliases) != 0 {
		t.Fatalf("renamed = %+v", renamed)
	}
	if !slices.Contains(patchIDs(patches), "canon:f3/delete") {
		t.Fatalf("patches = %v", patchIDs(patches))
	}
	// 改名后旧的本名与别名都不再指它（“小凡”不跟着走），还要用的列进 aliases。
	patches, _, err = story.ResolveEdit(Edit{Entities: []EntityEdit{{Name: "林凡", Kind: model.EntityCharacter, Rename: "林帆", Aliases: []string{"林凡"}}}}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if renamed := decodePatch[model.Entity](t, patches, "entity:e1"); renamed.Name != "林帆" || !slices.Equal(renamed.Aliases, []string{"林凡"}) {
		t.Fatalf("renamed = %+v", renamed)
	}
	if _, _, err := story.ResolveEdit(Edit{Entities: []EntityEdit{{Name: "苏雨", Kind: model.EntityCharacter, Aliases: []string{"苏晴"}}}}, Settings{}); err == nil || !strings.Contains(err.Error(), "用 rename") {
		t.Fatalf("a new entity taking an existing name: %v", err)
	}
	if _, _, err := story.ResolveEdit(Edit{Entities: []EntityEdit{{Name: "无名", Kind: model.EntityCharacter, Rename: "有名"}}}, Settings{}); err == nil {
		t.Fatal("renaming a missing entity must fail")
	}
}

// 事实引用错了时，报错说出用这个谓词的现有事实：事件按来源章区分，没给章号的照着补上。
func TestMissingFactReferenceNamesTheExistingOnes(t *testing.T) {
	_, _, err := fixture().ResolveEdit(Edit{Remove: []FactRef{{Subject: "林凡", Predicate: "event.oath"}}}, Settings{})
	if err == nil || !strings.Contains(err.Error(), "用这个谓词的事实有：「林凡」event.oath（chapter=2）；事件与关系按来源章区分，引用时照着带上 chapter") {
		t.Fatalf("err = %v", err)
	}
}

// 锁定一章管它的大纲，写了正文也管正文：还没写的章也能锁（S5 的关键剧情节点）；放开只删
// 已有的规则。
func TestChapterLockCoversItsOutlineAndText(t *testing.T) {
	story := fixture()
	patches, _, err := story.ResolveEdit(Edit{Locks: []LockEdit{
		{Chapter: 2, Control: model.ControlLocked},
		{Chapter: 3, Control: model.ControlGuided, Guidance: []string{"下山时要带着师父的剑"}},
	}}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if ids := patchIDs(patches); !slices.Equal(ids, []string{"ownership:plan:c2/put", "ownership:manuscript:c2/put", "ownership:plan:c3/put"}) {
		t.Fatalf("patches = %v", ids)
	}
	rules := []model.OwnershipRule{{Target: model.DocumentRef{Kind: model.DocumentPlan, ID: "c2"}, Control: model.ControlLocked}}
	patches, _, err = story.ResolveEdit(Edit{Locks: []LockEdit{{Chapter: 2, Control: model.ControlOpen}}}, Settings{Ownership: rules})
	if err != nil {
		t.Fatal(err)
	}
	if ids := patchIDs(patches); !slices.Equal(ids, []string{"ownership:plan:c2/delete"}) {
		t.Fatalf("open patches = %v", ids)
	}
}

// 锁定一条设定（S5 的角色底线）：已有的按主体与谓词找；同一次修改里新写的也能锁，用户说
// “人设锁死：……”时内容与锁定一次落下。
func TestFactLockFindsExistingAndNewFacts(t *testing.T) {
	patches, _, err := fixture().ResolveEdit(Edit{
		Facts: []FactEdit{{Subject: "苏晴", Predicate: "rule.temper", Value: json.RawMessage(`"沉默寡言"`)}},
		Locks: []LockEdit{
			{Fact: &FactRef{Subject: "苏晴", Predicate: "rule.temper"}, Control: model.ControlLocked},
			{Fact: &FactRef{Subject: "林凡", Predicate: "event.oath", Chapter: 2}, Control: model.ControlLocked},
		},
	}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if ids := patchIDs(patches); !slices.Equal(ids, []string{"canon:f4/put", "ownership:canon:f4/put", "ownership:canon:f2/put"}) {
		t.Fatalf("patches = %v", ids)
	}
	if _, _, err := fixture().ResolveEdit(Edit{Locks: []LockEdit{{Fact: &FactRef{Subject: "林凡", Predicate: "event.oath"}, Control: model.ControlLocked}}}, Settings{}); err == nil || !strings.Contains(err.Error(), "chapter=2") {
		t.Fatalf("lock without the event's chapter: %v", err)
	}
}

// 写作途中主编提的修改与写手交的稿出自同一基线（D77）：新文档的 ID 让开等裁决的提案改到的
// 文档，两份先后生效时不撞号。
func TestNewIDsAvoidPendingProposals(t *testing.T) {
	pending := []model.DocumentRef{{Kind: model.DocumentCanon, ID: "f4"}, {Kind: model.DocumentEntity, ID: "e3"}, {Kind: model.DocumentPlan, ID: "c4"}}
	patches, _, err := fixture().Avoiding(pending).ResolveEdit(Edit{
		Chapters: []ChapterEdit{{Chapter: 4, Arc: 1, Title: "出山", Summary: "告别", Turn: "师父交出旧剑"}},
		Entities: []EntityEdit{{Name: "陈渡", Kind: model.EntityCharacter}},
		Facts:    []FactEdit{{Subject: "苏晴", Predicate: "rule.temper", Value: json.RawMessage(`"沉默寡言"`)}},
	}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if ids := patchIDs(patches); !slices.Equal(ids, []string{"plan:c5/put", "entity:e4/put", "canon:f5/put"}) {
		t.Fatalf("patches = %v", ids)
	}
}

// 规划者想把伏笔的回收落在还没写的章：报错说那章还没写、该写进梗概，不说“不存在”（D81）。
func TestEffectiveChapterOnUnwrittenChapterPointsToTheSummary(t *testing.T) {
	_, err := fixture().Resolve(&model.RevisePlanInput{}, Submission{Facts: []FactEdit{
		{Subject: "林凡", Predicate: "foreshadow.jade_origin", Value: json.RawMessage(`"玉佩来历在第 9 章揭晓"`), EffectiveChapter: 9},
	}}, nil)
	if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "第 9 章还没写") || !strings.Contains(err.Error(), "梗概") {
		t.Fatalf("error = %v", err)
	}
}
