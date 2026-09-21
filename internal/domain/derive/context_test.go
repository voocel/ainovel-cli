package derive

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestContextKeyCanonicalizesEquivalentTaskJSON(t *testing.T) {
	left, err := ContextKey(model.OperationWriteChapter, json.RawMessage(`{"chapter_plan_id":"chapter-1","note":"雨"}`))
	if err != nil {
		t.Fatalf("left context key: %v", err)
	}
	right, err := ContextKey(model.OperationWriteChapter, json.RawMessage(`{ "note": "雨", "chapter_plan_id": "chapter-1" }`))
	if err != nil {
		t.Fatalf("right context key: %v", err)
	}
	if left != right {
		t.Fatalf("equivalent tasks produced different keys: %q != %q", left, right)
	}
}

func TestWriteContextMeetsMinimumWritingContract(t *testing.T) {
	// §6.5 写作最低契约：当前章 Plan 及其 arc/volume 祖先、全部章节摘要（Plan）、
	// 最新有效 Canon 状态、上一章正文结尾；更早的正文只留索引不进上下文。
	locked := model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-bottom-line"}
	relevant := model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-location"}
	context, err := BuildStoryContext(ProjectContent{
		ID: "book-1", Revision: 4, Intent: model.Intent{Premise: "凡人远行"},
		Plan: []model.PlanNode{
			{ID: "volume-1", Kind: model.PlanVolume, Title: "远行", Summary: "离开故乡"},
			{ID: "arc-1", Kind: model.PlanArc, ParentID: "volume-1", Title: "渡河", Summary: "寻找渡口"},
			{ID: "chapter-plan-1", Kind: model.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "启程"},
			{ID: "chapter-plan-2", Kind: model.PlanChapter, ParentID: "arc-1", Order: 2, Title: "第二章", Summary: "夜宿荒村"},
			{ID: "chapter-plan-3", Kind: model.PlanChapter, ParentID: "arc-1", Order: 3, Title: "第三章", Summary: "抵达河边", DependsOn: []model.DocumentRef{relevant}},
		},
		Entities: []model.Entity{
			{ID: "hero", Kind: model.EntityCharacter, Name: "主角"},
			{ID: "villain", Kind: model.EntityCharacter, Name: "反派"},
		},
		Canon: []model.CanonFact{
			{ID: locked.ID, Kind: model.CanonWorldRule, SubjectID: "hero", Predicate: "rule.bottom_line", Value: json.RawMessage(`"不伤无辜"`)},
			{ID: relevant.ID, Kind: model.CanonState, SubjectID: "hero", Predicate: "state.location", Value: json.RawMessage(`"河边"`)},
			{ID: "villain-location", Kind: model.CanonState, SubjectID: "villain", Predicate: "state.location", Value: json.RawMessage(`"京城"`)},
		},
		Manuscript: []model.ManuscriptChapter{
			{ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "第一章", Author: model.AuthorAI, Blocks: []model.ManuscriptBlock{{ID: "block-1", Text: "更早正文只留索引"}}},
			{ID: "chapter-2", PlanNodeID: "chapter-plan-2", Number: 2, Title: "第二章", Author: model.AuthorAI, Blocks: []model.ManuscriptBlock{{ID: "block-1", Text: "上一章结尾必须可见"}}},
		},
		Ownership: []model.OwnershipRule{{Target: locked, Control: model.ControlLocked}},
	}, model.OperationWriteChapter, json.RawMessage(`{"chapter_plan_id":"chapter-plan-3","chapter_number":3}`))
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if context.SchemaVersion != StoryContextKind {
		t.Fatalf("schema version = %q, want %q", context.SchemaVersion, StoryContextKind)
	}
	keys := make(map[string]bool)
	for _, document := range context.Documents {
		keys[document.Ref.Key()] = true
	}
	for _, key := range []string{
		"plan:volume-1", "plan:arc-1", "plan:chapter-plan-1", "plan:chapter-plan-2", "plan:chapter-plan-3",
		"canon:hero-location", "canon:hero-bottom-line", "canon:villain-location",
		"manuscript:chapter-2",
	} {
		if !keys[key] {
			t.Fatalf("missing minimum-contract document %q: %#v", key, context.Documents)
		}
	}
	if keys["manuscript:chapter-1"] || len(context.ManuscriptIndex) != 2 {
		t.Fatalf("older manuscript body entered context or index broken: %#v", context)
	}
}

// contextFixture 是依赖闭包用例的基础作品：chapter-plan-2 引用 canon:hero-location，
// 该事实又引用 entity:ferry；chapter-1 正文引用 entity:villain；villain-goal 来源于 chapter-1。
func contextFixture() ProjectContent {
	return ProjectContent{
		ID: "book-1", Revision: 3, Intent: model.Intent{Premise: "凡人远行"},
		Plan: []model.PlanNode{
			{ID: "volume-1", Kind: model.PlanVolume, Title: "远行", Summary: "离开故乡"},
			{ID: "arc-1", Kind: model.PlanArc, ParentID: "volume-1", Title: "渡河", Summary: "寻找渡口"},
			{ID: "chapter-plan-1", Kind: model.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "启程"},
			{ID: "chapter-plan-2", Kind: model.PlanChapter, ParentID: "arc-1", Order: 2, Title: "第二章", Summary: "夜宿荒村",
				DependsOn: []model.DocumentRef{{Kind: model.DocumentCanon, ID: "hero-location"}}},
			{ID: "chapter-plan-3", Kind: model.PlanChapter, ParentID: "arc-1", Order: 3, Title: "第三章", Summary: "抵达河边"},
		},
		Entities: []model.Entity{
			{ID: "hero", Kind: model.EntityCharacter, Name: "主角"},
			{ID: "villain", Kind: model.EntityCharacter, Name: "反派"},
			{ID: "ferry", Kind: model.EntityLocation, Name: "渡口"},
		},
		Canon: []model.CanonFact{
			{ID: "hero-location", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.location", Value: json.RawMessage(`"荒村"`),
				DependsOn: []model.DocumentRef{{Kind: model.DocumentEntity, ID: "ferry"}}},
			{ID: "villain-goal", Kind: model.CanonState, SubjectID: "villain", Predicate: "state.goal", Value: json.RawMessage(`"夺宝"`), SourceChapterID: "chapter-1"},
			{ID: "hero-bottom-line", Kind: model.CanonWorldRule, SubjectID: "hero", Predicate: "rule.bottom_line", Value: json.RawMessage(`"不伤无辜"`)},
		},
		Manuscript: []model.ManuscriptChapter{
			{ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "第一章", Author: model.AuthorAI,
				Blocks:    []model.ManuscriptBlock{{ID: "block-1", Text: "启程正文"}},
				DependsOn: []model.DocumentRef{{Kind: model.DocumentEntity, ID: "villain"}}},
			{ID: "chapter-2", PlanNodeID: "chapter-plan-2", Number: 2, Title: "第二章", Author: model.AuthorAI,
				Blocks: []model.ManuscriptBlock{{ID: "block-1", Text: "荒村正文"}, {ID: "block-2", Text: "夜话"}}},
		},
	}
}

func documentKeys(context StoryContext) []string {
	keys := make([]string, 0, len(context.Documents))
	for _, document := range context.Documents {
		keys = append(keys, document.Ref.Key())
	}
	return keys
}

const rewriteChapter2Task = `{"chapter_ids":["chapter-2"],"base_revision":3,"resolution_proposal_id":"p-1","reason":"底线改变"}`

func TestBuildStoryContextSelectsDocumentsByOperation(t *testing.T) {
	allStructural := []string{
		"canon:hero-bottom-line", "canon:hero-location", "canon:villain-goal",
		"entity:ferry", "entity:hero", "entity:villain",
		"plan:arc-1", "plan:chapter-plan-1", "plan:chapter-plan-2", "plan:chapter-plan-3", "plan:volume-1",
	}
	withManuscript := func(chapters ...string) []string {
		keys := append([]string(nil), allStructural...)
		for _, chapter := range chapters {
			keys = slices.Insert(keys, 6, "manuscript:"+chapter)
		}
		slices.Sort(keys)
		return keys
	}
	cases := []struct {
		name string
		kind model.OperationKind
		task string
		edit func(*ProjectContent)
		want []string
	}{
		{
			// 直接依赖（正文→计划节点）、传递依赖（章→弧→卷、计划→事实→实体）、实体引用（事实 depends_on）。
			name: "rewrite_affected follows the dependency closure",
			kind: model.OperationRewriteAffected, task: rewriteChapter2Task,
			want: []string{"canon:hero-location", "entity:ferry", "entity:hero", "manuscript:chapter-2", "plan:arc-1", "plan:chapter-plan-2", "plan:volume-1"},
		},
		{
			// 审阅范围：正文闭包加 Canon 作用域（本章来源事实、世界规则），不带无关状态事实。
			name: "review_range adds canon scope of the range",
			kind: model.OperationReviewRange,
			task: `{"chapter_ids":["chapter-1"],"basis":{"documents":[{"ref":{"kind":"manuscript","id":"chapter-1"},"revision":2}]}}`,
			want: []string{"canon:hero-bottom-line", "canon:villain-goal", "entity:hero", "entity:villain", "manuscript:chapter-1", "plan:arc-1", "plan:chapter-plan-1", "plan:volume-1"},
		},
		{
			name: "write_chapter includes previous chapter manuscript only",
			kind: model.OperationWriteChapter, task: `{"chapter_plan_id":"chapter-plan-3","chapter_number":3}`,
			want: withManuscript("chapter-2"),
		},
		{
			// 上一章没有正文时回退到更早的一章。
			name: "write_chapter skips unwritten previous chapter",
			kind: model.OperationWriteChapter, task: `{"chapter_plan_id":"chapter-plan-3","chapter_number":3}`,
			edit: func(content *ProjectContent) { content.Manuscript = content.Manuscript[:1] },
			want: withManuscript("chapter-1"),
		},
		{
			name: "write_chapter of the first chapter has no previous manuscript",
			kind: model.OperationWriteChapter, task: `{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`,
			want: withManuscript(),
		},
		{
			name: "rewrite_chapter includes own and previous manuscript",
			kind: model.OperationRewriteChapter,
			task: `{"chapter_id":"chapter-2","chapter_plan_id":"chapter-plan-2","chapter_number":2,"findings":["节奏拖沓"]}`,
			want: withManuscript("chapter-1", "chapter-2"),
		},
		{
			name: "revise_canon includes the verified chapter manuscript",
			kind: model.OperationReviseCanon, task: `{"chapter_id":"chapter-1","reason":"核验事实"}`,
			want: withManuscript("chapter-1"),
		},
		{
			name: "develop_plan includes structural documents only",
			kind: model.OperationDevelopPlan, task: `{"intent":"凡人远行","target_chapters":3,"requested_chapters":3}`,
			want: withManuscript(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := contextFixture()
			if tc.edit != nil {
				tc.edit(&content)
			}
			context, err := BuildStoryContext(content, tc.kind, json.RawMessage(tc.task))
			if err != nil {
				t.Fatalf("build context: %v", err)
			}
			if got := documentKeys(context); !slices.Equal(got, tc.want) {
				t.Fatalf("documents = %v, want %v", got, tc.want)
			}
			if context.SchemaVersion != "story_context.v2" || context.ProjectID != "book-1" || context.Revision != 3 {
				t.Fatalf("context header = %q %q %d", context.SchemaVersion, context.ProjectID, context.Revision)
			}
		})
	}
}

func TestBuildStoryContextIndexesEveryChapterAndEncodesDocuments(t *testing.T) {
	context, err := BuildStoryContext(contextFixture(), model.OperationRewriteAffected, json.RawMessage(rewriteChapter2Task))
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	wantIndex := []ChapterIndexEntry{
		{ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "第一章", Blocks: 1},
		{ID: "chapter-2", PlanNodeID: "chapter-plan-2", Number: 2, Title: "第二章", Blocks: 2},
	}
	if !slices.Equal(context.ManuscriptIndex, wantIndex) {
		t.Fatalf("manuscript index = %#v", context.ManuscriptIndex)
	}
	var chapter model.ManuscriptChapter
	if err := json.Unmarshal(context.Documents[3].Content, &chapter); err != nil || chapter.ID != "chapter-2" || len(chapter.Blocks) != 2 {
		t.Fatalf("manuscript document = %s (%v)", context.Documents[3].Content, err)
	}
}

func TestBuildStoryContextIncludesLockedAndGuidedOwnershipTargets(t *testing.T) {
	content := contextFixture()
	content.Ownership = []model.OwnershipRule{
		{Target: model.DocumentRef{Kind: model.DocumentManuscript, ID: "chapter-1"}, Control: model.ControlOpen},
		{Target: model.DocumentRef{Kind: model.DocumentEntity, ID: "villain"}, Control: model.ControlGuided, Guidance: []string{"反派不能脸谱化"}},
		{Target: model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-bottom-line"}, Control: model.ControlLocked},
	}
	context, err := BuildStoryContext(content, model.OperationRewriteAffected, json.RawMessage(rewriteChapter2Task))
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	// locked/guided 目标进入上下文，open 目标不进；规则本身全部保留并按目标键排序。
	want := []string{"canon:hero-bottom-line", "canon:hero-location", "entity:ferry", "entity:hero", "entity:villain", "manuscript:chapter-2", "plan:arc-1", "plan:chapter-plan-2", "plan:volume-1"}
	if got := documentKeys(context); !slices.Equal(got, want) {
		t.Fatalf("documents = %v, want %v", got, want)
	}
	if len(context.Ownership) != 3 || context.Ownership[0].Target.Key() != "canon:hero-bottom-line" ||
		context.Ownership[1].Target.Key() != "entity:villain" || context.Ownership[2].Target.Key() != "manuscript:chapter-1" {
		t.Fatalf("ownership = %#v", context.Ownership)
	}
	if content.Ownership[0].Control != model.ControlOpen {
		t.Fatalf("input ownership order was mutated: %#v", content.Ownership)
	}
}

func TestBuildStoryContextSkipsIntentDependencyWithoutDocument(t *testing.T) {
	content := contextFixture()
	content.Plan[0].DependsOn = []model.DocumentRef{{Kind: model.DocumentIntent, ID: model.SingletonDocumentID}}
	context, err := BuildStoryContext(content, model.OperationRewriteAffected, json.RawMessage(rewriteChapter2Task))
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	for _, key := range documentKeys(context) {
		if strings.HasPrefix(key, "intent:") {
			t.Fatalf("intent entered documents: %v", documentKeys(context))
		}
	}
	if context.Intent.Premise != "凡人远行" {
		t.Fatalf("intent = %#v", context.Intent)
	}
}

func TestBuildStoryContextRejectsMissingDependencies(t *testing.T) {
	cases := []struct {
		name string
		edit func(*ProjectContent)
		kind model.OperationKind
		task string
		want string
	}{
		{
			name: "plan node depends on an absent canon fact",
			edit: func(content *ProjectContent) {
				content.Plan[3].DependsOn = []model.DocumentRef{{Kind: model.DocumentCanon, ID: "missing"}}
			},
			kind: model.OperationRewriteAffected, task: rewriteChapter2Task,
			want: `context dependency "canon:missing" does not exist`,
		},
		{
			name: "canon subject entity is absent",
			edit: func(content *ProjectContent) { content.Entities = content.Entities[1:] },
			kind: model.OperationRewriteAffected, task: rewriteChapter2Task,
			want: `context dependency "entity:hero" does not exist`,
		},
		{
			name: "task references an absent manuscript",
			kind: model.OperationRewriteAffected,
			task: `{"chapter_ids":["chapter-9"],"base_revision":3,"resolution_proposal_id":"p-1","reason":"底线改变"}`,
			want: `context dependency "manuscript:chapter-9" does not exist`,
		},
		{
			name: "locked ownership targets an absent document",
			edit: func(content *ProjectContent) {
				content.Ownership = []model.OwnershipRule{{Target: model.DocumentRef{Kind: model.DocumentPlan, ID: "ending"}, Control: model.ControlLocked}}
			},
			kind: model.OperationDevelopPlan, task: `{"intent":"凡人远行","target_chapters":3,"requested_chapters":3}`,
			want: `context dependency "plan:ending" does not exist`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := contextFixture()
			if tc.edit != nil {
				tc.edit(&content)
			}
			_, err := BuildStoryContext(content, tc.kind, json.RawMessage(tc.task))
			if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
}

func TestBuildStoryContextRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		edit func(*ProjectContent)
		kind model.OperationKind
		task string
		want string
	}{
		{name: "missing project id", edit: func(c *ProjectContent) { c.ID = " " }, kind: model.OperationDevelopPlan, task: `{"intent":"x","target_chapters":1,"requested_chapters":1}`, want: "positive revision and task are required"},
		{name: "initial revision", edit: func(c *ProjectContent) { c.Revision = 0 }, kind: model.OperationDevelopPlan, task: `{"intent":"x","target_chapters":1,"requested_chapters":1}`, want: "positive revision and task are required"},
		{name: "empty task", kind: model.OperationDevelopPlan, task: ``, want: "positive revision and task are required"},
		{name: "malformed task", kind: model.OperationDevelopPlan, task: `{"intent":`, want: "positive revision and task are required"},
		{name: "invalid intent", edit: func(c *ProjectContent) { c.Intent.Premise = "" }, kind: model.OperationDevelopPlan, task: `{"intent":"x","target_chapters":1,"requested_chapters":1}`, want: "intent premise is required"},
		{name: "invalid task input", kind: model.OperationWriteChapter, task: `{"chapter_plan_id":"chapter-plan-1","chapter_number":0}`, want: "write_chapter task input"},
		{name: "invalid ownership rule", edit: func(c *ProjectContent) {
			c.Ownership = []model.OwnershipRule{{Target: model.DocumentRef{Kind: model.DocumentEntity, ID: "hero"}, Control: model.ControlGuided}}
		}, kind: model.OperationDevelopPlan, task: `{"intent":"x","target_chapters":1,"requested_chapters":1}`, want: "guided ownership requires guidance"},
		{name: "operation without context contract", kind: model.OperationGenerateAsset, task: `{"role":"cover","target":{"kind":"intent","id":"root"},"basis":{"documents":[{"ref":{"kind":"intent","id":"root"},"revision":1}]}}`, want: "no story context contract"},
		{name: "unknown operation kind", kind: "paint", task: `{}`, want: `unknown operation kind "paint"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := contextFixture()
			if tc.edit != nil {
				tc.edit(&content)
			}
			_, err := BuildStoryContext(content, tc.kind, json.RawMessage(tc.task))
			if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
}

func TestContextKeyIsCanonicalAndBoundToKindAndVersion(t *testing.T) {
	key := func(kind model.OperationKind, task string) string {
		t.Helper()
		value, err := ContextKey(kind, json.RawMessage(task))
		if err != nil {
			t.Fatalf("context key for %s %s: %v", kind, task, err)
		}
		return value
	}
	base := key(model.OperationWriteChapter, `{"chapter_plan_id":"c","chapter_number":9007199254740993,"directives":[]}`)
	if base != key(model.OperationWriteChapter, "\n{ \"directives\" : [ ],\n \"chapter_number\":9007199254740993, \"chapter_plan_id\":\"c\" }") {
		t.Fatal("key order and whitespace changed the context key")
	}
	if base == key(model.OperationWriteChapter, `{"chapter_plan_id":"c","chapter_number":9007199254740992,"directives":[]}`) {
		t.Fatal("large integers lost precision in the context key")
	}
	if base == key(model.OperationRewriteChapter, `{"chapter_plan_id":"c","chapter_number":9007199254740993,"directives":[]}`) {
		t.Fatal("operation kind is not part of the context key")
	}
	if StoryContextKind != "story_context.v2" {
		t.Fatalf("schema version = %q", StoryContextKind)
	}
	for name, invalid := range map[string]struct {
		kind model.OperationKind
		task string
	}{
		"empty kind":     {"", `{}`},
		"empty task":     {model.OperationWriteChapter, ``},
		"malformed task": {model.OperationWriteChapter, `{"a":`},
		"two values":     {model.OperationWriteChapter, `{} {}`},
	} {
		if _, err := ContextKey(invalid.kind, json.RawMessage(invalid.task)); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
}
