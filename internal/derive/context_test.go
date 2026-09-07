package derive

import (
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestContextKeyCanonicalizesEquivalentTaskJSON(t *testing.T) {
	left, err := ContextKey(domain.OperationWriteChapter, json.RawMessage(`{"chapter_plan_id":"chapter-1","note":"雨"}`))
	if err != nil {
		t.Fatalf("left context key: %v", err)
	}
	right, err := ContextKey(domain.OperationWriteChapter, json.RawMessage(`{ "note": "雨", "chapter_plan_id": "chapter-1" }`))
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
	locked := domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-bottom-line"}
	relevant := domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-location"}
	context, err := BuildStoryContext(ProjectContent{
		ID: "book-1", Revision: 4, Intent: domain.Intent{Premise: "凡人远行"},
		Plan: []domain.PlanNode{
			{ID: "volume-1", Kind: domain.PlanVolume, Title: "远行", Summary: "离开故乡"},
			{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Title: "渡河", Summary: "寻找渡口"},
			{ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "启程"},
			{ID: "chapter-plan-2", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 2, Title: "第二章", Summary: "夜宿荒村"},
			{ID: "chapter-plan-3", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 3, Title: "第三章", Summary: "抵达河边", DependsOn: []domain.DocumentRef{relevant}},
		},
		Canon: []domain.CanonFact{
			{ID: locked.ID, Kind: domain.CanonWorldRule, SubjectID: "hero", Predicate: "rule.bottom_line", Value: json.RawMessage(`"不伤无辜"`)},
			{ID: relevant.ID, Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.location", Value: json.RawMessage(`"河边"`)},
			{ID: "villain-location", Kind: domain.CanonState, SubjectID: "villain", Predicate: "state.location", Value: json.RawMessage(`"京城"`)},
		},
		Manuscript: []domain.ManuscriptChapter{
			{ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "第一章", Blocks: []domain.ManuscriptBlock{{ID: "block-1", Text: "更早正文只留索引"}}},
			{ID: "chapter-2", PlanNodeID: "chapter-plan-2", Number: 2, Title: "第二章", Blocks: []domain.ManuscriptBlock{{ID: "block-1", Text: "上一章结尾必须可见"}}},
		},
		Ownership: []domain.OwnershipRule{{Target: locked, Control: domain.ControlLocked}},
	}, domain.OperationWriteChapter, json.RawMessage(`{"chapter_plan_id":"chapter-plan-3"}`))
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
