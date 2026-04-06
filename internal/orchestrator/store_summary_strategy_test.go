package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestStoreSummaryCompactApplyUsesPersistentStoreData(t *testing.T) {
	s := seededWriterStore(t)
	strategy := NewStoreSummaryCompact(StoreSummaryCompactConfig{
		Store:              s,
		KeepRecentTokens:   80,
		SummaryTokenBudget: 2000,
	})

	msgs := []agentcore.AgentMessage{
		agentcore.UserMsg(strings.Repeat("旧上下文", 80)),
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{agentcore.TextBlock(strings.Repeat("旧回复", 80))},
		},
		agentcore.UserMsg("继续写第三章，注意承接第二章结尾。"),
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{agentcore.TextBlock("收到，我先梳理当前场景。")},
		},
	}

	out, result, err := strategy.Apply(context.Background(), msgs, msgs, corecontext.Budget{
		Tokens:    corecontext.EstimateTotal(msgs),
		Window:    128,
		Threshold: 32,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Applied {
		t.Fatal("expected store summary strategy to apply")
	}
	if result.Name != storeSummaryStrategyName {
		t.Fatalf("unexpected strategy name: %q", result.Name)
	}
	if len(out) < 2 {
		t.Fatalf("expected summary + kept messages, got %d", len(out))
	}
	summary, ok := out[0].(corecontext.ContextSummary)
	if !ok {
		t.Fatalf("expected ContextSummary, got %T", out[0])
	}
	if !strings.Contains(summary.Summary, "最近章节摘要") {
		t.Fatalf("expected persistent summaries in checkpoint, got %q", summary.Summary)
	}
	if !strings.Contains(summary.Summary, "当前章节计划") {
		t.Fatalf("expected chapter plan in checkpoint, got %q", summary.Summary)
	}
	if !strings.Contains(summary.Summary, "活跃伏笔") {
		t.Fatalf("expected foreshadow data in checkpoint, got %q", summary.Summary)
	}
	if !strings.Contains(summary.Summary, "待修审稿问题") {
		t.Fatalf("expected pending review section in checkpoint, got %q", summary.Summary)
	}
	if !strings.Contains(summary.Summary, "仓库线索需要再蓄压一拍") {
		t.Fatalf("expected pending review details in checkpoint, got %q", summary.Summary)
	}
	if result.Info == nil || result.Info.CompactedCount <= 0 {
		t.Fatalf("expected compaction info, got %+v", result.Info)
	}
}

func TestStoreSummaryCompactApplyFallsBackWhenStoreDataInsufficient(t *testing.T) {
	dir := t.TempDir()
	s := storepkg.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    1,
		TotalChapters:     3,
		CompletedChapters: nil,
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	strategy := NewStoreSummaryCompact(StoreSummaryCompactConfig{Store: s, KeepRecentTokens: 20})
	msgs := []agentcore.AgentMessage{
		agentcore.UserMsg(strings.Repeat("旧上下文", 40)),
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{agentcore.TextBlock(strings.Repeat("旧回复", 40))},
		},
	}

	out, result, err := strategy.Apply(context.Background(), msgs, msgs, corecontext.Budget{
		Tokens:    corecontext.EstimateTotal(msgs),
		Window:    64,
		Threshold: 16,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Applied {
		t.Fatal("expected no-op when persistent memory is insufficient")
	}
	if len(out) != len(msgs) {
		t.Fatalf("expected messages unchanged, got %d", len(out))
	}
}

func TestWriterRestorePackRefreshReusesStoreBuilder(t *testing.T) {
	s := seededWriterStore(t)
	pack := &writerRestorePack{}
	pack.Refresh(s)

	msg, ok := pack.buildMessage(restoreBudgetTokens)
	if !ok {
		t.Fatal("expected restore pack message")
	}
	text := msg.TextContent()
	if !strings.Contains(text, "<post-compact-context>") {
		t.Fatalf("expected wrapped restore context, got %q", text)
	}
	if !strings.Contains(text, "待修审稿问题") {
		t.Fatalf("expected pending review section, got %q", text)
	}
	if !strings.Contains(text, "当前章节计划") {
		t.Fatalf("expected chapter plan section, got %q", text)
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
