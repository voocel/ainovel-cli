package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func setupArcSummaryStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(4); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}}},
			{Index: 2, Chapters: []domain.OutlineEntry{{Title: "三"}, {Title: "四"}}},
		},
	}}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatal(err)
	}
	for chapter := 1; chapter <= 4; chapter++ {
		if err := s.Progress.MarkChapterComplete(chapter, 100, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 2, Scope: "arc", Verdict: "accept", Summary: "第一弧评审"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Title: "第一弧", Summary: "完成", KeyEvents: []string{"事件"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 4, Scope: "arc", Verdict: "accept", Summary: "第二弧评审"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func validArcSummaryArgs(t *testing.T) []byte {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"volume":     1,
		"arc":        2,
		"title":      "入山",
		"summary":    "主角完成入山试炼，确认后续追索方向。",
		"key_events": []string{"通过试炼", "发现旧案线索"},
		"character_snapshots": []map[string]any{
			{"name": "沈渊", "status": "存活", "motivation": "追查旧案"},
		},
		"style_rules": map[string]any{
			"prose": []string{"环境描写优先触觉和嗅觉", "动作戏用短句推进", "心理描写不解释结论"},
			"dialogue": []map[string]any{
				{"name": "沈渊", "rules": []string{"对话极简", "少用疑问句"}},
			},
			"taboos": []string{"避免章末长独白"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func TestSaveArcSummaryPersistsStyleRulesDialogueObjects(t *testing.T) {
	s := setupArcSummaryStore(t)

	tool := NewSaveArcSummaryTool(s)
	if _, err := tool.Execute(context.Background(), validArcSummaryArgs(t)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	rules, err := s.World.LoadStyleRules()
	if err != nil {
		t.Fatalf("LoadStyleRules: %v", err)
	}
	if rules == nil || len(rules.Dialogue) != 1 {
		t.Fatalf("expected one dialogue rule, got %+v", rules)
	}
	if rules.Dialogue[0].Name != "沈渊" || len(rules.Dialogue[0].Rules) != 2 {
		t.Fatalf("unexpected dialogue rule: %+v", rules.Dialogue[0])
	}
}

func TestSaveArcSummaryWritesCompletionMarkerLast(t *testing.T) {
	s := setupArcSummaryStore(t)
	snapshotPath := filepath.Join(s.Dir(), "meta", "snapshots", "v01a02.json")
	if err := os.MkdirAll(snapshotPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := NewSaveArcSummaryTool(s).Execute(context.Background(), validArcSummaryArgs(t)); err == nil || !strings.Contains(err.Error(), "save character snapshots") {
		t.Fatalf("expected snapshot write failure, got %v", err)
	}
	if summary, err := s.Summaries.LoadArcSummary(1, 2); err != nil || summary != nil {
		t.Fatalf("arc summary must remain absent after partial failure, summary=%+v err=%v", summary, err)
	}
}

func TestSaveArcSummaryRetriesCheckpointWithoutOverwriting(t *testing.T) {
	s := setupArcSummaryStore(t)
	checkpointPath := filepath.Join(s.Dir(), "meta", "checkpoints.jsonl")
	if err := os.MkdirAll(checkpointPath, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := NewSaveArcSummaryTool(s)
	args := validArcSummaryArgs(t)

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "checkpoint arc summary") {
		t.Fatalf("expected checkpoint failure, got %v", err)
	}
	if summary, err := s.Summaries.LoadArcSummary(1, 2); err != nil || summary == nil {
		t.Fatalf("semantic summary should already be persisted, summary=%+v err=%v", summary, err)
	}
	if err := os.Remove(checkpointPath); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("idempotent checkpoint retry: %v", err)
	}
	if cp := s.Checkpoints.LatestByStep(domain.ArcScope(1, 2), "arc_summary"); cp == nil {
		t.Fatal("checkpoint retry must finish the aggregate write")
	}
}

func TestSaveArcSummaryReplaysLegacyRulesWithoutHistory(t *testing.T) {
	s := setupArcSummaryStore(t)
	tool := NewSaveArcSummaryTool(s)
	args := validArcSummaryArgs(t)
	if _, err := tool.Execute(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	before, err := s.World.LoadStyleRules()
	if err != nil {
		t.Fatal(err)
	}
	// 旧格式仅有 meta/style_rules.json，没有按弧保存的规则工件。
	if err := os.Remove(filepath.Join(s.Dir(), store.ArcStyleRulesPath(1, 2))); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(t.Context(), args); err != nil {
		t.Fatalf("旧规则也须可完成幂等收尾: %v", err)
	}
	after, err := s.World.LoadArcStyleRules(1, 2)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("补齐历史规则不能改写原规则或时间戳: %+v %v", after, err)
	}
	checkpoints := len(s.Checkpoints.All())
	if err := s.World.SaveArcStyleRules(domain.WritingStyleRules{Volume: 2, Arc: 1, Prose: []string{"后期规则"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(t.Context(), args); err != nil {
		t.Fatalf("后期规则推进不能破坏历史弧的重放: %v", err)
	}
	if len(s.Checkpoints.All()) != checkpoints {
		t.Fatal("其他弧规则推进不应为同一弧重复生成 checkpoint")
	}
}

func TestSaveArcSummaryRejectsDialogueStringArray(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveArcSummaryTool(s)
	args, err := json.Marshal(map[string]any{
		"volume":              1,
		"arc":                 2,
		"title":               "入山",
		"summary":             "主角完成入山试炼，确认后续追索方向。",
		"key_events":          []string{"通过试炼"},
		"character_snapshots": []map[string]any{},
		"style_rules": map[string]any{
			"prose":    []string{"环境描写优先触觉和嗅觉"},
			"dialogue": []string{"沈渊对话极简"},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "style_rules.dialogue") {
		t.Fatalf("expected style_rules.dialogue validation error, got %v", err)
	}
}

func TestSaveArcSummaryRequiresStyleRules(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	args := json.RawMessage(`{
		"volume": 1,
		"arc": 2,
		"title": "入山",
		"summary": "主角完成入山试炼。",
		"key_events": ["通过试炼"],
		"character_snapshots": []
	}`)
	if _, err := NewSaveArcSummaryTool(s).Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "style_rules is required") {
		t.Fatalf("expected missing style_rules error, got %v", err)
	}
}
