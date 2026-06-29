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

func TestStopGuard_DefaultBlockMessageWaitsForHost(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("update phase: %v", err)
	}

	decision := NewStopGuard(s, nil)(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	if !strings.Contains(decision.InjectMessage, "[Host 下达指令]") {
		t.Fatalf("inject message should point to Host instruction, got %q", decision.InjectMessage)
	}
	for _, forbidden := range []string{"查 novel_context", "调子Proxy"} {
		if strings.Contains(decision.InjectMessage, forbidden) {
			t.Fatalf("inject message should not suggest freelance action %q: %q", forbidden, decision.InjectMessage)
		}
	}
}

func TestStopGuard_DefaultBlockMessageAllowsCoordinatorJudgmentWhenNoRoute(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	decision := NewStopGuard(s, nil)(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	if strings.Contains(decision.InjectMessage, "[Host 下达指令]") {
		t.Fatalf("no-route inject should not tell coordinator to wait for Host, got %q", decision.InjectMessage)
	}
	if !strings.Contains(decision.InjectMessage, "裁定Cảnh") {
		t.Fatalf("no-route inject should mention coordinator judgment, got %q", decision.InjectMessage)
	}
}

// TestSubAgentGuard_HardStopReasonEscalatesImmediately 验证：Mô hìnhQuay lại
// safety / content_filter 这类不可Phục hồi的 provider 端拒答时，子Proxy StopGuard
// 必须立即 Escalate 而不是注入催促消息。
//
// Lịch sử背景：实测 hy3-preview:free 写第 2 章时连续 8 次 stop_reason='safety'
// 拒答；Cũ逻辑反复注入"必须 commit"，Mô hìnhTiếp tục safety，攒到 3 次 block 才 escalate，
// 之后 coordinator 又重派 writer 总共 3 次。每次重派都是Mới的 SubAgent → 缓存
// 前缀Tất cả冷启动。修复后第一次 safety 立即 escalate，coordinator 从 LLM
// Lỗi消息看到不可Phục hồi，倾向于换Đường dẫn而不是重派。
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
// 不受硬Lỗi旁路的影响——LLM 自停且没 commit 时仍然要催。
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
// 不相邻（中间 LLM 做了 tool call 或用户 resume）时，consecutive 计数Đặt lại。
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

// TestEditorStopGuard_TaskAware 验证任务感知：被派生成弧Tóm tắt时，仅 save_review（复核）
// 不算Hoàn thành，必须产出 arc_summary 才放行——封堵卷中骨架弧死循环的起点 Defect C。
func TestEditorStopGuard_TaskAware(t *testing.T) {
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	// Tóm tắt任务 + 只存了 review → 必须阻拦（review 不满足 arc_summary 要求）。
	t.Run("summary task blocks on review only", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧Tóm tắt（save_arc_summary）")
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		if d := guard(context.Background(), normalStop); d.Allow {
			t.Fatal("summary task must NOT be satisfied by a review checkpoint")
		}
	})

	// Tóm tắt任务 + 已存 arc_summary → 放行。
	t.Run("summary task allows on arc_summary", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧Tóm tắt（save_arc_summary）")
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "arc_summary", "summaries/arc-v05a01.json", "d1"); err != nil {
			t.Fatalf("append arc_summary: %v", err)
		}
		if d := guard(context.Background(), normalStop); !d.Allow {
			t.Fatal("summary task must be satisfied by an arc_summary checkpoint")
		}
	})

	// 评审任务 + 存了 review → 放行（Mặc định宽松行为不变）。
	t.Run("review task allows on review", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "对第 5 卷第 1 弧做弧级评审（scope=arc）")
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		if d := guard(context.Background(), normalStop); !d.Allow {
			t.Fatal("review task must be satisfied by a review checkpoint")
		}
	})
}
