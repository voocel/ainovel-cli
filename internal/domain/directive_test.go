package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDirectiveScopeCoversTarget(t *testing.T) {
	plan := []PlanNode{
		{ID: "volume-1", Kind: PlanVolume, Order: 1, Title: "卷一", Summary: "s"},
		{ID: "arc-1", Kind: PlanArc, ParentID: "volume-1", Order: 1, Title: "弧一", Summary: "s"},
		{ID: "chapter-plan-3", Kind: PlanChapter, ParentID: "arc-1", Order: 3, Title: "三", Summary: "s"},
	}
	target := DirectiveTarget{ChapterNumber: 3, PlanNodeIDs: PlanAncestry(plan, "chapter-plan-3")}
	if got := target.PlanNodeIDs; len(got) != 3 || got[0] != "chapter-plan-3" || got[2] != "volume-1" {
		t.Fatalf("PlanAncestry = %v, want chapter → arc → volume", got)
	}
	cases := []struct {
		scope string
		want  bool
	}{
		{"project", true},
		{"plan_node:chapter-plan-3", true},
		{"plan_node:arc-1", true},
		{"plan_node:volume-1", true},
		{"plan_node:arc-2", false},
		{"chapter_range:1-3", true},
		{"chapter_range:4-6", false},
		{"from_chapter:3", true},
		{"from_chapter:4", false},
	}
	for _, tc := range cases {
		directive := Directive{ID: "d", Scope: tc.scope, Text: "要求", Status: DirectiveActive}
		if err := directive.Validate(); err != nil {
			t.Fatalf("scope %q: %v", tc.scope, err)
		}
		if got := directive.Covers(target); got != tc.want {
			t.Errorf("scope %q covers = %v, want %v", tc.scope, got, tc.want)
		}
	}
}

func TestDirectiveValidateRejectsMalformedScopeAndConstraints(t *testing.T) {
	bad := []Directive{
		{ID: "d", Scope: "chapter:3", Text: "x", Status: DirectiveActive},
		{ID: "d", Scope: "chapter_range:3-2", Text: "x", Status: DirectiveActive},
		{ID: "d", Scope: "from_chapter:0", Text: "x", Status: DirectiveActive},
		{ID: "d", Scope: "plan_node:", Text: "x", Status: DirectiveActive},
		{ID: "d", Scope: "project", Text: " ", Status: DirectiveActive},
		{ID: "d", Scope: "project", Text: "x", Status: "paused"},
		{ID: "d", Scope: "project", Text: "x", Status: DirectiveActive, Constraints: &DirectiveConstraints{MinWords: 10, MaxWords: 5}},
		{ID: "d", Scope: "project", Text: "x", Status: DirectiveActive, Constraints: &DirectiveConstraints{TargetWords: 100, MaxWords: 50}},
	}
	for i, directive := range bad {
		if err := directive.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d error = %v, want ErrInvalid", i, err)
		}
	}
}

func TestDirectiveConstraintBoundsDeriveFromTarget(t *testing.T) {
	cases := []struct {
		constraints *DirectiveConstraints
		min, max    int
		ok          bool
	}{
		{nil, 0, 0, false},
		{&DirectiveConstraints{}, 0, 0, false},
		{&DirectiveConstraints{TargetWords: 3000}, 2700, 3300, true},
		{&DirectiveConstraints{TargetWords: 3000, MinWords: 2000}, 2000, 3300, true},
		{&DirectiveConstraints{MaxWords: 500}, 0, 500, true},
	}
	for i, tc := range cases {
		min, max, ok := tc.constraints.Bounds()
		if min != tc.min || max != tc.max || ok != tc.ok {
			t.Errorf("case %d bounds = (%d, %d, %v), want (%d, %d, %v)", i, min, max, ok, tc.min, tc.max, tc.ok)
		}
	}
}

func TestActiveDirectivesForFiltersStatusAndSorts(t *testing.T) {
	directives := []Directive{
		{ID: "d-2", Scope: "from_chapter:2", Text: "b", Status: DirectiveActive},
		{ID: "d-1", Scope: "project", Text: "a", Status: DirectiveActive},
		{ID: "d-0", Scope: "project", Text: "retired", Status: DirectiveRetired},
		{ID: "d-3", Scope: "chapter_range:1-1", Text: "c", Status: DirectiveActive},
	}
	matched := ActiveDirectivesFor(directives, DirectiveTarget{ChapterNumber: 2})
	if len(matched) != 2 || matched[0].ID != "d-1" || matched[1].ID != "d-2" {
		t.Fatalf("matched = %v, want d-1, d-2", matched)
	}
}

func TestValidateDocumentContentDirective(t *testing.T) {
	ref := DocumentRef{Kind: DocumentDirective, ID: "d-1"}
	good := json.RawMessage(`{"id":"d-1","scope":"from_chapter:5","text":"主角要落败","constraints":{"target_words":3000},"status":"active"}`)
	if err := ValidateDocumentContent(ref, good); err != nil {
		t.Fatalf("valid directive rejected: %v", err)
	}
	mismatch := json.RawMessage(`{"id":"d-2","scope":"project","text":"x","status":"active"}`)
	if err := ValidateDocumentContent(ref, mismatch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("id mismatch error = %v, want ErrInvalid", err)
	}
	unknown := json.RawMessage(`{"id":"d-1","scope":"project","text":"x","status":"active","priority":1}`)
	if err := ValidateDocumentContent(ref, unknown); err == nil {
		t.Fatal("unknown field accepted")
	}
}
