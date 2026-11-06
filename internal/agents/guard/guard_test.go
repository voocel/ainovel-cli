package guard

import (
	"context"
	"errors"
	"fmt"
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

// normalStop 是一次"模型自己说要结束"的停机信息。
func normalStop() agentcore.StopInfo {
	return agentcore.StopInfo{Turns: 1, Message: agentcore.Message{Stop: agentcore.StopEnd}}
}

// TestSubAgentGuard_HardStopReasonEscalatesImmediately 验证：模型返回
// safety 这类不可恢复的 provider 端拒答时，子代理 StopGuard 必须立即升级终止
// 而不是注入催促消息。
//
// 历史背景：实测 hy3-preview:free 写第 2 章时连续 8 次 stop_reason='safety'
// 拒答；旧逻辑反复注入"必须 commit"，模型继续 safety，攒到 3 次 block 才 escalate，
// 之后 Engine 又重跑 writer 总共 3 次。修复后第一次 safety 立即升级，
// Engine 可直接按不可恢复错误暂停。
//
// 注意只测 safety：StopError / StopAborted 走 agentcore 的"直接结束 run"分支
// （loop.go 的 respond 把 vendor error 作为 run 错误返回），根本不会咨询 OnStop，
// 列进来反而引入死代码。
func TestSubAgentGuard_HardStopReasonEscalatesImmediately(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)
	info := agentcore.StopInfo{Turns: 1, Message: agentcore.Message{Stop: agentcore.StopSafety}}

	msgs, err := guard(context.Background(), info)
	if !errors.Is(err, ErrStopGuardEscalated) {
		t.Fatalf("safety 停机必须立即升级，got err=%v msgs=%v", err, msgs)
	}
	if len(msgs) != 0 {
		t.Fatalf("safety 停机不得注入任何催促消息，got %v", msgs)
	}
}

// TestSubAgentGuard_NormalStopStillBlocks 确保对正常停机的拦截行为
// 不受硬错误旁路的影响——LLM 自停且没 commit 时仍然要催。
func TestSubAgentGuard_NormalStopStillBlocks(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)

	msgs, err := guard(context.Background(), normalStop())
	if err != nil {
		t.Fatalf("首次拦截不应升级，got %v", err)
	}
	if len(msgs) != 1 || strings.TrimSpace(msgs[0].Text()) == "" {
		t.Fatalf("正常停机且无 commit 时必须注入一条催促使模型继续，got %v", msgs)
	}
}

// TestSubAgentGuard_ProgressBetweenBlocksResetsCounter 验证：两次拦截之间出现过
// 新 checkpoint（模型被催后重新 draft 等）时 consecutive 重置——升级只惩罚毫无
// 产物的连续空转，遵循"有进展即重置"语义（issue #75）。
func TestSubAgentGuard_ProgressBetweenBlocksResetsCounter(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)
	info := normalStop()

	// 拦截 → 落盘新草稿（有进展）→ 再拦截：往复超过阈值也不得升级。
	for i := range subagentMaxConsecutiveBlocks + 2 {
		if _, err := guard(context.Background(), info); err != nil {
			t.Fatalf("escalated at block %d despite progress between blocks: %v", i, err)
		}
		if _, err := s.Checkpoints.Append(domain.ChapterScope(1), "draft", "drafts/01.draft.md", fmt.Sprintf("d%d", i)); err != nil {
			t.Fatalf("append draft: %v", err)
		}
	}
	// 停止进展：连续空转拦截攒满阈值后才升级。
	for i := range subagentMaxConsecutiveBlocks {
		if _, err := guard(context.Background(), info); err != nil {
			t.Fatalf("escalated too early at idle block %d: %v", i, err)
		}
	}
	if _, err := guard(context.Background(), info); !errors.Is(err, ErrStopGuardEscalated) {
		t.Fatalf("expected escalate after consecutive no-progress blocks, got %v", err)
	}
}

// TestWriterStopGuard_StageAwareBlockMessage 验证催促消息按已落盘 step 组装：
// 静态的"必须调 commit_chapter"在前置步骤缺失或 commit 报错时会误导模型（issue #75）。
func TestWriterStopGuard_StageAwareBlockMessage(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)
	info := normalStop()

	// 无任何产物：应引导完整流程，而不是直接催 commit。
	msgs, err := guard(context.Background(), info)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("expected one blocking message, got %v err=%v", msgs, err)
	}
	if got := msgs[0].Text(); !strings.Contains(got, "draft_chapter") || !strings.Contains(got, "plan_chapter") {
		t.Fatalf("no-draft message should walk through the protocol, got %q", got)
	}

	// 草稿已落盘：应催 check_consistency 收尾。
	if _, err := s.Checkpoints.Append(domain.ChapterScope(1), "draft", "drafts/01.draft.md", "d1"); err != nil {
		t.Fatalf("append draft: %v", err)
	}
	msgs, err = guard(context.Background(), info)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("expected one blocking message, got %v err=%v", msgs, err)
	}
	if got := msgs[0].Text(); !strings.Contains(got, "check_consistency") {
		t.Fatalf("draft-only message should point to check_consistency, got %q", got)
	}

	// 草稿+一致性检查已完成：只差提交，且要为 commit 报错场景留出路。
	if _, err := s.Checkpoints.Append(domain.ChapterScope(1), "consistency_check", "meta/checks/01.json", "c1"); err != nil {
		t.Fatalf("append consistency_check: %v", err)
	}
	msgs, err = guard(context.Background(), info)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("expected one blocking message, got %v err=%v", msgs, err)
	}
	if got := msgs[0].Text(); !strings.Contains(got, "commit_chapter") || !strings.Contains(got, "错误") {
		t.Fatalf("ready-to-commit message should mention commit and error handling, got %q", got)
	}
}

// TestSubAgentGuard_BlockHookReceivesAgentAndReason 验证审计回调收到正确的
// agent 名与 reason 序列——Host 靠它把拦截浮出到 TUI。
func TestSubAgentGuard_BlockHookReceivesAgentAndReason(t *testing.T) {
	s := newTestStore(t)
	var agents, reasons []string
	guard := NewWriterStopGuard(s, func(agent, reason string, _ int32) {
		agents = append(agents, agent)
		reasons = append(reasons, reason)
	})
	info := normalStop()

	for range subagentMaxConsecutiveBlocks + 1 {
		_, _ = guard(context.Background(), info)
	}
	if len(reasons) != subagentMaxConsecutiveBlocks+1 {
		t.Fatalf("hook called %d times, want %d", len(reasons), subagentMaxConsecutiveBlocks+1)
	}
	for i, agent := range agents {
		if agent != "writer" {
			t.Fatalf("hook call %d: agent = %q, want writer", i, agent)
		}
	}
	for i := range subagentMaxConsecutiveBlocks {
		if reasons[i] != "blocked" {
			t.Fatalf("reason[%d] = %q, want blocked", i, reasons[i])
		}
	}
	if last := reasons[len(reasons)-1]; last != "escalated" {
		t.Fatalf("last reason = %q, want escalated", last)
	}

	// hard_stop 也要上报。
	var hardReasons []string
	hardGuard := NewWriterStopGuard(s, func(_, reason string, _ int32) {
		hardReasons = append(hardReasons, reason)
	})
	_, _ = hardGuard(context.Background(), agentcore.StopInfo{Turns: 1, Message: agentcore.Message{Stop: agentcore.StopSafety}})
	if len(hardReasons) != 1 || hardReasons[0] != "hard_stop" {
		t.Fatalf("hard stop hook reasons = %v, want [hard_stop]", hardReasons)
	}
}

// TestEditorStopGuard_TaskAware 验证任务感知：被派生成弧摘要时，仅 save_review（复核）
// 不算完成，必须产出 arc_summary 才放行——封堵卷中骨架弧死循环的起点 Defect C。
func TestEditorStopGuard_TaskAware(t *testing.T) {
	info := normalStop()

	// 摘要任务 + 只存了 review → 必须阻拦（review 不满足 arc_summary 要求）。
	t.Run("summary task blocks on review only", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧摘要（save_arc_summary）", nil)
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		msgs, err := guard(context.Background(), info)
		if err != nil {
			t.Fatalf("first block must not escalate: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("summary task must NOT be satisfied by a review checkpoint, got %v", msgs)
		}
	})

	// 摘要任务 + 已存 arc_summary → 放行。
	t.Run("summary task allows on arc_summary", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧摘要（save_arc_summary）", nil)
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "arc_summary", "summaries/arc-v05a01.json", "d1"); err != nil {
			t.Fatalf("append arc_summary: %v", err)
		}
		msgs, err := guard(context.Background(), info)
		if err != nil || len(msgs) != 0 {
			t.Fatalf("summary task must be satisfied by an arc_summary checkpoint, got %v err=%v", msgs, err)
		}
	})

	// 评审任务 + 存了 review → 放行（默认宽松行为不变）。
	t.Run("review task allows on review", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "对第 5 卷第 1 弧做弧级评审（scope=arc）", nil)
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		msgs, err := guard(context.Background(), info)
		if err != nil || len(msgs) != 0 {
			t.Fatalf("review task must be satisfied by a review checkpoint, got %v err=%v", msgs, err)
		}
	})
}
