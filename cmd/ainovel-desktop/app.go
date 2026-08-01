package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/logger"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/skills"
	buildversion "github.com/voocel/ainovel-cli/internal/version"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 是 Wails 绑定的根对象，持有当前打开的一本书对应的 *host.Host。
//
// 生命周期：OnStartup 只存 ctx（不建 Host）；Host 由 OpenBook 按书目录创建，
// 切书 = closeCurrentHost + 新建。所有 Host 动作方法都经 reqHost() 取当前 Host，
// 未开书时返回结构化错误，前端在书库/引导页不会误触到 nil host。
//
// 并发：mu 保护 host / pump 生命周期字段；Host 自身线程安全。
type App struct {
	ctx     context.Context
	version buildversion.Info

	mu         sync.Mutex
	hostMu     sync.Mutex // 串行化开书/关书，避免并发 OpenBook 泄漏 Host 与事件泵
	host       *host.Host
	baseCfg    bootstrap.Config // 已加载的全局配置（不含 OutputDir），OpenBook 时按书目录派生
	cfgLoaded  bool
	pumpCancel context.CancelFunc
	logCleanup func()

	ask      *askBridge
	cocreate coCreateJobs
	jobs     *jobRegistry
	lib      library
	cover    coverStore

	// closeConfirmed 记录用户已在"仍在创作中，确认退出？"对话框里点过确认。
	closeConfirmed bool
}

// NewApp 构造未打开任何书的 App。
func NewApp(version buildversion.Info) *App {
	return &App{version: version, jobs: newJobRegistry()}
}

// ── Wails 生命周期 ──

// OnStartup 保存 Wails 注入的 ctx（EventsEmit 需要它）。不在此建 Host：
// 是否需要首次引导、打开哪本书都由前端决定后回调 NeedsSetup / OpenBook。
func (a *App) OnStartup(ctx context.Context) {
	a.ctx = ctx
	slog.Info("桌面应用启动", "module", "desktop")
}

// OnShutdown 关闭当前 Host 并停止事件泵。
func (a *App) OnShutdown(_ context.Context) {
	a.hostMu.Lock()
	defer a.hostMu.Unlock()
	a.closeCurrentHost()
}

// OnBeforeClose 在创作或后台作业仍在运行时先弹原生确认框，避免误关中断在途工作。
// 返回 true = 阻止关闭。
//
// 中断本身是安全的（引擎 step 级 checkpoint，重开自动恢复），但正在烧 token 的
// 请求会被丢弃，值得问一句。用户确认过一次后不再重复询问。
func (a *App) OnBeforeClose(ctx context.Context) (prevent bool) {
	a.mu.Lock()
	h := a.host
	confirmed := a.closeConfirmed
	a.mu.Unlock()

	if confirmed {
		return false
	}
	engineRunning := h != nil && h.Snapshot().IsRunning
	backgroundRunning := a.jobs.active() || a.cocreate.active() || a.cover.active() != ""
	if !engineRunning && !backgroundRunning {
		return false
	}
	title := "后台任务仍在进行"
	message := "当前有后台任务仍在进行。退出会中断在途请求，确定退出吗？"
	if engineRunning {
		title = "创作仍在进行"
		message = "当前正在创作中。退出会中断在途的模型请求（已完成的进度已保存，下次打开可继续恢复）。确定退出吗？"
	}

	choice, err := wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{
		Type:          wailsruntime.QuestionDialog,
		Title:         title,
		Message:       message,
		Buttons:       []string{"退出", "继续创作"},
		DefaultButton: "继续创作",
		CancelButton:  "继续创作",
	})
	if err != nil {
		// 对话框不可用时不要把用户锁在应用里。
		slog.Warn("退出确认对话框失败，放行关闭", "module", "desktop", "err", err)
		return false
	}
	if choice != "退出" {
		return true
	}
	a.mu.Lock()
	a.closeConfirmed = true
	a.mu.Unlock()
	return false
}

// ── 配置 / 引导（M1 仅只读检查，写入在 M3） ──

// NeedsSetup 报告是否需要首次引导（全局与项目级配置都不存在）。
func (a *App) NeedsSetup() bool {
	return bootstrap.NeedsSetup()
}

// ensureConfig 惰性加载一次全局配置到 baseCfg。
func (a *App) ensureConfig() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfgLoaded {
		return nil
	}
	cfg, err := bootstrap.LoadConfig()
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	a.baseCfg = cfg
	a.cfgLoaded = true
	return nil
}

// ── 开书 / 切书 ──

// BookOpenResult 描述只读挂载后的书籍状态。HasProgress 不依赖 RecoveryLabel，
// 因为已完结书没有恢复标签，但仍应进入工作台供阅读、导出和重开。
type BookOpenResult struct {
	Phase         string `json:"phase"`
	HasProgress   bool   `json:"hasProgress"`
	RecoveryLabel string `json:"recoveryLabel"`
	IsRunning     bool   `json:"isRunning"`
}

func bookOpenResult(h *host.Host) BookOpenResult {
	return bookOpenResultFromSnapshot(h.Snapshot())
}

func bookOpenResultFromSnapshot(snap host.UISnapshot) BookOpenResult {
	return BookOpenResult{
		Phase:         snap.Phase,
		RecoveryLabel: snap.RecoveryLabel,
		IsRunning:     snap.IsRunning,
		HasProgress: snap.Phase != "" || snap.CurrentChapter > 0 || snap.TotalChapters > 0 ||
			snap.CompletedCount > 0 || snap.Premise != "" || len(snap.Outline) > 0,
	}
}

// OpenBook 打开（或切换到）一本书：以 dir 作为 OutputDir 新建 Host 并启动事件泵。
// 它只挂载上下文，不启动创作；明确进入创作工作台时由前端调用 ResumeBook。
// dir 为空表示用配置默认目录。
//
// 镜像 internal/entry/tui/app.go 的 Run：host.New → AskUser().SetHandler →
// logger.SetupFile → 起事件泵。切书前先彻底关闭旧 Host。
func (a *App) OpenBook(dir string) (BookOpenResult, error) {
	a.hostMu.Lock()
	defer a.hostMu.Unlock()

	if err := a.ensureConfig(); err != nil {
		return BookOpenResult{}, err
	}
	a.mu.Lock()
	cfg := bootstrap.CloneConfig(a.baseCfg)
	cur := a.host
	a.mu.Unlock()
	if strings.TrimSpace(dir) != "" {
		cfg.OutputDir = strings.TrimSpace(dir)
	}
	cfg.FillDefaults()
	targetDir := cfg.OutputDir

	// 已经是这本书就直接返回，不重建 Host。
	//
	// 重建的代价不只是浪费：closeCurrentHost 会 abortAll 掉所有在途作业。
	// 书库里的"封面""阅读"按钮都要先切上下文才能调对应接口，如果每次都重建，
	// 生图生到一半回书库点一下就会被自己取消掉（实测：已收 750KB、6m24s 后
	// context canceled，服务端已计费的图全丢）。同一本书时这一步必须是空操作。
	if cur != nil && sameDir(cur.Dir(), targetDir) {
		// 返回值决定前端进"创作台"还是"新书页"，不能因为走了快路径就返回空串。
		// Snapshot().RecoveryLabel 与 Resume() 同源（都取 resumeLabel(store)），
		// 且是纯 store 读取，不会启动引擎。
		return bookOpenResult(cur), nil
	}

	// 生图在途时不允许切到别的书。前端也拦了一道，但这里是唯一可靠的位置：
	// CreateBook 同样走 OpenBook，且界面状态可能因组件卸载而丢失。宁可报错让用户
	// 决定（等一会儿或点取消），也不能默默作废一张服务端已经计费的图。
	// 生图有 10 分钟总预算兜底，所以这个拦截不会无限期挡住用户。
	if active := a.cover.active(); active != "" && !sameDir(active, targetDir) {
		return BookOpenResult{}, fmt.Errorf("正在生成封面（%s），切换到其他书会中断它。请等生成结束，或先取消生图", active)
	}
	// 其他模型请求同样可能已经计费。切书会关闭旧 Host 并取消它们，因此必须让
	// 用户先显式暂停/取消，而不是把“返回书库”悄悄变成一次中止操作。
	if cur != nil && cur.Snapshot().IsRunning {
		return BookOpenResult{}, fmt.Errorf("当前书仍在创作，切换书籍会中断在途模型请求。请先暂停创作")
	}
	if a.cocreate.active() || a.jobs.active() {
		return BookOpenResult{}, fmt.Errorf("当前书仍有后台任务，切换书籍会中断在途请求。请先等待完成或取消任务")
	}
	a.closeCurrentHost()

	// 关掉引擎的系统通知通道：Windows 下它用 PowerShell 弹气泡
	// （internal/notify/notify.go），在 GUI 里表现为突兀的空白控制台窗口。
	// 桌面版自身就在前台，状态已由界面完整呈现，不需要系统气泡。
	// 用户显式配了自定义 command（如手机推送）则尊重其配置，不擅自关闭。
	if strings.TrimSpace(cfg.Notify.Command) == "" {
		disabled := false
		cfg.Notify.Enabled = &disabled
	}

	rules.EnsureHomeRulesDir()
	skills.EnsureHomeSkillsDir()
	bundle := assets.Load(cfg.Style, assets.DefaultLoadOptions(cfg.OutputDir))

	h, err := host.New(cfg, bundle)
	if err != nil {
		return BookOpenResult{}, fmt.Errorf("初始化创作引擎失败: %w", err)
	}

	a.ask = newAskBridge(func(id string, qs []askQuestion) {
		a.emit("engine:askuser", map[string]any{"id": id, "questions": qs})
	})
	h.AskUser().SetHandler(a.ask.handler)

	cleanup, logErr := logger.SetupFile(h.Dir(), "desktop.log", false)
	if logErr != nil {
		slog.Warn("桌面文件日志不可用，继续运行", "module", "desktop", "err", logErr)
		cleanup = func() {}
	}

	pumpCtx, pumpCancel := context.WithCancel(context.Background())

	a.mu.Lock()
	a.host = h
	a.pumpCancel = pumpCancel
	a.logCleanup = cleanup
	a.closeConfirmed = false // 换书后重新征询退出确认
	a.mu.Unlock()

	go a.runPump(pumpCtx, h)
	go a.runSnapshotLoop(pumpCtx, h)

	// 记入书库（用当前书名，可能为空——ListBooks 会现读 store 补全）。
	if err := a.lib.remember(h.Dir(), strings.TrimSpace(h.Snapshot().NovelName)); err != nil {
		slog.Warn("记录书库失败", "module", "desktop", "err", err)
	}

	return bookOpenResult(h), nil
}

// closeCurrentHost 彻底关闭当前 Host：停事件泵 → Host.Close（幂等）→ 关日志。
func (a *App) closeCurrentHost() {
	a.mu.Lock()
	h := a.host
	pumpCancel := a.pumpCancel
	logCleanup := a.logCleanup
	a.host = nil
	a.pumpCancel = nil
	a.logCleanup = nil
	a.ask = nil
	a.mu.Unlock()

	// 在途共创与长任务绑定的是旧 Host，必须先中断再关 Host。
	a.cocreate.abort()
	a.jobs.abortAll()
	if pumpCancel != nil {
		pumpCancel()
	}
	if h != nil {
		h.Close()
	}
	if logCleanup != nil {
		logCleanup()
	}
}

// reqHost 返回当前 Host，未开书时报错。
func (a *App) reqHost() (*host.Host, error) {
	a.mu.Lock()
	h := a.host
	a.mu.Unlock()
	if h == nil {
		return nil, fmt.Errorf("尚未打开任何书，请先在书库中选择或新建")
	}
	return h, nil
}

// emit 是所有 Wails 事件发射的唯一入口（ctx 缺失时静默丢弃）。
func (a *App) emit(name string, data ...any) {
	if a.ctx == nil {
		return
	}
	wailsEventsEmit(a.ctx, name, data...)
}

// ── 创作动作绑定 ──

// StartQuick 用一句话需求起新书：先归一化用户规则快照，再启动 Engine。
// 两步合并为一个绑定，避免前端在两次调用间掉线导致半初始化状态。
//
// reviewFirst=true 时先把推进模式切到逐章验收，于是引擎会在规划完设定、
// 正要写第 1 章之前停下（gate.Allow 拦截，见 internal/host/advance_gate.go），
// 让用户先审阅前提/大纲/人物/世界观。这是桌面版的默认路径。
func (a *App) StartQuick(rawRequirement string, reviewFirst bool, genre string) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	storyGenre, err := domain.ParseGenre(genre)
	if err != nil {
		return fmt.Errorf("无效的作品类型: %w", err)
	}
	plan, err := startup.PrepareQuick(startup.Request{
		Mode:       startup.ModeQuick,
		UserPrompt: rawRequirement,
	})
	if err != nil {
		return err
	}
	// 推进模式必须在启动引擎之前落盘，否则引擎可能已经开写第 1 章。
	mode := domain.ChapterAdvanceAuto
	if reviewFirst {
		mode = domain.ChapterAdvanceReview
	}
	if err := h.SetAdvanceMode(mode); err != nil {
		return err
	}
	if err := h.PrepareUserRules(plan.RawPrompt); err != nil {
		return err
	}
	return h.StartPreparedWithGenre(plan.RawPrompt, storyGenre)
}

// Continue 停机后用输入恢复创作（干预裁定 + 拉起引擎）。
func (a *App) Continue(text string) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	return h.Continue(text)
}

// ResumeBook 从已挂载书籍的 checkpoint 显式恢复创作。正在运行时为幂等空操作。
func (a *App) ResumeBook() error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	if h.Snapshot().IsRunning {
		return nil
	}
	_, err = h.Resume()
	return err
}

// Steer 运行中随时注入干预。
func (a *App) Steer(text string) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	return h.Steer(text)
}

// Abort 暂停当前创作。返回 false 表示当前无运行中的任务可暂停。
func (a *App) Abort() (bool, error) {
	h, err := a.reqHost()
	if err != nil {
		return false, err
	}
	return h.Abort(), nil
}

// Reopen 把已完结的书重开为创作态，可选带续写方向。
func (a *App) Reopen(direction string) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	if err := h.Reopen(direction); err != nil {
		return err
	}
	_, err = h.Resume()
	return err
}

// SetAdvanceMode 切换章节推进模式（"auto" / "review"）。
func (a *App) SetAdvanceMode(mode string) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	m := domain.ChapterAdvanceMode(mode)
	if m != domain.ChapterAdvanceAuto && m != domain.ChapterAdvanceReview {
		return fmt.Errorf("无效的推进模式: %q（应为 auto 或 review）", mode)
	}
	return h.SetAdvanceMode(m)
}

// AdvanceOneChapter 逐章验收模式下放行下一章。
func (a *App) AdvanceOneChapter() error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	return h.AdvanceOneChapter()
}

// GetSnapshot 拉取一次聚合状态快照（首屏挂载 / 动作后即时刷新用）。
func (a *App) GetSnapshot() (host.UISnapshot, error) {
	h, err := a.reqHost()
	if err != nil {
		return host.UISnapshot{}, err
	}
	return h.Snapshot(), nil
}

// Version 返回构建版本信息（About 弹窗）。
func (a *App) Version() buildversion.Info {
	return a.version
}
