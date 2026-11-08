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
	"github.com/voocel/ainovel-cli/internal/stylestat"
	"github.com/voocel/ainovel-cli/internal/tools"
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
		body := "不是风停了，而是所有人都屏住呼吸。沉默了片刻，林墨握紧药囊。"
		text := "# 标题\n\n" + body
		if ch%2 == 1 {
			text = domain.ChapterText(ch, "标题", body)
		}
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
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{Name: "dispatch"}),
			agentcore.ToolCallBlock(agentcore.ToolCall{Name: "novel_context"}),
		},
	})

	col := Collect(dir, nil)
	if len(col.LoadErrors) != 0 {
		t.Fatalf("不应有读取错误: %v", col.LoadErrors)
	}
	if col.Style.Status != "ok" || col.Style.Stats == nil {
		t.Fatalf("stylestat 应可计算，得到 status=%s stats=%v", col.Style.Status, col.Style.Stats)
	}
	if f := col.Style.Stats.TitleFormats; f == nil || f.WithPrefix != 3 || f.WithoutPrefix != 2 {
		t.Fatalf("终稿标题混用应被准确捕获: %+v", f)
	}
	if !col.Usage.UsageRecorded || col.Usage.Input != 100 || col.Usage.Output != 40 || col.Usage.CostUSD != 0.12 {
		t.Fatalf("usage 读取不正确: %+v", col.Usage)
	}
	if col.ToolCalls != 2 {
		t.Fatalf("tool calls = %d want 2", col.ToolCalls)
	}
}

func TestCollectStyleAgreesWithRuntimeHeadings(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "统一终稿", true: "混用终稿"}[mixed], func(t *testing.T) {
			s := store.NewStore(t.TempDir())
			if err := s.Init(); err != nil {
				t.Fatal(err)
			}
			var entries []domain.OutlineEntry
			p := &domain.Progress{Phase: domain.PhaseWriting, TotalChapters: 8, CompletedChapters: []int{6, 2, 4, 1, 3, 5}}
			for ch := 1; ch <= 8; ch++ {
				entries = append(entries, domain.OutlineEntry{Chapter: ch, Title: "第九章 尚未采用的计划标题"})
				if ch > 6 {
					continue
				}
				body := domain.ChapterText(ch, "风起", "夜色落下，他启程寻找失踪的亲人。")
				if mixed && ch%2 == 0 {
					body = "# 风起\n\n夜色落下，他启程寻找失踪的亲人。"
				}
				if err := s.Drafts.SaveFinalChapter(ch, body); err != nil {
					t.Fatal(err)
				}
				facts := domain.ChapterFacts{Title: "不带章号的摘要标题", Summary: "主角启程", KeyEvents: []string{"出发"}}
				if _, err := s.ChapterRecords.Accept(ch, domain.ChapterOriginGenerated, body, facts, domain.StyleDelta{}); err != nil {
					t.Fatal(err)
				}
				if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: ch, Title: facts.Title, Summary: facts.Summary}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Outline.SaveOutline(entries); err != nil {
				t.Fatal(err)
			}
			if err := s.Progress.Save(p); err != nil {
				t.Fatal(err)
			}
			raw, err := tools.NewContextTool(s, tools.References{}, "default", tools.NewStyleStatsIndex(s)).Execute(t.Context(), json.RawMessage(`{"chapter":7}`))
			if err != nil {
				t.Fatal(err)
			}
			var runtime struct {
				Episodic struct {
					Stats *stylestat.Stats `json:"style_stats"`
				} `json:"episodic_memory"`
			}
			if err := json.Unmarshal(raw, &runtime); err != nil {
				t.Fatal(err)
			}
			offline := Collect(s.Dir(), nil)
			if len(offline.LoadErrors) > 0 || offline.Style.Stats == nil || runtime.Episodic.Stats == nil {
				t.Fatalf("两条路径都应正常统计已完成章节: errors=%v offline=%+v runtime=%+v", offline.LoadErrors, offline.Style, runtime)
			}
			for _, stats := range []*stylestat.Stats{runtime.Episodic.Stats, offline.Style.Stats} {
				if stats.Chapters != 6 {
					t.Fatalf("未写章节不得计入统计: %+v", stats)
				}
				if mixed {
					if f := stats.TitleFormats; f == nil || f.WithPrefix != 3 || f.WithoutPrefix != 3 {
						t.Fatalf("应按终稿报告3章有章号、3章无章号: %+v", f)
					}
				} else if stats.TitleFormats != nil {
					t.Fatalf("终稿统一时不得受摘要或计划标题干扰: %+v", stats.TitleFormats)
				}
			}
		})
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

func TestChapterTitleDoesNotUsePlannedTitle(t *testing.T) {
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
	if got != "# 正文兜底标题" {
		t.Fatalf("应保留实际终稿首行，不能改取分层计划标题，得到 %q", got)
	}
	if got := chapterTitle(s, 7, "", func(string, error) {}); got != "" {
		t.Fatalf("没有终稿或摘要标题时不得用大纲伪造标题: %q", got)
	}
}

func TestChapterTitlePrefersFinalHeading(t *testing.T) {
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
	if got != "# 正文标题" {
		t.Fatalf("chapterTitle = %q, want final heading", got)
	}
	if got := chapterTitle(s, 1, " \n\t", func(string, error) {}); got != "终稿标题" {
		t.Fatalf("没有可用正文首行时应回落到摘要标题: %q", got)
	}
}

func TestChapterTitleDoesNotHideCorruptSummary(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "summaries", "01.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	var loadErrors []string
	title := chapterTitle(s, 1, "# 第 1 章 风起\n\n内容", func(name string, err error) {
		if err != nil {
			loadErrors = append(loadErrors, name)
		}
	})
	if title != "# 第 1 章 风起" || !containsString(loadErrors, "summary:1") {
		t.Fatalf("正文优先，但实际工件读取错误仍须上报: title=%q errors=%v", title, loadErrors)
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
