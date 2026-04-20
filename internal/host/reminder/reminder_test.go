package reminder

import (
	"context"
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

// TestAggregate_EmptyDefault_EmitsNothing 验证当前装配（Default() 为空）不产生任何 reminder。
// 所有路由职责已下沉到 Host Flow Router。
func TestAggregate_EmptyDefault_EmitsNothing(t *testing.T) {
	s := newTestStore(t)
	gen := Aggregate(s, Default()...)
	if got := gen(context.Background(), agentcore.TurnInfo{TurnIndex: 0}); len(got) != 0 {
		t.Fatalf("expected no reminders, got %d: %+v", len(got), got)
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
// 不相邻（中间 LLM 做了 tool call 或用户 resume）时，consecutive 计数重置。
func TestStopGuard_NonConsecutiveTurnResetsCounter(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	guard := NewStopGuard(s, nil)

	for i := 0; i < maxConsecutiveBlocks; i++ {
		if d := guard(context.Background(), agentcore.StopInfo{TurnIndex: i}); d.Escalate {
			t.Fatalf("escalated too early at iteration %d", i)
		}
	}

	d := guard(context.Background(), agentcore.StopInfo{TurnIndex: maxConsecutiveBlocks + 10})
	if d.Escalate {
		t.Fatal("non-consecutive block must NOT escalate; counter should have been reset")
	}
	if d.Allow {
		t.Fatal("stop must still be blocked when Phase != Complete")
	}

	d = guard(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	if d.Escalate {
		t.Fatal("resume (TurnIndex backflow) must NOT escalate")
	}
}
