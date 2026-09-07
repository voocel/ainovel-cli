package change

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestPrepareReportsTransitiveStructuralImpact(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)

	proposal := pendingChange("update-origin", target, 1, domain.AuthorAI, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-origin"},
		Operation: domain.PatchPut,
		Content: documentJSON(t, domain.CanonFact{
			ID: "hero-origin", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.origin",
			PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"孤儿"`),
		}),
	})
	prepared, err := engine.Prepare(ctx, proposal)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var impact StructuralImpact
	if err := json.Unmarshal(prepared.Impact.Structural, &impact); err != nil {
		t.Fatalf("decode impact: %v", err)
	}
	if got := refKeys(impact.Direct); !slices.Equal(got, []string{"canon:hero-origin"}) {
		t.Fatalf("direct = %v", got)
	}
	if got := refKeys(impact.Affected); !slices.Equal(got, []string{"manuscript:chapter-1"}) {
		t.Fatalf("affected = %v", got)
	}
}

func TestLockedDocumentRejectsSystemApproval(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, true)
	engine := New(s)

	proposal := pendingChange("update-origin", target, 1, domain.AuthorAI, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-origin"},
		Operation: domain.PatchPut,
		Content: documentJSON(t, domain.CanonFact{
			ID: "hero-origin", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.origin",
			PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"孤儿"`),
		}),
	})
	prepared, err := engine.Prepare(ctx, proposal)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	decidedAt := testTime().Add(time.Minute)
	autoApproved, err := Decide(prepared, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorSystem, ID: "auto-policy"}, decidedAt)
	if err != nil {
		t.Fatalf("auto decision: %v", err)
	}
	if _, err := engine.Commit(ctx, autoApproved); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("auto commit error = %v, want ErrUnauthorized", err)
	}

	userApproved, err := Decide(prepared, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, decidedAt)
	if err != nil {
		t.Fatalf("user decision: %v", err)
	}
	committed, err := engine.Commit(ctx, userApproved)
	if err != nil {
		t.Fatalf("user commit: %v", err)
	}
	if committed.NewRevision != 2 {
		t.Fatalf("revision = %d, want 2", committed.NewRevision)
	}
}

func TestGuidedDocumentRequiresPassingCompliance(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)

	canon := domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-origin"}
	guardPending := pendingChange("guard", target, 1, domain.AuthorUser, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentOwnership, ID: canon.Key()},
		Operation: domain.PatchPut,
		Content:   documentJSON(t, domain.OwnershipRule{Target: canon, Control: domain.ControlGuided, Guidance: []string{"出身设定不得偏离寒门"}}),
	})
	guardPending, err := engine.Prepare(ctx, guardPending)
	if err != nil {
		t.Fatalf("prepare guided ownership: %v", err)
	}
	guard, err := Decide(guardPending, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, testTime().Add(30*time.Second))
	if err != nil {
		t.Fatalf("decide guided ownership: %v", err)
	}
	if _, err := engine.Commit(ctx, guard); err != nil {
		t.Fatalf("commit guided ownership: %v", err)
	}

	base := pendingChange("guided-update", target, 2, domain.AuthorAI, domain.Patch{
		Document:  canon,
		Operation: domain.PatchPut,
		Content: documentJSON(t, domain.CanonFact{
			ID: "hero-origin", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.origin",
			PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"寒门孤儿"`),
		}),
	})
	decidedAt := testTime().Add(time.Minute)
	system := domain.Author{Kind: domain.AuthorSystem, ID: "auto-policy"}

	cases := []struct {
		name       string
		compliance json.RawMessage
		wantErr    bool
	}{
		{"missing evidence", nil, true},
		{"conflict report", documentJSON(t, domain.SemanticComplianceReport{
			Status: domain.SemanticComplianceConflict,
			Findings: []domain.SemanticComplianceFinding{{
				Constraint: canon, Explanation: "正文实质改写了受限出身",
			}},
		}), true},
		{"pass report", documentJSON(t, domain.SemanticComplianceReport{Status: domain.SemanticCompliancePass}), false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			proposal := base
			proposal.ID = "guided-" + strings.ReplaceAll(testCase.name, " ", "-")
			proposal.Impact.Compliance = testCase.compliance
			prepared, err := engine.Prepare(ctx, proposal)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			prepared.Impact.Compliance = testCase.compliance
			approved, err := Decide(prepared, domain.ApprovalApproved, system, decidedAt)
			if err != nil {
				t.Fatalf("decide: %v", err)
			}
			_, err = engine.Commit(ctx, approved)
			if testCase.wantErr {
				if !errors.Is(err, ErrUnauthorized) {
					t.Fatalf("commit error = %v, want ErrUnauthorized", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("commit with passing compliance: %v", err)
			}
		})
	}
}

func TestIntentChangesAfterInitializationRequireUserApproval(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	engine := New(s)
	decidedAt := testTime().Add(time.Minute)
	system := domain.Author{Kind: domain.AuthorSystem, ID: "approval-policy:auto"}

	// 初始化事务：AI 在第一个 Revision 补全 Intent 可以自动批准（D28）。
	initial := pendingChange("ai-init", target, 0, domain.AuthorAI, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
		Operation: domain.PatchPut,
		Content:   documentJSON(t, domain.Intent{Premise: "凡人修仙", EndingDirection: "问鼎大道"}),
	})
	prepared, err := engine.Prepare(ctx, initial)
	if err != nil {
		t.Fatalf("prepare init: %v", err)
	}
	approved, err := Decide(prepared, domain.ApprovalApproved, system, decidedAt)
	if err != nil {
		t.Fatalf("decide init: %v", err)
	}
	if _, err := engine.Commit(ctx, approved); err != nil {
		t.Fatalf("AI intent completion in the initialization transaction must auto-commit: %v", err)
	}

	// 初始化之后：任何 Intent 变化都必须用户确认，包括补填空字段（D25/D28）。
	update := pendingChange("ai-intent-drift", target, 1, domain.AuthorAI, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
		Operation: domain.PatchPut,
		Content:   documentJSON(t, domain.Intent{Premise: "凡人修仙", EndingDirection: "陨落成魔", Audience: "成年读者"}),
	})
	prepared, err = engine.Prepare(ctx, update)
	if err != nil {
		t.Fatalf("prepare drift: %v", err)
	}
	autoApproved, err := Decide(prepared, domain.ApprovalApproved, system, decidedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("decide drift: %v", err)
	}
	if _, err := engine.Commit(ctx, autoApproved); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("post-init AI intent change commit error = %v, want ErrUnauthorized", err)
	}
	userApproved, err := Decide(prepared, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, decidedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("user decide drift: %v", err)
	}
	if _, err := engine.Commit(ctx, userApproved); err != nil {
		t.Fatalf("user-approved intent change: %v", err)
	}
}

func TestApprovalPolicyChangesRequireUserApproval(t *testing.T) {
	// D25/D31：审批策略是控制边界，AI/系统不得自动裁决其变更。
	ctx := context.Background()
	engine := New(openStore(t))
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	decidedAt := testTime().Add(time.Minute)
	proposal := pendingChange("tighten-approval", target, 0, domain.AuthorAI, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentApproval, ID: "root"},
		Operation: domain.PatchPut,
		Content:   documentJSON(t, domain.ApprovalSetting{Policy: domain.ApprovalManual}),
	})
	prepared, err := engine.Prepare(ctx, proposal)
	if err != nil {
		t.Fatalf("prepare approval change: %v", err)
	}
	autoApproved, err := Decide(prepared, domain.ApprovalApproved,
		domain.Author{Kind: domain.AuthorSystem, ID: "approval-policy:auto"}, decidedAt)
	if err != nil {
		t.Fatalf("decide approval change: %v", err)
	}
	if _, err := engine.Commit(ctx, autoApproved); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("system-approved approval change commit error = %v, want ErrUnauthorized", err)
	}
	userApproved, err := Decide(prepared, domain.ApprovalApproved,
		domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, decidedAt)
	if err != nil {
		t.Fatalf("user decide approval change: %v", err)
	}
	if _, err := engine.Commit(ctx, userApproved); err != nil {
		t.Fatalf("user-approved approval change: %v", err)
	}
}

func TestOverlayAndAssetChangesRequireUserApproval(t *testing.T) {
	// D31/D33：Project Overlay、资产固定引用与 Directive 是 AI 的行为边界，AI 可以提
	// Proposal，但只有用户能裁决——AI 不能自动改写自己的指令、要求或可复用资产。
	ctx := context.Background()
	engine := New(openStore(t))
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	decidedAt := testTime().Add(time.Minute)
	patches := []domain.Patch{
		{
			Document:  domain.DocumentRef{Kind: domain.DocumentOverlay, ID: "root"},
			Operation: domain.PatchPut,
			Content:   documentJSON(t, domain.ProjectOverlay{Rules: []string{"每章结尾留悬念"}}),
		},
		{
			Document:  domain.DocumentRef{Kind: domain.DocumentAssets, ID: "root"},
			Operation: domain.PatchPut,
			Content:   documentJSON(t, domain.ProjectAssetRefs{Packs: []domain.ProjectPackRef{{ID: "my-style", Revision: 1}}}),
		},
		{
			// D33：Directive 只能由用户创建；AI 提的要求同样必须用户裁决。
			Document:  domain.DocumentRef{Kind: domain.DocumentDirective, ID: "directive-1"},
			Operation: domain.PatchPut,
			Content: documentJSON(t, domain.Directive{
				ID: "directive-1", Scope: "from_chapter:2", Text: "第二章起节奏加快", Status: domain.DirectiveActive,
			}),
		},
	}
	for index, patch := range patches {
		proposal := pendingChange(fmt.Sprintf("boundary-%d", index), target, domain.Revision(index), domain.AuthorAI, patch)
		prepared, err := engine.Prepare(ctx, proposal)
		if err != nil {
			t.Fatalf("prepare boundary change %d: %v", index, err)
		}
		autoApproved, err := Decide(prepared, domain.ApprovalApproved,
			domain.Author{Kind: domain.AuthorSystem, ID: "approval-policy:auto"}, decidedAt)
		if err != nil {
			t.Fatalf("decide boundary change %d: %v", index, err)
		}
		if _, err := engine.Commit(ctx, autoApproved); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("system-approved boundary change %d error = %v, want ErrUnauthorized", index, err)
		}
		userApproved, err := Decide(prepared, domain.ApprovalApproved,
			domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, decidedAt)
		if err != nil {
			t.Fatalf("user decide boundary change %d: %v", index, err)
		}
		if _, err := engine.Commit(ctx, userApproved); err != nil {
			t.Fatalf("user-approved boundary change %d: %v", index, err)
		}
	}
}

func TestPrepareRejectsMissingPlanParent(t *testing.T) {
	s := openStore(t)
	engine := New(s)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	proposal := pendingChange("bad-plan", target, 0, domain.AuthorUser, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: "chapter-plan-1"},
		Operation: domain.PatchPut,
		Content: documentJSON(t, domain.PlanNode{
			ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "missing-arc",
			Title: "第一章", Summary: "开篇",
		}),
	})
	if _, err := engine.Prepare(context.Background(), proposal); !errors.Is(err, ErrStructuralConflict) {
		t.Fatalf("prepare error = %v, want ErrStructuralConflict", err)
	}
}

func TestPrepareRejectsDocumentInWrongAuthority(t *testing.T) {
	s := openStore(t)
	engine := New(s)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProfile, ID: "user-1", Scope: "global"}
	proposal := pendingChange("wrong-target", target, 0, domain.AuthorUser, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
		Operation: domain.PatchPut,
		Content:   documentJSON(t, domain.Intent{Premise: "凡人修仙"}),
	})
	if _, err := engine.Prepare(context.Background(), proposal); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("prepare error = %v, want ErrInvalid", err)
	}
}

func TestPrepareWithSemanticRequiresAnalyzer(t *testing.T) {
	s := openStore(t)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	proposal := pendingChange("semantic", target, 0, domain.AuthorUser, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
		Operation: domain.PatchPut,
		Content:   documentJSON(t, domain.Intent{Premise: "凡人修仙"}),
	})
	if _, err := New(s).PrepareWithSemantic(context.Background(), proposal); !errors.Is(err, ErrSemanticUnavailable) {
		t.Fatalf("prepare error = %v, want ErrSemanticUnavailable", err)
	}

	analyzer := semanticAnalyzerFunc(func(context.Context, domain.Proposal, StructuralImpact) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"consistent","findings":[],"options":[]}`), nil
	})
	prepared, err := NewWithSemanticAnalyzer(s, analyzer).PrepareWithSemantic(context.Background(), proposal)
	if err != nil {
		t.Fatalf("prepare with semantic analyzer: %v", err)
	}
	if string(prepared.Impact.Semantic) != `{"status":"consistent","findings":[],"options":[]}` {
		t.Fatalf("semantic impact = %s", prepared.Impact.Semantic)
	}
}

func TestRevertCreatesNewRevisionWithoutRewritingHistory(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)

	update, err := engine.Prepare(ctx, pendingChange("update-origin", target, 1, domain.AuthorUser, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-origin"},
		Operation: domain.PatchPut,
		Content: documentJSON(t, domain.CanonFact{
			ID: "hero-origin", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.origin",
			PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"孤儿"`),
		}),
	}))
	if err != nil {
		t.Fatalf("prepare update: %v", err)
	}
	update, err = Decide(update, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, testTime().Add(time.Minute))
	if err != nil {
		t.Fatalf("approve update: %v", err)
	}
	if _, err := engine.Commit(ctx, update); err != nil {
		t.Fatalf("commit update: %v", err)
	}

	revert, err := engine.PrepareRevert(
		ctx, "revert-origin", target, 1,
		domain.Author{Kind: domain.AuthorUser, ID: "user-1"},
		"恢复到首次批准版本", testTime().Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("prepare revert: %v", err)
	}
	revert, err = Decide(revert, domain.ApprovalApproved, domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, testTime().Add(3*time.Minute))
	if err != nil {
		t.Fatalf("approve revert: %v", err)
	}
	committed, err := engine.Commit(ctx, revert)
	if err != nil {
		t.Fatalf("commit revert: %v", err)
	}
	if committed.NewRevision != 3 {
		t.Fatalf("revert revision = %d, want 3", committed.NewRevision)
	}

	ref := domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-origin"}
	atTwo, err := s.GetDocument(ctx, target, ref, 2)
	if err != nil {
		t.Fatalf("read revision 2: %v", err)
	}
	atThree, err := s.GetDocument(ctx, target, ref, 3)
	if err != nil {
		t.Fatalf("read revision 3: %v", err)
	}
	if string(atTwo.Content) == string(atThree.Content) {
		t.Fatal("revert rewrote history instead of creating a distinct current value")
	}
	var fact domain.CanonFact
	if err := json.Unmarshal(atThree.Content, &fact); err != nil {
		t.Fatalf("decode reverted fact: %v", err)
	}
	if string(fact.Value) != `"农家子"` {
		t.Fatalf("reverted value = %s", fact.Value)
	}
}

type semanticAnalyzerFunc func(context.Context, domain.Proposal, StructuralImpact) (json.RawMessage, error)

func (f semanticAnalyzerFunc) Analyze(ctx context.Context, proposal domain.Proposal, impact StructuralImpact) (json.RawMessage, error) {
	return f(ctx, proposal, impact)
}

func seedProject(t *testing.T, ctx context.Context, s *store.Store, target domain.AuthorityTarget, locked bool) {
	t.Helper()
	patches := []domain.Patch{
		{Document: domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, Operation: domain.PatchPut, Content: documentJSON(t, domain.Intent{Premise: "凡人修仙"})},
		{Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "volume-1"}, Operation: domain.PatchPut, Content: documentJSON(t, domain.PlanNode{ID: "volume-1", Kind: domain.PlanVolume, Title: "第一卷", Summary: "入道"})},
		{Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-1"}, Operation: domain.PatchPut, Content: documentJSON(t, domain.PlanNode{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "进入宗门"})},
		{Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "chapter-plan-1"}, Operation: domain.PatchPut, Content: documentJSON(t, domain.PlanNode{ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Title: "第一章", Summary: "抵达山门"})},
		{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-origin"}, Operation: domain.PatchPut, Content: documentJSON(t, domain.CanonFact{ID: "hero-origin", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.origin", Value: json.RawMessage(`"农家子"`)})},
		{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-1"}, Operation: domain.PatchPut, Content: documentJSON(t, domain.ManuscriptChapter{
			ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门",
			Blocks:    []domain.ManuscriptBlock{{ID: "p-1", Text: "他第一次看见山门。"}},
			DependsOn: []domain.DocumentRef{{Kind: domain.DocumentCanon, ID: "hero-origin"}},
		})},
	}
	if locked {
		canon := domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-origin"}
		patches = append(patches, domain.Patch{
			Document:  domain.DocumentRef{Kind: domain.DocumentOwnership, ID: canon.Key()},
			Operation: domain.PatchPut,
			Content:   documentJSON(t, domain.OwnershipRule{Target: canon, Control: domain.ControlLocked}),
		})
	}
	change := approvedChange("seed", target, 0, patches...)
	pending := change
	pending.ApprovalState = domain.ApprovalPending
	pending.DecidedBy = nil
	pending.DecidedAt = nil
	if _, err := s.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save seed proposal: %v", err)
	}
	if _, err := s.CommitProposal(ctx, change); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

func pendingChange(id string, target domain.AuthorityTarget, base domain.Revision, author domain.AuthorKind, patches ...domain.Patch) domain.Proposal {
	return domain.Proposal{
		ID: id, Target: target, BaseRevision: base,
		Author: domain.Author{Kind: author, ID: "author-1"},
		Reason: "test proposal", Patches: patches,
		ApprovalState: domain.ApprovalPending,
		CreatedAt:     testTime(),
	}
}

func approvedChange(id string, target domain.AuthorityTarget, base domain.Revision, patches ...domain.Patch) domain.Proposal {
	decidedAt := testTime().Add(time.Second)
	change := pendingChange(id, target, base, domain.AuthorUser, patches...)
	change.ApprovalState = domain.ApprovalApproved
	change.DecidedBy = &domain.Author{Kind: domain.AuthorUser, ID: "user-1"}
	change.DecidedAt = &decidedAt
	return change
}

func documentJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return payload
}

func refKeys(refs []domain.DocumentRef) []string {
	keys := make([]string, len(refs))
	for i, ref := range refs {
		keys[i] = ref.Key()
	}
	return keys
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s
}

func testTime() time.Time {
	return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
}
