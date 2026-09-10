package change

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// commitUserChange 以用户身份在当前 Revision 上提交一组补丁，返回新 Revision。
func commitUserChange(t *testing.T, ctx context.Context, s *store.Store, target domain.AuthorityTarget, id string, patches ...domain.Patch) domain.Revision {
	t.Helper()
	current, err := s.CurrentRevision(ctx, target)
	if err != nil {
		t.Fatalf("current revision: %v", err)
	}
	change := approvedChange(id, target, current, patches...)
	pending := change
	pending.ApprovalState = domain.ApprovalPending
	pending.DecidedBy = nil
	pending.DecidedAt = nil
	if _, err := s.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save %s: %v", id, err)
	}
	committed, err := s.CommitProposal(ctx, change)
	if err != nil {
		t.Fatalf("commit %s: %v", id, err)
	}
	return committed.NewRevision
}

func directivePatch(t *testing.T, id, scope string) domain.Patch {
	t.Helper()
	return domain.Patch{
		Document: domain.DocumentRef{Kind: domain.DocumentDirective, ID: id}, Operation: domain.PatchPut,
		Content: documentJSON(t, domain.Directive{ID: id, Scope: scope, Text: "要求 " + id, Status: domain.DirectiveActive}),
	}
}

func ownershipPatch(t *testing.T, ref domain.DocumentRef, control domain.ControlLevel) domain.Patch {
	t.Helper()
	rule := domain.OwnershipRule{Target: ref, Control: control}
	if control == domain.ControlGuided {
		rule.Guidance = []string{"保持基调"}
	}
	return domain.Patch{
		Document: domain.DocumentRef{Kind: domain.DocumentOwnership, ID: ref.Key()}, Operation: domain.PatchPut,
		Content: documentJSON(t, rule),
	}
}

// TestRelocateFollowsControlOnlyRule 钉住 D51：中间只有用户专属变化且任务基线仍成立时
// 提案搬到当前 Revision 并重算影响；相干要求、内容变化或提案自己的文档被改都冲突。
func TestRelocateFollowsControlOnlyRule(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)
	hero := domain.DocumentRef{Kind: domain.DocumentEntity, ID: "hero"}
	chapterTwo := domain.DirectiveTarget{ChapterNumber: 2, PlanNodeIDs: []string{"chapter-plan-2", "arc-1", "volume-1"}}
	scopeBasis := func(members ...domain.DocumentBasis) domain.EvidenceBasis {
		return domain.EvidenceBasis{Scopes: []domain.ScopeBasis{{Kind: domain.ScopeDirective, Target: chapterTwo, Digest: domain.ScopeDigest(members)}}}
	}
	proposal := pendingChange("ai-fact", target, 1, domain.AuthorAI, domain.Patch{
		Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-mood"}, Operation: domain.PatchPut,
		Content: documentJSON(t, domain.CanonFact{ID: "hero-mood", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.mood", Value: json.RawMessage(`"忐忑"`)}),
	})
	proposal.Impact.Compliance = json.RawMessage(`{"status":"pass"}`)

	same, moved, err := engine.Relocate(ctx, proposal, scopeBasis())
	if err != nil || moved || same.BaseRevision != 1 {
		t.Fatalf("relocate at current = %#v, %v, %v", same, moved, err)
	}
	// 锁定实体 + 只覆盖第 1 章的要求：用户专属且不相干，重定位到 Revision 3。
	commitUserChange(t, ctx, s, target, "lock-hero", ownershipPatch(t, hero, domain.ControlLocked))
	commitUserChange(t, ctx, s, target, "unrelated", directivePatch(t, "d1", "chapter_range:1-1"))
	relocated, moved, err := engine.Relocate(ctx, proposal, scopeBasis())
	if err != nil || !moved || relocated.BaseRevision != 3 || len(relocated.Impact.Structural) == 0 ||
		string(relocated.Impact.Compliance) != `{"status":"pass"}` {
		t.Fatalf("relocated = %#v, %v, %v", relocated, moved, err)
	}
	if proposal.BaseRevision != 1 {
		t.Fatal("relocation must not mutate the input proposal")
	}
	// 覆盖第 2 章的要求：作用域摘要变了，任务基线不再成立。
	related := commitUserChange(t, ctx, s, target, "related", directivePatch(t, "d2", "from_chapter:2"))
	if _, _, err := engine.Relocate(ctx, proposal, scopeBasis()); !errors.Is(err, store.ErrRevisionConflict) || !errors.Is(err, ErrBasisMismatch) {
		t.Fatalf("related directive err = %v", err)
	}
	// 基线跟上后，内容变化仍然冲突并说明是哪份文档。
	upToDate := scopeBasis(domain.DocumentBasis{Ref: domain.DocumentRef{Kind: domain.DocumentDirective, ID: "d2"}, Revision: related})
	if relocated, moved, err := engine.Relocate(ctx, proposal, upToDate); err != nil || !moved || relocated.BaseRevision != related {
		t.Fatalf("relocate with current scope = %#v, %v, %v", relocated, moved, err)
	}
	entity := commitUserChange(t, ctx, s, target, "rename", domain.Patch{
		Document: hero, Operation: domain.PatchPut,
		Content: documentJSON(t, domain.Entity{ID: "hero", Kind: domain.EntityCharacter, Name: "主角二号"}),
	})
	_, _, err = engine.Relocate(ctx, proposal, upToDate)
	if !errors.Is(err, store.ErrRevisionConflict) || !strings.Contains(err.Error(), "entity:hero changed at revision 5") {
		t.Fatalf("content drift err = %v", err)
	}
	// 提案自己要改的用户专属文档被用户改过：同样冲突；改别的则重定位。
	ownership := pendingChange("ai-guide", target, entity, domain.AuthorAI, ownershipPatch(t, hero, domain.ControlGuided))
	commitUserChange(t, ctx, s, target, "relock", ownershipPatch(t, hero, domain.ControlLocked))
	if _, _, err := engine.Relocate(ctx, ownership, domain.EvidenceBasis{}); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("own document drift err = %v", err)
	}
	other := pendingChange("ai-guide-plan", target, entity, domain.AuthorAI, ownershipPatch(t, domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-1"}, domain.ControlGuided))
	if relocated, moved, err := engine.Relocate(ctx, other, domain.EvidenceBasis{}); err != nil || !moved || relocated.BaseRevision != entity+1 {
		t.Fatalf("relocate past another ownership change = %#v, %v, %v", relocated, moved, err)
	}
	// 基线领先于当前是调用方错误。
	ahead := pendingChange("ahead", target, 99, domain.AuthorAI)
	if _, _, err := engine.Relocate(ctx, ahead, domain.EvidenceBasis{}); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("ahead err = %v", err)
	}
}

func TestVerifyBasisAtRevision(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)
	hero := domain.DocumentRef{Kind: domain.DocumentEntity, ID: "hero"}
	directive := commitUserChange(t, ctx, s, target, "hook", directivePatch(t, "hook", "from_chapter:1"))
	renamed := commitUserChange(t, ctx, s, target, "rename", domain.Patch{
		Document: hero, Operation: domain.PatchPut,
		Content: documentJSON(t, domain.Entity{ID: "hero", Kind: domain.EntityCharacter, Name: "主角二号"}),
	})
	document := func(ref domain.DocumentRef, revision domain.Revision) domain.EvidenceBasis {
		return domain.EvidenceBasis{Documents: []domain.DocumentBasis{{Ref: ref, Revision: revision}}}
	}
	chapterOne := domain.DirectiveTarget{ChapterNumber: 1, PlanNodeIDs: []string{"chapter-plan-1", "arc-1", "volume-1"}}
	scope := domain.EvidenceBasis{Scopes: []domain.ScopeBasis{{
		Kind: domain.ScopeDirective, Target: chapterOne,
		Digest: domain.ScopeDigest([]domain.DocumentBasis{{Ref: domain.DocumentRef{Kind: domain.DocumentDirective, ID: "hook"}, Revision: directive}}),
	}}}
	cases := []struct {
		name  string
		basis domain.EvidenceBasis
		at    domain.Revision
		holds bool
	}{
		{"document at its last change", document(hero, renamed), renamed, true},
		{"document before the change", document(hero, 1), directive, true},
		{"document after the change", document(hero, 1), renamed, false},
		{"absent document", document(domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-9"}, 1), renamed, false},
		{"scope with the directive", scope, renamed, true},
		{"scope before the directive", scope, 1, false},
		{"missing artifact", domain.EvidenceBasis{Artifacts: []domain.ArtifactRef{{ID: "missing", Digest: "x"}}}, renamed, false},
		{"empty basis", domain.EvidenceBasis{}, renamed, true},
	}
	for _, tc := range cases {
		err := engine.VerifyBasis(ctx, target, tc.basis, tc.at)
		if tc.holds && err != nil {
			t.Fatalf("%s: unexpected err %v", tc.name, err)
		}
		if !tc.holds && !errors.Is(err, ErrBasisMismatch) {
			t.Fatalf("%s: err = %v, want ErrBasisMismatch", tc.name, err)
		}
	}
}
