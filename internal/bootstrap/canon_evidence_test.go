package bootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	novelapp "github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/derive"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func editCanonEvidence(t *testing.T, api *testApp, project projectdoc.Snapshot, id string, patches ...model.Patch) projectdoc.Snapshot {
	t.Helper()
	at := testTime().Add(time.Duration(project.Revision) * time.Hour)
	_, err := api.changes.CommitUser(context.Background(), model.Proposal{
		ID: id, Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: project.ID}, BaseRevision: project.Revision,
		Author: model.Author{Kind: model.AuthorUser, ID: "user-1"}, Reason: "核对事实",
		Patches: patches, ApprovalState: model.ApprovalPending, CreatedAt: at,
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := api.Projects.Project(context.Background(), project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func canonEvidencePatch(t *testing.T, fact model.CanonFact) model.Patch {
	t.Helper()
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	return model.Patch{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: fact.ID}, Operation: model.PatchPut, Content: raw}
}

func TestCanonChangesInvalidateReviewButFutureFactsDoNot(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{ProjectID: "canon-evidence", UserID: "user-1", Premise: "邮差送信", Chapters: 3, WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: testTime()}
	if _, err := api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatal(err)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"chapter-chapter-plan-1"}
	basis, err := novelapp.ReviewBasis(project, ids)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(model.ReviewRangeInput{ChapterIDs: ids, Basis: basis})
	story, err := derive.BuildStoryContext(derive.ProjectContent{ID: project.ID, Revision: project.Revision, Intent: project.Intent, Plan: project.Plan, Entities: project.Entities, Canon: project.Canon, Manuscript: project.Manuscript}, model.OperationReviewRange, input)
	if err != nil {
		t.Fatal(err)
	}
	var contextFacts, basisFacts []string
	for _, document := range story.Documents {
		if document.Ref.Kind == model.DocumentCanon {
			contextFacts = append(contextFacts, document.Ref.ID)
		}
	}
	for _, document := range basis.Documents {
		if document.Ref.Kind == model.DocumentCanon {
			basisFacts = append(basisFacts, document.Ref.ID)
		}
	}
	if !slices.Equal(contextFacts, basisFacts) || len(contextFacts) != 1 {
		t.Fatalf("context facts %v, evidence facts %v", contextFacts, basisFacts)
	}
	assertValid := func(project projectdoc.Snapshot, basis model.EvidenceBasis, valid bool) {
		t.Helper()
		target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: project.ID}
		got, err := api.Evidence.Valid(ctx, target, project.Revision, basis)
		if err != nil || got != valid {
			t.Fatalf("valid = %v, err = %v, want valid %v", got, err, valid)
		}
		// The public reader translates the commit engine's mismatch into invalidity.
		err = api.changes.VerifyBasis(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: project.ID}, basis, project.Revision)
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
	basis, err = novelapp.ReviewBasis(project, ids)
	if err != nil {
		t.Fatal(err)
	}
	added := model.CanonFact{ID: "new-fact", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.mood", Value: json.RawMessage(`"疑惑"`), SourceChapterID: ids[0]}
	project = editCanonEvidence(t, api, project, "added-fact", canonEvidencePatch(t, added))
	assertValid(project, basis, false)
	basis, err = novelapp.ReviewBasis(project, ids)
	if err != nil {
		t.Fatal(err)
	}
	project = editCanonEvidence(t, api, project, "deleted-fact", model.Patch{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: added.ID}, Operation: model.PatchDelete})
	assertValid(project, basis, false)
}

func TestQuickWriteReviewsAgainAfterCanonOnlyChange(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{ProjectID: "canon-rereview", UserID: "user-1", Premise: "邮差送信", Chapters: 1, WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: testTime()}
	if _, err := api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatal(err)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	fact := project.Canon[0]
	fact.PreviousValue, fact.Value = fact.Value, json.RawMessage(`"主角其实已经死亡"`)
	project = editCanonEvidence(t, api, project, "correct-fact", canonEvidencePatch(t, fact))
	verdicts, err := api.Reviews.ListVerdicts(ctx, project)
	if err != nil || len(verdicts) != 0 {
		t.Fatalf("old verdicts still valid: %v, %v", verdicts, err)
	}
	before := executor.calls
	result, err := api.Novels.QuickWrite(ctx, command)
	if err != nil || result.RunState != model.RunCompleted || executor.calls != before+1 {
		t.Fatalf("result = %+v, calls %d -> %d, err %v", result, before, executor.calls, err)
	}
}

func TestCanonSourceHistorySurvivesStateMovingToLaterChapter(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{ProjectID: "canon-history", UserID: "user-1", Premise: "邮差送信", Chapters: 2, WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: testTime()}
	if _, err := api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatal(err)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := project.Manuscript[0]
	state := model.CanonFact{ID: "hero-state", Kind: model.CanonState, SubjectID: "hero", Predicate: "state.location", Value: json.RawMessage(`"第一站"`), SourceChapterID: first.ID}
	project = editCanonEvidence(t, api, project, "state-first", canonEvidencePatch(t, state))
	state.PreviousValue, state.Value = state.Value, json.RawMessage(`"第二站"`)
	state.SourceChapterID = project.Manuscript[1].ID
	project = editCanonEvidence(t, api, project, "state-second", canonEvidencePatch(t, state), model.Patch{Document: model.DocumentRef{Kind: model.DocumentCanon, ID: first.ID + "-outcome"}, Operation: model.PatchDelete})
	if gaps := novelapp.CanonGaps(project); len(gaps) != 0 {
		t.Fatalf("state moved but chapter is already recorded: %+v", gaps)
	}
	first.Blocks[0].Text = "用户修订了第一站的故事"
	raw, _ := json.Marshal(first)
	project = editCanonEvidence(t, api, project, "edit-source", model.Patch{Document: model.DocumentRef{Kind: model.DocumentManuscript, ID: first.ID}, Operation: model.PatchPut, Content: raw})
	if gaps := novelapp.CanonGaps(project); len(gaps) != 1 || !gaps[0].Unrecorded || gaps[0].ChapterID != first.ID {
		t.Fatalf("edited old source must be checked again: %+v", gaps)
	}
}
