package orchestrator

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestShouldUseHandoff(t *testing.T) {
	if shouldUseHandoff(nil) {
		t.Fatal("nil progress should not use handoff")
	}
	if !shouldUseHandoff(&domain.Progress{Flow: domain.FlowReviewing}) {
		t.Fatal("reviewing flow should use handoff")
	}
	if !shouldUseHandoff(&domain.Progress{Layered: true, CompletedChapters: []int{1, 2, 3, 4, 5, 6}}) {
		t.Fatal("layered project with 6 chapters should use handoff")
	}
	if shouldUseHandoff(&domain.Progress{CompletedChapters: []int{1, 2, 3}}) {
		t.Fatal("short project should not use handoff")
	}
	if !shouldUseHandoff(&domain.Progress{TotalChapters: 60, CompletedChapters: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}}) {
		t.Fatal("long project with many completed chapters should use handoff")
	}
}

func TestBuildHandoffPack(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("测试小说", 20); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	progress.Phase = domain.PhaseWriting
	progress.Flow = domain.FlowReviewing
	progress.CompletedChapters = []int{1, 2, 3, 4, 5, 6}
	progress.TotalWordCount = 18000
	progress.ChapterWordCounts = map[int]int{6: 3200}
	if err := s.Progress.Save(progress); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}
	if err := s.RunMeta.Init("default", "openrouter", "gpt-5.4"); err != nil {
		t.Fatalf("InitRunMeta: %v", err)
	}
	if err := s.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("SetPlanningTier: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 4, Summary: "第四章摘要"}); err != nil {
		t.Fatalf("SaveSummary 4: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 5, Summary: "第五章摘要"}); err != nil {
		t.Fatalf("SaveSummary 5: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 6, Summary: "第六章摘要"}); err != nil {
		t.Fatalf("SaveSummary 6: %v", err)
	}

	pack, err := buildHandoffPack(s, "review-ch06-accept")
	if err != nil {
		t.Fatalf("buildHandoffPack: %v", err)
	}
	if pack == nil {
		t.Fatal("expected handoff pack, got nil")
	}
	if pack.NextChapter != 7 || pack.CompletedCount != 6 {
		t.Fatalf("unexpected handoff pack header: %+v", pack)
	}
	if len(pack.RecentSummaries) == 0 {
		t.Fatalf("expected recent summaries, got %+v", pack)
	}
	if pack.MemoryPolicy == nil || !pack.MemoryPolicy.HandoffPreferred {
		t.Fatalf("expected memory policy in handoff pack, got %+v", pack.MemoryPolicy)
	}
}

func TestSessionRecoveryAppliesHandoff(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	progress := &domain.Progress{
		NovelName:         "测试小说",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowReviewing,
		CurrentChapter:    7,
		CompletedChapters: []int{1, 2, 3, 4, 5, 6},
		TotalWordCount:    18000,
		TotalChapters:     20,
		ChapterWordCounts: map[int]int{6: 3200},
	}
	if err := s.Progress.Save(progress); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}
	if err := s.World.SaveHandoffPack(domain.HandoffPack{
		Reason:         "review-ch06-accept",
		NovelName:      "测试小说",
		Phase:          "writing",
		Flow:           "reviewing",
		NextChapter:    7,
		CompletedCount: 6,
		TotalChapters:  20,
		TotalWordCount: 18000,
		LastCommit:     "第6章已完成，约3200字。下一章=7。",
		Guidance:       []string{"优先依赖 handoff pack。"},
	}); err != nil {
		t.Fatalf("SaveHandoffPack: %v", err)
	}

	sess := newSession(nil, s, "openrouter", nil, nil, nil)
	recovery := sess.recovery()
	if !strings.Contains(recovery.Label, "Handoff") {
		t.Fatalf("expected handoff label, got %q", recovery.Label)
	}
	if !strings.Contains(recovery.PromptText, "[系统-Handoff]") {
		t.Fatalf("expected handoff prompt prefix, got %q", recovery.PromptText)
	}
	if !strings.Contains(recovery.PromptText, "记忆策略：") {
		t.Fatalf("expected memory policy in handoff prompt, got %q", recovery.PromptText)
	}
}
