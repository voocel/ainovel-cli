// internal/host/reminder/contest_guards_test.go
package reminder

import (
	"context"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
	"github.com/voocel/agentcore"
)

// TestCandidateStopGuard_BlocksWithoutDraft 验证：没有 draft checkpoint 时应拦截 end_turn。
func TestCandidateStopGuard_BlocksWithoutDraft(t *testing.T) {
	st := store.NewStore(t.TempDir())
	guard := NewCandidateStopGuard(st)
	// StopInfo 使用正常 stop reason，确保走到 checkpoint 扫描路径，不触发 hardStop
	dec := guard(context.Background(), agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	})
	if dec.Allow {
		t.Fatal("无 draft checkpoint 时应拦截 end_turn")
	}
}

// TestCandidateStopGuard_AllowsAfterDraft 验证：baseline 之后出现 draft checkpoint 后应放行。
func TestCandidateStopGuard_AllowsAfterDraft(t *testing.T) {
	st := store.NewStore(t.TempDir())
	guard := NewCandidateStopGuard(st) // baseline 在此刻捕获
	// 使用 Append（直接提供 digest）避免读取实际文件
	if _, err := st.Checkpoints.Append(domain.ChapterScope(1), "draft", "drafts/01.cand-wuzei.md", "sha256:test"); err != nil {
		t.Fatalf("append checkpoint: %v", err)
	}
	dec := guard(context.Background(), agentcore.StopInfo{
		TurnIndex: 2,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	})
	if !dec.Allow {
		t.Fatal("已有新 draft checkpoint 时应放行")
	}
}

// TestJudgeStopGuard_RequiresVerdict 验证：没有 verdict checkpoint 时应拦截 end_turn。
func TestJudgeStopGuard_RequiresVerdict(t *testing.T) {
	st := store.NewStore(t.TempDir())
	guard := NewJudgeStopGuard(st)
	if guard(context.Background(), agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	}).Allow {
		t.Fatal("无 verdict 应拦截")
	}
}
