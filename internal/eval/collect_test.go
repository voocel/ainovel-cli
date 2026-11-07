package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
)

func TestCollectReadsStyleUsageAndToolCalls(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		TotalChapters:     5,
		CompletedChapters: []int{3, 1, 5, 2, 4},
	}
	if err := s.Progress.Save(progress); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "第一章 起风"},
		{Chapter: 2, Title: "破局"},
		{Chapter: 3, Title: "第三章 入局"},
		{Chapter: 4, Title: "追问"},
		{Chapter: 5, Title: "第五章 回声"},
	}); err != nil {
		t.Fatalf("save outline: %v", err)
	}
	if err := s.Characters.Save([]domain.Character{{Name: "林墨", Aliases: []string{"药童"}}}); err != nil {
		t.Fatalf("save characters: %v", err)
	}
	for ch := 1; ch <= 5; ch++ {
		text := "# 标题\n\n不是风停了，而是所有人都屏住呼吸。沉默了片刻，林墨握紧药囊。"
		if err := s.Drafts.SaveFinalChapter(ch, text); err != nil {
			t.Fatalf("save chapter %d: %v", ch, err)
		}
	}
	if err := s.Usage.Save(domain.UsageState{
		Overall:      domain.AgentUsageTotals{Input: 100, Output: 40, Cost: 0.12},
		MissingUsage: 1,
	}); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	writeSessionLine(t, dir, "meta/sessions/agents/writer-ch01.jsonl", agentcore.Message{
		Role: litellm.RoleAssistant,
		Blocks: []litellm.Block{
			litellm.ToolUseBlock{ID: "call_1", Name: "dispatch", Arguments: "{}"},
			litellm.ToolUseBlock{ID: "call_2", Name: "novel_context", Arguments: "{}"},
		},
	})

	col := Collect(dir, nil)
	if len(col.LoadErrors) != 0 {
		t.Fatalf("不应有读取错误: %v", col.LoadErrors)
	}
	if col.Style.Status != "ok" || col.Style.Stats == nil {
		t.Fatalf("stylestat 应可计算，得到 status=%s stats=%v", col.Style.Status, col.Style.Stats)
	}
	if col.Style.Stats.TitleFormats == nil {
		t.Fatal("标题混用应被 stylestat 捕获")
	}
	if !col.Usage.UsageRecorded || col.Usage.Input != 100 || col.Usage.Output != 40 || col.Usage.CostUSD != 0.12 {
		t.Fatalf("usage 读取不正确: %+v", col.Usage)
	}
	if col.ToolCalls != 2 {
		t.Fatalf("tool calls = %d want 2", col.ToolCalls)
	}
}

func TestCollectStyleInsufficientSample(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Progress.Save(&domain.Progress{CompletedChapters: []int{1}}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(1, "只有一章"); err != nil {
		t.Fatalf("save chapter: %v", err)
	}
	col := Collect(dir, nil)
	if col.Style.Status != "insufficient_sample" {
		t.Fatalf("一章样本应 insufficient_sample，得到 %s", col.Style.Status)
	}
}

func TestCollectFailsLoudWhenCompletedChapterMissing(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Progress.Save(&domain.Progress{CompletedChapters: []int{1}}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	col := Collect(dir, nil)
	if !containsString(col.LoadErrors, "progress 标记已完成但终稿为空") {
		t.Fatalf("缺终稿应进入 LoadErrors，实际 %v", col.LoadErrors)
	}
}

func TestChapterTitleUsesLayeredEntryChapter(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{
		{
			Index: 1,
			Arcs: []domain.ArcOutline{
				{Index: 1}, // 未展开 arc 不应让后续章节位置漂移
				{
					Index: 2,
					Chapters: []domain.OutlineEntry{
						{Chapter: 7, Title: "第七章 真标题"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("save layered outline: %v", err)
	}

	got := chapterTitle(s, 7, "# 正文兜底标题\n\n内容", func(string, error) {})
	if got != "第七章 真标题" {
		t.Fatalf("应按 entry.Chapter 匹配分层标题，得到 %q", got)
	}
}

func TestChapterTitleUsesCommittedTitle(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "计划标题"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Title: "终稿标题"}); err != nil {
		t.Fatal(err)
	}
	got := chapterTitle(s, 1, "# 正文标题\n\n内容", func(string, error) {})
	if got != "终稿标题" {
		t.Fatalf("chapterTitle = %q, want committed title", got)
	}
}

func containsString(items []string, sub string) bool {
	for _, item := range items {
		if strings.Contains(item, sub) {
			return true
		}
	}
	return false
}

func writeSessionLine(t *testing.T, root, rel string, msg agentcore.Message) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
}
