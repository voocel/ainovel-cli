package reminder

import (
	"context"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
	"github.com/voocel/agentcore"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s
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

// TestSubAgentGuard_HardStopReasonEscalatesImmediately 验证：模型返回
// safety / content_filter 这类不可恢复的 provider 端拒答时，子代理 StopGuard
// 必须立即 Escalate 而不是注入催促消息。
//
// 历史背景：实测 hy3-preview:free 写第 2 章时连续 8 次 stop_reason='safety'
// 拒答；旧逻辑反复注入"必须 commit"，模型继续 safety，攒到 3 次 block 才 escalate，
// 之后 coordinator 又重派 writer 总共 3 次。每次重派都是新的 SubAgent → 缓存
// 前缀全部冷启动。修复后第一次 safety 立即 escalate，coordinator 从 LLM
// 错误消息看到不可恢复，倾向于换路径而不是重派。
//
// 注意只测 safety / content_filter：StopReasonError / StopReasonAborted 走
// agentcore loop.go 直接终止 run 的分支，根本不会调用 StopGuard，列进来反而
// 引入死代码。
func TestSubAgentGuard_HardStopReasonEscalatesImmediately(t *testing.T) {
	cases := []agentcore.StopReason{
		agentcore.StopReason("safety"),
		agentcore.StopReason("content_filter"),
	}
	for _, sr := range cases {
		t.Run(string(sr), func(t *testing.T) {
			s := newTestStore(t)
			guard := NewWriterStopGuard(s)
			info := agentcore.StopInfo{
				TurnIndex: 1,
				Message:   agentcore.Message{StopReason: sr},
			}
			d := guard(context.Background(), info)
			if !d.Escalate {
				t.Fatalf("stop_reason=%q must escalate immediately, got %#v", sr, d)
			}
			if d.InjectMessage != "" {
				t.Fatalf("stop_reason=%q must not inject any message, got %q", sr, d.InjectMessage)
			}
		})
	}
}

// TestSubAgentGuard_NormalStopStillBlocks 确保对正常 stop_reason 的拦截行为
// 不受硬错误旁路的影响——LLM 自停且没 commit 时仍然要催。
func TestSubAgentGuard_NormalStopStillBlocks(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s)
	info := agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	}
	d := guard(context.Background(), info)
	if d.Escalate {
		t.Fatal("normal stop must not escalate on first block")
	}
	if d.Allow {
		t.Fatal("normal stop must be blocked when no commit checkpoint exists")
	}
	if d.InjectMessage == "" {
		t.Fatal("normal stop must inject a follow-up message")
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
