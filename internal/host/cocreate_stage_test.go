package host

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/store"
)

// newFlagTestHost 造一个最小 Host，只够驱动 cocreating 标记状态机与并发守卫。
// emitEvent 用 recover + 非阻塞 select，缓冲 events 通道即可，无需 observer。
// PauseForCoCreate 的运行态分支会调 Engine Abort（复用已验证的 Esc 暂停路径），
// 不在此单测；这里只覆盖非运行态与标记/守卫逻辑。
func newFlagTestHost(lc lifecycle, cocreating bool) *Host {
	return &Host{
		lifecycle:  lc,
		cocreating: cocreating,
		engine:     &engine{}, // acquireExclusive 查 engine.isRunning()（停止窗口门禁）
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
		t.Error("全书完成后不应允许进入阶段共创")
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
		t.Error("取消后 cocreating 应清空")
	}
	if h.lifecycle != lifecyclePaused {
		t.Errorf("取消不应改 lifecycle，得 %s", h.lifecycle)
	}
}

func TestCancelCoCreate_NoopWhenNotCocreating(t *testing.T) {
	h := newFlagTestHost(lifecycleRunning, false)
	h.CancelCoCreate() // 不应 panic，不应改状态
	if h.cocreating || h.lifecycle != lifecycleRunning {
		t.Error("非共创态 CancelCoCreate 应为 no-op")
	}
}

func TestResumeFromCoCreate_RejectsEmptyDraft(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	if err := h.ResumeFromCoCreate("   "); err == nil {
		t.Fatal("空 draft 应报错")
	}
	if !h.cocreating {
		t.Error("空 draft 在清标记前返回，cocreating 应保持 true")
	}
}

func TestResumeFromCoCreate_RejectsWhenNotCocreating(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, false)
	err := h.ResumeFromCoCreate("## 后续走向\n- 进入第二卷")
	if err == nil || !strings.Contains(err.Error(), "not in co-create") {
		t.Fatalf("非共创态应报 not in co-create，得 %v", err)
	}
}

func TestAcquireExclusive(t *testing.T) {
	cases := []struct {
		name       string
		lc         lifecycle
		cocreating bool
		exclusive  string
		wantErr    string // 空=期望放行
	}{
		{"running", lifecycleRunning, false, "", "运行中"},
		{"cocreating", lifecyclePaused, true, "", "阶段共创"},
		{"busy", lifecycleIdle, false, "导入", "进行中"},
		{"idle free", lifecycleIdle, false, "", ""},
		{"paused free", lifecyclePaused, false, "", ""},
	}
	// Abort 停止窗口：lifecycle 已置 paused 但引擎 goroutine 尚未退净，仍须拒绝——
	// 否则导入会与引擎收尾并发写同一 store。
	drain := newFlagTestHost(lifecyclePaused, false)
	drain.engine.running = true
	if err := drain.acquireExclusive("导入"); err == nil {
		t.Fatal("引擎排水期应拒绝独占作业")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFlagTestHost(c.lc, c.cocreating)
			h.exclusive = c.exclusive
			err := h.acquireExclusive("导入")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("应放行，得 %v", err)
				}
				if h.exclusive != "导入" {
					t.Fatalf("放行后应登记占用，得 %q", h.exclusive)
				}
				h.releaseExclusive()
				if h.exclusive != "" {
					t.Fatalf("释放后占用应清空，得 %q", h.exclusive)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("应含 %q，得 %v", c.wantErr, err)
			}
			if !strings.Contains(err.Error(), "导入") {
				t.Errorf("错误文案应带 action %q，得 %v", "导入", err)
			}
		})
	}
}

// TestExclusiveBlocksCreationEntries 守护 #2：后台独占作业（导入/仿写）进行中时，
// 不仅第二个后台作业被堵，创作写入口（Continue/Resume）与新后台作业也必须被堵，
// 否则 Continue 会在引擎被门禁拦下前就让 Arbiter 改状态、Resume/next 期间引擎可抢跑。
func TestExclusiveBlocksCreationEntries(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	h.exclusive = "导入"
	if _, err := h.ImportFrom(context.Background(), imp.Options{}); err == nil {
		t.Error("独占作业期间 ImportFrom 应被拒")
	}
	if err := h.Continue("继续写"); err == nil {
		t.Error("独占作业期间 Continue 应被拒（须在 Arbiter 裁定前挡住）")
	}
	if _, err := h.Resume(); err == nil {
		t.Error("独占作业期间 Resume 应被拒")
	}
}

// TestStageCoCreate_OccupancyBlocksConcurrentEntries 验证共创窗口内独占性入口全部被堵：
// import/start/resume/continue 在 cocreating 期间都应被拒，补上 paused 期只查 ==running 的缺口。
func TestStageCoCreate_OccupancyBlocksConcurrentEntries(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	if !h.PauseForCoCreate() {
		t.Fatal("进入阶段共创失败")
	}

	if _, err := h.ImportFrom(context.Background(), imp.Options{}); err == nil {
		t.Error("共创窗口内 ImportFrom 应被拒")
	}
	if err := h.StartPrepared("写个新故事"); err == nil {
		t.Error("共创窗口内 StartPrepared 应被拒")
	}
	if _, err := h.Resume(); err == nil {
		t.Error("共创窗口内 Resume 应被拒")
	}
	if err := h.Continue("继续写"); err == nil {
		t.Error("共创窗口内 Continue 应被拒")
	}

	// 退出共创后占用解除（这里走 Cancel；Resume 干预路径归集成验证）
	h.CancelCoCreate()
	if h.cocreating {
		t.Fatal("退出后占用标记应解除")
	}
}

func TestBuildStoryStateSummary_NilStore(t *testing.T) {
	if got := buildStoryStateSummary(nil); got != "" {
		t.Errorf("nil store 应返回空串，得 %q", got)
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
	for _, want := range []string{"影之诗", "已完成 3 章", "下一章为第 4 章", "主角登临绝巅", "师门血仇未报", "预计 4-6 卷"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要应含 %q，实际:\n%s", want, got)
		}
	}
}
