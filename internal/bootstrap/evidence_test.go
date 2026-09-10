package bootstrap_test

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"time"

	novelapp "github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func basisKeys(basis model.EvidenceBasis) []string {
	keys := make([]string, 0, len(basis.Documents))
	for _, document := range basis.Documents {
		keys = append(keys, document.Ref.Key()+"@"+strconv.FormatInt(int64(document.Revision), 10))
	}
	return keys
}

func TestBasisForIncludesDependencyClosureAndScopes(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "basis-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 1, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	if _, err := api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatalf("quick write: %v", err)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	basis, err := novelapp.ReviewBasis(project, []string{"chapter-chapter-plan-1"})
	if err != nil {
		t.Fatalf("review basis: %v", err)
	}
	// 目标正文 + 结构依赖闭包（章节计划及其祖先、随章实体）+ Intent，各取最后变化 revision。
	want := []string{
		"canon:chapter-chapter-plan-1-outcome@3",
		"entity:hero@2", "intent:root@1", "manuscript:chapter-chapter-plan-1@3",
		"plan:arc-1@2", "plan:chapter-plan-1@2", "plan:volume-1@2",
	}
	if got := basisKeys(basis); !slices.Equal(got, want) {
		t.Fatalf("basis documents = %v, want %v", got, want)
	}
	if len(basis.Scopes) != 2 || basis.Scopes[1].Kind != model.ScopeDirective ||
		basis.Scopes[1].Target.ChapterNumber != 1 ||
		!slices.Equal(basis.Scopes[1].Target.PlanNodeIDs, []string{"chapter-plan-1", "arc-1", "volume-1"}) ||
		basis.Scopes[1].Digest != model.ScopeDigest(nil) {
		t.Fatalf("basis scopes = %#v", basis.Scopes)
	}
	if _, err := novelapp.ReviewBasis(project, []string{"chapter-missing"}); err == nil {
		t.Fatalf("missing chapter must be rejected")
	}
}

func TestEvidenceValidityDetectsDocumentScopeAndArtifactDrift(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: testTime()}
	api := newQuickTestApp(t, executor)
	command := novelapp.QuickWriteCommand{
		ProjectID: "drift-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 1, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	if _, err := api.Novels.QuickWrite(ctx, command); err != nil {
		t.Fatalf("quick write: %v", err)
	}
	project, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	basis, err := novelapp.ReviewBasis(project, []string{"chapter-chapter-plan-1"})
	if err != nil {
		t.Fatalf("review basis: %v", err)
	}
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: project.ID}
	if valid, err := api.Evidence.Valid(ctx, target, project.Revision, basis); err != nil || !valid {
		t.Fatalf("fresh basis valid = %v, %v", valid, err)
	}

	// 不相干的要求（只覆盖第 2 章起）不使第 1 章的证据失效。
	addDirective := func(id, scope string, at time.Time) projectdoc.Snapshot {
		t.Helper()
		if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
			ProjectID: command.ProjectID, ChangeID: "add-" + id, UserID: "user-1", DirectiveID: id,
			Scope: scope, Text: "要求 " + id, Reason: "测试", CreatedAt: at,
		}); err != nil {
			t.Fatalf("add directive %s: %v", id, err)
		}
		project, err := api.Projects.Project(ctx, command.ProjectID, 0)
		if err != nil {
			t.Fatalf("read project: %v", err)
		}
		return project
	}
	unrelated := addDirective("later", "from_chapter:2", testTime().Add(time.Hour))
	if valid, err := api.Evidence.Valid(ctx, target, unrelated.Revision, basis); err != nil || !valid {
		t.Fatalf("unrelated directive valid = %v, %v", valid, err)
	}
	related := addDirective("hook", "from_chapter:1", testTime().Add(2*time.Hour))
	scopeBasis := model.EvidenceBasis{Scopes: basis.Scopes}
	valid, err := api.Evidence.Valid(ctx, target, related.Revision, scopeBasis)
	if err != nil || valid {
		t.Fatalf("related directive valid = %v, %v", valid, err)
	}

	// 依赖闭包内的文档改版（章节计划）使证据失效。
	node, _ := json.Marshal(model.PlanNode{
		ID: "chapter-plan-1", Kind: model.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第1章", Summary: "改写后的章节摘要",
	})
	if _, err := api.changes.CommitUser(ctx, model.Proposal{
		ID: "edit-plan", Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID},
		BaseRevision: related.Revision, Author: model.Author{Kind: model.AuthorUser, ID: "user-1"}, Reason: "改摘要",
		Patches: []model.Patch{{
			Document: model.DocumentRef{Kind: model.DocumentPlan, ID: "chapter-plan-1"}, Operation: model.PatchPut, Content: node,
		}},
		ApprovalState: model.ApprovalPending, CreatedAt: testTime().Add(3 * time.Hour),
	}, testTime().Add(3*time.Hour)); err != nil {
		t.Fatalf("edit plan: %v", err)
	}
	edited, err := api.Projects.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	documentBasis := model.EvidenceBasis{Documents: basis.Documents}
	valid, err = api.Evidence.Valid(ctx, target, edited.Revision, documentBasis)
	if err != nil || valid {
		t.Fatalf("edited plan valid = %v, %v", valid, err)
	}

	// 工件条目：元数据不存在即失效。
	withArtifact := basis
	withArtifact.Artifacts = []model.ArtifactRef{{ID: "op/cover", Digest: model.Digest([]byte("missing artifact"))}}
	valid, err = api.Evidence.Valid(ctx, target, project.Revision, withArtifact)
	if err != nil || valid {
		t.Fatalf("missing artifact valid = %v, %v", valid, err)
	}
}
