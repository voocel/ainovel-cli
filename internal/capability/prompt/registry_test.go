package prompt

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestReloadPersistsImmutableExecutionProfiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ainovel.db")
	authorityStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	registry := NewRegistry(authorityStore)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	request := testCompileRequest(t)
	first, err := registry.Reload(ctx, request, now)
	if err != nil {
		t.Fatalf("reload first profile: %v", err)
	}
	if repeated, err := registry.Reload(ctx, request, now.Add(time.Minute)); err != nil || repeated.ProfileDigest != first.ProfileDigest {
		t.Fatalf("idempotent reload = %q, %v", repeated.ProfileDigest, err)
	}

	request.Task = json.RawMessage(`{"chapter_plan_id":"chapter-plan-2"}`)
	second, err := registry.Reload(ctx, request, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("reload second profile: %v", err)
	}
	if first.ProfileDigest == second.ProfileDigest {
		t.Fatal("changed task reused old execution profile digest")
	}
	diff, err := registry.Diff(ctx, first.ProfileDigest, second.ProfileDigest)
	if err != nil {
		t.Fatalf("diff profiles: %v", err)
	}
	if diff.StablePrefixChanged || !diff.DynamicTailChanged || diff.ToolsChanged {
		t.Fatalf("diff = %#v", diff)
	}
	if diff.StablePrefixDiff != nil || diff.DynamicTailDiff == nil ||
		len(diff.DynamicTailDiff.Removed) == 0 || len(diff.DynamicTailDiff.Added) == 0 {
		t.Fatalf("text diff = %#v", diff)
	}
	if err := authorityStore.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	authorityStore, err = store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer authorityStore.Close()
	loaded, err := NewRegistry(authorityStore).Load(ctx, first.ProfileDigest)
	if err != nil {
		t.Fatalf("load old profile after reopen: %v", err)
	}
	if loaded.Snapshot.ExecutionProfileDigest != first.ProfileDigest || loaded.PromptDigest != first.PromptDigest {
		t.Fatalf("loaded profile = %#v", loaded)
	}
}

func TestLintReportsDeterministicPackOverlayConflict(t *testing.T) {
	request := testCompileRequest(t)
	request.Packs[1].Manifest.PromptOverlays = map[string]string{
		"writer.chapter_draft": "增加悬念",
		"editor.story_review":  "未激活的审阅规则",
	}
	diagnostics, err := Lint(request)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if len(diagnostics) != 2 || diagnostics[0].Code != "inactive_pack_overlay" || diagnostics[1].Code != "multiple_pack_overlays" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestRegistryLintUsesFrozenExecutionProfileSources(t *testing.T) {
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	request := testCompileRequest(t)
	request.Packs[1].Manifest.PromptOverlays = map[string]string{
		"writer.chapter_draft": "增加悬念",
		"editor.story_review":  "未激活的审阅规则",
	}
	registry := NewRegistry(authorityStore)
	compiled, err := registry.Reload(ctx, request, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	diagnostics, err := registry.Lint(ctx, compiled.ProfileDigest)
	if err != nil {
		t.Fatalf("lint stored profile: %v", err)
	}
	if len(diagnostics) != 2 || diagnostics[0].Code != "inactive_pack_overlay" || diagnostics[1].Code != "multiple_pack_overlays" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}
