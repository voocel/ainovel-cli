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

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

func TestPrepareReportsTransitiveStructuralImpact(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)

	proposal := pendingChange("update-origin", target, 1, model.AuthorAI, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin"},
		Operation: model.PatchPut,
		Content: documentJSON(t, model.CanonFact{
			ID: "hero-origin", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.origin",
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
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, true)
	engine := New(s)

	proposal := pendingChange("update-origin", target, 1, model.AuthorAI, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin"},
		Operation: model.PatchPut,
		Content: documentJSON(t, model.CanonFact{
			ID: "hero-origin", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.origin",
			PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"孤儿"`),
		}),
	})
	prepared, err := engine.Prepare(ctx, proposal)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	decidedAt := testTime().Add(time.Minute)
	autoApproved, err := Decide(prepared, model.ApprovalApproved, model.Author{Kind: model.AuthorSystem, ID: "auto-policy"}, decidedAt)
	if err != nil {
		t.Fatalf("auto decision: %v", err)
	}
	if _, err := engine.Commit(ctx, autoApproved); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("auto commit error = %v, want ErrUnauthorized", err)
	}

	userApproved, err := Decide(prepared, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, decidedAt)
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
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)

	canon := model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin"}
	guardPending := pendingChange("guard", target, 1, model.AuthorUser, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentOwnership, ID: canon.Key()},
		Operation: model.PatchPut,
		Content:   documentJSON(t, model.OwnershipRule{Target: canon, Control: model.ControlGuided, Guidance: []string{"出身设定不得偏离寒门"}}),
	})
	guardPending, err := engine.Prepare(ctx, guardPending)
	if err != nil {
		t.Fatalf("prepare guided ownership: %v", err)
	}
	guard, err := Decide(guardPending, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, testTime().Add(30*time.Second))
	if err != nil {
		t.Fatalf("decide guided ownership: %v", err)
	}
	if _, err := engine.Commit(ctx, guard); err != nil {
		t.Fatalf("commit guided ownership: %v", err)
	}

	base := pendingChange("guided-update", target, 2, model.AuthorAI, model.Patch{
		Document:  canon,
		Operation: model.PatchPut,
		Content: documentJSON(t, model.CanonFact{
			ID: "hero-origin", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.origin",
			PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"寒门孤儿"`),
		}),
	})
	decidedAt := testTime().Add(time.Minute)
	system := model.Author{Kind: model.AuthorSystem, ID: "auto-policy"}

	cases := []struct {
		name       string
		compliance json.RawMessage
		wantErr    bool
	}{
		{"missing evidence", nil, true},
		{"conflict report", documentJSON(t, model.SemanticComplianceReport{
			Status: model.SemanticComplianceConflict,
			Findings: []model.SemanticComplianceFinding{{
				Constraint: canon, Explanation: "正文实质改写了受限出身",
			}},
		}), true},
		{"pass report", documentJSON(t, model.SemanticComplianceReport{Status: model.SemanticCompliancePass}), false},
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
			approved, err := Decide(prepared, model.ApprovalApproved, system, decidedAt)
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
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	engine := New(s)
	decidedAt := testTime().Add(time.Minute)
	system := model.Author{Kind: model.AuthorSystem, ID: "approval-policy:auto"}

	// 初始化事务：AI 在第一个 Revision 补全 Intent 可以自动批准（D28）。
	initial := pendingChange("ai-init", target, 0, model.AuthorAI, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentIntent, ID: "root"},
		Operation: model.PatchPut,
		Content:   documentJSON(t, model.Intent{Premise: "凡人修仙", EndingDirection: "问鼎大道"}),
	})
	prepared, err := engine.Prepare(ctx, initial)
	if err != nil {
		t.Fatalf("prepare init: %v", err)
	}
	approved, err := Decide(prepared, model.ApprovalApproved, system, decidedAt)
	if err != nil {
		t.Fatalf("decide init: %v", err)
	}
	if _, err := engine.Commit(ctx, approved); err != nil {
		t.Fatalf("AI intent completion in the initialization transaction must auto-commit: %v", err)
	}

	// 初始化之后：任何 Intent 变化都必须用户确认，包括补填空字段（D25/D28）。
	update := pendingChange("ai-intent-drift", target, 1, model.AuthorAI, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentIntent, ID: "root"},
		Operation: model.PatchPut,
		Content:   documentJSON(t, model.Intent{Premise: "凡人修仙", EndingDirection: "陨落成魔", Audience: "成年读者"}),
	})
	prepared, err = engine.Prepare(ctx, update)
	if err != nil {
		t.Fatalf("prepare drift: %v", err)
	}
	autoApproved, err := Decide(prepared, model.ApprovalApproved, system, decidedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("decide drift: %v", err)
	}
	if _, err := engine.Commit(ctx, autoApproved); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("post-init AI intent change commit error = %v, want ErrUnauthorized", err)
	}
	userApproved, err := Decide(prepared, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, decidedAt.Add(time.Minute))
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
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	decidedAt := testTime().Add(time.Minute)
	proposal := pendingChange("tighten-approval", target, 0, model.AuthorAI, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentApproval, ID: "root"},
		Operation: model.PatchPut,
		Content:   documentJSON(t, model.ApprovalSetting{Policy: model.ApprovalManual}),
	})
	prepared, err := engine.Prepare(ctx, proposal)
	if err != nil {
		t.Fatalf("prepare approval change: %v", err)
	}
	autoApproved, err := Decide(prepared, model.ApprovalApproved,
		model.Author{Kind: model.AuthorSystem, ID: "approval-policy:auto"}, decidedAt)
	if err != nil {
		t.Fatalf("decide approval change: %v", err)
	}
	if _, err := engine.Commit(ctx, autoApproved); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("system-approved approval change commit error = %v, want ErrUnauthorized", err)
	}
	userApproved, err := Decide(prepared, model.ApprovalApproved,
		model.Author{Kind: model.AuthorUser, ID: "user-1"}, decidedAt)
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
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	decidedAt := testTime().Add(time.Minute)
	patches := []model.Patch{
		{
			Document:  model.DocumentRef{Kind: model.DocumentOverlay, ID: "root"},
			Operation: model.PatchPut,
			Content:   documentJSON(t, model.ProjectOverlay{Rules: []string{"每章结尾留悬念"}}),
		},
		{
			Document:  model.DocumentRef{Kind: model.DocumentAssets, ID: "root"},
			Operation: model.PatchPut,
			Content:   documentJSON(t, model.ProjectAssetRefs{Packs: []model.ProjectPackRef{{ID: "my-style", Revision: 1}}}),
		},
		{
			// D33：Directive 只能由用户创建；AI 提的要求同样必须用户裁决。
			Document:  model.DocumentRef{Kind: model.DocumentDirective, ID: "directive-1"},
			Operation: model.PatchPut,
			Content: documentJSON(t, model.Directive{
				ID: "directive-1", Scope: "from_chapter:2", Text: "第二章起节奏加快", Status: model.DirectiveActive,
			}),
		},
	}
	for index, patch := range patches {
		proposal := pendingChange(fmt.Sprintf("boundary-%d", index), target, model.Revision(index), model.AuthorAI, patch)
		prepared, err := engine.Prepare(ctx, proposal)
		if err != nil {
			t.Fatalf("prepare boundary change %d: %v", index, err)
		}
		autoApproved, err := Decide(prepared, model.ApprovalApproved,
			model.Author{Kind: model.AuthorSystem, ID: "approval-policy:auto"}, decidedAt)
		if err != nil {
			t.Fatalf("decide boundary change %d: %v", index, err)
		}
		if _, err := engine.Commit(ctx, autoApproved); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("system-approved boundary change %d error = %v, want ErrUnauthorized", index, err)
		}
		userApproved, err := Decide(prepared, model.ApprovalApproved,
			model.Author{Kind: model.AuthorUser, ID: "user-1"}, decidedAt)
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
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	proposal := pendingChange("bad-plan", target, 0, model.AuthorUser, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentPlan, ID: "chapter-plan-1"},
		Operation: model.PatchPut,
		Content: documentJSON(t, model.PlanNode{
			ID: "chapter-plan-1", Kind: model.PlanChapter, ParentID: "missing-arc",
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
	target := model.AuthorityTarget{Kind: model.AuthorityProfile, ID: "user-1", Scope: "global"}
	proposal := pendingChange("wrong-target", target, 0, model.AuthorUser, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentIntent, ID: "root"},
		Operation: model.PatchPut,
		Content:   documentJSON(t, model.Intent{Premise: "凡人修仙"}),
	})
	if _, err := engine.Prepare(context.Background(), proposal); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("prepare error = %v, want ErrInvalid", err)
	}
}

func TestPrepareWithSemanticRequiresAnalyzer(t *testing.T) {
	s := openStore(t)
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	proposal := pendingChange("semantic", target, 0, model.AuthorUser, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentIntent, ID: "root"},
		Operation: model.PatchPut,
		Content:   documentJSON(t, model.Intent{Premise: "凡人修仙"}),
	})
	if _, err := New(s).PrepareWithSemantic(context.Background(), proposal); !errors.Is(err, ErrSemanticUnavailable) {
		t.Fatalf("prepare error = %v, want ErrSemanticUnavailable", err)
	}

	analyzer := semanticAnalyzerFunc(func(context.Context, model.Proposal, StructuralImpact) (json.RawMessage, error) {
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
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)

	update, err := engine.Prepare(ctx, pendingChange("update-origin", target, 1, model.AuthorUser, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin"},
		Operation: model.PatchPut,
		Content: documentJSON(t, model.CanonFact{
			ID: "hero-origin", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.origin",
			PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"孤儿"`),
		}),
	}))
	if err != nil {
		t.Fatalf("prepare update: %v", err)
	}
	update, err = Decide(update, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, testTime().Add(time.Minute))
	if err != nil {
		t.Fatalf("approve update: %v", err)
	}
	if _, err := engine.Commit(ctx, update); err != nil {
		t.Fatalf("commit update: %v", err)
	}

	revert, err := engine.PrepareRevert(
		ctx, "revert-origin", target, 1,
		model.Author{Kind: model.AuthorUser, ID: "user-1"},
		"恢复到首次批准版本", testTime().Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("prepare revert: %v", err)
	}
	revert, err = Decide(revert, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, testTime().Add(3*time.Minute))
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

	ref := model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin"}
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
	var fact model.CanonFact
	if err := json.Unmarshal(atThree.Content, &fact); err != nil {
		t.Fatalf("decode reverted fact: %v", err)
	}
	if string(fact.Value) != `"农家子"` {
		t.Fatalf("reverted value = %s", fact.Value)
	}
}

type semanticAnalyzerFunc func(context.Context, model.Proposal, StructuralImpact) (json.RawMessage, error)

func (f semanticAnalyzerFunc) Analyze(ctx context.Context, proposal model.Proposal, impact StructuralImpact) (json.RawMessage, error) {
	return f(ctx, proposal, impact)
}

func seedProject(t *testing.T, ctx context.Context, s *store.Store, target model.AuthorityTarget, locked bool) {
	t.Helper()
	patches := []model.Patch{
		{Document: model.DocumentRef{Kind: model.DocumentIntent, ID: "root"}, Operation: model.PatchPut, Content: documentJSON(t, model.Intent{Premise: "凡人修仙"})},
		{Document: model.DocumentRef{Kind: model.DocumentPlan, ID: "volume-1"}, Operation: model.PatchPut, Content: documentJSON(t, model.PlanNode{ID: "volume-1", Kind: model.PlanVolume, Title: "第一卷", Summary: "入道"})},
		{Document: model.DocumentRef{Kind: model.DocumentPlan, ID: "arc-1"}, Operation: model.PatchPut, Content: documentJSON(t, model.PlanNode{ID: "arc-1", Kind: model.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "进入宗门"})},
		{Document: model.DocumentRef{Kind: model.DocumentPlan, ID: "chapter-plan-1"}, Operation: model.PatchPut, Content: documentJSON(t, model.PlanNode{ID: "chapter-plan-1", Kind: model.PlanChapter, ParentID: "arc-1", Title: "第一章", Summary: "抵达山门"})},
		{Document: model.DocumentRef{Kind: model.DocumentEntity, ID: "hero"}, Operation: model.PatchPut, Content: documentJSON(t, model.Entity{ID: "hero", Kind: model.EntityCharacter, Name: "主角"})},
		{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin"}, Operation: model.PatchPut, Content: documentJSON(t, model.CanonFact{ID: "hero-origin", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.origin", Value: json.RawMessage(`"农家子"`)})},
		{Document: model.DocumentRef{Kind: model.DocumentManuscript, ID: "chapter-1"}, Operation: model.PatchPut, Content: documentJSON(t, model.ManuscriptChapter{
			ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门", Author: model.AuthorUser,
			Blocks:    []model.ManuscriptBlock{{ID: "p-1", Text: "他第一次看见山门。"}},
			DependsOn: []model.DocumentRef{{Kind: model.DocumentCanon, ID: "hero-origin"}},
		})},
	}
	if locked {
		canon := model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin"}
		patches = append(patches, model.Patch{
			Document:  model.DocumentRef{Kind: model.DocumentOwnership, ID: canon.Key()},
			Operation: model.PatchPut,
			Content:   documentJSON(t, model.OwnershipRule{Target: canon, Control: model.ControlLocked}),
		})
	}
	change := approvedChange("seed", target, 0, patches...)
	pending := change
	pending.ApprovalState = model.ApprovalPending
	pending.DecidedBy = nil
	pending.DecidedAt = nil
	if _, err := s.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save seed proposal: %v", err)
	}
	if _, err := s.CommitProposal(ctx, change); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

func pendingChange(id string, target model.AuthorityTarget, base model.Revision, author model.AuthorKind, patches ...model.Patch) model.Proposal {
	return model.Proposal{
		ID: id, Target: target, BaseRevision: base,
		Author: model.Author{Kind: author, ID: "author-1"},
		Reason: "test proposal", Patches: patches,
		ApprovalState: model.ApprovalPending,
		CreatedAt:     testTime(),
	}
}

func approvedChange(id string, target model.AuthorityTarget, base model.Revision, patches ...model.Patch) model.Proposal {
	decidedAt := testTime().Add(time.Second)
	change := pendingChange(id, target, base, model.AuthorUser, patches...)
	change.ApprovalState = model.ApprovalApproved
	change.DecidedBy = &model.Author{Kind: model.AuthorUser, ID: "user-1"}
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

func refKeys(refs []model.DocumentRef) []string {
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

func TestUserAuthoredChapterIsLockedByDefault(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)

	rewrite := func(id string, author model.AuthorKind) model.Proposal {
		return pendingChange(id, target, 1, model.AuthorAI, model.Patch{
			Document: model.DocumentRef{Kind: model.DocumentManuscript, ID: "chapter-1"}, Operation: model.PatchPut,
			Content: documentJSON(t, model.ManuscriptChapter{
				ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门", Author: author,
				Blocks: []model.ManuscriptBlock{{ID: "p-1", Text: "改写后的第一段。"}},
			}),
		}, model.Patch{
			Document: model.DocumentRef{Kind: model.DocumentCanon, ID: "chapter-1-outcome"}, Operation: model.PatchPut,
			Content: documentJSON(t, model.CanonFact{
				ID: "chapter-1-outcome", Kind: model.CanonEvent, SubjectID: "hero", Predicate: "event.chapter_outcome",
				Value: json.RawMessage(`"抵达山门"`), SourceChapterID: "chapter-1",
			}),
		})
	}
	// AI 提案里的章节必须署 AI 自己的名（D34）。
	if _, err := engine.Prepare(ctx, rewrite("forged", model.AuthorUser)); !errors.Is(err, ErrStructuralConflict) {
		t.Fatalf("forged author err = %v", err)
	}
	prepared, err := engine.Prepare(ctx, rewrite("ai-rewrite", model.AuthorAI))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// 用户写的章没有 ownership 规则也视为 locked：系统裁决不能放行，只有用户可以。
	approved, err := Decide(prepared, model.ApprovalApproved, model.Author{Kind: model.AuthorSystem, ID: "auto-policy"}, testTime().Add(time.Minute))
	if err != nil {
		t.Fatalf("system decide: %v", err)
	}
	if _, err := engine.Commit(ctx, approved); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("system commit err = %v, want ErrUnauthorized", err)
	}
	approved, err = Decide(prepared, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, testTime().Add(2*time.Minute))
	if err != nil {
		t.Fatalf("user decide: %v", err)
	}
	if _, err := engine.Commit(ctx, approved); err != nil {
		t.Fatalf("user commit: %v", err)
	}
}

func TestAttachmentRequiresPublishedArtifactAndFollowsTarget(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)
	artifact := publishTestArtifact(t, ctx, s, target.ID, "cover", "封面")

	attachment := func(id string, ref model.ArtifactRef) model.Patch {
		return model.Patch{
			Document: model.DocumentRef{Kind: model.DocumentAttachment, ID: id}, Operation: model.PatchPut,
			Content: documentJSON(t, model.Attachment{
				ID: id, Target: model.DocumentRef{Kind: model.DocumentManuscript, ID: "chapter-1"}, Role: "cover", Artifact: ref,
			}),
		}
	}
	// 未发布或摘要漂移的工件都不能进入权威（D47）。
	if _, err := engine.Prepare(ctx, pendingChange("unpublished", target, 1, model.AuthorUser,
		attachment("cover-1", model.ArtifactRef{ID: "asset-op/missing", Digest: artifact.Digest}))); !errors.Is(err, ErrStructuralConflict) {
		t.Fatalf("unpublished artifact err = %v", err)
	}
	if _, err := engine.Prepare(ctx, pendingChange("drifted", target, 1, model.AuthorUser,
		attachment("cover-1", model.ArtifactRef{ID: artifact.ID, Digest: model.Digest([]byte("different"))}))); !errors.Is(err, ErrStructuralConflict) {
		t.Fatalf("drifted digest err = %v", err)
	}
	prepared, err := engine.Prepare(ctx, pendingChange("attach", target, 1, model.AuthorUser, attachment("cover-1", artifact.Ref())))
	if err != nil {
		t.Fatalf("prepare attachment: %v", err)
	}
	approved, err := Decide(prepared, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, testTime().Add(time.Minute))
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if _, err := engine.Commit(ctx, approved); err != nil {
		t.Fatalf("commit attachment: %v", err)
	}
	// 附件随目标进入结构影响：改章节依赖的 Canon，受影响集合含章节与附件。
	impacted, err := engine.Prepare(ctx, pendingChange("update-origin", target, 2, model.AuthorAI, model.Patch{
		Document: model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin"}, Operation: model.PatchPut,
		Content: documentJSON(t, model.CanonFact{
			ID: "hero-origin", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.origin",
			PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"孤儿"`),
		}),
	}))
	if err != nil {
		t.Fatalf("prepare impact: %v", err)
	}
	var impact StructuralImpact
	if err := json.Unmarshal(impacted.Impact.Structural, &impact); err != nil {
		t.Fatalf("decode impact: %v", err)
	}
	if got := refKeys(impact.Affected); !slices.Equal(got, []string{"attachment:cover-1", "manuscript:chapter-1"}) {
		t.Fatalf("affected = %v", got)
	}
}

// publishTestArtifact 以一次执行尝试的身份发布工件并落盘元数据。
func publishTestArtifact(t *testing.T, ctx context.Context, s *store.Store, projectID, key, content string) model.Artifact {
	t.Helper()
	now := testTime()
	if _, err := s.CreateCreationRun(ctx, model.CreationRun{
		ID: "run:" + projectID, ProjectID: projectID,
		Goal:     model.NovelGoal{Premise: "测试创作", TargetChapters: 3}.Goal(),
		Strategy: model.CreationRunStrategy{PlanWindowChapters: 3, ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: 3},
		Preset:   model.CreationRunPreset{Source: "test", Digest: "test-preset", Approval: model.ApprovalAuto},
		State:    model.RunRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	input := json.RawMessage(`{"target":{"kind":"manuscript","id":"chapter-1"},"role":"cover","basis":{"documents":[{"ref":{"kind":"manuscript","id":"chapter-1"},"revision":1}]}}`)
	if _, err := s.CreateOperation(ctx, model.Operation{
		ID: "asset-op", Kind: model.OperationGenerateAsset, Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: projectID},
		State: model.OperationQueued, RunID: "run:" + projectID,
		Snapshot: model.ExecutionSnapshot{
			Executor: "external.test@1", BaseRevision: 1, InputDigest: model.Digest(input),
			ConfigDigest: "config", ApprovalPolicy: model.ApprovalAuto,
		},
		Input: input, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create asset operation: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now)
	if err != nil {
		t.Fatalf("claim asset operation: %v", err)
	}
	writer, err := s.NewArtifactWriter(claimed.ID, claimed.Attempt)
	if err != nil {
		t.Fatalf("artifact writer: %v", err)
	}
	writer.Write([]byte(content))
	digest, size, err := writer.Publish()
	if err != nil {
		t.Fatalf("publish artifact: %v", err)
	}
	artifact := model.Artifact{
		ID: model.ArtifactID(claimed.ID, key), ProjectID: projectID, Digest: digest, MediaType: "text/plain",
		Size: size, OperationID: claimed.ID, Attempt: claimed.Attempt, CreatedAt: now,
	}
	if err := s.SaveExecutionArtifacts(ctx, []model.Artifact{artifact}, claimed.ID, claimed.Attempt); err != nil {
		t.Fatalf("save artifact: %v", err)
	}
	if _, err := s.ConcludeOperation(ctx, claimed.ID, claimed.Attempt, model.OperationSucceeded, "", now.Add(time.Second)); err != nil {
		t.Fatalf("conclude asset operation: %v", err)
	}
	return artifact
}

// TestAppendOnlyDocumentsRejectOverwriteAndDeleteAndSurviveRevert 守护只追加文档（D43
// 裁决）：不得覆盖、不得删除；回滚到裁决之前的 Revision 也不删它。
func TestAppendOnlyDocumentsRejectOverwriteAndDeleteAndSurviveRevert(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)
	ref := model.DocumentRef{Kind: model.DocumentAdjudication, ID: "accept-1"}
	record := model.Adjudication{
		ID: "accept-1", Finding: "review/0", Reason: "可以接受", CreatedAt: testTime(),
		Basis: model.EvidenceBasis{Documents: []model.DocumentBasis{{Ref: model.DocumentRef{Kind: model.DocumentManuscript, ID: "chapter-1"}, Revision: 1}}},
	}
	commit := func(id string, base model.Revision, patch model.Patch) error {
		prepared, err := engine.Prepare(ctx, pendingChange(id, target, base, model.AuthorUser, patch))
		if err != nil {
			return err
		}
		approved, err := Decide(prepared, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, testTime().Add(time.Minute))
		if err != nil {
			return err
		}
		_, err = engine.Commit(ctx, approved)
		return err
	}
	if err := commit("accept", 1, model.Patch{Document: ref, Operation: model.PatchPut, Content: documentJSON(t, record)}); err != nil {
		t.Fatalf("append adjudication: %v", err)
	}
	record.Reason = "改写理由"
	if err := commit("overwrite", 2, model.Patch{Document: ref, Operation: model.PatchPut, Content: documentJSON(t, record)}); !errors.Is(err, ErrStructuralConflict) {
		t.Fatalf("overwrite err = %v, want ErrStructuralConflict", err)
	}
	if err := commit("delete", 2, model.Patch{Document: ref, Operation: model.PatchDelete}); !errors.Is(err, ErrStructuralConflict) {
		t.Fatalf("delete err = %v, want ErrStructuralConflict", err)
	}
	if _, err := engine.PrepareRevert(ctx, "revert", target, 1, model.Author{Kind: model.AuthorUser, ID: "user-1"}, "回滚", testTime().Add(2*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("revert across an adjudication-only revision must be a no-op, got %v", err)
	}
}

// TestAICanonRulesForFlashbacksEventsAndRedeclaration 钉住 §4.4 D41 对 AI 提案的四条规则：
// 来源归属、重写重申报、事件跨章只追加、状态生效位置不回退；用户提案不受限。
func TestAICanonRulesForFlashbacksEventsAndRedeclaration(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)
	chapter := func(id string, number int, author model.AuthorKind) model.Patch {
		return model.Patch{
			Document: model.DocumentRef{Kind: model.DocumentManuscript, ID: id}, Operation: model.PatchPut,
			Content: documentJSON(t, model.ManuscriptChapter{
				ID: id, PlanNodeID: "chapter-plan-" + id[len("chapter-"):], Number: number, Title: id, Author: author,
				Blocks: []model.ManuscriptBlock{{ID: id + "-p1", Text: "正文"}},
			}),
		}
	}
	fact := func(f model.CanonFact) model.Patch {
		return model.Patch{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: f.ID}, Operation: model.PatchPut, Content: documentJSON(t, f)}
	}
	plan := func(id string, order int) model.Patch {
		return model.Patch{
			Document: model.DocumentRef{Kind: model.DocumentPlan, ID: id}, Operation: model.PatchPut,
			Content: documentJSON(t, model.PlanNode{ID: id, Kind: model.PlanChapter, ParentID: "arc-1", Order: order, Title: id, Summary: "推进"}),
		}
	}
	event := model.CanonFact{ID: "ch2-event", Kind: model.CanonEvent, SubjectID: "hero", Predicate: "event.arrival", Value: json.RawMessage(`"抵达"`), SourceChapterID: "chapter-2"}
	mood := model.CanonFact{ID: "hero-mood", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.mood", Value: json.RawMessage(`"忐忑"`), SourceChapterID: "chapter-2"}
	base := commitUserChange(t, ctx, s, target, "chapter-2",
		plan("chapter-plan-2", 2), plan("chapter-plan-3", 3), chapter("chapter-2", 2, model.AuthorAI), fact(event), fact(mood))
	updated := func(f model.CanonFact, source, effective string) model.CanonFact {
		f.PreviousValue, f.Value = f.Value, json.RawMessage(`"新值"`)
		f.SourceChapterID, f.EffectiveChapterID = source, effective
		return f
	}
	confirmed := func(f model.CanonFact) model.CanonFact {
		f.PreviousValue = f.Value
		return f
	}
	placed := func(f model.CanonFact, source, effective string) model.CanonFact {
		f.SourceChapterID, f.EffectiveChapterID = source, effective
		return f
	}
	newEvent := model.CanonFact{ID: "ch3-event", Kind: model.CanonEvent, SubjectID: "hero", Predicate: "event.departure", Value: json.RawMessage(`"离开"`), SourceChapterID: "chapter-3"}
	cases := []struct {
		name    string
		author  model.AuthorKind
		patches []model.Patch
		wantErr string
	}{
		{"rewrite must redeclare every chapter fact", model.AuthorAI,
			[]model.Patch{chapter("chapter-2", 2, model.AuthorAI), fact(confirmed(event))}, `redeclare canon "hero-mood"`},
		{"rewrite redeclaring every fact passes", model.AuthorAI,
			[]model.Patch{chapter("chapter-2", 2, model.AuthorAI), fact(confirmed(event)), fact(confirmed(mood))}, ""},
		{"facts must be sourced from proposal chapters", model.AuthorAI,
			[]model.Patch{chapter("chapter-3", 3, model.AuthorAI), fact(placed(newEvent, "chapter-2", ""))}, "sourced from a chapter in the same proposal"},
		{"events of other chapters are append-only", model.AuthorAI,
			[]model.Patch{chapter("chapter-3", 3, model.AuthorAI), fact(newEvent), fact(updated(event, "chapter-3", ""))}, "append-only across chapters"},
		{"state may advance to a later chapter", model.AuthorAI,
			[]model.Patch{chapter("chapter-3", 3, model.AuthorAI), fact(newEvent), fact(updated(mood, "chapter-3", ""))}, ""},
		{"flashback cannot rewind state", model.AuthorAI,
			[]model.Patch{chapter("chapter-3", 3, model.AuthorAI), fact(newEvent), fact(updated(mood, "chapter-3", "chapter-1"))}, "record flashbacks as events"},
		{"user proposals are exempt", model.AuthorUser,
			[]model.Patch{chapter("chapter-3", 3, model.AuthorUser), fact(updated(mood, "chapter-3", "chapter-1"))}, ""},
		{"effective chapter must exist", model.AuthorAI,
			[]model.Patch{chapter("chapter-3", 3, model.AuthorAI), fact(placed(newEvent, "chapter-3", "chapter-9"))}, `missing chapter "chapter-9"`},
	}
	for index, tc := range cases {
		_, err := engine.Prepare(ctx, pendingChange(fmt.Sprintf("canon-rule-%d", index), target, base, tc.author, tc.patches...))
		if tc.wantErr == "" && err != nil {
			t.Fatalf("%s: unexpected err %v", tc.name, err)
		}
		if tc.wantErr != "" && (!errors.Is(err, ErrStructuralConflict) || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}

// Validate 是工具边界的只读结构校验：冲突当场报出、不落库，也不因基线落后于当前
// Revision 而拒绝——漂移由收尾时的重定位处理。
func TestValidateReportsStructuralConflictWithoutWriting(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"}
	seedProject(t, ctx, s, target, false)
	engine := New(s)

	// 给已有事实另起新 id 并带 old_value：真实模型犯过的错，必须在工具边界被指出。
	renamed := pendingChange("renamed-origin", target, 1, model.AuthorAI, model.Patch{
		Document:  model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin-update"},
		Operation: model.PatchPut,
		Content: documentJSON(t, model.CanonFact{
			ID: "hero-origin-update", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.origin",
			PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"孤儿"`),
		}),
	})
	err := engine.Validate(ctx, renamed)
	if !errors.Is(err, ErrStructuralConflict) || !strings.Contains(err.Error(), "cannot declare old_value") {
		t.Fatalf("validate renamed fact: %v", err)
	}
	if _, err := s.GetProposal(ctx, renamed.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("validate wrote the proposal: %v", err)
	}

	update := func(id string) model.Proposal {
		return pendingChange(id, target, 1, model.AuthorAI, model.Patch{
			Document:  model.DocumentRef{Kind: model.DocumentCanon, ID: "hero-origin"},
			Operation: model.PatchPut,
			Content: documentJSON(t, model.CanonFact{
				ID: "hero-origin", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.origin",
				PreviousValue: json.RawMessage(`"农家子"`), Value: json.RawMessage(`"孤儿"`),
			}),
		})
	}
	if err := engine.Validate(ctx, update("valid-update")); err != nil {
		t.Fatalf("validate correct update: %v", err)
	}
	prepared, err := engine.Prepare(ctx, update("committed-update"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	approved, err := Decide(prepared, model.ApprovalApproved, model.Author{Kind: model.AuthorUser, ID: "user-1"}, testTime().Add(time.Minute))
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if _, err := engine.Commit(ctx, approved); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// 当前 Revision 已到 2：Prepare 报冲突，Validate 仍按提案基线 1 校验通过。
	if _, err := engine.Prepare(ctx, update("late-prepare")); !errors.Is(err, model.ErrRevisionConflict) {
		t.Fatalf("prepare after drift: %v", err)
	}
	if err := engine.Validate(ctx, update("late-validate")); err != nil {
		t.Fatalf("validate after drift: %v", err)
	}
}
