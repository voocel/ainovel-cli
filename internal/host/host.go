package host

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/flow"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	modelreg "github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/ainovel-cli/internal/notify"
	"github.com/voocel/ainovel-cli/internal/rules"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// Host 是运行时薄外壳。
// 职责：启动/Phục hồi/干预注入/事件投影/Mô hình管理。
// 不做任何调度决策，不做Rỗng闲续跑。
type Host struct {
	cfg               bootstrap.Config
	bundle            assets.Bundle
	store             *storepkg.Store
	models            *bootstrap.ModelSet
	coordinator       *agentcore.Agent
	coordinatorCtxMgr *corecontext.ContextEngine // 切 default/coordinator Mô hình时联动 SetContextWindow + SetReserveTokens
	thinkingApplier   agents.ApplyThinking       // /model 调思考强度时联动 live agent（coordinator + 子Proxy）
	askUser           *tools.AskUserTool
	writerRestore     *ctxpack.WriterRestorePack
	observer          *observer
	router            *flow.Dispatcher
	usage             *UsageTracker
	usageCancel       context.CancelFunc // 停掉 autoSaveLoop 并触发最后一次 flush
	budget            *BudgetSentinel    // 预算政策；未Bật为 nil（方法 nil 安全）
	budgetDetach      func()
	notifier          *notify.Notifier // Không có人值守告警；未Bật为 nil（Send nil 安全）

	events   chan Event
	streamCh chan string
	done     chan struct{}

	mu         sync.Mutex
	lifecycle  lifecycle
	cocreating bool // 阶段共创占用：paused Cửa sổ内堵住 import/simulate/continue 的并发介入
	closeOnce  sync.Once
}

type lifecycle string

const (
	lifecycleIdle      lifecycle = "idle"
	lifecycleRunning   lifecycle = "running"
	lifecyclePaused    lifecycle = "paused"
	lifecycleCompleted lifecycle = "completed"
)

// New Tạo Host。
func New(cfg bootstrap.Config, bundle assets.Bundle) (*Host, error) {
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return nil, err
	}
	slog.Info("启动", "module", "boot", "provider", cfg.Provider, "model", cfg.ModelName, "output", cfg.OutputDir)

	// 起后台 goroutine 从 OpenRouter Làm mớiMô hình元数据（Cửa sổ/价格），磁盘缓存 24h。
	modelreg.StartPricingRefresh(modelreg.DefaultRegistry(), bootstrap.DefaultConfigDir())

	store := storepkg.NewStore(cfg.OutputDir)
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}

	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return nil, fmt.Errorf("create models: %w", err)
	}
	slog.Info("Mô hình就绪", "module", "boot", "summary", models.Summary())

	usage := NewUsageTracker(models, store)
	// 优先读 meta/usage.json；以下情况都走 sessions/*.jsonl 一次性回填：
	//   - Tập tin不存在（首次升级到带持久化的Phiên bản）
	//   - schema Phiên bản不匹配（未来升级后丢弃Cũ格式）
	//   - Tập tin存在但损坏 / IO Lỗi（不能让坏数据让累计永久归零）
	// 回填完立即 SaveNow，把Kết quả固化下来，下次启动直接 Load 命中。
	loaded, loadErr := usage.LoadFromStore()
	if loadErr != nil {
		slog.Warn("usage 加载Thất bại，将尝试从 sessions 回填", "module", "usage", "err", loadErr)
	}
	if !loaded {
		if n, err := usage.ReplaySessions(cfg.OutputDir); err != nil {
			slog.Warn("usage replay Thất bại", "module", "usage", "err", err)
		} else if n > 0 {
			slog.Info("usage 从 session 回填Hoàn thành", "module", "usage", "messages", n)
			if err := usage.SaveNow(); err != nil {
				slog.Warn("usage 回填后LưuThất bại", "module", "usage", "err", err)
			}
		}
	}
	usageCtx, usageCancel := context.WithCancel(context.Background())
	usage.StartAutoSave(usageCtx)

	var router *flow.Dispatcher
	var budget *BudgetSentinel
	coordinator, askUser, restore, coordinatorCtxMgr, applyThinking := agents.BuildCoordinator(cfg, store, models, bundle, usage.Record, func(string) {
		if budget != nil && budget.HandleBoundary() {
			return
		}
		if router != nil {
			router.Dispatch()
		}
	})
	store.Signals.ClearStaleSignals()

	h := &Host{
		cfg:               cfg,
		bundle:            bundle,
		store:             store,
		models:            models,
		coordinator:       coordinator,
		coordinatorCtxMgr: coordinatorCtxMgr,
		thinkingApplier:   applyThinking,
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
	if cfg.Notify.IsEnabled() {
		h.notifier = notify.New(cfg.Notify.Command, cfg.Notify.Events)
	}
	// 预算哨兵订阅子Proxy边界事件执行停机；Dispatcher 由工具执行链同步触发，
	// 不再通过事件订阅抢占下一轮Mô hình调用。
	if sentinel := NewBudgetSentinel(cfg.Budget,
		func() float64 { c, _, _, _, _ := usage.Totals(); return c },
		func(reason string) { h.abortWithEvent(reason, "error") },
		func(level, summary string) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
			h.notifier.Send(notify.Notification{Kind: "budget", Level: level, Title: "ainovel: 预算", Body: summary})
		},
	); sentinel != nil {
		h.budget = sentinel
		budget = sentinel
		usage.SetOnCost(sentinel.OnCost)
		h.budgetDetach = coordinator.Subscribe(sentinel.HandleEvent)
		// 计费盲区告警：Mô hình不报 usage 时成本恒 0，预算永不触发——保险丝没接上必须喊人。
		usage.SetOnMissingUsage(func() {
			const blind = "预算盲区: Mô hình未Quay lại usage 数据，成本统计为 0，预算上限不会触发（Tuỳ chỉnhMô hìnhVui lòngXác nhận注册表价格或上游 include_usage）"
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: blind, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: "budget", Level: "warn", Title: "ainovel: 预算", Body: blind})
		})
	}
	h.router = flow.NewDispatcher(coordinator, store)
	router = h.router
	// 重复指令告警：纯 telemetry，挂机时"Mô hình可能在原地打转"值得喊人看一眼。
	// 事件流与 notify 成对发出——notify 只是屏内事件的离屏副本（架构 §2.3）。
	h.router.SetOnRepeat(func(agent, task string, n int) {
		body := fmt.Sprintf("同一指令已第 %d 次下达（%s）：%s", n, agent, task)
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "指令重复: " + body, Level: "warn"})
		h.notifier.Send(notify.Notification{Kind: "repeat", Level: "warn", Title: "ainovel: 指令重复", Body: body})
	})

	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		slog.Error("初始化运行元信息Thất bại", "module", "boot", "err", err)
	}

	return h, nil
}

// ── 生命周期 ──

// Start Mới建模式：初始化Tiến độ并启动 coordinator 长循环。
func (h *Host) Start(prompt string) error {
	return h.StartPrepared(BuildStartPrompt(prompt))
}

// StartPrepared 使用已编排Hoàn thành的启动 prompt Bắt đầu创作。
func (h *Host) StartPrepared(promptText string) error {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("阶段共创Đang thực hiện，Vui lòng先Kết thúc共创")
	}
	h.mu.Unlock()

	promptText = strings.TrimSpace(promptText)
	if promptText == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	if err := h.store.Checkpoints.Reset(); err != nil {
		return fmt.Errorf("reset checkpoints: %w", err)
	}
	if err := h.store.Progress.Init("", 0); err != nil {
		return fmt.Errorf("init progress: %w", err)
	}

	slog.Info("Bắt đầu创作", "module", "host", "prompt_len", len(promptText))
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Bắt đầu创作", Level: "info"})
	h.observer.setAborting(false)
	// 先Đặt lại重复追踪并Bật路由，再启动 Prompt，避免首轮事件先于 Enable 抵达
	h.router.ResetRepeat()
	h.router.Enable()
	if err := h.coordinator.Prompt(context.Background(), promptText); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	// 主动派发一次首条指令：若已进入Viết阶段（Phase=Writing），Host 立即下达；
	// 规划阶段 Route Quay lại nil，Không có副作用。
	h.router.Dispatch()

	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	go h.waitDone()
	return nil
}

// Resume Phục hồi模式：从 checkpoint + progress 生成 resume prompt 并启动。
func (h *Host) Resume() (string, error) {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return "", fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return "", fmt.Errorf("阶段共创Đang thực hiện，Vui lòng先Kết thúc共创")
	}
	h.mu.Unlock()

	prompt, label, err := buildResumePrompt(h.store)
	if err != nil {
		return "", err
	}
	if label == "" {
		return "", nil // Mới建模式，Không cóPhục hồi
	}
	if err := h.budget.Refuse(); err != nil {
		return "", err
	}

	slog.Info("Phục hồi创作", "module", "host", "label", label)
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Phục hồi创作: " + label, Level: "info"})
	for _, w := range h.store.CheckConsistency() {
		slog.Warn("Nhất quán告警", "module", "host", "detail", w)
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Nhất quán告警: " + w, Level: "warn"})
	}
	h.refreshWriterRestore()
	h.observer.setAborting(false)
	h.router.ResetRepeat()
	h.router.Enable()
	if err := h.coordinator.Prompt(context.Background(), prompt); err != nil {
		return "", fmt.Errorf("resume prompt: %w", err)
	}
	// 主动派发一次首条指令，避免 Coordinator 对Phục hồi prompt 只回文字而 StopGuard 反复拦截。
	h.router.Dispatch()

	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	go h.waitDone()
	return label, nil
}

// interventionMsg 把用户文本包装成 Coordinator 可识别的干预消息。
// Steer 与 Continue 共用同一 framing：两条入口的用户指令都带 `[用户干预]` 前缀，
// 才能稳定触发 coordinator.md 的干预分类。否则 Continue 的裸文本会绕过路由规则，
// Coordinator 失去分类锚点而误派子Proxy（曾导致"改已写Chương"被派给 writer 撞 edit_chapter 守卫）。
func interventionMsg(text string) agentcore.Message {
	return agentcore.UserMsg("[用户干预] " + text)
}

// Continue 用指定 prompt Tiếp tục。停机后用户在Nhập框Nhập时调用。
func (h *Host) Continue(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	h.mu.Lock()
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("阶段共创Đang thực hiện，Vui lòng先Kết thúc共创")
	}
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()

	h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[Tiếp tục] " + text, Level: "info"})

	if running {
		h.coordinator.FollowUp(interventionMsg(text))
		return nil
	}
	// 停机后 → 注入并自动Phục hồi（Phục hồi run 也受预算前置约束）
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	h.refreshWriterRestore()
	h.observer.setAborting(false)
	_, err := h.coordinator.Inject(interventionMsg(text))
	if err != nil {
		return fmt.Errorf("inject: %w", err)
	}
	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	go h.waitDone()
	return nil
}

// Steer Nộp用户干预。
func (h *Host) Steer(text string) {
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()

	h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[用户干预] " + text, Level: "info"})

	msg := interventionMsg(text)
	if running {
		if _, err := h.coordinator.Inject(msg); err != nil {
			slog.Error("steer inject Thất bại", "module", "host", "err", err)
		}
		return
	}
	// 停机：持久化待下次启动 + 反馈系统Trạng thái（"已Lưu"是 USER 事件之外的系统提示）
	_ = h.store.RunMeta.SetPendingSteer(text)
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "干预已Lưu，下次启动时生效", Level: "info"})
}

// Abort Tạm dừngHiện tại coordinator。
func (h *Host) Abort() bool {
	return h.abortWithEvent("用户手动Tạm dừngHiện tại创作", "warn")
}

// abortWithEvent 以指定原因事件执行Tạm dừng。预算停机与手动Tạm dừng共用同一停机机制，
// 仅事件文案不同（预算停机=用户预先签署的 Abort 指令，语义等同手动Tạm dừng）。
func (h *Host) abortWithEvent(summary, level string) bool {
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
	// Thất bại事件，observer 凭此标志识别为 abort 衍生噪声并抑制。
	h.observer.setAborting(true)
	h.coordinator.Abort()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
	return true
}

// Close 终止 coordinator 并关闭事件通道。
//
// Usage 持久化语义：先Hủy autoSaveLoop（它自行 flush 最后一次 dirty Trạng thái），
// 再补一次同步 SaveNow 收尾。已知缺口：AbortSilent 之后若仍有 in-flight LLM
// 调用回来，触发的 OnMessage → Record 会更Mới内存但**不会被持久化**。这Phần
// "最末几百 token" 的丢失在下次启动时会由 session jsonl replay 自动补回。
func (h *Host) Close() {
	h.observer.setAborting(true)
	h.coordinator.AbortSilent()
	if h.budgetDetach != nil {
		h.budgetDetach()
		h.budgetDetach = nil
	}
	if h.usageCancel != nil {
		h.usageCancel()
		h.usageCancel = nil
	}
	if err := h.usage.SaveNow(); err != nil {
		slog.Warn("usage Thoát前落盘Thất bại", "module", "usage", "err", err)
	}
	h.closeOnce.Do(func() {
		close(h.done)
		close(h.events)
		close(h.streamCh)
	})
}

// waitDone 等待 coordinator 停机并发布终态事件。
//
// 不做任何续跑。Run Kết thúc = Host 进入终态：
//   - Phase=Complete  → 标记 completed，发"创作Hoàn thành"事件
//   - 其它            → 标记 idle，发"Coordinator 停止"事件
//
// 用户要Tiếp tục创作只有两条Đường dẫn：手动 Continue（停机注入）或重启进程走 Resume。
// 见 docs/architecture.md §13.3、§8.3。
func (h *Host) waitDone() {
	h.coordinator.WaitForIdle()
	h.observer.finalize()

	h.mu.Lock()
	progress, _ := h.store.Progress.Load()
	if progress != nil && progress.Phase == domain.PhaseComplete {
		h.lifecycle = lifecycleCompleted
		summary := fmt.Sprintf("创作Hoàn thành: %d 章 %d 字", len(progress.CompletedChapters), progress.TotalWordCount)
		h.mu.Unlock()
		slog.Info(summary, "module", "host")
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "success"})
		h.notifier.Send(notify.Notification{
			Kind: "run_end", Level: "info", Title: "ainovel: 创作Hoàn thành",
			Body: h.runEndBody(progress.NovelName, summary),
		})
	} else {
		wasRunning := h.lifecycle == lifecycleRunning
		if wasRunning {
			h.lifecycle = lifecycleIdle
		}
		completed := 0
		name := ""
		if progress != nil {
			completed = len(progress.CompletedChapters)
			name = progress.NovelName
		}
		h.mu.Unlock()
		if wasRunning {
			summary := fmt.Sprintf("Coordinator 停止 (Đã hoàn thành %d 章)", completed)
			slog.Warn(summary, "module", "host")
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "warn"})
			h.notifier.Send(notify.Notification{
				Kind: "run_end", Level: "warn", Title: "ainovel: 创作停止",
				Body: h.runEndBody(name, summary),
			})
		}
	}

	select {
	case h.done <- struct{}{}:
	default:
	}
}

// runEndBody 组装 run_end 通知Chính văn：Tên sách + Tiến độTóm tắt + 累计花费。
func (h *Host) runEndBody(novelName, summary string) string {
	if name := strings.TrimSpace(novelName); name != "" {
		summary = "《" + name + "》" + summary
	}
	cost, _, _, _, _ := h.usage.Totals()
	if cost > 0 {
		summary += fmt.Sprintf(" · 花费 $%.2f", cost)
	}
	return summary
}

// ── 通道 ──

// StreamClearSentinel 通过 streamCh 单条发送以示意"清RỗngHiện tại流式 round"。
// 不再用独立 clearCh —— 双通道Không có序导致 ✻ header 时常落到上一个 round 末尾。
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
	// 终止在 tui.log 上Không thể区分。
	if ev.Summary != "" || ev.Detail != "" {
		level := slog.LevelInfo
		switch ev.Level {
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
		// 日志记完整 Detail（排查用，不截断）；Detail 为Rỗng才回退到 Summary。
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

// ── Snapshot (TUI Trạng thái聚合) ──

func (h *Host) Snapshot() UISnapshot {
	h.mu.Lock()
	state := h.lifecycle
	provider, model, _ := h.models.CurrentSelection("default")
	h.mu.Unlock()

	// 动态解析Hiện tạiMô hình的Cửa sổ ngữ cảnh，/model 切换后下一次 Snapshot 自动反映
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
		BudgetLimitUSD:         h.budget.Limit(),
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

	// Phục hồi标签
	if _, label, err := buildResumePrompt(h.store); err == nil && label != "" {
		snap.RecoveryLabel = label
	}

	h.fillDetails(&snap, progress)

	return snap
}

// fillContextStatus 填充 Coordinator Ngữ cảnh健康度信息。
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
}

// fillDetails 填充详情区:设定、角色、最近 commit/review/Tóm tắt。
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

// ── Mô hình管理 ──

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
	h.normalizeThinkingLocked(role)
	if path := bootstrap.DefaultConfigPath(); path != "" {
		if err := bootstrap.SaveConfig(path, h.cfg); err != nil {
			slog.Warn("Lưu cấu hìnhThất bại", "module", "host", "err", err)
		}
	}
	h.applyThinkingLocked(role)
	// 切到未登记Mô hình时打一行 warn，提示用户走了 128k 兜底——长篇容易被提前压缩。
	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	window, source := h.cfg.ResolveContextWindow(model)
	bootstrap.LogContextWindowChoice(logRole, model, window, source)

	// 切到 default/coordinator 时，联动 coordinator engine 的Cửa sổ与 reserve。
	// writer/architect/editor 走 ContextManagerFactory 自动按MớiMô hình重建，不Cần联动。
	// 不联动会导致：1M→128k 切换时 coordinator engine 仍按 1M 算 threshold，
	// 累积 messages 超过 128k 就 API 报错；128k→1M 时阈值被钉在 96k，浪费长Ngữ cảnh。
	//
	// 关键：必须用 models.CurrentSelection("coordinator") 拿"coordinator 实际使用"的Mô hình
	// 算Cửa sổ——而不是直接用切换目标的 model。当用户配了 roles.coordinator 单独Mô hình时，
	// 切 default 不影响 coordinator 实际Mô hình；用切换目标的Cửa sổ去 SetContextWindow 会错
	// 把 coordinator 阈值调到不相干的值（例：default 切 1M Mô hình时把 200k 的 coordinator
	// engine 阈值拉到 891k，写超 200k 直接爆 API）。
	if h.coordinatorCtxMgr != nil && (role == "" || role == "default" || role == "coordinator") {
		_, coordinatorModel, _ := h.models.CurrentSelection("coordinator")
		coordinatorWindow, coordSource := h.cfg.ResolveContextWindow(coordinatorModel)
		h.coordinator.SetContextWindow(coordinatorWindow)
		h.coordinatorCtxMgr.SetContextWindow(coordinatorWindow)
		h.coordinatorCtxMgr.SetReserveTokens(bootstrap.CompactReserveTokens(coordinatorWindow))
		// coordinator 实际Mô hình与切换目标不同（用户切 default 但 coordinator 有专属 role）时，
		// 上面 LogContextWindowChoice 打的是 default 的Cửa sổ，与实际生效值不一致；补一行。
		if coordinatorModel != model {
			bootstrap.LogContextWindowChoice("coordinator", coordinatorModel, coordinatorWindow, coordSource)
		}
	}

	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("Mô hình已切换：%s → %s/%s", role, provider, model),
		Level:    "info",
	})
	return nil
}

// concreteThinkingRoles 是可Áp dụng思考强度的具体角色（与 agents.ApplyThinking 路由一致）。
// 调 default 时按各角色 ResolveThinking 逐个重MớiÁp dụng。
var concreteThinkingRoles = []string{"coordinator", "architect", "writer", "editor"}

// CurrentThinking Quay lại某角色Hiện tại生效的思考强度原始串（供 /model 面板同步Hiện tại值）。
func (h *Host) CurrentThinking(role string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.ResolveThinking(strings.ToLower(strings.TrimSpace(role)))
}

func (h *Host) AvailableThinking(role string) []agentcore.ThinkingLevel {
	h.mu.Lock()
	model := h.models.ForRole(strings.ToLower(strings.TrimSpace(role)))
	h.mu.Unlock()
	return agents.AvailableThinkingForModel(model)
}

func (h *Host) normalizeThinkingLocked(role string) agentcore.ThinkingLevel {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		parsed, _ := agents.ParseThinkingLevel(h.cfg.Thinking)
		for _, r := range concreteThinkingRoles {
			resolved, ok := agents.ResolveThinkingForModel(h.models.ForRole(r), parsed)
			if !ok || resolved != parsed {
				h.cfg.Thinking = string(resolved)
				return resolved
			}
		}
		h.cfg.Thinking = string(parsed)
		return parsed
	}

	_, hasRoleThinking := h.cfg.Roles[role]
	hasRoleThinking = hasRoleThinking && h.cfg.Roles[role].Thinking != ""
	parsed, _ := agents.ParseThinkingLevel(h.cfg.ResolveThinking(role))
	resolved, _ := agents.ResolveThinkingForModel(h.models.ForRole(role), parsed)
	if !hasRoleThinking {
		if resolved != parsed {
			h.cfg.Thinking = string(resolved)
		}
		return resolved
	}
	if h.cfg.Roles == nil {
		h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
	}
	rc := h.cfg.Roles[role]
	rc.Thinking = string(resolved)
	h.cfg.Roles[role] = rc
	return resolved
}

func (h *Host) applyThinkingLocked(role string) {
	if h.thinkingApplier == nil {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		for _, r := range concreteThinkingRoles {
			lv, _ := agents.ParseThinkingLevel(h.cfg.ResolveThinking(r))
			h.thinkingApplier(r, lv)
		}
		return
	}
	lv, _ := agents.ParseThinkingLevel(h.cfg.ResolveThinking(role))
	h.thinkingApplier(role, lv)
}

// SetRoleThinking Thiết lập某角色（或 default）的思考强度：校验→持久化→联动 live agent→事件。
// 镜像 SwitchModel 的结构；与Mô hìnhChọn正交，可单独调整。level 为Rỗng = 不覆盖（继承）。
func (h *Host) SetRoleThinking(role, level string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	parsed, err := agents.ParseThinkingLevel(level)
	if err != nil {
		return err
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		for _, r := range concreteThinkingRoles {
			if resolved, ok := agents.ResolveThinkingForModel(h.models.ForRole(r), parsed); !ok || resolved != parsed {
				parsed = resolved
				break
			}
		}
	} else {
		parsed, _ = agents.ResolveThinkingForModel(h.models.ForRole(role), parsed)
	}
	// 持久化：具体角色写 Roles[role].Thinking，default/"" 写顶层 Thinking。
	if role == "" || role == "default" {
		h.cfg.Thinking = string(parsed)
	} else {
		if h.cfg.Roles == nil {
			h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := h.cfg.Roles[role]
		rc.Thinking = string(parsed)
		h.cfg.Roles[role] = rc
	}
	if path := bootstrap.DefaultConfigPath(); path != "" {
		if err := bootstrap.SaveConfig(path, h.cfg); err != nil {
			slog.Warn("Lưu cấu hìnhThất bại", "module", "host", "err", err)
		}
	}

	// 联动 live：具体角色直接Áp dụng；default 则遍历各具体角色按 ResolveThinking 重MớiÁp dụng
	// （已被角色级覆盖的保留自身，未覆盖的吃上MớiMặc định）。
	h.applyThinkingLocked(role)

	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	shown := string(parsed)
	if shown == "" {
		shown = "Mặc định(继承)"
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("思考强度已切换：%s → %s", logRole, shown),
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

// CoCreateStream 冷启动共创：从零澄清需求，产出整本书的创作指令。
func (h *Host) CoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, h.store.Sessions, coCreateSystemPrompt, history, onProgress)
}

// StageCoCreateStream 阶段共创：在已写内容的基础上规划后续方向。
// 系统提示 = 阶段 prompt + Hiện tại故事Trạng tháiTóm tắt，让助手知道"已经写了什么"。
func (h *Host) StageCoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, h.store.Sessions, stageSystemPrompt(h.store), history, onProgress)
}

// stagePlanPrefix 把共创产出的"后续方向 brief"包装成一条阶段规划干预，交 Coordinator 裁定。
// 只贴 [阶段规划] 事实标记 + 中性陈述，不写死"怎么落地"——具体路由（compass / architect /
// save_directive）交给 coordinator.md 的「阶段规划」判据，避免与 prompt 形成第二真相源、
// 也不堵死风格类要求走 directive（守"分类裁定归 LLM"）。Continue 再叠加 [用户干预] 前缀。
const stagePlanPrefix = "[阶段规划] 我Tạm dừng创作，和共创助手一起梳理了下面的后续方向，Vui lòng按你的干预分类裁定如何落地，然后Tiếp tục创作。后续方向如下：\n\n"

// PauseForCoCreate 进入阶段共创：置共创占用标记，Đang chạy则一并Tạm dừng coordinator。
// Quay lại false 表示Không thể进入（全书Đã hoàn thành或已在共创中），调用方忽略即可。
// 占用标记在共创Cửa sổ内堵住 import/simulate/start/resume/continue 的并发介入——
// Đang chạyTạm dừng后 lifecycle=paused，现有 ==running 互斥失效，靠该标记补缺；
// 已停止（idle/paused）也允许进入，规划完经 Continue 续跑。
func (h *Host) PauseForCoCreate() bool {
	h.mu.Lock()
	if h.cocreating || h.lifecycle == lifecycleCompleted {
		h.mu.Unlock()
		return false
	}
	h.cocreating = true
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()

	// Đang chạy复用 abortWithEvent 停机（running→paused + setAborting + Abort + 事件），与手动
	// Tạm dừng同序、不另抄一遍；已停止（idle/paused）只置标记，规划完经 Continue 续跑。
	if running {
		h.abortWithEvent("进入阶段共创，创作已Tạm dừng", "info")
	} else {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "进入阶段共创", Level: "info"})
	}
	return true
}

// ResumeFromCoCreate Kết thúc阶段共创：把共创产出的后续方向作为干预注入并Phục hồi创作。
// 清占用标记后复用 Continue 的停机注入Đường dẫn（受预算前置约束）。
// 注：draft 为Rỗng时提前Quay lại、不清标记是有意的（共创尚未Kết thúc）；TUI 侧 canStart() 守卫
// 与此处用同一"非Rỗng"判据，保证该Đường dẫn不可达，cocreating 不会因此泄漏。
func (h *Host) ResumeFromCoCreate(draft string) error {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return fmt.Errorf("draft is required")
	}
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("not in co-create")
	}
	h.cocreating = false
	h.mu.Unlock()

	// PauseForCoCreate 的 Abort 是异步的：Phục hồi前等Cũ run 收敛，回到与手动Tạm dừng后 Continue
	// 一致的"真停机"前提，避免把续跑指令 steer 进ĐangThoát的Cũ run。非运行态进共创（未
	// Abort）时 coordinator 本就 idle，WaitForIdle 立即Quay lại。
	h.coordinator.WaitForIdle()

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "阶段共创Hoàn thành，已注入后续方向并Phục hồi创作", Level: "info"})
	return h.Continue(stagePlanPrefix + draft)
}

// CancelCoCreate 放弃阶段共创：清占用标记，保持Tạm dừng态（用户可在Nhập框Tiếp tục或重启 Resume）。
func (h *Host) CancelCoCreate() {
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return
	}
	h.cocreating = false
	h.mu.Unlock()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "已Thoát阶段共创，创作保持Tạm dừng（可在Nhập框Tiếp tục）", Level: "info"})
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

// ImportFrom 启动一次外部小说反推Nhập：切分 → 反推 foundation → 逐章分析落盘。
// 与 Coordinator 互斥；NhậpHoàn thành后调用方可立即 Resume() 续写。
// Quay lại的事件通道由 imp.Run 关闭，调用方负责消费（满则丢弃以防阻塞分析协程）。
func (h *Host) ImportFrom(ctx context.Context, opts imp.Options) (<-chan imp.Event, error) {
	if err := h.guardExclusive("Nhập"); err != nil {
		return nil, err
	}

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

// Simulate Đọc simulate Thư mục并生成或增量更Mới仿写画像。
func (h *Host) Simulate(ctx context.Context) (<-chan sim.Event, error) {
	if err := h.guardExclusive("生成仿写画像"); err != nil {
		return nil, err
	}

	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working dir: %w", err)
	}
	deps := sim.Deps{
		Store: h.store,
		LLM:   h.models.ForRole("architect"),
		Prompts: sim.Prompts{
			Source: h.bundle.Prompts.SimulationSource,
			Merge:  h.bundle.Prompts.SimulationMerge,
		},
	}
	return sim.Run(ctx, deps, sim.Options{SourceDir: filepath.Join(wd, "simulate")})
}

// ImportSimulationProfile Nhập此前生成的仿写画像。
func (h *Host) ImportSimulationProfile(ctx context.Context, path string) (<-chan sim.Event, error) {
	if err := h.guardExclusive("Nhập仿写画像"); err != nil {
		return nil, err
	}
	return sim.RunImport(ctx, h.store, path)
}

// guardExclusive Kiểm tra独占占用：coordinator Đang chạy或阶段共创Cửa sổ内时拒绝会改写Trạng thái的入口
// （import/simulate）。补上 paused 期间只查 ==running 的并发缺口。
func (h *Host) guardExclusive(action string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.lifecycle == lifecycleRunning:
		return fmt.Errorf("coordinator Đang chạy，Vui lòng先Tạm dừng后再%s", action)
	case h.cocreating:
		return fmt.Errorf("阶段共创Đang thực hiện，Vui lòng先Kết thúc共创后再%s", action)
	}
	return nil
}

// Export XuấtĐã hoàn thànhChương为外部Tập tin（Hiện tại仅支持 TXT）。
//
// 与 ImportFrom 不同：Xuất是只读操作（不动 Progress / Checkpoint），
// 因此**不要求 Coordinator Rỗng闲**——Viết中途也可以随时Xuất"现阶段成品"。
// 只读到 Progress.CompletedChapters + Chương终稿 + Đại cương + premise 的一致Chụp。
func (h *Host) Export(ctx context.Context, opts exp.Options) (*exp.Result, error) {
	return exp.Run(ctx, exp.Deps{Store: h.store}, opts)
}
