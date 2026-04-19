package reminder

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s
}

func TestAggregate_NoProgress_EmitsOnlyTurnAgnosticReminders(t *testing.T) {
	s := newTestStore(t)
	gen := Aggregate(s, Default()...)

	got := gen(context.Background(), agentcore.TurnInfo{TurnIndex: 0})
	if len(got) != 0 {
		// Progress 不存在时所有 generator 都应返回空
		t.Fatalf("expected 0 reminders, got %d: %+v", len(got), got)
	}
}

func TestAggregate_WritingFlow_EmitsFlowReminder(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 20); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("update phase: %v", err)
	}

	gen := Aggregate(s, Default()...)
	got := gen(context.Background(), agentcore.TurnInfo{TurnIndex: 3})

	sources := map[string]string{}
	for _, r := range got {
		sources[r.Source] = r.Content
	}
	if flow, ok := sources["flow"]; !ok || !strings.Contains(flow, "next_chapter=1") {
		t.Fatalf("flow reminder missing/incorrect: %q", flow)
	}
	if _, ok := sources["book_complete"]; ok {
		t.Fatal("book_complete should not fire during writing")
	}
	if _, ok := sources["queue_guard"]; ok {
		t.Fatal("queue_guard should not fire when pending_rewrites is empty")
	}
}

// TestAggregate_OutlinePhase_FoundationReady_PointsToNextChapter 验证脏状态兜底：
// phase 还停在 outline 但 foundation 已齐（可能跨进程恢复或上个 run 崩在推进 phase 前），
// reminder 必须指向正确的 NextChapter 而不是写死第 1 章。
func TestAggregate_OutlinePhase_FoundationReady_PointsToNextChapter(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatalf("update phase: %v", err)
	}
	// 凑齐 foundation：premise / outline / characters / world_rules
	if err := s.Outline.SavePremise("# 测试书\n\n## 题材和基调\n试"); err != nil {
		t.Fatalf("save premise: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "c1"}}); err != nil {
		t.Fatalf("save outline: %v", err)
	}
	if err := s.Characters.Save([]domain.Character{{Name: "A"}}); err != nil {
		t.Fatalf("save characters: %v", err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{{Category: "c", Rule: "r"}}); err != nil {
		t.Fatalf("save world: %v", err)
	}
	// 模拟已完成 2 章但 phase 错在 outline
	for _, ch := range []int{1, 2} {
		if err := s.Progress.MarkChapterComplete(ch, 1000, "d", "q"); err != nil {
			t.Fatalf("mark ch%d: %v", ch, err)
		}
	}

	gen := Aggregate(s, Default()...)
	got := gen(context.Background(), agentcore.TurnInfo{})

	var flow string
	for _, r := range got {
		if r.Source == "flow" {
			flow = r.Content
		}
	}
	if !strings.Contains(flow, "写第 3 章") {
		t.Fatalf("flow should use NextChapter() fallback, got: %q", flow)
	}
}

// TestAggregate_OutlinePhase_ReaffirmsMissingFacts 验证：outline 阶段 reminder
// 只重申"还缺什么"的事实，不再教 LLM 路由分发流程。
func TestAggregate_OutlinePhase_ReaffirmsMissingFacts(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatalf("update phase: %v", err)
	}

	gen := Aggregate(s, Default()...)
	got := gen(context.Background(), agentcore.TurnInfo{})

	var flow string
	for _, r := range got {
		if r.Source == "flow" {
			flow = r.Content
		}
	}
	if flow == "" {
		t.Fatalf("flow reminder missing in outline phase: %+v", got)
	}
	// 必须列出具体缺项；不应再出现"每次只补一项"这类路由指令
	for _, item := range []string{"premise", "characters", "world_rules"} {
		if !strings.Contains(flow, item) {
			t.Fatalf("flow should list missing %q, got: %q", item, flow)
		}
	}
	if strings.Contains(flow, "每次只补一项") {
		t.Fatalf("flow must not prescribe routing details, got: %q", flow)
	}
}

func TestAggregate_QueueGuardFires_WhenPolishingWithPending(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 20); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("update phase: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{5, 8}, "minor polish"); err != nil {
		t.Fatalf("set pending: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("set flow: %v", err)
	}

	gen := Aggregate(s, Default()...)
	got := gen(context.Background(), agentcore.TurnInfo{})

	var queueMsg string
	for _, r := range got {
		if r.Source == "queue_guard" {
			queueMsg = r.Content
		}
	}
	if queueMsg == "" {
		t.Fatalf("queue_guard not emitted: %+v", got)
	}
	if !strings.Contains(queueMsg, "[5 8]") {
		t.Fatalf("queue_guard should list pending chapters, got: %q", queueMsg)
	}
	if !strings.Contains(queueMsg, "打磨") {
		t.Fatalf("queue_guard should mention polishing verb, got: %q", queueMsg)
	}
}

func TestAggregate_BookComplete_MutesOthers(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseComplete); err != nil {
		t.Fatalf("update phase: %v", err)
	}

	gen := Aggregate(s, Default()...)
	got := gen(context.Background(), agentcore.TurnInfo{})

	sources := map[string]struct{}{}
	for _, r := range got {
		sources[r.Source] = struct{}{}
	}
	if _, ok := sources["book_complete"]; !ok {
		t.Fatalf("book_complete should fire; got: %+v", got)
	}
	if _, ok := sources["flow"]; ok {
		t.Fatal("flow must NOT fire when Phase=Complete")
	}
}

// TestAggregate_ArcHandoff_RedirectsAwayFromNextChapter 验证：分层模式下
// 下一章超出已展开大纲范围时，flow reminder 必须刹车而非派 writer 写新章。
// 这是 2026-04-18 "精修完 arc 1 最后一章后直接跳去写 ch11" bug 的回归防护。
func TestAggregate_ArcHandoff_RedirectsAwayFromNextChapter(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatalf("set layered: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("update phase: %v", err)
	}

	// 大纲：弧 1 有 2 章，弧 2 骨架未展开
	volumes := []domain.VolumeOutline{{
		Index: 1, Title: "卷一",
		Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{
				{Chapter: 1, Title: "c1"}, {Chapter: 2, Title: "c2"},
			}},
			{Index: 2, EstimatedChapters: 2},
		},
	}}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("save layered: %v", err)
	}
	for _, ch := range []int{1, 2} {
		if err := s.Progress.MarkChapterComplete(ch, 1000, "desire", "quest"); err != nil {
			t.Fatalf("mark ch%d: %v", ch, err)
		}
	}

	gen := Aggregate(s, Default()...)
	extractFlow := func() string {
		for _, r := range gen(context.Background(), agentcore.TurnInfo{}) {
			if r.Source == "flow" {
				return r.Content
			}
		}
		return ""
	}

	// 1) 弧 1 写完、弧 2 未展开 → 刹车，禁止派 writer
	flow := extractFlow()
	if strings.Contains(flow, "写第 3 章") {
		t.Fatalf("flow must NOT push to next chapter while arc handoff pending: %q", flow)
	}
	if !strings.Contains(flow, "arc_summary") {
		t.Fatalf("flow should brake with handoff hint, got: %q", flow)
	}

	// 2) 展开弧 2 后 → 刹车释放，回到 next_chapter 指令
	volumes[0].Arcs[1].Chapters = []domain.OutlineEntry{
		{Chapter: 3, Title: "c3"}, {Chapter: 4, Title: "c4"},
	}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("re-save layered: %v", err)
	}
	if flow = extractFlow(); !strings.Contains(flow, "写第 3 章") {
		t.Fatalf("flow should resume next_chapter after expansion: %q", flow)
	}
}

func TestStopGuard_AllowsStopOnlyWhenComplete(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	guard := NewStopGuard(s, nil)

	// 尚未 Complete：必须阻拦 + 注入
	decision := guard(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	if decision.Allow {
		t.Fatal("stop must be blocked before Phase=Complete")
	}
	if decision.InjectMessage == "" {
		t.Fatal("inject message required when blocking")
	}

	// 转 Complete：放行
	if err := s.Progress.UpdatePhase(domain.PhaseComplete); err != nil {
		t.Fatalf("update phase: %v", err)
	}
	decision = guard(context.Background(), agentcore.StopInfo{TurnIndex: 2})
	if !decision.Allow {
		t.Fatal("stop must be allowed when Phase=Complete")
	}
}

func TestStopGuard_EscalatesAfterTooManyConsecutiveBlocks(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	var blocks []string
	guard := NewStopGuard(s, func(reason string, _ int32) {
		blocks = append(blocks, reason)
	})

	for i := 0; i < maxConsecutiveBlocks; i++ {
		decision := guard(context.Background(), agentcore.StopInfo{TurnIndex: i})
		if decision.Escalate {
			t.Fatalf("escalated too early at iteration %d", i)
		}
	}
	decision := guard(context.Background(), agentcore.StopInfo{TurnIndex: maxConsecutiveBlocks})
	if !decision.Escalate {
		t.Fatalf("expected escalate after %d consecutive blocks", maxConsecutiveBlocks+1)
	}
	if len(blocks) != maxConsecutiveBlocks+1 {
		t.Fatalf("audit callback called %d times, want %d", len(blocks), maxConsecutiveBlocks+1)
	}
	if blocks[len(blocks)-1] != "escalated" {
		t.Fatalf("last audit reason should be 'escalated', got %q", blocks[len(blocks)-1])
	}
}

// TestStopGuard_NonConsecutiveTurnResetsCounter 验证：两次 block 之间 TurnIndex
// 不相邻（中间 LLM 做了 tool call 或用户 resume）时，consecutive 计数重置，
// 不会把跨次 run 的 block 计数累加而立刻升级终止。
func TestStopGuard_NonConsecutiveTurnResetsCounter(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	guard := NewStopGuard(s, nil)

	// 先连续 block maxConsecutiveBlocks 次（到 escalate 前一步）
	for i := 0; i < maxConsecutiveBlocks; i++ {
		if d := guard(context.Background(), agentcore.StopInfo{TurnIndex: i}); d.Escalate {
			t.Fatalf("escalated too early at iteration %d", i)
		}
	}

	// 模拟中间 LLM 成功调了工具（TurnIndex 跳了若干 turn），再次触发 stop
	// 不应直接 escalate，而是从头开始累计
	d := guard(context.Background(), agentcore.StopInfo{TurnIndex: maxConsecutiveBlocks + 10})
	if d.Escalate {
		t.Fatal("non-consecutive block must NOT escalate; counter should have been reset")
	}
	if d.Allow {
		t.Fatal("stop must still be blocked when Phase != Complete")
	}

	// 模拟 resume：TurnIndex 倒流回 1
	d = guard(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	if d.Escalate {
		t.Fatal("resume (TurnIndex backflow) must NOT escalate")
	}
}
