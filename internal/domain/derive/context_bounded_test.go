package derive

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// longBook 构造规划 planned 章、写到 written 章的作品：每 10 章一弧；每章一段正文与两条
// 事件；主角的五个状态按键原地演进，另每章沉淀一条新键状态；每章登场一名配角并带一条
// 状态；每 3 章埋一条伏笔，埋下 6 章后回收（来源章随回收移到回收章）。
func longBook(planned, written int) ProjectContent {
	content := ProjectContent{ID: "book-long", Revision: model.Revision(written + 1)}
	content.Plan = append(content.Plan, model.PlanNode{ID: "volume-1", Kind: model.PlanVolume, Title: "卷一", Summary: "长路"})
	content.Entities = append(content.Entities, model.Entity{ID: "hero", Kind: model.EntityCharacter, Name: "主角"})
	fact := func(id string, kind model.CanonFactKind, subject, predicate, value, source string) model.CanonFact {
		return model.CanonFact{ID: id, Kind: kind, SubjectID: subject, Predicate: predicate, Value: json.RawMessage(`"` + value + `"`), SourceChapterID: source}
	}
	for i := 1; i <= planned; i++ {
		arc := fmt.Sprintf("arc-%02d", (i-1)/10+1)
		if (i-1)%10 == 0 {
			content.Plan = append(content.Plan, model.PlanNode{ID: arc, Kind: model.PlanArc, ParentID: "volume-1", Order: i, Title: arc, Summary: "一段旅程"})
		}
		mate := fmt.Sprintf("配角%03d", i)
		content.Plan = append(content.Plan, model.PlanNode{
			ID: planID(i), Kind: model.PlanChapter, Turn: "转折", ParentID: arc, Order: i, Title: fmt.Sprintf("第%d章", i), Summary: "主角遇见" + mate,
		})
		if i > written {
			continue
		}
		chapter := chapterID(i)
		content.Entities = append(content.Entities, model.Entity{ID: "mate-" + chapter, Kind: model.EntityCharacter, Name: mate})
		content.Manuscript = append(content.Manuscript, model.ManuscriptChapter{
			ID: chapter, PlanNodeID: planID(i), Number: i, Author: model.AuthorAI,
			Blocks:    []model.ManuscriptBlock{{ID: "b1", Text: strings.Repeat("字", 1500)}},
			DependsOn: []model.DocumentRef{{Kind: model.DocumentEntity, ID: "mate-" + chapter}},
		})
		content.Canon = append(content.Canon,
			fact("event-a-"+chapter, model.CanonEvent, "hero", "event.travel", "第"+chapter+"章事件甲", chapter),
			fact("event-b-"+chapter, model.CanonEvent, "hero", "event.meet", "第"+chapter+"章事件乙", chapter),
			fact("mate-state-"+chapter, model.CanonState, "mate-"+chapter, "state.first_seen", "初见于"+chapter, chapter),
			// 主角每章沉淀一条新键状态：相关实体的事实本身就会超出预算。
			fact("hero-insight-"+chapter, model.CanonState, "hero", "state.insight_"+chapter, "第"+chapter+"章的领悟", chapter),
		)
		if i%3 == 0 {
			hook := fact("hook-"+chapter, model.CanonForeshadow, "hero", "foreshadow.hook_"+chapter, "埋于"+chapter, chapter)
			if i+6 <= written {
				hook.SourceChapterID, hook.Resolved = chapterID(i+6), true
			}
			content.Canon = append(content.Canon, hook)
		}
	}
	for k := range 5 {
		if source := written - k; source >= 1 {
			content.Canon = append(content.Canon, fact(fmt.Sprintf("hero-state-%d", k), model.CanonState, "hero", fmt.Sprintf("state.aspect_%d", k), "最新", chapterID(source)))
		}
	}
	if written >= 1 {
		// 开篇立下的世界规则与贯穿全书、始终未回收的主线伏笔。
		content.Canon = append(content.Canon,
			fact("rule-origin", model.CanonWorldRule, "hero", "rule.origin", "灵根决定命数", chapterID(1)),
			fact("hook-main", model.CanonForeshadow, "hero", "foreshadow.main_mystery", "身世之谜", chapterID(1)),
		)
	}
	return content
}

func planID(i int) string    { return fmt.Sprintf("plan-%03d", i) }
func chapterID(i int) string { return fmt.Sprintf("ch-%03d", i) }

func writeTask(i int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"chapter_plan_id":%q}`, planID(i)))
}

func keySet(context selection) map[string]bool {
	keys := make(map[string]bool, len(context.Documents))
	for _, document := range context.Documents {
		keys[document.Ref.Key()] = true
	}
	return keys
}

// §6.5 固定段：目标、上一章正文、近 5 章计划与事件、后 3 章计划；更早的历史只计入 omitted。
func TestWriteContextKeepsWindowAndOmitsHistory(t *testing.T) {
	context, err := selectContext(longBook(60, 49), model.OperationWriteChapter, writeTask(50))
	if err != nil {
		t.Fatal(err)
	}
	keys := keySet(context)
	for _, want := range []string{
		"plan:plan-050", "plan:arc-05", "plan:volume-1", "manuscript:ch-049",
		"plan:plan-045", "plan:plan-049", "canon:event-a-ch-045", "canon:event-b-ch-049",
		"plan:plan-051", "plan:plan-053", "canon:hook-ch-045", "canon:hook-ch-048",
		"canon:hero-state-0", "canon:mate-state-ch-049",
	} {
		if !keys[want] {
			t.Fatalf("missing %q", want)
		}
	}
	for _, absent := range []string{
		"manuscript:ch-048", "plan:plan-044", "plan:plan-054", "canon:event-a-ch-044", "canon:hook-ch-003",
	} {
		if keys[absent] {
			t.Fatalf("history %q entered the bounded context", absent)
		}
	}
	omitted := context.Budget.Omitted
	if omitted[model.DocumentManuscript] != 48 || omitted[model.DocumentCanon] == 0 || omitted[model.DocumentPlan] == 0 {
		t.Fatalf("omitted = %v", omitted)
	}
	if context.Budget.Used > context.Budget.Limit {
		t.Fatalf("budget used %d > limit %d", context.Budget.Used, context.Budget.Limit)
	}
	if context.Budget.Limit != defaultPolicy.Budget-utf8.RuneCount(writeTask(50)) {
		t.Fatalf("limit must reserve the task: %d", context.Budget.Limit)
	}
}

// 与章数脱钩：配角状态随章数线性增长，写到第 200 章仍不超预算，固定段完整保留。
func TestWriteContextStaysWithinBudgetAsBookGrows(t *testing.T) {
	for _, chapter := range []int{50, 200} {
		context, err := selectContext(longBook(250, chapter-1), model.OperationWriteChapter, writeTask(chapter))
		if err != nil {
			t.Fatal(err)
		}
		keys := keySet(context)
		if context.Budget.Used > context.Budget.Limit || !keys["manuscript:"+chapterID(chapter-1)] || !keys["plan:"+planID(chapter+3)] {
			t.Fatalf("chapter %d: used %d / %d, previous and lookahead kept = %v %v",
				chapter, context.Budget.Used, context.Budget.Limit, keys["manuscript:"+chapterID(chapter-1)], keys["plan:"+planID(chapter+3)])
		}
		// 近期配角因计划提及而相关，优先于远期配角进入。
		if !keys["canon:mate-state-"+chapterID(chapter-1)] {
			t.Fatalf("chapter %d: relevant recent mate state was trimmed", chapter)
		}
		// 约束与未回收伏笔不因年代久远被裁：越早立下越要记住。
		if !keys["canon:rule-origin"] || !keys["canon:hook-main"] {
			t.Fatalf("chapter %d: early world rule or open thread was trimmed", chapter)
		}
	}
}

// 窗口审阅（D62）与写作同一有界装配：窗口正文与来源事实、衔接章、近期事件照装，
// 窗口外的旧正文与旧事件不进入；审阅成本随窗口而非全书增长。
func TestReviewContextIsBoundedToTheWindow(t *testing.T) {
	for _, first := range []int{48, 198} {
		content := longBook(250, first+2)
		ids := []string{chapterID(first), chapterID(first + 1), chapterID(first + 2)}
		task, _ := json.Marshal(model.ReviewRangeInput{ChapterIDs: ids, Basis: model.EvidenceBasis{Documents: []model.DocumentBasis{
			{Ref: model.DocumentRef{Kind: model.DocumentManuscript, ID: ids[0]}, Revision: 2},
		}}})
		context, err := selectContext(content, model.OperationReviewRange, task)
		if err != nil {
			t.Fatal(err)
		}
		keys := keySet(context)
		for _, want := range []string{
			"manuscript:" + ids[0], "manuscript:" + ids[1], "manuscript:" + ids[2], "manuscript:" + chapterID(first-1),
			"canon:event-a-" + ids[2], "canon:event-b-" + chapterID(first-3), "canon:rule-origin", "canon:hook-main",
		} {
			if !keys[want] {
				t.Fatalf("window at %d: missing %q", first, want)
			}
		}
		for _, absent := range []string{"manuscript:" + chapterID(first-2), "canon:event-a-" + chapterID(first-6)} {
			if keys[absent] {
				t.Fatalf("window at %d: history %q entered the review context", first, absent)
			}
		}
		if manuscripts := context.Budget.Omitted[model.DocumentManuscript]; manuscripts != first-2 {
			t.Fatalf("window at %d: omitted manuscripts = %d, want %d", first, manuscripts, first-2)
		}
	}
}

// 审阅窗口按容量切（D67）：窗口正文填满 ReviewTextBudget、连同衔接章与各章基线，世界
// 规则与主线伏笔仍装得下，且不越过预算——容量取值必须给这些约束留出余地。
func TestReviewTextBudgetLeavesRoomForConstraints(t *testing.T) {
	const first, runes = 48, 1500 // longBook 每章正文 1500 字
	count := ReviewTextBudget / runes
	content := longBook(250, first+count-1)
	var ids []string
	var basis []model.DocumentBasis
	for number := first; number < first+count; number++ {
		ids = append(ids, chapterID(number))
		basis = append(basis,
			model.DocumentBasis{Ref: model.DocumentRef{Kind: model.DocumentManuscript, ID: chapterID(number)}, Revision: model.Revision(number + 1)},
			model.DocumentBasis{Ref: model.DocumentRef{Kind: model.DocumentPlan, ID: planID(number)}, Revision: 1},
		)
	}
	task, _ := json.Marshal(model.ReviewRangeInput{ChapterIDs: ids, Basis: model.EvidenceBasis{Documents: basis}})
	context, err := selectContext(content, model.OperationReviewRange, task)
	if err != nil {
		t.Fatal(err)
	}
	keys := keySet(context)
	for _, want := range []string{"manuscript:" + ids[0], "manuscript:" + ids[count-1], "manuscript:" + chapterID(first-1), "canon:rule-origin", "canon:hook-main"} {
		if !keys[want] {
			t.Fatalf("a full review window lost %q", want)
		}
	}
	if context.Budget.Used > context.Budget.Limit {
		t.Fatalf("a full review window overflows the context: used %d > limit %d", context.Budget.Used, context.Budget.Limit)
	}
}

// 排序段只在一处截断：相关实体、世界规则、伏笔优先，无关事实先被裁掉；必选内容照装，
// 超出预算时 Used > Limit 本身就是可见的冲突信号。
func TestRankTrimsIrrelevantFactsFirstAndExposesRequiredOverflow(t *testing.T) {
	content := contextFixture()
	content.Canon = append(content.Canon,
		model.CanonFact{ID: "villain-lair", Kind: model.CanonState, SubjectID: "villain", Predicate: "state.lair", Value: json.RawMessage(`"` + strings.Repeat("远", 400) + `"`)},
	)
	full, err := selectContext(content, model.OperationWriteChapter, json.RawMessage(`{"chapter_plan_id":"chapter-plan-3"}`))
	if err != nil {
		t.Fatal(err)
	}
	fixed := full.Budget.Used - 500 // 留出小于巨型无关事实的余量
	tight, err := buildStoryContext(content, model.OperationWriteChapter, json.RawMessage(`{"chapter_plan_id":"chapter-plan-3"}`),
		contextPolicy{Window: 5, Lookahead: 3, Budget: fixed + utf8.RuneCount([]byte(`{"chapter_plan_id":"chapter-plan-3"}`))})
	if err != nil {
		t.Fatal(err)
	}
	keys := keySet(tight)
	if keys["canon:villain-lair"] || !keys["canon:hero-bottom-line"] || tight.Budget.Omitted[model.DocumentCanon] == 0 {
		t.Fatalf("trim order wrong: %v omitted %v", documentKeys(tight), tight.Budget.Omitted)
	}
	starved, err := buildStoryContext(content, model.OperationWriteChapter, json.RawMessage(`{"chapter_plan_id":"chapter-plan-3"}`),
		contextPolicy{Window: 5, Lookahead: 3, Budget: 10})
	if err != nil {
		t.Fatal(err)
	}
	if keys := keySet(starved); !keys["plan:chapter-plan-3"] || !keys["manuscript:chapter-2"] || starved.Budget.Used <= starved.Budget.Limit {
		t.Fatalf("required segment must stay and overflow must be visible: %v used %d limit %d", documentKeys(starved), starved.Budget.Used, starved.Budget.Limit)
	}
}

// 重写必须看到本章来源的全部事实（D41 重申报），即使它们排在裁剪线之后；本章回收的
// 伏笔（hook-ch-006 于第 12 章回收）平时不装配，这里也必须出现。
func TestRewriteContextsCarrySourcedFacts(t *testing.T) {
	content := longBook(30, 30)
	rewrite := json.RawMessage(fmt.Sprintf(`{"chapter_id":%q,"chapter_plan_id":%q,"findings":["节奏"]}`, chapterID(12), planID(12)))
	for _, tc := range []struct {
		kind model.OperationKind
		task json.RawMessage
	}{
		{model.OperationRewriteChapter, rewrite},
		{model.OperationReviseCanon, json.RawMessage(fmt.Sprintf(`{"chapter_id":%q,"reason":"核验"}`, chapterID(12)))},
	} {
		context, err := buildStoryContext(content, tc.kind, tc.task, contextPolicy{Window: 5, Lookahead: 3, Budget: 10})
		if err != nil {
			t.Fatal(err)
		}
		keys := keySet(context)
		for _, want := range []string{"manuscript:ch-012", "canon:event-a-ch-012", "canon:event-b-ch-012", "canon:mate-state-ch-012", "canon:hook-ch-006"} {
			if !keys[want] {
				t.Fatalf("%s: missing sourced %q", tc.kind, want)
			}
		}
	}
}

// 规划锚点是第一个未写的章节：近期窗口给方向，锚点起的全部章节计划是工作对象，不带正文。
func TestPlanningContextAnchorsAtFirstUnwrittenChapter(t *testing.T) {
	context, err := selectContext(longBook(13, 10), model.OperationRevisePlan,
		json.RawMessage(`{"fixed_chapters":20,"existing_chapters":13}`))
	if err != nil {
		t.Fatal(err)
	}
	keys := keySet(context)
	for _, want := range []string{"plan:plan-006", "plan:plan-010", "plan:plan-011", "plan:plan-013", "canon:event-a-ch-010"} {
		if !keys[want] {
			t.Fatalf("missing %q", want)
		}
	}
	for _, absent := range []string{"plan:plan-005", "canon:event-a-ch-005", "manuscript:ch-010"} {
		if keys[absent] {
			t.Fatalf("%q entered the planning context", absent)
		}
	}
}

// 规划的工作对象是全书走向（D95）：写到第 200 章，事实早已超出预算，全书的卷与故事弧
// 仍完整装入，裁掉的是事实；约束照旧排在最前。
func TestPlanningContextKeepsTheWholeStructure(t *testing.T) {
	context, err := selectContext(longBook(250, 200), model.OperationRevisePlan, json.RawMessage(`{"existing_chapters":250}`))
	if err != nil {
		t.Fatal(err)
	}
	keys := keySet(context)
	for arc := 1; arc <= 25; arc++ {
		if !keys[fmt.Sprintf("plan:arc-%02d", arc)] {
			t.Fatalf("planning lost arc %d: omitted %v", arc, context.Budget.Omitted)
		}
	}
	if !keys["canon:rule-origin"] || !keys["canon:hook-main"] {
		t.Fatal("structure displaced the constraints")
	}
	if context.Budget.Omitted[model.DocumentCanon] == 0 || context.Budget.Used > context.Budget.Limit {
		t.Fatalf("facts must yield within the budget: omitted %v, used %d / %d", context.Budget.Omitted, context.Budget.Used, context.Budget.Limit)
	}
}

// 结构本身超出预算时，先装全书各卷，再由近到远装故事弧。
func TestPlanningContextTrimsOldArcsBeforeVolumes(t *testing.T) {
	content := longBook(250, 200)
	for volume := 2; volume <= 5; volume++ {
		content.Plan = append(content.Plan, model.PlanNode{ID: fmt.Sprintf("volume-%d", volume), Kind: model.PlanVolume, Order: volume, Title: "卷"})
	}
	for i, node := range content.Plan {
		switch node.Kind {
		case model.PlanVolume:
			content.Plan[i].Summary = strings.Repeat("卷", 1000)
		case model.PlanArc:
			arc := (node.Order-1)/10 + 1
			content.Plan[i].ParentID = fmt.Sprintf("volume-%d", (arc-1)/5+1)
			content.Plan[i].Summary = strings.Repeat("弧", 1500)
		}
	}
	context, err := selectContext(content, model.OperationRevisePlan, json.RawMessage(`{"existing_chapters":250}`))
	if err != nil {
		t.Fatal(err)
	}
	keys := keySet(context)
	for volume := 1; volume <= 5; volume++ {
		if !keys[fmt.Sprintf("plan:volume-%d", volume)] {
			t.Fatalf("planning lost volume %d", volume)
		}
	}
	if !keys["plan:arc-19"] || keys["plan:arc-01"] || context.Budget.Used > context.Budget.Limit {
		t.Fatalf("arcs must fill from the newest: arc-19 %v, arc-01 %v, used %d / %d",
			keys["plan:arc-19"], keys["plan:arc-01"], context.Budget.Used, context.Budget.Limit)
	}
}

// 输入顺序不影响结果：缓存键相同的上下文必须逐字节一致。
func TestStoryContextIsDeterministicUnderInputOrder(t *testing.T) {
	content := longBook(80, 70)
	want, err := selectContext(content, model.OperationWriteChapter, writeTask(71))
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	random := rand.New(rand.NewPCG(1, 2))
	for range 5 {
		shuffled := content
		shuffled.Plan, shuffled.Canon = slices.Clone(content.Plan), slices.Clone(content.Canon)
		shuffled.Entities, shuffled.Manuscript = slices.Clone(content.Entities), slices.Clone(content.Manuscript)
		random.Shuffle(len(shuffled.Plan), func(i, j int) { shuffled.Plan[i], shuffled.Plan[j] = shuffled.Plan[j], shuffled.Plan[i] })
		random.Shuffle(len(shuffled.Canon), func(i, j int) { shuffled.Canon[i], shuffled.Canon[j] = shuffled.Canon[j], shuffled.Canon[i] })
		random.Shuffle(len(shuffled.Entities), func(i, j int) {
			shuffled.Entities[i], shuffled.Entities[j] = shuffled.Entities[j], shuffled.Entities[i]
		})
		random.Shuffle(len(shuffled.Manuscript), func(i, j int) {
			shuffled.Manuscript[i], shuffled.Manuscript[j] = shuffled.Manuscript[j], shuffled.Manuscript[i]
		})
		got, err := selectContext(shuffled, model.OperationWriteChapter, writeTask(71))
		if err != nil {
			t.Fatal(err)
		}
		if gotJSON, _ := json.Marshal(got); string(gotJSON) != string(wantJSON) {
			t.Fatal("shuffled input changed the story context")
		}
	}
}
