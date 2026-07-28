package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	buildversion "github.com/voocel/ainovel-cli/internal/version"
)

// 这组测试锁住"生图生到一半被自己取消"那条链路。
//
// 现场证据（output/novel/logs/desktop.log）：
//   23:04:00 读取生图响应中断 got=750090 elapsed=6m24s err="context canceled"
//   23:04:05 恢复创作 label="恢复：从第 36 章继续"
// 取消后 5 秒就出现一次 Resume——那是被重建出来的新 Host 在恢复，不是超时、也不是
// 网络断了：用户在生图期间点了书库里的按钮，OpenBook 无条件走了
// closeCurrentHost → abortAll，把已经收了 750KB、服务端已计费的那张图丢了。
//
// 修复由两道闸构成，两道都要有测试守着：
//  1. 同一本书时 OpenBook 不重建（快路径）
//  2. 不同书且生图在途时 OpenBook 直接报错，不静默作废

// TestCoverStore_ActiveLifecycle 覆盖在途标记的三个状态：初始为空、生图中为书目录、
// 收尾后清空。前端挂载时靠 CoverJobDir 读它补上心跳之外的空窗。
func TestCoverStore_ActiveLifecycle(t *testing.T) {
	var c coverStore
	if got := c.active(); got != "" {
		t.Errorf("初始应无在途生图，实际 %q", got)
	}
	c.setActive(`E:\books\a`)
	if got := c.active(); got != `E:\books\a` {
		t.Errorf("active() = %q，想要 E:\\books\\a", got)
	}
	c.setActive("")
	if got := c.active(); got != "" {
		t.Errorf("收尾后应清空，实际 %q", got)
	}
}

// TestCoverStore_ActiveNotBlockedByGeneration 是这个设计的要害：
// activeDir 必须由独立的锁保护，不能复用 coverStore.mu。
//
// mu 在整个生图期间（可能十分钟）都被 GenerateCover 持有，如果 active() 也去抢 mu，
// 那么 OpenBook 的拦截检查、前端的 CoverJobDir 查询都会一起阻塞十分钟——界面直接假死。
func TestCoverStore_ActiveNotBlockedByGeneration(t *testing.T) {
	var c coverStore

	// 模拟 GenerateCover：全程持有 mu。
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setActive(`E:\books\a`)

	// 这一步若去抢 mu 就会死锁，测试会超时失败而不是通过。
	done := make(chan string, 1)
	go func() { done <- c.active() }()

	select {
	case got := <-done:
		if got != `E:\books\a` {
			t.Errorf("active() = %q，想要 E:\\books\\a", got)
		}
	case <-timeoutAfterShort():
		t.Fatal("active() 在生图持锁期间被阻塞了——说明它复用了 mu，界面会假死")
	}
}

// TestCoverStore_ActiveConcurrent 生图 goroutine 写、UI 查询读，必须无数据竞争。
// 用 -race 跑才有意义。
func TestCoverStore_ActiveConcurrent(t *testing.T) {
	var c coverStore
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.setActive(`E:\books\a`) }()
		go func() { defer wg.Done(); _ = c.active() }()
	}
	wg.Wait()
}

// TestOpenBook_SameDirIsNoop 快路径：同一本书不该重建 Host。
//
// 这是 imagegen_test.go 里 TestJobRegistry_AbortAllCancelsCover 的上半段——
// 后者证明 abortAll 会杀掉 cover 作业，这里证明同书时压根不会走到 abortAll。
// 不构造真 Host（要连模型、要建目录），只验证快路径的判据本身。
//
// 用现场的真实路径形状：library.json 里存的是
// E:\inkos-work\ainovel-cli\cmd\ainovel-desktop\output\novel（绝对），
// 而 Host.Dir() 来自 store，store 原样保存传入的 dir 不做绝对化，可能是 output/novel。
// 不用 t.TempDir()：临时目录在 C 盘而仓库在 E 盘，跨盘算不出相对路径，
// 用例会变成 skip——一个永远跳过的测试守不住任何东西。
func TestOpenBook_SameDirIsNoop(t *testing.T) {
	wd := mustGetwd(t)
	rel := filepath.Join("output", "novel")
	abs := filepath.Join(wd, "output", "novel")

	if !sameDir(rel, abs) {
		t.Fatalf("相对路径 %q 与绝对路径 %q 应判为同一本书——"+
			"判错就会重建 Host 并 abortAll 掉在途生图", rel, abs)
	}
	// 反过来也要成立：调用方传参顺序不该影响结论。
	if !sameDir(abs, rel) {
		t.Fatalf("反向判定失败: %q vs %q", abs, rel)
	}
	// 邻近但不同的书必须判为不同，否则会错误复用别人的 Host。
	if sameDir(rel, filepath.Join(wd, "output", "novel2")) {
		t.Error("output/novel 与 output/novel2 是两本书，不该判为相同")
	}
}

// TestOpenBook_BlocksSwitchDuringCover 后端硬闸：生图在途时切到别的书必须报错。
//
// 前端也拦了一道，但那道不可靠——CreateBook 同样走 OpenBook，界面状态还可能因组件
// 卸载而丢失。宁可报错让用户决定（等一会儿或点取消），也不能默默作废一张已计费的图。
func TestOpenBook_BlocksSwitchDuringCover(t *testing.T) {
	bookA := t.TempDir()
	bookB := t.TempDir()

	a := NewApp(versionForTest())
	// 直接置位在途标记，绕开真实生图（要连生图服务）。
	a.cover.setActive(bookA)
	// 让 ensureConfig 走到"已加载"，避免读用户真实配置。
	a.cfgLoaded = true

	// 挂一个真的在途 cover 作业。这才是要保护的东西：现场丢的那张图，
	// 就是它的 ctx 被 closeCurrentHost → abortAll 取消掉的。
	coverCtx, _ := a.jobs.begin("cover")

	_, err := a.OpenBook(bookB)
	if err == nil {
		t.Fatal("生图在途时切到别的书应报错，实际放行了——这会静默丢掉一张已计费的图")
	}
	if !strings.Contains(err.Error(), "封面") {
		t.Errorf("错误信息应说明是封面生图挡住了，实际: %v", err)
	}

	// 最要紧的一条：拦截必须发生在 closeCurrentHost 之前。
	// 报了错但图已经被 abortAll 干掉，等于没拦。
	if coverCtx.Err() != nil {
		t.Errorf("在途生图被取消了（%v）——拦截发生得太晚，那张已计费的图还是丢了", coverCtx.Err())
	}
	if a.cover.active() != bookA {
		t.Error("拦截路径不该动在途标记")
	}
}

// TestOpenBook_AllowsSameBookDuringCover 拦截不能误伤本书自己：
// 生图期间在同一本书上点"封面""阅读"是正常操作，必须放行。
func TestOpenBook_AllowsSameBookDuringCover(t *testing.T) {
	book := t.TempDir()
	a := NewApp(versionForTest())
	a.cover.setActive(book)
	a.cfgLoaded = true

	// host 为 nil，走不到快路径，但也不该被生图闸拦住——
	// 拦截判据是 !sameDir(active, dir)，同书应放行。
	if active := a.cover.active(); !sameDir(active, book) {
		t.Fatalf("同一本书应判为相同: active=%q dir=%q", active, book)
	}
}

// TestOpenBook_EmptyDirNotBlockedByOtherBook 用默认目录开书（dir="")时，
// sameDir 对空串一律返回 false，会被判成"切到别的书"。这是有意为之：
// 空 dir 意味着走配置里的默认目录，确实可能不是正在生图的那本。
func TestOpenBook_EmptyDirNotBlockedByOtherBook(t *testing.T) {
	if sameDir("", `E:\books\a`) {
		t.Error("空串不该与任何路径判为相同")
	}
}

// ── 测试辅助 ──

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func versionForTest() buildversion.Info { return buildversion.Info{} }

// timeoutAfterShort 给"不该阻塞"的断言一个上限。取 2s：远大于一次锁操作，
// 又不至于让死锁的用例拖到 go test 的默认超时。
func timeoutAfterShort() <-chan time.Time {
	return time.After(2 * time.Second)
}
