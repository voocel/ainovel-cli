package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type gateRecorder struct {
	paused  int
	reasons []string
}

func newAdvanceGateTest(t *testing.T, mode domain.ChapterAdvanceMode) (*storepkg.Store, *ChapterAdvanceGate, *gateRecorder) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("store init: %v", err)
	}
	if err := st.RunMeta.Init("default", "test", "test"); err != nil {
		t.Fatalf("run meta init: %v", err)
	}
	if err := st.RunMeta.SetAdvanceMode(mode); err != nil {
		t.Fatalf("advance mode: %v", err)
	}
	if err := st.Progress.Init("闸门测试", 10, domain.GenreNovel); err != nil {
		t.Fatalf("progress init: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	recorder := &gateRecorder{}
	gate := NewChapterAdvanceGate(st, func(reason string) {
		recorder.paused++
		recorder.reasons = append(recorder.reasons, reason)
	}, func(_ string, summary string) {
		recorder.reasons = append(recorder.reasons, summary)
	})
	return st, gate, recorder
}

func TestChapterAdvanceGateReviewRequiresExactPermit(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceReview)
	forward := &flow.Instruction{Agent: "writer", Chapter: 1, Task: "写第 1 章"}

	allowed, err := gate.Allow(forward)
	if err != nil {
		t.Fatal(err)
	}
	if allowed || recorder.paused != 1 {
		t.Fatalf("未授权新章必须暂停: allowed=%v paused=%d", allowed, recorder.paused)
	}
	if len(recorder.reasons) == 0 || !strings.Contains(recorder.reasons[len(recorder.reasons)-1], "/next") {
		t.Fatalf("暂停文案必须给出明确放行方式: %v", recorder.reasons)
	}

	if err := st.RunMeta.GrantAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(forward)
	if err != nil || !allowed {
		t.Fatalf("匹配许可应放行: allowed=%v err=%v", allowed, err)
	}
	if err := st.RunMeta.ClearAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.GrantAdvancePermit(2); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(forward)
	if err == nil || allowed {
		t.Fatalf("不匹配许可必须显式失败: allowed=%v err=%v", allowed, err)
	}
}

func TestChapterAdvanceGateDoesNotGateRewriteOrRecovery(t *testing.T) {
	st, gate, _ := newAdvanceGateTest(t, domain.ChapterAdvanceReview)
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.SetPendingRewrites([]int{1}, "返工"); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.GrantAdvancePermit(2); err != nil {
		t.Fatal(err)
	}

	allowed, err := gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 1, Task: "重写第 1 章"})
	if err != nil || !allowed {
		t.Fatalf("返工不消耗正向章节许可: allowed=%v err=%v", allowed, err)
	}
	if gate.HandleBoundary() {
		t.Fatal("返工队列存在时 permit 与 NextChapter 的正常交错不应误报损坏")
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvancePermitChapter != 2 {
		t.Fatalf("返工期间许可必须保持: %+v", meta)
	}

	if err := st.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 2, Stage: domain.CommitStageStarted}); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 2, Task: "恢复第 2 章提交"})
	if err != nil || !allowed {
		t.Fatalf("提交恢复不得被当成新章: allowed=%v err=%v", allowed, err)
	}
}

func TestChapterAdvanceGateConsumesPermitOnlyAfterStableCommit(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceReview)
	if err := st.RunMeta.GrantAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 1, Stage: domain.CommitStageProgressMarked}); err != nil {
		t.Fatal(err)
	}
	if gate.HandleBoundary() {
		t.Fatal("提交 saga 未完成时不能消费许可或停机")
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvancePermitChapter != 1 {
		t.Fatalf("pending commit 期间许可必须保留: %+v", meta)
	}

	if err := st.Signals.ClearPendingCommit(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.ChapterScope(1), "commit", "", ""); err != nil {
		t.Fatal(err)
	}
	if gate.HandleBoundary() {
		t.Fatal("稳定提交只消费许可，下一轮派发前才进入等待")
	}
	meta, _ = st.RunMeta.Load()
	if meta.AdvancePermitChapter != 0 {
		t.Fatalf("稳定提交后许可必须消费: %+v", meta)
	}
	allowed, err := gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 2})
	if err != nil || allowed || recorder.paused != 1 {
		t.Fatalf("消费后下一章必须重新等待授权: allowed=%v paused=%d err=%v", allowed, recorder.paused, err)
	}
}

func TestChapterAdvanceGateRejectsCorruptPermitState(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceReview)
	if err := st.RunMeta.GrantAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if !gate.HandleBoundary() || recorder.paused != 1 {
		t.Fatal("已完成但缺少 commit checkpoint 必须显式报错并暂停")
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvancePermitChapter != 1 {
		t.Fatal("损坏状态下不得猜测消费许可")
	}
}

func TestChapterAdvanceGateHoldLifecycle(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceAuto)
	hold := domain.AdvanceHold{After: domain.AdvanceHoldAfterRewritesDrained, Reason: "改完让我验收"}
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.SetPendingRewrites([]int{1}, "返工"); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.SetAdvanceHold(hold); err != nil {
		t.Fatal(err)
	}
	if gate.HandleBoundary() {
		t.Fatal("返工未排空时不能提前暂停")
	}
	if err := st.Progress.CompleteRewrite(1); err != nil {
		t.Fatal(err)
	}
	if !gate.HandleBoundary() || recorder.paused != 1 {
		t.Fatal("返工排空后必须消费 hold 并暂停")
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvanceHold != nil {
		t.Fatalf("暂停前 hold 必须原子消费: %+v", meta.AdvanceHold)
	}
}
