package host

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/flow"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	modelreg "github.com/voocel/ainovel-cli/internal/models"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// Host 是运行时薄外壳。
// 职责：启动/恢复/干预注入/事件投影/模型管理。
// 不做任何调度决策，不做空闲续跑。
type Host struct {
	cfg           bootstrap.Config
	bundle        assets.Bundle
	store         *storepkg.Store
	models        *bootstrap.ModelSet
	coordinator   *agentcore.Agent
	askUser       *tools.AskUserTool
	writerRestore *ctxpack.WriterRestorePack
	observer      *observer
	router        *flow.Dispatcher
	routerDetach  func()
	usage         *UsageTracker

	events   chan Event
	streamCh chan string
	clearCh  chan struct{}
	done     chan struct{}

	mu              sync.Mutex
	lifecycle       lifecycle
	idleResumeCount int // 连续异常停机自动恢复次数
	closeOnce       sync.Once
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

	usage := NewUsageTracker(models)
	coordinator, askUser, restore := agents.BuildCoordinator(cfg, store, models, bundle, usage.Record)
	store.Signals.ClearStaleSignals()

	h := &Host{
		cfg:           cfg,
		bundle:        bundle,
		store:         store,
		models:        models,
		coordinator:   coordinator,
		askUser:       askUser,
		writerRestore: restore,
		usage:         usage,
		events:        make(chan Event, 100),
		streamCh:      make(chan string, 256),
		clearCh:       make(chan struct{}, 4),
		done:          make(chan struct{}, 4),
		lifecycle:     lifecycleIdle,
	}
	h.observer = newObserver(coordinator, store, h.emitEvent, h.emitDelta, h.emitClear)
	h.router = flow.NewDispatcher(coordinator, store)
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
	h.idleResumeCount = 0
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
	h.router.ResetDedupe()
	h.router.Enable()
	if err := h.coordinator.Prompt(prompt); err != nil {
		return "", fmt.Errorf("resume prompt: %w", err)
	}
	// 主动派发一次首条指令，避免 Coordinator 对恢复 prompt 只回文字而 StopGuard 反复拦截。
	h.router.Dispatch()

	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.idleResumeCount = 0
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

	if running {
		h.coordinator.FollowUp(agentcore.UserMsg(text))
		return nil
	}
	// 停机后 → 注入并自动恢复
	h.refreshWriterRestore()
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

	msg := agentcore.UserMsg("[用户干预] " + text)
	if running {
		if _, err := h.coordinator.Inject(msg); err != nil {
			slog.Error("steer inject 失败", "module", "host", "err", err)
		}
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "干预已提交: " + truncate(text, 40), Level: "info"})
		return
	}
	// 停机：持久化待下次启动
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
	h.coordinator.Abort()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "用户手动暂停当前创作", Level: "warn"})
	return true
}

// Close 终止 coordinator 并关闭事件通道。
func (h *Host) Close() {
	h.coordinator.AbortSilent()
	if h.routerDetach != nil {
		h.routerDetach()
		h.routerDetach = nil
	}
	h.closeOnce.Do(func() {
		close(h.done)
		close(h.events)
		close(h.streamCh)
		close(h.clearCh)
	})
}

const maxIdleResumes = 3

// waitDone — 核心：等待 coordinator 停机，处理结束状态。
// 当 coordinator 在创作未完成时异常停机（如 LLM 返回空响应），自动注入恢复指令重新启动。
func (h *Host) waitDone() {
	h.coordinator.WaitForIdle()
	h.observer.finalize()

	h.mu.Lock()
	progress, _ := h.store.Progress.Load()

	if progress != nil && progress.Phase == domain.PhaseComplete {
		h.lifecycle = lifecycleCompleted
		h.idleResumeCount = 0
		summary := fmt.Sprintf("创作完成: %d 章 %d 字", len(progress.CompletedChapters), progress.TotalWordCount)
		slog.Info(summary, "module", "host")
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "success"})
		h.mu.Unlock()
		select {
		case h.done <- struct{}{}:
		default:
		}
		return
	}

	if h.lifecycle == lifecycleRunning && progress != nil &&
		progress.Phase == domain.PhaseWriting && h.idleResumeCount < maxIdleResumes {
		h.idleResumeCount++
		attempt := h.idleResumeCount
		h.mu.Unlock()

		slog.Warn("Coordinator 异常停机，自动恢复",
			"module", "host", "attempt", attempt,
			"completed", len(progress.CompletedChapters))
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
			Summary: fmt.Sprintf("Coordinator 异常停机，自动恢复 (%d/%d)", attempt, maxIdleResumes),
			Level:   "warn"})

		if _, err := h.coordinator.Inject(agentcore.UserMsg("继续创作")); err != nil {
			slog.Error("自动恢复失败", "module", "host", "err", err)
			h.mu.Lock()
			h.lifecycle = lifecycleIdle
			h.mu.Unlock()
			select {
			case h.done <- struct{}{}:
			default:
			}
			return
		}
		go h.waitDone()
		return
	}

	if h.lifecycle == lifecycleRunning {
		h.lifecycle = lifecycleIdle
		completed := 0
		if progress != nil {
			completed = len(progress.CompletedChapters)
		}
		summary := fmt.Sprintf("Coordinator 停止 (已完成 %d 章)", completed)
		slog.Warn(summary, "module", "host")
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "warn"})
	}
	h.idleResumeCount = 0
	h.mu.Unlock()

	select {
	case h.done <- struct{}{}:
	default:
	}
}

// ── 通道 ──

func (h *Host) Events() <-chan Event         { return h.events }
func (h *Host) Stream() <-chan string        { return h.streamCh }
func (h *Host) StreamClear() <-chan struct{} { return h.clearCh }
func (h *Host) Done() <-chan struct{}        { return h.done }
func (h *Host) Dir() string                  { return h.store.Dir() }
func (h *Host) AskUser() *tools.AskUserTool  { return h.askUser }

// ── 事件发射 ──

func (h *Host) emitEvent(ev Event) {
	defer func() { recover() }()
	// 所有事件的唯一 slog 入口。observer 翻译的 agentcore 事件和 Host 自发的
	// SYSTEM 事件（Start/Abort/Resume…）都在这里落日志，避免 ESC abort 与外部
	// 终止在 tui.log 上无法区分。
	if ev.Summary != "" {
		level := slog.LevelInfo
		switch ev.Level {
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
		slog.Log(context.Background(), level, ev.Summary,
			"module", "event", "category", ev.Category, "agent", ev.Agent)
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
	defer func() { recover() }()
	select {
	case h.clearCh <- struct{}{}:
	default:
	}
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

	snap := UISnapshot{
		Provider:              provider,
		ModelName:             model,
		ModelContextWindow:    modelWindow,
		Style:                 h.cfg.Style,
		RuntimeState:          string(state),
		IsRunning:             state == lifecycleRunning,
		TotalInputTokens:      tokIn,
		TotalOutputTokens:     tokOut,
		TotalCacheReadTokens:  cacheRead,
		TotalCacheWriteTokens: cacheWrite,
		TotalCostUSD:          cost,
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
			snap.NovelName = extractNovelName(premise)
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

func extractNovelName(premise string) string {
	for _, line := range strings.Split(premise, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
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

func (h *Host) CoCreateStream(ctx context.Context, history []CoCreateMessage, onReply func(string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, history, onReply)
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

	deps := imp.Deps{
		Store:      h.store,
		CommitTool: tools.NewCommitChapterTool(h.store),
		LLM:        h.models.ForRole("architect"),
		Prompts: imp.Prompts{
			Foundation: h.bundle.Prompts.ImportFoundation,
			Analyzer:   h.bundle.Prompts.ImportAnalyzer,
		},
	}
	return imp.Run(ctx, deps, opts)
}
