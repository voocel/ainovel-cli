package host

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Accelerator-mzq/ainovel-cli/assets"
	"github.com/Accelerator-mzq/ainovel-cli/internal/agents"
	"github.com/Accelerator-mzq/ainovel-cli/internal/agents/ctxpack"
	"github.com/Accelerator-mzq/ainovel-cli/internal/bootstrap"
	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/host/exp"
	"github.com/Accelerator-mzq/ainovel-cli/internal/host/flow"
	"github.com/Accelerator-mzq/ainovel-cli/internal/host/imp"
	"github.com/Accelerator-mzq/ainovel-cli/internal/host/persona"
	modelreg "github.com/Accelerator-mzq/ainovel-cli/internal/models"
	"github.com/Accelerator-mzq/ainovel-cli/internal/rules"
	storepkg "github.com/Accelerator-mzq/ainovel-cli/internal/store"
	"github.com/Accelerator-mzq/ainovel-cli/internal/tools"
	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
)

// Host 是运行时薄外壳。
// 职责：启动/恢复/干预注入/事件投影/模型管理。
// 不做任何调度决策，不做空闲续跑。
type Host struct {
	cfg               bootstrap.Config
	bundle            assets.Bundle
	store             *storepkg.Store
	models            *bootstrap.ModelSet
	coordinator       *agentcore.Agent
	coordinatorCtxMgr *corecontext.ContextEngine // 切 default/coordinator 模型时联动 SetContextWindow + SetReserveTokens
	askUser           *tools.AskUserTool
	writerRestore     *ctxpack.WriterRestorePack
	observer          *observer
	router            *flow.Dispatcher
	routerDetach      func()
	usage             *UsageTracker
	usageCancel       context.CancelFunc // 停掉 autoSaveLoop 并触发最后一次 flush

	events   chan Event
	streamCh chan string
	done     chan struct{}

	mu        sync.Mutex
	lifecycle lifecycle
	closeOnce sync.Once
}

type lifecycle string

const (
	lifecycleIdle      lifecycle = "idle"
	lifecycleRunning   lifecycle = "running"
	lifecyclePaused    lifecycle = "paused"
	lifecycleCompleted lifecycle = "completed"
)

// New 创建 Host。
func New(cfg bootstrap.Config, bundle assets.Bundle) (*Host, error) {
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return nil, err
	}
	slog.Info("启动", "module", "boot", "provider", cfg.Provider, "model", cfg.ModelName, "output", cfg.OutputDir)

	// 起后台 goroutine 从 OpenRouter 刷新模型元数据（窗口/价格），磁盘缓存 24h。
	modelreg.StartPricingRefresh(modelreg.DefaultRegistry(), bootstrap.DefaultConfigDir())

	store := storepkg.NewStore(cfg.OutputDir)
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}

	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return nil, fmt.Errorf("create models: %w", err)
	}
	slog.Info("模型就绪", "module", "boot", "summary", models.Summary())

	usage := NewUsageTracker(models, store)
	// 优先读 meta/usage.json；以下情况都走 sessions/*.jsonl 一次性回填：
	//   - 文件不存在（首次升级到带持久化的版本）
	//   - schema 版本不匹配（未来升级后丢弃旧格式）
	//   - 文件存在但损坏 / IO 错误（不能让坏数据让累计永久归零）
	// 回填完立即 SaveNow，把结果固化下来，下次启动直接 Load 命中。
	loaded, loadErr := usage.LoadFromStore()
	if loadErr != nil {
		slog.Warn("usage 加载失败，将尝试从 sessions 回填", "module", "usage", "err", loadErr)
	}
	if !loaded {
		if n, err := usage.ReplaySessions(cfg.OutputDir); err != nil {
			slog.Warn("usage replay 失败", "module", "usage", "err", err)
		} else if n > 0 {
			slog.Info("usage 从 session 回填完成", "module", "usage", "messages", n)
			if err := usage.SaveNow(); err != nil {
				slog.Warn("usage 回填后保存失败", "module", "usage", "err", err)
			}
		}
	}
	usageCtx, usageCancel := context.WithCancel(context.Background())
	usage.StartAutoSave(usageCtx)

	coordinator, askUser, restore, coordinatorCtxMgr := agents.BuildCoordinator(cfg, store, models, bundle, usage.Record)
	store.Signals.ClearStaleSignals()

	h := &Host{
		cfg:               cfg,
		bundle:            bundle,
		store:             store,
		models:            models,
		coordinator:       coordinator,
		coordinatorCtxMgr: coordinatorCtxMgr,
		askUser:           askUser,
		writerRestore:     restore,
		usage:             usage,
		usageCancel:       usageCancel,
		events:            make(chan Event, 100),
		streamCh:          make(chan string, 256),
		done:              make(chan struct{}, 4),
		lifecycle:         lifecycleIdle,
	}
	h.observer = newObserver(coordinator, store, h.emitEvent, h.emitDelta, h.emitClear)
	h.router = flow.NewDispatcher(coordinator, store)
	// 竞稿模式：向 Dispatcher 注入 persona slug 列表，与 build.go 注册的 agent 命名保持一致。
	// Slugs 与 EnsurePersonas 内部共用同一个 slugFor，保证 writer_<slug> 名字匹配。
	if wc := cfg.WritingContest.Normalize(); wc.Enabled() {
		h.router.SetContest(flow.ContestConfig{
			Personas:    persona.Slugs(wc.Personas), // persona slug 列表
			Concurrency: wc.Concurrency,             // 并发开关透传
		})
	}
	h.routerDetach = h.router.Attach()

	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		slog.Error("初始化运行元信息失败", "module", "boot", "err", err)
	}

	return h, nil
}

// ── 生命周期 ──

// Start 新建模式：初始化进度并启动 coordinator 长循环。
func (h *Host) Start(prompt string) error {
	return h.StartPrepared(BuildStartPrompt(prompt))
}

// StartPrepared 使用已编排完成的启动 prompt 开始创作。
func (h *Host) StartPrepared(promptText string) error {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return fmt.Errorf("already running")
	}
	h.mu.Unlock()

	promptText = strings.TrimSpace(promptText)
	if promptText == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := h.store.Checkpoints.Reset(); err != nil {
		return fmt.Errorf("reset checkpoints: %w", err)
	}
	if err := h.store.Progress.Init("", 0); err != nil {
		return fmt.Errorf("init progress: %w", err)
	}

	slog.Info("开始创作", "module", "host", "prompt_len", len(promptText))
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "开始创作", Level: "info"})
	h.observer.setAborting(false)
	// 先重置去重并启用路由，再启动 Prompt，避免首轮事件先于 Enable 抵达
	h.router.ResetDedupe()
	h.router.Enable()
	if err := h.coordinator.Prompt(promptText); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	// 主动派发一次首条指令：若已进入写作阶段（Phase=Writing），Host 立即下达；
	// 规划阶段 Route 返回 nil，无副作用。
	h.router.Dispatch()

	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	go h.waitDone()
	return nil
}

// Resume 恢复模式：从 checkpoint + progress 生成 resume prompt 并启动。
func (h *Host) Resume() (string, error) {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return "", fmt.Errorf("already running")
	}
	h.mu.Unlock()

	prompt, label, err := buildResumePrompt(h.store)
	if err != nil {
		return "", err
	}
	if label == "" {
		return "", nil // 新建模式，无恢复
	}

	slog.Info("恢复创作", "module", "host", "label", label)
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "恢复创作: " + label, Level: "info"})
	for _, w := range h.store.CheckConsistency() {
		slog.Warn("一致性告警", "module", "host", "detail", w)
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "一致性告警: " + w, Level: "warn"})
	}
	h.refreshWriterRestore()
	h.observer.setAborting(false)
	h.router.ResetDedupe()
	h.router.Enable()
	if err := h.coordinator.Prompt(prompt); err != nil {
		return "", fmt.Errorf("resume prompt: %w", err)
	}
	// 主动派发一次首条指令，避免 Coordinator 对恢复 prompt 只回文字而 StopGuard 反复拦截。
	h.router.Dispatch()

	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	go h.waitDone()
	return label, nil
}

// Continue 用指定 prompt 继续。停机后用户在输入框输入时调用。
func (h *Host) Continue(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()

	h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[继续] " + text, Level: "info"})

	if running {
		h.coordinator.FollowUp(agentcore.UserMsg(text))
		return nil
	}
	// 停机后 → 注入并自动恢复
	h.refreshWriterRestore()
	h.observer.setAborting(false)
	_, err := h.coordinator.Inject(agentcore.UserMsg(text))
	if err != nil {
		return fmt.Errorf("inject: %w", err)
	}
	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	go h.waitDone()
	return nil
}

// Steer 提交用户干预。
func (h *Host) Steer(text string) {
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()

	h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[用户干预] " + text, Level: "info"})

	msg := agentcore.UserMsg("[用户干预] " + text)
	if running {
		if _, err := h.coordinator.Inject(msg); err != nil {
			slog.Error("steer inject 失败", "module", "host", "err", err)
		}
		return
	}
	// 停机：持久化待下次启动 + 反馈系统状态（"已保存"是 USER 事件之外的系统提示）
	_ = h.store.RunMeta.SetPendingSteer(text)
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "干预已保存，下次启动时生效", Level: "info"})
}

// Abort 暂停当前 coordinator。
func (h *Host) Abort() bool {
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	if running {
		h.lifecycle = lifecyclePaused
	}
	h.mu.Unlock()
	if !running {
		return false
	}
	// 置位必须在 coordinator.Abort 之前：cancel 传播会立刻引发 stream init / subagent
	// 失败事件，observer 凭此标志识别为 abort 衍生噪声并抑制。
	h.observer.setAborting(true)
	h.coordinator.Abort()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "用户手动暂停当前创作", Level: "warn"})
	return true
}

// Close 终止 coordinator 并关闭事件通道。
//
// Usage 持久化语义：先取消 autoSaveLoop（它自行 flush 最后一次 dirty 状态），
// 再补一次同步 SaveNow 收尾。已知缺口：AbortSilent 之后若仍有 in-flight LLM
// 调用回来，触发的 OnMessage → Record 会更新内存但**不会被持久化**。这部分
// "最末几百 token" 的丢失在下次启动时会由 session jsonl replay 自动补回。
func (h *Host) Close() {
	h.observer.setAborting(true)
	h.coordinator.AbortSilent()
	if h.routerDetach != nil {
		h.routerDetach()
		h.routerDetach = nil
	}
	if h.usageCancel != nil {
		h.usageCancel()
		h.usageCancel = nil
	}
	if err := h.usage.SaveNow(); err != nil {
		slog.Warn("usage 退出前落盘失败", "module", "usage", "err", err)
	}
	h.closeOnce.Do(func() {
		close(h.done)
		close(h.events)
		close(h.streamCh)
	})
}

// waitDone 等待 coordinator 停机并发布终态事件。
//
// 不做任何续跑。Run 结束 = Host 进入终态：
//   - Phase=Complete  → 标记 completed，发"创作完成"事件
//   - 其它            → 标记 idle，发"Coordinator 停止"事件
//
// 用户要继续创作只有两条路径：手动 Continue（停机注入）或重启进程走 Resume。
// 见 docs/architecture.md §13.3、§8.3。
func (h *Host) waitDone() {
	h.coordinator.WaitForIdle()
	h.observer.finalize()

	h.mu.Lock()
	progress, _ := h.store.Progress.Load()
	if progress != nil && progress.Phase == domain.PhaseComplete {
		h.lifecycle = lifecycleCompleted
		summary := fmt.Sprintf("创作完成: %d 章 %d 字", len(progress.CompletedChapters), progress.TotalWordCount)
		h.mu.Unlock()
		slog.Info(summary, "module", "host")
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "success"})
	} else {
		wasRunning := h.lifecycle == lifecycleRunning
		if wasRunning {
			h.lifecycle = lifecycleIdle
		}
		completed := 0
		if progress != nil {
			completed = len(progress.CompletedChapters)
		}
		h.mu.Unlock()
		if wasRunning {
			summary := fmt.Sprintf("Coordinator 停止 (已完成 %d 章)", completed)
			slog.Warn(summary, "module", "host")
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "warn"})
		}
	}

	select {
	case h.done <- struct{}{}:
	default:
	}
}

// ── 通道 ──

// StreamClearSentinel 通过 streamCh 单条发送以示意"清空当前流式 round"。
// 不再用独立 clearCh —— 双通道无序导致 ✻ header 时常落到上一个 round 末尾。
const StreamClearSentinel = "\x00\x00CLEAR\x00\x00"

func (h *Host) Events() <-chan Event        { return h.events }
func (h *Host) Stream() <-chan string       { return h.streamCh }
func (h *Host) Done() <-chan struct{}       { return h.done }
func (h *Host) Dir() string                 { return h.store.Dir() }
func (h *Host) AskUser() *tools.AskUserTool { return h.askUser }

// ── 事件发射 ──

func (h *Host) emitEvent(ev Event) {
	defer func() { recover() }()
	// 所有事件的唯一 slog 入口。observer 翻译的 agentcore 事件和 Host 自发的
	// SYSTEM 事件（Start/Abort/Resume…）都在这里落日志，避免 ESC abort 与外部
	// 终止在 tui.log 上无法区分。
	if ev.Summary != "" || ev.Detail != "" {
		level := slog.LevelInfo
		switch ev.Level {
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
		// 日志记完整 Detail（排查用，不截断）；Detail 为空才回退到 Summary。
		msg := ev.Detail
		if msg == "" {
			msg = ev.Summary
		}
		attrs := []any{"module", "event", "category", ev.Category, "agent", ev.Agent}
		if ev.Kind != "" {
			attrs = append(attrs, "kind", ev.Kind)
		}
		slog.Log(context.Background(), level, msg, attrs...)
	}
	select {
	case h.events <- ev:
	default:
		select {
		case <-h.events:
		default:
		}
		select {
		case h.events <- ev:
		default:
		}
	}
}

func (h *Host) emitDelta(delta string) {
	defer func() { recover() }()
	select {
	case h.streamCh <- delta:
	default:
		select {
		case <-h.streamCh:
		default:
		}
		select {
		case h.streamCh <- delta:
		default:
		}
	}
}

func (h *Host) emitClear() {
	// 通过 streamCh 走"sentinel"，保证与 emitDelta 在同一条通道里有序送达 TUI。
	h.emitDelta(StreamClearSentinel)
}

// ── Snapshot (TUI 状态聚合) ──

func (h *Host) Snapshot() UISnapshot {
	h.mu.Lock()
	state := h.lifecycle
	provider, model, _ := h.models.CurrentSelection("default")
	h.mu.Unlock()

	// 动态解析当前模型的上下文窗口，/model 切换后下一次 Snapshot 自动反映
	modelWindow, _ := h.cfg.ResolveContextWindow(model)
	cost, tokIn, tokOut, cacheRead, cacheWrite := h.usage.Totals()
	saved := h.usage.SavedUSD()
	overallCapable := h.usage.OverallCacheCapable()
	recentRead, recentInput, recentSamples := h.usage.OverallRecent()
	perAgent := h.usage.PerAgent()
	cacheStats := make([]AgentCacheStat, 0, len(perAgent))
	for _, a := range perAgent {
		cacheStats = append(cacheStats, AgentCacheStat{
			Role:            a.Role,
			Input:           a.Input,
			Output:          a.Output,
			CacheRead:       a.CacheRead,
			CacheWrite:      a.CacheWrite,
			Cost:            a.Cost,
			Saved:           a.Saved,
			CacheCapable:    a.CacheCapable,
			RecentCacheRead: a.RecentCacheRead,
			RecentInput:     a.RecentInput,
			RecentSamples:   a.RecentSamples,
		})
	}
	perModel := h.usage.PerModel()
	modelStats := make([]AgentCacheStat, 0, len(perModel))
	for _, a := range perModel {
		modelStats = append(modelStats, AgentCacheStat{
			Model:        a.Model,
			Input:        a.Input,
			Output:       a.Output,
			CacheRead:    a.CacheRead,
			CacheWrite:   a.CacheWrite,
			Cost:         a.Cost,
			Saved:        a.Saved,
			CacheCapable: a.CacheCapable,
		})
	}

	snap := UISnapshot{
		Provider:               provider,
		ModelName:              model,
		ModelContextWindow:     modelWindow,
		Style:                  h.cfg.Style,
		RuntimeState:           string(state),
		IsRunning:              state == lifecycleRunning,
		TotalInputTokens:       tokIn,
		TotalOutputTokens:      tokOut,
		TotalCacheReadTokens:   cacheRead,
		TotalCacheWriteTokens:  cacheWrite,
		TotalCostUSD:           cost,
		TotalSavedUSD:          saved,
		OverallCacheCapable:    overallCapable,
		OverallRecentCacheRead: recentRead,
		OverallRecentInput:     recentInput,
		OverallRecentSamples:   recentSamples,
		CachePerAgent:          cacheStats,
		CachePerModel:          modelStats,
		MissingAssistantUsage:  h.usage.MissingAssistantUsage(),
	}

	progress, _ := h.store.Progress.Load()
	if progress != nil {
		snap.NovelName = strings.TrimSpace(progress.NovelName)
		snap.Phase = string(progress.Phase)
		snap.Flow = string(progress.Flow)
		snap.CurrentChapter = progress.CurrentChapter
		snap.TotalChapters = progress.TotalChapters
		snap.CompletedCount = len(progress.CompletedChapters)
		snap.TotalWordCount = progress.TotalWordCount
		snap.InProgressChapter = progress.InProgressChapter
		snap.PendingRewrites = progress.PendingRewrites
		snap.RewriteReason = progress.RewriteReason
		snap.Layered = progress.Layered
		if progress.CurrentVolume > 0 {
			snap.CurrentVolumeArc = fmt.Sprintf("第%d卷·第%d弧", progress.CurrentVolume, progress.CurrentArc)
		}
	}
	if snap.NovelName == "" {
		if premise, _ := h.store.Outline.LoadPremise(); premise != "" {
			snap.NovelName = domain.ExtractNovelNameFromPremise(premise)
		}
	}
	if meta, _ := h.store.RunMeta.Load(); meta != nil {
		snap.PendingSteer = meta.PendingSteer
	}

	snap.Agents = h.observer.agentSnapshots()
	h.fillContextStatus(&snap)
	snap.StatusLabel = deriveStatusLabel(snap)

	// 恢复标签
	if _, label, err := buildResumePrompt(h.store); err == nil && label != "" {
		snap.RecoveryLabel = label
	}

	h.fillDetails(&snap, progress)

	return snap
}

// fillContextStatus 填充 Coordinator 上下文健康度信息。
func (h *Host) fillContextStatus(snap *UISnapshot) {
	if h.coordinator == nil {
		return
	}
	if usage := h.coordinator.BaselineContextUsage(); usage != nil {
		snap.ContextTokens = usage.Tokens
		snap.ContextWindow = usage.ContextWindow
		snap.ContextPercent = usage.Percent
	}
	if ctx := h.coordinator.ContextSnapshot(); ctx != nil {
		snap.ContextScope = ctx.Scope
		snap.ContextStrategy = ctx.LastStrategy
		snap.ContextActiveMessages = ctx.ActiveMessages
		snap.ContextSummaryCount = ctx.SummaryMessages
		snap.ContextCompactedCount = ctx.LastCompactedCount
		snap.ContextKeptCount = ctx.LastKeptCount
		if snap.ContextTokens == 0 {
			if ctx.BaselineUsage != nil {
				snap.ContextTokens = ctx.BaselineUsage.Tokens
				snap.ContextWindow = ctx.BaselineUsage.ContextWindow
				snap.ContextPercent = ctx.BaselineUsage.Percent
			} else if ctx.Usage != nil {
				snap.ContextTokens = ctx.Usage.Tokens
				snap.ContextWindow = ctx.Usage.ContextWindow
				snap.ContextPercent = ctx.Usage.Percent
			}
		}
	}
	for _, agent := range snap.Agents {
		if agent.Name != "coordinator" {
			continue
		}
		p := agent.RecentProjection
		if p.ContextWindow > 0 && p.Tokens > 0 {
			snap.ProjectionTokens = p.Tokens
			snap.ProjectionWindow = p.ContextWindow
			snap.ProjectionPercent = p.Percent
			snap.ProjectionStrategy = p.Strategy
			snap.ProjectionCompacted = p.CompactedCount
			snap.ProjectionKept = p.KeptCount
		}
		break
	}
}

// fillDetails 填充详情区:设定、角色、最近 commit/review/摘要。
func (h *Host) fillDetails(snap *UISnapshot, progress *domain.Progress) {
	if premise, _ := h.store.Outline.LoadPremise(); premise != "" {
		snap.Premise = truncate(premise, 80)
	}
	if outline, _ := h.store.Outline.LoadOutline(); len(outline) > 0 {
		for _, e := range outline {
			snap.Outline = append(snap.Outline, OutlineSnapshot{
				Chapter: e.Chapter, Title: e.Title, CoreEvent: e.CoreEvent,
			})
		}
	}
	if progress != nil && progress.Layered {
		if compass, _ := h.store.Outline.LoadCompass(); compass != nil {
			snap.CompassDirection = compass.EndingDirection
			snap.CompassScale = compass.EstimatedScale
		}
		if volumes, _ := h.store.Outline.LoadLayeredOutline(); len(volumes) > 0 {
			for _, v := range volumes {
				if v.Index > progress.CurrentVolume {
					snap.NextVolumeTitle = v.Title
					break
				}
			}
		}
	}
	if chars, _ := h.store.Characters.Load(); len(chars) > 0 {
		for _, c := range chars {
			label := c.Name
			if c.Role != "" {
				label += "（" + c.Role + "）"
			}
			snap.Characters = append(snap.Characters, label)
		}
	}
	if ledger, _ := h.store.Cast.Load(); len(ledger) > 0 {
		snap.SupportingCount = len(ledger)
		recent, _ := h.store.Cast.RecentActive(5)
		for _, e := range recent {
			label := e.Name
			if e.BriefRole != "" {
				label += "（" + e.BriefRole + "）"
			}
			snap.RecentSupporting = append(snap.RecentSupporting, label)
		}
	}
	if progress != nil && len(progress.CompletedChapters) > 0 {
		lastCh := progress.CompletedChapters[len(progress.CompletedChapters)-1]
		wc := progress.ChapterWordCounts[lastCh]
		snap.LastCommitSummary = fmt.Sprintf("第%d章 %d字", lastCh, wc)
	}
	currentCh := 1
	if progress != nil && len(progress.CompletedChapters) > 0 {
		currentCh = progress.CompletedChapters[len(progress.CompletedChapters)-1]
	}
	if review, err := h.store.World.LoadLastReview(currentCh); err == nil && review != nil {
		snap.LastReviewSummary = fmt.Sprintf("verdict=%s %d个问题", review.Verdict, len(review.Issues))
		if len(review.AffectedChapters) > 0 {
			snap.LastReviewSummary += fmt.Sprintf(" 影响%v", review.AffectedChapters)
		}
	}
	if cp := h.store.Checkpoints.LatestGlobal(); cp != nil {
		snap.LastCheckpointName = fmt.Sprintf("%s.%s", cp.Scope, cp.Step)
	}
	if progress != nil {
		for i := len(progress.CompletedChapters) - 1; i >= 0 && len(snap.RecentSummaries) < 2; i-- {
			ch := progress.CompletedChapters[i]
			if summary, err := h.store.Summaries.LoadSummary(ch); err == nil && summary != nil {
				snap.RecentSummaries = append(snap.RecentSummaries,
					fmt.Sprintf("第%d章: %s", ch, truncate(summary.Summary, 50)))
			}
		}
	}
}

func deriveStatusLabel(s UISnapshot) string {
	switch {
	case s.Phase == string(domain.PhaseComplete):
		return "COMPLETE"
	case s.Flow == string(domain.FlowReviewing):
		return "REVIEW"
	case s.Flow == string(domain.FlowRewriting) || s.Flow == string(domain.FlowPolishing):
		return "REWRITE"
	case s.RuntimeState == "running":
		return "RUNNING"
	default:
		return "READY"
	}
}

// ── 模型管理 ──

func (h *Host) ConfiguredProviders() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	providers := make([]string, 0, len(h.cfg.Providers))
	for name := range h.cfg.Providers {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers
}

func (h *Host) ConfiguredModels(provider string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.CandidateModels(provider)
}

func (h *Host) CurrentModelSelection(role string) (string, string, bool) {
	return h.models.CurrentSelection(role)
}

func (h *Host) SwitchModel(role, provider, model string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	if err := h.models.Swap(role, provider, model); err != nil {
		return err
	}
	if role == "" || role == "default" {
		h.cfg.Provider = provider
		h.cfg.ModelName = model
	} else {
		if h.cfg.Roles == nil {
			h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := h.cfg.Roles[role]
		rc.Provider = provider
		rc.Model = model
		h.cfg.Roles[role] = rc
	}
	if path := bootstrap.DefaultConfigPath(); path != "" {
		if err := bootstrap.SaveConfig(path, h.cfg); err != nil {
			slog.Warn("保存配置失败", "module", "host", "err", err)
		}
	}
	// 切到未登记模型时打一行 warn，提示用户走了 128k 兜底——长篇容易被提前压缩。
	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	window, source := h.cfg.ResolveContextWindow(model)
	bootstrap.LogContextWindowChoice(logRole, model, window, source)

	// 切到 default/coordinator 时，联动 coordinator engine 的窗口与 reserve。
	// writer/architect/editor 走 ContextManagerFactory 自动按新模型重建，不需要联动。
	// 不联动会导致：1M→128k 切换时 coordinator engine 仍按 1M 算 threshold，
	// 累积 messages 超过 128k 就 API 报错；128k→1M 时阈值被钉在 96k，浪费长上下文。
	//
	// 关键：必须用 models.CurrentSelection("coordinator") 拿"coordinator 实际使用"的模型
	// 算窗口——而不是直接用切换目标的 model。当用户配了 roles.coordinator 单独模型时，
	// 切 default 不影响 coordinator 实际模型；用切换目标的窗口去 SetContextWindow 会错
	// 把 coordinator 阈值调到不相干的值（例：default 切 1M 模型时把 200k 的 coordinator
	// engine 阈值拉到 891k，写超 200k 直接爆 API）。
	if h.coordinatorCtxMgr != nil && (role == "" || role == "default" || role == "coordinator") {
		_, coordinatorModel, _ := h.models.CurrentSelection("coordinator")
		coordinatorWindow, coordSource := h.cfg.ResolveContextWindow(coordinatorModel)
		h.coordinator.SetContextWindow(coordinatorWindow)
		h.coordinatorCtxMgr.SetContextWindow(coordinatorWindow)
		h.coordinatorCtxMgr.SetReserveTokens(bootstrap.CompactReserveTokens(coordinatorWindow))
		// coordinator 实际模型与切换目标不同（用户切 default 但 coordinator 有专属 role）时，
		// 上面 LogContextWindowChoice 打的是 default 的窗口，与实际生效值不一致；补一行。
		if coordinatorModel != model {
			bootstrap.LogContextWindowChoice("coordinator", coordinatorModel, coordinatorWindow, coordSource)
		}
	}

	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("模型已切换：%s → %s/%s", role, provider, model),
		Level:    "info",
	})
	return nil
}

// ── 事件回放 ──

func (h *Host) ReplayQueue(afterSeq int64) ([]domain.RuntimeQueueItem, error) {
	if h.store == nil || h.store.Runtime == nil {
		return nil, nil
	}
	return h.store.Runtime.LoadQueueAfter(afterSeq)
}

// ── 共创 ──

func (h *Host) CoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, h.store.Sessions, history, onProgress)
}

// ── 工具 ──

func (h *Host) refreshWriterRestore() {
	if h.writerRestore != nil {
		h.writerRestore.Refresh(h.store)
	}
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// ImportFrom 启动一次外部小说反推导入：切分 → 反推 foundation → 逐章分析落盘。
// 与 Coordinator 互斥；导入完成后调用方可立即 Resume() 续写。
// 返回的事件通道由 imp.Run 关闭，调用方负责消费（满则丢弃以防阻塞分析协程）。
func (h *Host) ImportFrom(ctx context.Context, opts imp.Options) (<-chan imp.Event, error) {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return nil, fmt.Errorf("coordinator 运行中，请先暂停后再导入")
	}
	h.mu.Unlock()

	rulesOpts := rules.DefaultOptions(h.bundle.RulesFS)
	deps := imp.Deps{
		Store:      h.store,
		CommitTool: tools.NewCommitChapterTool(h.store).WithRules(rulesOpts),
		LLM:        h.models.ForRole("architect"),
		Prompts: imp.Prompts{
			Foundation: h.bundle.Prompts.ImportFoundation,
			Analyzer:   h.bundle.Prompts.ImportAnalyzer,
		},
	}
	return imp.Run(ctx, deps, opts)
}

// Export 导出已完成章节为外部文件（当前仅支持 TXT）。
//
// 与 ImportFrom 不同：导出是只读操作（不动 Progress / Checkpoint），
// 因此**不要求 Coordinator 空闲**——写作中途也可以随时导出"现阶段成品"。
// 只读到 Progress.CompletedChapters + 章节终稿 + 大纲 + premise 的一致快照。
func (h *Host) Export(ctx context.Context, opts exp.Options) (*exp.Result, error) {
	return exp.Run(ctx, exp.Deps{Store: h.store}, opts)
}
