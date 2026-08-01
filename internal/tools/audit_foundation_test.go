package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
)

func completeShortFoundation(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init("审查测试", 1, domain.GenreNovel); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SavePremise("# 审查测试\n\n## 主角目标\n林舟求生"); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "求生", CoreEvent: "林舟脱险"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Characters.Save([]domain.Character{{Name: "林舟", Role: "主角", Description: "求生者"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "城门夜禁", Boundary: "入夜关闭"}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuditFoundationControlsWritingTransition(t *testing.T) {
	s := completeShortFoundation(t)
	tool := NewAuditFoundationTool(s)
	if !tool.StrictSchema() {
		t.Fatal("audit_foundation must use strict schema")
	}
	if err := llmcontract.ValidateStrictReady(tool.Schema()); err != nil {
		t.Fatalf("audit_foundation schema is not strict-ready: %v", err)
	}
	missing, err := s.FoundationMissing()
	if err != nil || len(missing) != 1 || missing[0] != "foundation_audit" {
		t.Fatalf("expected only foundation_audit, got %v, err=%v", missing, err)
	}
	fingerprint, err := s.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	failed, _ := json.Marshal(map[string]any{
		"fingerprint": fingerprint,
		"ready":       false,
		"summary":     "角色名不一致",
		"issues": []map[string]any{{
			"artifact": "characters", "description": "人物不一致", "evidence": "前提为林舟，角色表为他人", "suggestion": "统一角色",
		}},
	})
	if _, err := tool.Execute(context.Background(), failed); err != nil {
		t.Fatalf("failed audit should persist guidance: %v", err)
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseWriting {
		t.Fatal("failed audit must not enter writing")
	}

	passed, _ := json.Marshal(map[string]any{
		"fingerprint": fingerprint,
		"ready":       true,
		"summary":     "基础设定一致",
		"issues":      []any{},
	})
	if _, err := tool.Execute(context.Background(), passed); err != nil {
		t.Fatalf("passed audit: %v", err)
	}
	if p, _ := s.Progress.Load(); p.Phase != domain.PhaseWriting {
		t.Fatalf("passed audit must enter writing, got %s", p.Phase)
	}
}

func TestSaveFoundationWaitsForSemanticAudit(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init("test", 1, domain.GenreNovel); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SavePremise("# test"); err != nil {
		t.Fatal(err)
	}
	if err := s.Characters.Save([]domain.Character{{Name: "林舟", Role: "主角"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "夜禁", Boundary: "入夜"}}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"type": "outline", "scale": "short",
		"content": []map[string]any{{"chapter": 1, "title": "开端", "core_event": "林舟入城", "hook": "夜禁", "scenes": []string{"入城"}}},
	})
	result, err := NewSaveFoundationTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Ready     bool     `json:"foundation_ready"`
		Remaining []string `json:"remaining"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Ready || len(payload.Remaining) != 1 || payload.Remaining[0] != "foundation_audit" {
		t.Fatalf("save_foundation must wait for audit: %+v", payload)
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseWriting {
		t.Fatal("save_foundation must not enter writing before audit")
	}
}

func TestAuditFoundationRejectsStaleFingerprint(t *testing.T) {
	s := completeShortFoundation(t)
	fingerprint, err := s.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SavePremise("# 已修改的版本"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"fingerprint": fingerprint, "ready": true, "summary": "通过", "issues": []any{},
	})
	if _, err := NewAuditFoundationTool(s).Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "重新调用 novel_context") {
		t.Fatalf("expected stale fingerprint rejection, got %v", err)
	}
}
