package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/derive"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func editCanonEvidence(t *testing.T, api *Service, project ProjectSnapshot, id string, patches ...domain.Patch) ProjectSnapshot {
	t.Helper()
	at := serviceTime().Add(time.Duration(project.Revision) * time.Hour)
	_, err := api.commitUserProposal(context.Background(), domain.Proposal{
		ID: id, Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: project.ID}, BaseRevision: project.Revision,
		Author: domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, Reason: "核对事实",
		Patches: patches, ApprovalState: domain.ApprovalPending, CreatedAt: at,
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := api.Project(context.Background(), project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func canonEvidencePatch(t *testing.T, fact domain.CanonFact) domain.Patch {
	t.Helper()
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: fact.ID}, Operation: domain.PatchPut, Content: raw}
}

func TestCanonChangesInvalidateReviewButFutureFactsDoNot(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{ProjectID: "canon-evidence", UserID: "user-1", Premise: "邮差送信", Chapters: 3, WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: serviceTime()}
	if _, err := api.QuickWrite(ctx, command); err != nil {
		t.Fatal(err)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"chapter-chapter-plan-1"}
	basis, err := reviewBasis(project, ids)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(domain.ReviewRangeInput{ChapterIDs: ids, Basis: basis})
	story, err := derive.BuildStoryContext(derive.ProjectContent{ID: project.ID, Revision: project.Revision, Intent: project.Intent, Plan: project.Plan, Entities: project.Entities, Canon: project.Canon, Manuscript: project.Manuscript}, domain.OperationReviewRange, input)
	if err != nil {
		t.Fatal(err)
	}
	var contextFacts, basisFacts []string
	for _, document := range story.Documents {
		if document.Ref.Kind == domain.DocumentCanon {
			contextFacts = append(contextFacts, document.Ref.ID)
		}
	}
	for _, document := range basis.Documents {
		if document.Ref.Kind == domain.DocumentCanon {
			basisFacts = append(basisFacts, document.Ref.ID)
		}
	}
	if !slices.Equal(contextFacts, basisFacts) || len(contextFacts) != 1 {
		t.Fatalf("context facts %v, evidence facts %v", contextFacts, basisFacts)
	}
	assertValid := func(project ProjectSnapshot, basis domain.EvidenceBasis, valid bool) {
		t.Helper()
		stale, err := api.staleBasis(ctx, project, basis)
		if err != nil || (len(stale) == 0) != valid {
			t.Fatalf("stale = %v, err = %v, want valid %v", stale, err, valid)
		}
		err = api.changes.VerifyBasis(ctx, domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: project.ID}, basis, project.Revision)
		if valid && err != nil || !valid && !errors.Is(err, change.ErrBasisMismatch) {
			t.Fatalf("VerifyBasis = %v, want valid %v", err, valid)
		}
	}
	// 同一角色未来章节的事实不能使第一章的窗口失效。
	future := project.Canon[2]
	future.PreviousValue, future.Value = future.Value, json.RawMessage(`"第三章的新事实"`)
	project = editCanonEvidence(t, api, project, "future-fact", canonEvidencePatch(t, future))
	assertValid(project, basis, true)
	first := project.Canon[0]
	first.PreviousValue, first.Value = first.Value, json.RawMessage(`"第一章事实已改变"`)
	project = editCanonEvidence(t, api, project, "changed-fact", canonEvidencePatch(t, first))
	assertValid(project, basis, false)
	basis, err = reviewBasis(project, ids)
	if err != nil {
		t.Fatal(err)
	}
	added := domain.CanonFact{ID: "new-fact", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.mood", Value: json.RawMessage(`"疑惑"`), SourceChapterID: ids[0]}
	project = editCanonEvidence(t, api, project, "added-fact", canonEvidencePatch(t, added))
	assertValid(project, basis, false)
	basis, err = reviewBasis(project, ids)
	if err != nil {
		t.Fatal(err)
	}
	project = editCanonEvidence(t, api, project, "deleted-fact", domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: added.ID}, Operation: domain.PatchDelete})
	assertValid(project, basis, false)
}

func TestQuickWriteReviewsAgainAfterCanonOnlyChange(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{ProjectID: "canon-rereview", UserID: "user-1", Premise: "邮差送信", Chapters: 1, WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: serviceTime()}
	if _, err := api.QuickWrite(ctx, command); err != nil {
		t.Fatal(err)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	fact := project.Canon[0]
	fact.PreviousValue, fact.Value = fact.Value, json.RawMessage(`"主角其实已经死亡"`)
	project = editCanonEvidence(t, api, project, "correct-fact", canonEvidencePatch(t, fact))
	verdicts, err := api.listVerdicts(ctx, project)
	if err != nil || len(verdicts) != 0 {
		t.Fatalf("old verdicts still valid: %v, %v", verdicts, err)
	}
	before := executor.calls
	result, err := api.QuickWrite(ctx, command)
	if err != nil || result.RunState != domain.RunCompleted || executor.calls != before+1 {
		t.Fatalf("result = %+v, calls %d -> %d, err %v", result, before, executor.calls, err)
	}
}

func TestCanonSourceHistorySurvivesStateMovingToLaterChapter(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{ProjectID: "canon-history", UserID: "user-1", Premise: "邮差送信", Chapters: 2, WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: serviceTime()}
	if _, err := api.QuickWrite(ctx, command); err != nil {
		t.Fatal(err)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := project.Manuscript[0]
	state := domain.CanonFact{ID: "hero-state", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.location", Value: json.RawMessage(`"第一站"`), SourceChapterID: first.ID}
	project = editCanonEvidence(t, api, project, "state-first", canonEvidencePatch(t, state))
	state.PreviousValue, state.Value = state.Value, json.RawMessage(`"第二站"`)
	state.SourceChapterID = project.Manuscript[1].ID
	project = editCanonEvidence(t, api, project, "state-second", canonEvidencePatch(t, state), domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: first.ID + "-outcome"}, Operation: domain.PatchDelete})
	if gaps := canonGaps(project); len(gaps) != 0 {
		t.Fatalf("state moved but chapter is already recorded: %+v", gaps)
	}
	first.Blocks[0].Text = "用户修订了第一站的故事"
	raw, _ := json.Marshal(first)
	project = editCanonEvidence(t, api, project, "edit-source", domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: first.ID}, Operation: domain.PatchPut, Content: raw})
	if gaps := canonGaps(project); len(gaps) != 1 || !gaps[0].Unrecorded || gaps[0].ChapterID != first.ID {
		t.Fatalf("edited old source must be checked again: %+v", gaps)
	}
}
