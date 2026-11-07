package ctxpack

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

func TestWriterRestoreIncludesOptionalDataWarnings(t *testing.T) {
	s := seededWriterStore(t)
	if err := os.WriteFile(filepath.Join(s.Dir(), "meta", "style_rules.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, ok, err := buildWriterRestoreText(s, restoreBudgetTokens)
	if err != nil {
		t.Fatalf("辅助数据损坏不应阻止恢复上下文: %v", err)
	}
	if !ok || !strings.Contains(text, "数据告警") || !strings.Contains(text, "style_rules") {
		t.Fatalf("恢复上下文应向模型暴露读取告警: %q", text)
	}
}

// store 里还没有可用的章节事实（第 1 章）时，压缩不能变成空转：退回模型摘要。
func TestWriterCompactorFallsBackToModelSummary(t *testing.T) {
	s := storepkg.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		Phase:          domain.PhaseWriting,
		CurrentChapter: 1,
		TotalChapters:  3,
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	provider := litellmtest.New(litellmtest.Text("<analysis>思考</analysis><summary>## 当前任务\n写第一章\n\n## 当前进度\n开场</summary>"))
	client, err := litellm.New(provider)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}

	compactor := NewWriterCompactor(WriterCompactConfig{Store: s, KeepRecentTokens: 200})
	msgs := []agentcore.Message{
		agentcore.UserText(strings.Repeat("旧上下文", 100)),
		{Role: litellm.RoleAssistant, Blocks: []litellm.Block{litellm.Text(strings.Repeat("旧回复", 100))}},
		agentcore.UserText(strings.Repeat("旧任务", 100)),
		{Role: litellm.RoleAssistant, Blocks: []litellm.Block{litellm.Text(strings.Repeat("旧结论", 100))}},
		agentcore.UserText(strings.Repeat("最近的任务", 100)),
	}
	call := func([]agentcore.Message) agentcore.Call {
		return agentcore.Call{Client: client, Request: litellm.Request{Model: "writer"}}
	}

	out, err := compactor.Compact(context.Background(), msgs, call, "")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if out == nil {
		t.Fatal("expected a model-written checkpoint when store facts are insufficient")
	}
	if out.Replaced != 4 || len(out.Messages) != 2 {
		t.Fatalf("expected summary + one kept message, got Replaced=%d len=%d", out.Replaced, len(out.Messages))
	}
	if out.Messages[0].Kind != agentcore.KindSummary {
		t.Fatalf("expected a summary message first, got %q", out.Messages[0].Kind)
	}
	if summary := agentcore.SummaryText(out.Messages[0]); !strings.Contains(summary, "## 当前进度") {
		t.Fatalf("expected the tagged checkpoint, got %q", summary)
	}
	if got := out.Messages[1].Role; got != litellm.RoleUser {
		t.Fatalf("recent history must be kept verbatim, got %q", got)
	}
	requests := provider.Requests()
	if len(requests) != 1 {
		t.Fatalf("expected exactly one summary call, got %d", len(requests))
	}
	if !requestTextContains(requests[0], WriterSummaryPrompt) {
		t.Fatal("expected the writer summary prompt in the summary request")
	}
	if !requestTextContains(requests[0], WriterSummarySystemPrompt) {
		t.Fatal("expected the writer summary system prompt in the summary request")
	}
}

// requestTextContains 报告请求的任意消息文本里是否含 want。
func requestTextContains(req litellm.Request, want string) bool {
	for _, msg := range req.Messages {
		for _, block := range msg.Blocks {
			if text, ok := block.(litellm.TextBlock); ok && strings.Contains(text.Text, want) {
				return true
			}
		}
	}
	return false
}

func TestWriterRestorePackRefreshReusesStoreBuilder(t *testing.T) {
	s := seededWriterStore(t)
	pack := &WriterRestorePack{}
	pack.Refresh(s)

	msg, ok, err := pack.buildMessage(restoreBudgetTokens)
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	if !ok {
		t.Fatal("expected restore pack message")
	}
	text := msg.Text()
	if !strings.Contains(text, "<post-compact-context>") {
		t.Fatalf("expected wrapped restore context, got %q", text)
	}
	if !strings.Contains(text, "待修审稿问题") {
		t.Fatalf("expected pending review section, got %q", text)
	}
	if !strings.Contains(text, "当前章节计划") {
		t.Fatalf("expected chapter plan section, got %q", text)
	}

	if _, _, err := pack.buildMessage(0); err == nil {
		t.Fatal("expected an explicit error when the restore pack does not fit")
	}
}

func seededWriterStore(t *testing.T) *storepkg.Store {
	t.Helper()

	s := storepkg.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    3,
		TotalChapters:     6,
		CompletedChapters: []int{1, 2},
		Flow:              domain.FlowWriting,
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "第一章", CoreEvent: "开场"},
		{Chapter: 2, Title: "第二章", CoreEvent: "冲突升级"},
		{Chapter: 3, Title: "第三章", CoreEvent: "追查线索", Scenes: []string{"主角追查失踪案", "发现旧仓库线索"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter:    3,
		Title:      "第三章",
		Goal:       "推进失踪案调查",
		Conflict:   "主角与搭档对调查方向分歧",
		Hook:       "仓库中发现可疑录音",
		EmotionArc: "怀疑到紧张",
	}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:    1,
		Summary:    "主角接下委托，发现失踪案并不简单。",
		Characters: []string{"林岚", "周策"},
		KeyEvents:  []string{"委托成立"},
	}); err != nil {
		t.Fatalf("SaveSummary 1: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:    2,
		Summary:    "两人追查旧码头，线索指向废弃仓库。",
		Characters: []string{"林岚", "周策", "沈叔"},
		KeyEvents:  []string{"旧码头冲突", "仓库线索出现"},
	}); err != nil {
		t.Fatalf("SaveSummary 2: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "tape", Description: "失踪者留下的录音带", PlantedAt: 2, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}
	if err := s.World.SaveTimeline([]domain.TimelineEvent{
		{Chapter: 2, Time: "夜晚", Event: "旧码头交锋", Characters: []string{"林岚", "周策"}},
	}); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{
		Prose:  []string{"句子偏短，保持压迫感"},
		Taboos: []string{"避免直白解释谜团"},
	}); err != nil {
		t.Fatalf("SaveStyleRules: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 2,
		Scope:   "chapter",
		Verdict: "polish",
		Summary: "第二章结尾铺垫偏急，需要补一拍仓库前的压迫感。",
		Issues: []domain.ConsistencyIssue{
			{
				Type:        "pacing",
				Severity:    "warning",
				Description: "仓库线索出现过快，悬疑蓄压不够。",
				Suggestion:  "在进入仓库前增加一段迟疑与环境压迫描写。",
			},
		},
		ContractMisses: []string{"章末钩子不够强"},
	}); err != nil {
		t.Fatalf("Save chapter review: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 2,
		Scope:   "global",
		Verdict: "polish",
		Summary: "第二章尾声节奏偏快，仓库线索需要再蓄压一拍。",
	}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	return s
}

func toolGroup(id, name, result string) []agentcore.Message {
	return []agentcore.Message{
		{
			Role:   litellm.RoleAssistant,
			Blocks: []litellm.Block{litellm.ToolUseBlock{ID: id, Name: name, Arguments: `{}`}},
		},
		agentcore.ToolResult(id, agentcore.TextResult(result)),
	}
}

// Writer 一次运行只有一条任务消息，其余全是工具组：压缩必须落在工具循环内部，
// 摘要来自 store 事实，任务文本跟着摘要走，再次压缩也不能丢。
func TestWriterCompactorCompactsToolLoop(t *testing.T) {
	const task = "返工第 3 章：结尾要承接第二章的仓库线索\n\n补充：结尾留一个钩子"
	compactor := NewWriterCompactor(WriterCompactConfig{
		Store:              seededWriterStore(t),
		KeepRecentTokens:   600,
		SummaryTokenBudget: 2000,
	})
	msgs := []agentcore.Message{agentcore.UserText(task)}
	for i := 1; i <= 6; i++ {
		msgs = append(msgs, toolGroup("t"+strconv.Itoa(i), "read_chapter", strings.Repeat("a", 1600))...)
	}

	compact := func(in []agentcore.Message) *agentcore.Compaction {
		out, err := compactor.Compact(context.Background(), in, func([]agentcore.Message) agentcore.Call {
			t.Fatal("store summary must apply without a model call")
			return agentcore.Call{}
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		if out == nil || out.Replaced <= 0 {
			t.Fatalf("store summary must apply inside a tool loop, got %+v", out)
		}
		if out.Messages[0].Kind != agentcore.KindSummary {
			t.Fatalf("expected a summary message first, got %q", out.Messages[0].Kind)
		}
		summary := agentcore.SummaryText(out.Messages[0])
		if !strings.Contains(summary, taskHeading+task+"\n\n## ") {
			t.Fatalf("summary must carry the task section verbatim, got %q", summary)
		}
		for _, want := range []string{"最近章节摘要", "当前章节计划", "活跃伏笔", "待修审稿问题", "仓库线索需要再蓄压一拍"} {
			if !strings.Contains(summary, want) {
				t.Fatalf("expected %q in store checkpoint", want)
			}
		}
		if next := out.Messages[1]; len(next.ToolCalls()) == 0 {
			t.Fatalf("kept suffix must start with a tool call, got %+v", next.Blocks)
		}
		return out
	}

	first := compact(msgs)
	if len(first.Messages) >= len(msgs) {
		t.Fatalf("expected compaction, %d -> %d", len(msgs), len(first.Messages))
	}
	second := compact(append(first.Messages, toolGroup("t7", "read_chapter", strings.Repeat("a", 1600))...))
	if summary := agentcore.SummaryText(second.Messages[0]); !strings.Contains(summary, taskHeading+task) {
		t.Fatalf("task must survive a second compaction, got %q", summary)
	}
}

// 压缩产出的历史是：摘要 → 恢复包 → 最近原文。
func TestWriterCompactorInjectsRestorePack(t *testing.T) {
	s := seededWriterStore(t)
	pack := &WriterRestorePack{}
	pack.Refresh(s)
	compactor := NewWriterCompactor(WriterCompactConfig{
		Store:              s,
		Restore:            pack,
		KeepRecentTokens:   600,
		SummaryTokenBudget: 2000,
	})
	msgs := []agentcore.Message{agentcore.UserText("继续写第 3 章")}
	for i := 1; i <= 6; i++ {
		msgs = append(msgs, toolGroup("t"+strconv.Itoa(i), "read_chapter", strings.Repeat("a", 1600))...)
	}

	out, err := compactor.Compact(context.Background(), msgs, nil, "")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if out == nil || len(out.Messages) < 3 {
		t.Fatalf("expected summary + restore + kept history, got %+v", out)
	}
	restore := out.Messages[1].Text()
	if !strings.Contains(restore, "<post-compact-context>") || !strings.Contains(restore, "当前章节计划") {
		t.Fatalf("expected the restore pack after the summary, got %q", restore)
	}
	if len(out.Messages) > 4 {
		t.Fatalf("expected exactly one restore message, got %d messages", len(out.Messages))
	}
}

// 保留段里陈旧的工具结果被就地清理，最新几条保持原文。
func TestWriterCompactorClearsStaleToolResults(t *testing.T) {
	compactor := NewWriterCompactor(WriterCompactConfig{
		Store:              seededWriterStore(t),
		KeepRecentTokens:   4000,
		SummaryTokenBudget: 2000,
	})
	msgs := []agentcore.Message{agentcore.UserText("继续写第 3 章")}
	for i := 1; i <= 12; i++ {
		msgs = append(msgs, toolGroup("t"+strconv.Itoa(i), "read_chapter", strings.Repeat("a", 1600))...)
	}

	out, err := compactor.Compact(context.Background(), msgs, nil, "")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if out == nil {
		t.Fatal("expected compaction")
	}
	var cleared, full int
	for _, m := range out.Messages {
		if m.Role != litellm.RoleTool {
			continue
		}
		if m.Text() == clearedToolResult {
			cleared++
			continue
		}
		full++
	}
	if cleared == 0 {
		t.Fatal("expected stale tool results in the kept suffix to be cleared")
	}
	if full < keepRecentToolResults {
		t.Fatalf("expected at least %d recent tool results kept verbatim, got %d", keepRecentToolResults, full)
	}
	last := out.Messages[len(out.Messages)-1]
	if last.Role != litellm.RoleTool || last.Text() == clearedToolResult {
		t.Fatal("the newest tool result must stay verbatim")
	}
}
