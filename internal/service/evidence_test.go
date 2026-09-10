package service

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func basisKeys(basis domain.EvidenceBasis) []string {
	keys := make([]string, 0, len(basis.Documents))
	for _, document := range basis.Documents {
		keys = append(keys, document.Ref.Key()+"@"+strconv.FormatInt(int64(document.Revision), 10))
	}
	return keys
}

func TestBasisForIncludesDependencyClosureAndScopes(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "basis-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 1, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	if _, err := api.QuickWrite(ctx, command); err != nil {
		t.Fatalf("quick write: %v", err)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	basis, err := reviewBasis(project, []string{"chapter-chapter-plan-1"})
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
	if len(basis.Scopes) != 2 || basis.Scopes[1].Kind != domain.ScopeDirective ||
		basis.Scopes[1].Target.ChapterNumber != 1 ||
		!slices.Equal(basis.Scopes[1].Target.PlanNodeIDs, []string{"chapter-plan-1", "arc-1", "volume-1"}) ||
		basis.Scopes[1].Digest != domain.ScopeDigest(nil) {
		t.Fatalf("basis scopes = %#v", basis.Scopes)
	}
	if _, err := reviewBasis(project, []string{"chapter-missing"}); err == nil {
		t.Fatalf("missing chapter must be rejected")
	}
}

func TestStaleBasisEntriesDetectsDocumentScopeAndArtifactDrift(t *testing.T) {
	ctx := context.Background()
	executor := &scriptedQuickExecutor{now: serviceTime()}
	api := newQuickTestService(t, executor)
	command := QuickWriteCommand{
		ProjectID: "drift-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 1, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: serviceTime(),
	}
	if _, err := api.QuickWrite(ctx, command); err != nil {
		t.Fatalf("quick write: %v", err)
	}
	project, err := api.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	basis, err := reviewBasis(project, []string{"chapter-chapter-plan-1"})
	if err != nil {
		t.Fatalf("review basis: %v", err)
	}
	if stale, err := api.staleBasis(ctx, project, basis); err != nil || len(stale) != 0 {
		t.Fatalf("fresh basis stale = %v, %v", stale, err)
	}

	// 不相干的要求（只覆盖第 2 章起）不使第 1 章的证据失效。
	addDirective := func(id, scope string, at time.Time) ProjectSnapshot {
		t.Helper()
		if _, err := api.AddDirective(ctx, AddDirectiveCommand{
			ProjectID: command.ProjectID, ChangeID: "add-" + id, UserID: "user-1", DirectiveID: id,
			Scope: scope, Text: "要求 " + id, Reason: "测试", CreatedAt: at,
		}); err != nil {
			t.Fatalf("add directive %s: %v", id, err)
		}
		project, err := api.Project(ctx, command.ProjectID, 0)
		if err != nil {
			t.Fatalf("read project: %v", err)
		}
		return project
	}
	unrelated := addDirective("later", "from_chapter:2", serviceTime().Add(time.Hour))
	if stale, err := api.staleBasis(ctx, unrelated, basis); err != nil || len(stale) != 0 {
		t.Fatalf("unrelated directive stale = %v, %v", stale, err)
	}
	related := addDirective("hook", "from_chapter:1", serviceTime().Add(2*time.Hour))
	stale, err := api.staleBasis(ctx, related, basis)
	if err != nil || len(stale) != 1 || stale[0] != "directives covering chapter 1 changed" {
		t.Fatalf("related directive stale = %v, %v", stale, err)
	}

	// 依赖闭包内的文档改版（章节计划）使证据失效。
	node, _ := json.Marshal(domain.PlanNode{
		ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第1章", Summary: "改写后的章节摘要",
	})
	if _, err := api.commitUserProposal(ctx, domain.Proposal{
		ID: "edit-plan", Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID},
		BaseRevision: related.Revision, Author: domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, Reason: "改摘要",
		Patches: []domain.Patch{{
			Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "chapter-plan-1"}, Operation: domain.PatchPut, Content: node,
		}},
		ApprovalState: domain.ApprovalPending, CreatedAt: serviceTime().Add(3 * time.Hour),
	}, serviceTime().Add(3*time.Hour)); err != nil {
		t.Fatalf("edit plan: %v", err)
	}
	edited, err := api.Project(ctx, command.ProjectID, 0)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	stale, err = api.staleBasis(ctx, edited, basis)
	if err != nil || len(stale) != 2 || !strings.HasPrefix(stale[1], "plan:chapter-plan-1 changed at revision") {
		t.Fatalf("edited plan stale = %v, %v", stale, err)
	}

	// 工件条目：元数据不存在即失效。
	withArtifact := basis
	withArtifact.Artifacts = []domain.ArtifactRef{{ID: "op/cover", Digest: "d"}}
	stale, err = api.staleBasis(ctx, project, withArtifact)
	if err != nil || len(stale) != 1 || stale[0] != "artifact op/cover deleted" {
		t.Fatalf("missing artifact stale = %v, %v", stale, err)
	}
}
