package workbench

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 进行中环节要点名正在处理的章：审阅与重写的都是已入稿章节，不能笼统成种类文案。
func TestOperationPhaseNamesChaptersInFlight(t *testing.T) {
	project := projectdoc.Snapshot{Manuscript: []model.ManuscriptChapter{
		{ID: "c1", PlanNodeID: "p1", Number: 1}, {ID: "c2", PlanNodeID: "p2", Number: 2}, {ID: "c3", PlanNodeID: "p3", Number: 3},
	}}
	const basis = `"basis":{"documents":[{"ref":{"kind":"manuscript","id":"c1"},"revision":3}]}`
	for _, tc := range []struct {
		kind               model.OperationKind
		input, phase, plan string
	}{
		{model.OperationReviewRange, `{"chapter_ids":["c1","c2","c3"],` + basis + `}`, "正在审阅第 1–3 章", ""},
		{model.OperationReviewRange, `{"chapter_ids":["c2"],` + basis + `}`, "正在审阅第 2 章", ""},
		{model.OperationReviewRange, `{"chapter_ids":["c1","c2","c3"],"reviewed":["c1","c3"],` + basis + `}`, "正在复审第 1–3 章", ""},
		{model.OperationRewriteChapter, `{"chapter_id":"c1","chapter_plan_id":"p1","chapter_number":1,"findings":["称呼混用"]}`, "正在按意见重写第 1 章", "p1"},
	} {
		if phase, plan := operationPhase(model.Operation{Kind: tc.kind, Input: []byte(tc.input)}, project); phase != tc.phase || plan != tc.plan {
			t.Errorf("%s: phase=%q plan=%q, want %q %q", tc.kind, phase, plan, tc.phase, tc.plan)
		}
	}
}

// 待确认方案用故事语言呈现并投进大纲：规模摘要、按卷弧章成树的大纲、人物名、设定句，
// 全程不露文档 ID；未过期的方案在大纲里标为 Proposed，过期的不投。
func TestPendingProposalReadsAsStoryAndPreviewsInOutline(t *testing.T) {
	put := func(kind model.DocumentKind, id string, value any) model.Patch {
		content, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return model.Patch{Document: model.DocumentRef{Kind: kind, ID: id}, Operation: model.PatchPut, Content: content}
	}
	volume := model.PlanNode{ID: "plan-volume-1", Kind: model.PlanVolume, Order: 1, Title: "雨城", Summary: "旧案重启"}
	arc := model.PlanNode{ID: "plan-arc-1", Kind: model.PlanArc, ParentID: volume.ID, Order: 1, Title: "来信", Summary: "无名信"}
	chapter := model.PlanNode{ID: "plan-chapter-1", Kind: model.PlanChapter, ParentID: arc.ID, Order: 1, Title: "来信", Summary: "陈渡收到信"}
	project := projectdoc.Snapshot{
		Plan:       []model.PlanNode{volume, arc, chapter},
		Entities:   []model.Entity{{ID: "entity-hero", Kind: model.EntityCharacter, Name: "陈渡"}},
		Manuscript: []model.ManuscriptChapter{{ID: "chapter-1", PlanNodeID: chapter.ID, Number: 1, Title: "来信"}},
	}
	revised := chapter
	revised.Summary = "陈渡收到信，认出笔迹"
	newArc := model.PlanNode{ID: "plan-arc-2", Kind: model.PlanArc, ParentID: volume.ID, Order: 2, Title: "对峙", Summary: "门后的人"}
	proposal := model.Proposal{Patches: []model.Patch{
		put(model.DocumentPlan, revised.ID, revised),
		put(model.DocumentPlan, newArc.ID, newArc),
		put(model.DocumentPlan, "plan-chapter-2", model.PlanNode{ID: "plan-chapter-2", Kind: model.PlanChapter, ParentID: newArc.ID, Order: 2, Title: "门后", Summary: "声音"}),
		put(model.DocumentPlan, "plan-chapter-3", model.PlanNode{ID: "plan-chapter-3", Kind: model.PlanChapter, ParentID: newArc.ID, Order: 3, Title: "旧友", Summary: "相认"}),
		put(model.DocumentEntity, "entity-su", model.Entity{ID: "entity-su", Kind: model.EntityCharacter, Name: "苏晚", Aliases: []string{"晚晚"}}),
		put(model.DocumentCanon, "fact-su", model.CanonFact{ID: "fact-su", SubjectID: "entity-su", Predicate: "身份", Value: json.RawMessage(`"陈渡的旧友"`)}),
		put(model.DocumentCompass, model.SingletonDocumentID, model.Compass{ScaleMax: 60, Ending: "真相大白"}),
		put(model.DocumentOverlay, model.SingletonDocumentID, map[string]string{}),
	}}

	view, err := proposalView(project, proposal)
	if err != nil {
		t.Fatal(err)
	}
	wantSummary := []string{"新增 1 个故事弧 · 2 章", "修订大纲 1 处", "新增人物地点 1 个", "设定 1 条", "其他变更 1 项"}
	if !slices.Equal(view.Summary, wantSummary) {
		t.Fatalf("summary = %q, want %q", view.Summary, wantSummary)
	}
	if len(view.Outline) != 1 || len(view.Outline[0].Arcs) != 2 || view.Outline[0].Arcs[0].Chapters[0].Summary != revised.Summary ||
		len(view.Outline[0].Arcs[1].Chapters) != 2 || view.Outline[0].Arcs[1].Chapters[1].Chapter != 3 {
		t.Fatalf("outline tree = %+v", view.Outline)
	}
	if len(view.Entities) != 1 || view.Entities[0].Name != "苏晚" || view.Facts[0] != "「苏晚」身份：陈渡的旧友" ||
		view.Compass == nil || view.Compass.ScaleMax != 60 || !slices.Equal(view.Other, []string{"书级创作规则"}) {
		t.Fatalf("view = %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"plan-", "entity-", "fact-", "overlay"} {
		if strings.Contains(string(encoded), id) {
			t.Fatalf("view leaks document id %q: %s", id, encoded)
		}
	}

	decision := &PendingDecision{Proposal: proposal, HasProposal: true}
	plan, proposed, err := proposedPlan(project.Plan, decision)
	if err != nil {
		t.Fatal(err)
	}
	outline := buildOutline(plan, proposed, project.Manuscript, nil, "")
	var got []string
	for _, entry := range outline {
		got = append(got, fmt.Sprintf("%s#%d:%s:%t:%s", entry.Node.Title, entry.Number, entry.State, entry.Proposed, entry.Detail))
	}
	want := []string{
		"雨城#0::false:", "来信#0::false:", "来信#1:confirmed:true:",
		"对峙#0::true:", "门后#2:planned:true:方案待你确认", "旧友#3:planned:true:方案待你确认",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("outline = %q\nwant %q", got, want)
	}
	decision.Stale = true
	if plan, proposed, err = proposedPlan(project.Plan, decision); err != nil || len(plan) != 3 || len(proposed) != 0 {
		t.Fatalf("stale proposal must not project: plan=%d proposed=%d err=%v", len(plan), len(proposed), err)
	}
}

// 一章的依据：覆盖它的要求（意图、全书与本章要求）带着当前裁定的结论，没审过的章没有
// 结论；这一章记下的设定用故事语言，正文改过的标待核验。
func TestChapterBriefsCarryCoveringRequirementsAndChecks(t *testing.T) {
	plan := []model.PlanNode{{ID: "v", Kind: model.PlanVolume, Order: 1, Title: "雨城"}}
	for i := 1; i <= 3; i++ {
		plan = append(plan, model.PlanNode{ID: fmt.Sprint("p", i), Kind: model.PlanChapter, ParentID: "v", Order: i, Title: fmt.Sprint("第", i, "章")})
	}
	project := projectdoc.Snapshot{
		Plan:     plan,
		Intent:   model.Intent{Required: []string{"保持悬疑"}, Forbidden: []string{"主角死亡"}},
		Entities: []model.Entity{{ID: "hero", Kind: model.EntityCharacter, Name: "陈渡"}},
		Directives: []model.Directive{
			{ID: "d1", Scope: model.DirectiveScopeProject, Text: "每章结尾留钩子", Status: model.DirectiveActive},
			{ID: "d2", Scope: model.DirectiveScopeChapters(2, 2), Text: "门后的人不露脸", Status: model.DirectiveActive},
		},
		Manuscript: []model.ManuscriptChapter{{ID: "c1", PlanNodeID: "p1", Number: 1}, {ID: "c2", PlanNodeID: "p2", Number: 2}},
		Canon: []model.CanonFact{
			{ID: "f1", SubjectID: "hero", Predicate: "职业", Value: json.RawMessage(`"邮差"`), SourceChapterID: "c1"},
			{ID: "f2", SubjectID: "hero", Predicate: "持有", Value: json.RawMessage(`"一把陌生的钥匙"`), SourceChapterID: "c2"},
		},
		// 第 2 章正文在 f2 入账之后改过：f2 待核验。
		Index: projectdoc.DocumentIndex{
			model.DocumentRef{Kind: model.DocumentManuscript, ID: "c2"}.Key(): {Revision: 5},
			model.DocumentRef{Kind: model.DocumentCanon, ID: "f2"}.Key():      {Revision: 3},
		},
	}
	window := &novel.StoredVerdict{Key: "review-1", Verdict: model.ReviewVerdict{ChapterIDs: []string{"c1", "c2"}, Checks: []model.RequirementCheck{
		{ID: "intent:required:0", Status: model.CheckSatisfied},
		{ID: "directive:d1", Status: model.CheckPending},
		{ID: "directive:d2", Status: model.CheckViolated},
	}}}
	briefs := chapterBriefs(project, 0, map[string]*novel.StoredVerdict{"c1": window, "c2": window})

	line := func(r BriefRequirement) string {
		return fmt.Sprintf("%s|%s|%t|%s", r.Text, r.Scope, r.Forbidden, r.Status)
	}
	var second, third []string
	for _, r := range briefs[2].Requirements {
		second = append(second, line(r))
	}
	for _, r := range briefs[3].Requirements {
		third = append(third, line(r))
	}
	if want := []string{"保持悬疑||false|satisfied", "主角死亡||true|", "每章结尾留钩子||false|pending", "门后的人不露脸|第 2 章|false|violated"}; !slices.Equal(second, want) {
		t.Fatalf("chapter 2 requirements = %q, want %q", second, want)
	}
	if want := []string{"保持悬疑||false|", "主角死亡||true|", "每章结尾留钩子||false|"}; !slices.Equal(third, want) {
		t.Fatalf("an unreviewed chapter has no checks and only its covering requirements: %q", third)
	}
	if facts := briefs[2].Facts; len(facts) != 1 || facts[0].Text != "「陈渡」持有（第 2 章）：一把陌生的钥匙" || !facts[0].Pending ||
		len(briefs[1].Facts) != 1 || briefs[1].Facts[0].Pending {
		t.Fatalf("facts = %+v / %+v", briefs[1].Facts, briefs[2].Facts)
	}
}
