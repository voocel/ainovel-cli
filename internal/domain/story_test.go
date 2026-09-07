package domain

import (
	"encoding/json"
	"errors"
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
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门",
		Blocks: []ManuscriptBlock{{ID: "p-1", Text: "第一段"}, {ID: "p-1", Text: "第二段"}},
	}
	if err := chapter.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestDocumentDependenciesIncludeStableParentAndPlan(t *testing.T) {
	planContent := json.RawMessage(`{
		"id":"chapter-plan-1",
		"kind":"chapter",
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

func TestValidatePlanChapterTargetRequiresExactCoverage(t *testing.T) {
	base := []PlanNode{
		{ID: "chapter-1", Kind: PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "开端"},
		{ID: "chapter-2", Kind: PlanChapter, ParentID: "arc-1", Order: 2, Title: "第二章", Summary: "推进"},
	}
	extra, err := json.Marshal(PlanNode{
		ID: "chapter-3", Kind: PlanChapter, ParentID: "arc-1", Order: 3, Title: "第三章", Summary: "额外章节",
	})
	if err != nil {
		t.Fatalf("marshal plan node: %v", err)
	}
	patches := []Patch{{
		Document:  DocumentRef{Kind: DocumentPlan, ID: "chapter-3"},
		Operation: PatchPut, Content: extra,
	}}
	if err := ValidatePlanChapterTarget(base, nil, 2); err != nil {
		t.Fatalf("exact target rejected: %v", err)
	}
	if err := ValidatePlanChapterTarget(base, patches, 2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("extra chapter error = %v, want ErrInvalid", err)
	}
}
