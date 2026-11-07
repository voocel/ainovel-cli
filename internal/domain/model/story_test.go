package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
)

func TestValidateDocumentContentRejectsUnknownFields(t *testing.T) {
	content := json.RawMessage(`{"premise":"凡人修仙","surprise":"silent drift"}`)
	err := ValidateDocumentContent(DocumentRef{Kind: DocumentIntent, ID: "root"}, content)
	if err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestValidateDocumentContentRequiresStableID(t *testing.T) {
	content := json.RawMessage(`{"id":"arc-2","kind":"arc","parent_id":"volume-1","order":1,"title":"入门","summary":"进入宗门"}`)
	err := ValidateDocumentContent(DocumentRef{Kind: DocumentPlan, ID: "arc-1"}, content)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestManuscriptRejectsDuplicateBlockID(t *testing.T) {
	chapter := ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Author: AuthorAI,
		Blocks: []ManuscriptBlock{{ID: "p-1", Text: "第一段"}, {ID: "p-1", Text: "第二段"}},
	}
	if err := chapter.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestDocumentDependenciesIncludeStableParentAndPlan(t *testing.T) {
	planContent := json.RawMessage(`{
		"id":"chapter-plan-1",
		"kind":"chapter","turn":"转折",
		"parent_id":"arc-1",
		"order":1,
		"title":"山门",
		"summary":"抵达山门",
		"depends_on":[{"kind":"canon","id":"hero-origin"}]
	}`)
	dependencies, err := DocumentDependencies(DocumentRef{Kind: DocumentPlan, ID: "chapter-plan-1"}, planContent)
	if err != nil {
		t.Fatalf("dependencies: %v", err)
	}
	keys := make([]string, len(dependencies))
	for i, dependency := range dependencies {
		keys[i] = dependency.Key()
	}
	if want := []string{"canon:hero-origin", "plan:arc-1"}; !slices.Equal(keys, want) {
		t.Fatalf("dependencies = %v, want %v", keys, want)
	}
}

// 蓝图不变量（§6.3 D63/D67）：规划任务至少追加一章（或以现有章数收官），固定篇幅不超过
// 全书章数；AI 定篇幅时罗盘必填，收官后按收官章数封顶，未收官不超过篇幅上限（D96）；
// 罗盘只能由规划任务修改。
func TestValidateBlueprint(t *testing.T) {
	chapters := func(ids ...int) []PlanNode {
		var nodes []PlanNode
		for _, n := range ids {
			nodes = append(nodes, PlanNode{ID: fmt.Sprintf("chapter-%d", n), Kind: PlanChapter, Turn: "转折", ParentID: "arc-1", Order: n, Title: "章", Summary: "推进"})
		}
		return nodes
	}
	put := func(t *testing.T, ref DocumentRef, value any) Patch {
		t.Helper()
		content, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return Patch{Document: ref, Operation: PatchPut, Content: content}
	}
	plans := func(t *testing.T, ids ...int) []Patch {
		var patches []Patch
		for _, node := range chapters(ids...) {
			patches = append(patches, put(t, DocumentRef{Kind: DocumentPlan, ID: node.ID}, node))
		}
		return patches
	}
	compass := func(t *testing.T, scaleMax, final int) Patch {
		return put(t, DocumentRef{Kind: DocumentCompass, ID: SingletonDocumentID}, Compass{ScaleMax: scaleMax, Ending: "称帝", Final: final})
	}
	extend := func(t *testing.T, fixed, existing int) Operation {
		input, err := json.Marshal(RevisePlanInput{FixedChapters: fixed, ExistingChapters: existing})
		if err != nil {
			t.Fatal(err)
		}
		return Operation{Kind: OperationRevisePlan, Input: input}
	}
	develop := func(t *testing.T, fixed int) Operation {
		input, err := json.Marshal(DevelopPlanInput{FixedChapters: fixed})
		if err != nil {
			t.Fatal(err)
		}
		return Operation{Kind: OperationDevelopPlan, Input: input}
	}
	blueprint := func(compass *Compass, nodes ...PlanNode) story {
		s := story{plans: make(map[string]PlanNode), compass: compass}
		for _, node := range nodes {
			s.plans[node.ID] = node
		}
		return s
	}
	upTo := func(n int) []PlanNode {
		ids := make([]int, n)
		for i := range ids {
			ids[i] = i + 1
		}
		return chapters(ids...)
	}
	base := blueprint(&Compass{Ending: "称帝"}, chapters(1, 2, 3)...)
	nearCap := blueprint(&Compass{Ending: "称帝"}, upTo(CompassAutonomyCeiling-1)...)
	atCap := blueprint(&Compass{Ending: "称帝"}, upTo(CompassAutonomyCeiling)...)
	unbounded := blueprint(nil, chapters(1, 2, 3)...)
	open := func(t *testing.T) Operation { return extend(t, 0, 3) }
	nearCapOp := func(t *testing.T) Operation { return extend(t, 0, CompassAutonomyCeiling-1) }
	atCapOp := func(t *testing.T) Operation { return extend(t, 0, CompassAutonomyCeiling) }
	fixedSix := func(t *testing.T) Operation { return extend(t, 6, 3) }
	chapterOp, _ := json.Marshal(WriteChapterInput{ChapterPlanID: "chapter-1"})
	for _, c := range []struct {
		name      string
		operation func(*testing.T) Operation
		base      story
		patches   func(*testing.T) []Patch
		ok        bool
	}{
		// 章数由规划者决定（D67）：边界只守进展与上界。
		{"open phase appends as many as it sees fit", open, base,
			func(t *testing.T) []Patch { return plans(t, 4, 5, 6, 7, 8, 9) }, true},
		{"open phase may append a single chapter", open, base,
			func(t *testing.T) []Patch { return plans(t, 4) }, true},
		{"open phase must append at least one chapter", open, base,
			func(t *testing.T) []Patch { return plans(t, 3) }, false},
		// 篇幅不预计（D96）：未收官的蓝图只受护栏约束，触及时收官或申请更高的上限。
		{"an unfinished plan may reach the guardrail", nearCapOp, nearCap,
			func(t *testing.T) []Patch { return plans(t, CompassAutonomyCeiling) }, true},
		{"an unfinished plan cannot pass the guardrail", nearCapOp, nearCap,
			func(t *testing.T) []Patch { return plans(t, CompassAutonomyCeiling, CompassAutonomyCeiling+1) }, false},
		{"a higher limit lets the plan pass the guardrail", nearCapOp, nearCap,
			func(t *testing.T) []Patch {
				return append(plans(t, CompassAutonomyCeiling, CompassAutonomyCeiling+1), compass(t, CompassAutonomyCeiling+100, 0))
			}, true},
		{"at the guardrail the plan closes with a final", atCapOp, atCap,
			func(t *testing.T) []Patch { return []Patch{compass(t, 0, CompassAutonomyCeiling)} }, true},
		{"at the guardrail an unfinished plan cannot grow", atCapOp, atCap,
			func(t *testing.T) []Patch { return plans(t, CompassAutonomyCeiling+1) }, false},
		{"final may roll below it", open, base,
			func(t *testing.T) []Patch { return append(plans(t, 4, 5), compass(t, 0, 8)) }, true},
		{"final may close on the existing chapters", open, base,
			func(t *testing.T) []Patch { return []Patch{compass(t, 0, 3)} }, true},
		{"final cannot be passed", open, base,
			func(t *testing.T) []Patch { return append(plans(t, 4, 5, 6), compass(t, 0, 5)) }, false},
		{"final ahead still needs progress", open, base,
			func(t *testing.T) []Patch { return []Patch{compass(t, 0, 5)} }, false},
		{"final below the planned chapters is rejected", open, base,
			func(t *testing.T) []Patch { return []Patch{compass(t, 0, 2)} }, false},
		{"first plan may declare a final beyond its chapters", func(t *testing.T) Operation { return develop(t, 0) }, blueprint(nil),
			func(t *testing.T) []Patch { return append(plans(t, 1, 2), compass(t, 0, 5)) }, true},
		{"AI length requires a compass", open, unbounded,
			func(t *testing.T) []Patch { return plans(t, 4, 5, 6) }, false},
		{"fixed length allows any count up to it", fixedSix, unbounded,
			func(t *testing.T) []Patch { return plans(t, 4) }, true},
		{"fixed length may be laid out at once", fixedSix, unbounded,
			func(t *testing.T) []Patch { return plans(t, 4, 5, 6) }, true},
		{"fixed length cannot be passed", fixedSix, unbounded,
			func(t *testing.T) []Patch { return plans(t, 4, 5, 6, 7) }, false},
		{"fixed length must append a chapter", fixedSix, unbounded,
			func(t *testing.T) []Patch { return plans(t, 3) }, false},
		{"only planning tasks may change the compass", func(*testing.T) Operation { return Operation{Kind: OperationWriteChapter, Input: chapterOp} }, base,
			func(t *testing.T) []Patch { return []Patch{compass(t, 0, 3)} }, false},
		{"non-planning tasks cannot add chapter nodes", func(*testing.T) Operation { return Operation{Kind: OperationWriteChapter, Input: chapterOp} }, base,
			func(t *testing.T) []Patch { return plans(t, 4) }, false},
		{"non-planning tasks may edit existing nodes", func(*testing.T) Operation { return Operation{Kind: OperationWriteChapter, Input: chapterOp} }, base,
			func(t *testing.T) []Patch { return plans(t, 3) }, true},
		{"fixed length leaves the compass to the user", fixedSix, base,
			func(t *testing.T) []Patch { return append(plans(t, 4, 5, 6), compass(t, 6, 6)) }, false},
		// 终局方向总由规划写（D70）：两种篇幅下罗盘都可以只有终局（D96）。
		{"fixed length writes an ending-only compass", fixedSix, unbounded,
			func(t *testing.T) []Patch { return append(plans(t, 4), compass(t, 0, 0)) }, true},
		{"AI length accepts an ending-only compass", open, unbounded,
			func(t *testing.T) []Patch { return append(plans(t, 4, 5), compass(t, 0, 0)) }, true},
		{"planning may not remove chapter nodes", open, base,
			func(t *testing.T) []Patch {
				return []Patch{{Document: DocumentRef{Kind: DocumentPlan, ID: "chapter-3"}, Operation: PatchDelete}, compass(t, 0, 5)}
			}, false},
		{"deleting the compass keeps a node named root", open,
			blueprint(&Compass{Ending: "称帝"}, append(chapters(1, 2), PlanNode{ID: "root", Kind: PlanChapter, Turn: "转折", ParentID: "arc-1", Order: 3, Title: "章", Summary: "推进"})...),
			func(t *testing.T) []Patch {
				return append(plans(t, 4, 5, 6), Patch{Document: DocumentRef{Kind: DocumentCompass, ID: SingletonDocumentID}, Operation: PatchDelete}, compass(t, 0, 0))
			}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			patches := c.patches(t)
			projected := blueprint(c.base.compass, slices.Collect(maps.Values(c.base.plans))...)
			for _, patch := range patches {
				switch {
				case patch.Document.Kind == DocumentCompass && patch.Operation == PatchDelete:
					projected.compass = nil
				case patch.Operation == PatchDelete:
					delete(projected.plans, patch.Document.ID)
				case patch.Document.Kind == DocumentCompass:
					projected.compass = new(Compass)
					if err := json.Unmarshal(patch.Content, projected.compass); err != nil {
						t.Fatal(err)
					}
				default:
					var node PlanNode
					if err := json.Unmarshal(patch.Content, &node); err != nil {
						t.Fatal(err)
					}
					projected.plans[node.ID] = node
				}
			}
			operation := c.operation(t)
			input, err := DecodeTaskInput(operation.Kind, operation.Input)
			if err != nil {
				t.Fatal(err)
			}
			err = validateBlueprint(input, c.base, projected, patches)
			if c.ok && err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if !c.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// 规划输入不带章数（D67）：扩窗要有已写基础，固定篇幅时还得留有余地。
func TestPlanInputsValidateRequest(t *testing.T) {
	for _, c := range []struct {
		input TaskInput
		ok    bool
	}{
		{DevelopPlanInput{}, true},
		{DevelopPlanInput{FixedChapters: 2}, true},
		{DevelopPlanInput{FixedChapters: -1}, false},
		{RevisePlanInput{ExistingChapters: 3}, true},
		{RevisePlanInput{FixedChapters: 5, ExistingChapters: 3}, true},
		{RevisePlanInput{}, false},
		{RevisePlanInput{FixedChapters: 3, ExistingChapters: 3}, false},
	} {
		if err := c.input.Validate(); (err == nil) != c.ok {
			t.Fatalf("%+v validate = %v, want ok %v", c.input, err, c.ok)
		}
	}
}

func TestCompassValidateAndNeverDependency(t *testing.T) {
	for _, c := range []struct {
		compass Compass
		ok      bool
	}{
		// 收官承诺不超过篇幅上限：护栏，或用户认可的更高上限（D96）。
		{Compass{Ending: "称帝"}, true},
		{Compass{Ending: "称帝", Final: CompassAutonomyCeiling}, true},
		{Compass{Ending: "称帝", Final: CompassAutonomyCeiling + 1}, false},
		{Compass{ScaleMax: 450, Ending: "称帝", Final: 450}, true},
		{Compass{ScaleMax: 450, Ending: "称帝", Final: 451}, false},
		{Compass{ScaleMax: -1, Ending: "称帝"}, false},
		{Compass{ScaleMax: 450}, false},
		{Compass{Ending: "称帝", Final: -1}, false},
		// 长线（D91）：每条有问题，ID 与短名各不相同。
		{Compass{Ending: "称帝", Threads: []StoryThread{{ID: "t1", Name: "王位", Question: "能否称帝"}, {ID: "t2", Name: "旧债", Question: "父仇谁报"}}}, true},
		{Compass{Ending: "称帝", Threads: []StoryThread{{ID: "t1", Name: "王位"}}}, false},
		{Compass{Ending: "称帝", Threads: []StoryThread{{ID: "t1", Question: "能否称帝"}}}, false},
		{Compass{Ending: "称帝", Threads: []StoryThread{{ID: "t1", Name: "王位", Question: "能否称帝"}, {ID: "t2", Name: "王位", Question: "父仇谁报"}}}, false},
		{Compass{Ending: "称帝", Threads: []StoryThread{{ID: "t1", Name: "王位", Question: "能否称帝"}, {ID: "t1", Name: "旧债", Question: "父仇谁报"}}}, false},
	} {
		if err := c.compass.Validate(); (err == nil) != c.ok {
			t.Fatalf("%+v validate = %v, want ok %v", c.compass, err, c.ok)
		}
	}
	node := PlanNode{ID: "chapter-1", Kind: PlanChapter, Turn: "转折", ParentID: "arc-1", Title: "章", Summary: "推进",
		DependsOn: []DocumentRef{{Kind: DocumentCompass, ID: SingletonDocumentID}}}
	if err := node.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("compass dependency error = %v, want ErrInvalid", err)
	}
}

// 转折（D92）是章节计划的一部分：章必有，卷、弧没有。
func TestOnlyChapterPlansCarryATurn(t *testing.T) {
	for _, c := range []struct {
		node PlanNode
		ok   bool
	}{
		{PlanNode{ID: "c", Kind: PlanChapter, ParentID: "a", Title: "章", Summary: "推进", Turn: "钥匙开了门"}, true},
		{PlanNode{ID: "c", Kind: PlanChapter, ParentID: "a", Title: "章", Summary: "推进", Turn: " "}, false},
		{PlanNode{ID: "a", Kind: PlanArc, ParentID: "v", Title: "弧", Summary: "推进"}, true},
		{PlanNode{ID: "a", Kind: PlanArc, ParentID: "v", Title: "弧", Summary: "推进", Turn: "钥匙开了门"}, false},
	} {
		if err := c.node.Validate(); (err == nil) != c.ok {
			t.Fatalf("%+v validate = %v, want ok %v", c.node, err, c.ok)
		}
	}
}

func TestChapterPlansInOrderSortsByOrderThenID(t *testing.T) {
	plan := []PlanNode{
		{ID: "c", Kind: PlanChapter, Turn: "转折", Order: 2}, {ID: "arc", Kind: PlanArc},
		{ID: "b", Kind: PlanChapter, Turn: "转折", Order: 1}, {ID: "a", Kind: PlanChapter, Turn: "转折", Order: 2},
	}
	var ids []string
	for _, node := range ChapterPlansInOrder(plan) {
		ids = append(ids, node.ID)
	}
	if !slices.Equal(ids, []string{"b", "a", "c"}) {
		t.Fatalf("order = %v", ids)
	}
}
