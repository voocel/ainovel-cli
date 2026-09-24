package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

func TestReplaceChapterBlockUsesStableIDAndWorkspaceVersion(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	run := model.CreationRun{
		ID: "run:book-1", ProjectID: "book-1",
		Goal: model.NovelGoal{Premise: "测试创作", TargetChapters: 1}.Goal(),
		Strategy: model.CreationRunStrategy{
			PlanWindowChapters: 1, ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: 1,
		},
		Preset: model.CreationRunPreset{Source: "test", Digest: "test-preset", Approval: model.ApprovalAuto},
		State:  model.RunRunning, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateCreationRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	input := json.RawMessage(`{"chapter_id":"chapter-1","chapter_plan_id":"chapter-plan-1","chapter_number":1,"findings":["结尾仓促"]}`)
	operation := model.Operation{
		ID: "rewrite-1", Kind: model.OperationRewriteChapter,
		Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book-1"},
		State:  model.OperationQueued, RunID: run.ID,
		Snapshot: model.ExecutionSnapshot{
			Executor: "llm.agent@1", BaseRevision: 1, InputDigest: model.Digest(input),
			ConfigDigest: "execution-profile", ApprovalPolicy: model.ApprovalManual,
		},
		Input: input, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	claimed, err := authorityStore.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}

	workspace := New(authorityStore)
	chapter := model.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门", Author: model.AuthorAI,
		Blocks: []model.ManuscriptBlock{{ID: "p-1", Text: "旧段落"}, {ID: "p-2", Text: "保持不变"}},
	}
	artifact, err := workspace.PutChapter(ctx, operation.ID, "chapter/chapter-1", chapter, nil, claimed.Attempt, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("put chapter: %v", err)
	}
	artifact, err = workspace.ReplaceChapterBlock(
		ctx, operation.ID, artifact.Key, "p-1", "新段落", artifact.Version, claimed.Attempt, now.Add(3*time.Second),
	)
	if err != nil {
		t.Fatalf("replace block: %v", err)
	}
	if artifact.Version != 2 {
		t.Fatalf("workspace version = %d, want 2", artifact.Version)
	}
	var updated model.ManuscriptChapter
	if err := json.Unmarshal(artifact.Content, &updated); err != nil {
		t.Fatalf("decode updated chapter: %v", err)
	}
	if updated.Blocks[0].ID != "p-1" || updated.Blocks[0].Text != "新段落" || updated.Blocks[1].Text != "保持不变" {
		t.Fatalf("updated blocks = %#v", updated.Blocks)
	}
	if _, err := workspace.ReplaceChapterBlock(
		ctx, operation.ID, artifact.Key, "p-1", "过期覆盖", 1, claimed.Attempt, now.Add(4*time.Second),
	); !errors.Is(err, model.ErrWorkspaceConflict) {
		t.Fatalf("stale edit error = %v, want ErrWorkspaceConflict", err)
	}
	if _, err := workspace.ReplaceChapterBlock(
		ctx, operation.ID, artifact.Key, "missing", "内容", 2, claimed.Attempt, now.Add(5*time.Second),
	); !errors.Is(err, ErrBlockNotFound) {
		t.Fatalf("missing block error = %v, want ErrBlockNotFound", err)
	}
}
