package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// TestHostReopen 守护 /reopen 的用户级重开出口：完本是重决策，重开只能由用户显式
// 发起——未完结拒绝、运行中拒绝；重开成功把 phase 回退 writing，附带的续写方向登记为
// 待处理干预（PendingSteer），恢复时先经 Arbiter 裁定注入再续跑。
func TestHostReopen(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	h := &Host{store: st, events: make(chan Event, 8)}

	if err := st.Progress.Init("书", 2, domain.GenreNovel); err != nil {
		t.Fatal(err)
	}
	if err := h.Reopen(""); err == nil {
		t.Fatal("未完结的书应拒绝重开")
	}

	_ = st.Progress.UpdatePhase(domain.PhaseWriting)
	if err := st.Progress.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	if err := h.Reopen("以八十年大限开新卷"); err != nil {
		t.Fatalf("完结书重开应成功：%v", err)
	}
	p, _ := st.Progress.Load()
	if p.Phase != domain.PhaseWriting {
		t.Fatalf("重开后 phase 应为 writing，得 %s", p.Phase)
	}
	if len(p.PendingRewrites) != 0 || p.ReopenedFromComplete {
		t.Fatalf("续写重开不得携带返工语义：%+v", p)
	}
	// 重开计数必须落盘：再完结的 progress digest 才会与上次不同——checkpoint 同 digest
	// 幂等去重，字节相同的再完结无新 checkpoint，StopGuard 会把成功完本误判为空转终止。
	if p.ReopenCount != 1 {
		t.Fatalf("重开计数应为 1，得 %d", p.ReopenCount)
	}
	meta, _ := st.RunMeta.Load()
	if meta == nil || !strings.Contains(meta.PendingSteer, "八十年大限") {
		t.Fatalf("续写方向应登记为待处理干预，得 %+v", meta)
	}

	running := &Host{store: st, lifecycle: lifecycleRunning, events: make(chan Event, 1)}
	if err := running.Reopen(""); err == nil {
		t.Fatal("引擎运行中应拒绝重开")
	}
}
