package host

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/store"
)

// newFlagTestHost 造一个最小 Host，只够驱动 cocreating 标记Trạng thái机与并发守卫。
// emitEvent 用 recover + 非阻塞 select，缓冲 events 通道即可，Không có需 coordinator/observer。
// PauseForCoCreate 的运行态分支会调 coordinator.Abort（复用已验证的 Esc Tạm dừngĐường dẫn），
// 不在此单测；这里只覆盖不依赖 coordinator 的非运行态与标记/守卫逻辑。
func newFlagTestHost(lc lifecycle, cocreating bool) *Host {
	return &Host{
		lifecycle:  lc,
		cocreating: cocreating,
		events:     make(chan Event, 16),
	}
}

func TestPauseForCoCreate_NonRunningSetsFlag(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	if !h.PauseForCoCreate() {
		t.Fatal("idle 态应允许进入阶段共创")
	}
	if !h.cocreating {
		t.Error("进入后 cocreating 应为 true")
	}
	if h.lifecycle != lifecycleIdle {
		t.Errorf("非运行态进入不应改 lifecycle，得 %s", h.lifecycle)
	}
}

func TestPauseForCoCreate_RejectsCompleted(t *testing.T) {
	h := newFlagTestHost(lifecycleCompleted, false)
	if h.PauseForCoCreate() {
		t.Error("全书Hoàn thành后不应允许进入阶段共创")
	}
	if h.cocreating {
		t.Error("拒绝后不应置位 cocreating")
	}
}

func TestPauseForCoCreate_RejectsReentrant(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	if h.PauseForCoCreate() {
		t.Error("已在共创中应拒绝重入")
	}
}

func TestCancelCoCreate_ClearsFlag(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	h.CancelCoCreate()
	if h.cocreating {
		t.Error("Hủy后 cocreating 应清Rỗng")
	}
	if h.lifecycle != lifecyclePaused {
		t.Errorf("Hủy不应改 lifecycle，得 %s", h.lifecycle)
	}
}

func TestCancelCoCreate_NoopWhenNotCocreating(t *testing.T) {
	h := newFlagTestHost(lifecycleRunning, false)
	h.CancelCoCreate() // 不应 panic，不应改Trạng thái
	if h.cocreating || h.lifecycle != lifecycleRunning {
		t.Error("非共创态 CancelCoCreate 应为 no-op")
	}
}

func TestResumeFromCoCreate_RejectsEmptyDraft(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	if err := h.ResumeFromCoCreate("   "); err == nil {
		t.Fatal("Rỗng draft 应报错")
	}
	if !h.cocreating {
		t.Error("Rỗng draft 在清标记前Quay lại，cocreating 应保持 true")
	}
}

func TestResumeFromCoCreate_RejectsWhenNotCocreating(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, false)
	err := h.ResumeFromCoCreate("## 后续走向\n- 进入第二卷")
	if err == nil || !strings.Contains(err.Error(), "not in co-create") {
		t.Fatalf("非共创态应报 not in co-create，得 %v", err)
	}
}

func TestGuardExclusive(t *testing.T) {
	cases := []struct {
		name       string
		lc         lifecycle
		cocreating bool
		wantErr    string // Rỗng=期望放行
	}{
		{"running", lifecycleRunning, false, "Đang chạy"},
		{"cocreating", lifecyclePaused, true, "阶段共创"},
		{"idle free", lifecycleIdle, false, ""},
		{"paused free", lifecyclePaused, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFlagTestHost(c.lc, c.cocreating)
			err := h.guardExclusive("Nhập")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("应放行，得 %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("应含 %q，得 %v", c.wantErr, err)
			}
			if !strings.Contains(err.Error(), "Nhập") {
				t.Errorf("Lỗi文案应带 action %q，得 %v", "Nhập", err)
			}
		})
	}
}

// TestStageCoCreate_OccupancyBlocksConcurrentEntries 验证共创Cửa sổ内独占性入口Tất cả被堵：
// import/start/resume/continue 在 cocreating 期间都应被拒，补上 paused 期只查 ==running 的缺口。
func TestStageCoCreate_OccupancyBlocksConcurrentEntries(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	if !h.PauseForCoCreate() {
		t.Fatal("进入阶段共创Thất bại")
	}

	if _, err := h.ImportFrom(context.Background(), imp.Options{}); err == nil {
		t.Error("共创Cửa sổ内 ImportFrom 应被拒")
	}
	if err := h.StartPrepared("写个Mới故事"); err == nil {
		t.Error("共创Cửa sổ内 StartPrepared 应被拒")
	}
	if _, err := h.Resume(); err == nil {
		t.Error("共创Cửa sổ内 Resume 应被拒")
	}
	if err := h.Continue("Tiếp tục写"); err == nil {
		t.Error("共创Cửa sổ内 Continue 应被拒")
	}

	// Thoát共创后占用解除（这里走 Cancel；Resume 注入Đường dẫn需 coordinator，归集成验证）
	h.CancelCoCreate()
	if h.cocreating {
		t.Fatal("Thoát后占用标记应解除")
	}
}

func TestBuildStoryStateSummary_NilStore(t *testing.T) {
	if got := buildStoryStateSummary(nil); got != "" {
		t.Errorf("nil store 应Quay lạiRỗng串，得 %q", got)
	}
}

func TestBuildStoryStateSummary_Populated(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("影之诗", 100); err != nil {
		t.Fatal(err)
	}
	p, _ := st.Progress.Load()
	p.CompletedChapters = []int{1, 2, 3}
	p.TotalWordCount = 12000
	if err := st.Progress.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveCompass(domain.StoryCompass{
		EndingDirection: "主角登临绝巅",
		OpenThreads:     []string{"师门血仇未报"},
		EstimatedScale:  "预计 4-6 卷",
	}); err != nil {
		t.Fatal(err)
	}

	got := buildStoryStateSummary(st)
	for _, want := range []string{"影之诗", "Đã hoàn thành 3 章", "下一章为第 4 章", "主角登临绝巅", "师门血仇未报", "预计 4-6 卷"} {
		if !strings.Contains(got, want) {
			t.Errorf("Tóm tắt应含 %q，实际:\n%s", want, got)
		}
	}
}
