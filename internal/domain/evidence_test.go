package domain

import (
	"errors"
	"slices"
	"testing"
)

func TestEvidenceBasisNormalizeAndValidate(t *testing.T) {
	chapter := DocumentRef{Kind: DocumentManuscript, ID: "chapter-1"}
	plan := DocumentRef{Kind: DocumentPlan, ID: "chapter-plan-1"}
	scope := ScopeBasis{Kind: ScopeDirective, Target: DirectiveTarget{ChapterNumber: 1, PlanNodeIDs: []string{"chapter-plan-1"}}, Digest: "s"}
	basis := EvidenceBasis{
		Documents: []DocumentBasis{{Ref: chapter, Revision: 3}, {Ref: plan, Revision: 2}, {Ref: chapter, Revision: 3}},
		Scopes:    []ScopeBasis{scope, scope},
		Artifacts: []ArtifactRef{{ID: "op/b", Digest: Digest([]byte("2"))}, {ID: "op/a", Digest: Digest([]byte("1"))}, {ID: "op/a", Digest: Digest([]byte("1"))}},
	}
	normalized := basis.Normalize()
	if !slices.Equal(normalized.Documents, []DocumentBasis{{Ref: chapter, Revision: 3}, {Ref: plan, Revision: 2}}) ||
		len(normalized.Scopes) != 1 || !slices.Equal(normalized.Artifacts, []ArtifactRef{{ID: "op/a", Digest: Digest([]byte("1"))}, {ID: "op/b", Digest: Digest([]byte("2"))}}) {
		t.Fatalf("normalized = %#v", normalized)
	}
	if !basis.Equal(normalized) || !normalized.Equal(basis) {
		t.Fatalf("basis must equal its normalized form")
	}
	if err := normalized.Validate(); err != nil {
		t.Fatalf("normalized basis: %v", err)
	}
	// 同一文档两个 revision 是自相矛盾的基线。
	conflicting := EvidenceBasis{Documents: []DocumentBasis{{Ref: chapter, Revision: 3}, {Ref: chapter, Revision: 4}}}
	if err := conflicting.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("conflicting revisions err = %v", err)
	}
	if err := (EvidenceBasis{Documents: []DocumentBasis{{Ref: chapter}}}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero revision err = %v", err)
	}
	if err := (EvidenceBasis{Scopes: []ScopeBasis{{Kind: "other", Digest: "s"}}}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown scope kind err = %v", err)
	}
	changed := normalized
	changed.Scopes = []ScopeBasis{{Kind: ScopeDirective, Target: scope.Target, Digest: "t"}}
	if changed.Equal(normalized) {
		t.Fatalf("scope digest change must break equality")
	}
}

func TestScopeDigestChangesOnMembershipAndVersion(t *testing.T) {
	hook := DocumentBasis{Ref: DocumentRef{Kind: DocumentDirective, ID: "hook"}, Revision: 2}
	rain := DocumentBasis{Ref: DocumentRef{Kind: DocumentDirective, ID: "rain"}, Revision: 3}
	empty := ScopeDigest(nil)
	single := ScopeDigest([]DocumentBasis{hook})
	both := ScopeDigest([]DocumentBasis{hook, rain})
	if empty == single || single == both || empty == both {
		t.Fatalf("membership must change the digest: %s %s %s", empty, single, both)
	}
	if ScopeDigest([]DocumentBasis{rain, hook}) != both {
		t.Fatalf("scope digest must not depend on member order")
	}
	revised := hook
	revised.Revision = 5
	if ScopeDigest([]DocumentBasis{revised}) == single {
		t.Fatalf("member revision must change the digest")
	}
}
