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
	"github.com/voocel/ainovel-cli/internal/skills"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
	"github.com/voocel/ainovel-cli/internal/userrules"
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
	thinkingApplier   agents.ApplyThinking       // /model 调推理强度时联动 live agent（coordinator + 子代理）
	askUser           *tools.AskUserTool
	webSearch         *tools.WebSearchTool
	skillStore        *skills.Store
	writerRestore     *ctxpack.WriterRestorePack
	observer          *observer
	router            *flow.Dispatcher
	usage             *UsageTracker
	usageCancel       context.CancelFunc // 停掉 autoSaveLoop 并触发最后一次 flush
	budget            *BudgetSentinel    // 预算政策；未启用为 nil（方法 nil 安全）
	budgetDetach      func()
	pauser            *PausePointSentinel // 用户停靠点政策（方法 nil 安全）
	notifier          *notify.Notifier    // 无人值守告警；未启用为 nil（Send nil 安全）

	events   chan Event
	streamCh chan string
	done     chan struct{}

	mu         sync.Mutex
	lifecycle  lifecycle
	cocreating bool // 阶段共创占用：paused 窗口内堵住 import/simulate/continue 的并发介入
	closeOnce  sync.Once
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

	var router *flow.Dispatcher
	var budget *BudgetSentinel
	var pauser *PausePointSentinel
	// onGuardBlock 与 router/budget 同款前置声明：h 构造后才能挂事件浮出闭包。
	var onGuardBlock func(agent, reason string, consecutive int32)
	coordinator, askUser, restore, coordinatorCtxMgr, applyThinking, webSearchTool, skillStore := agents.BuildCoordinator(cfg, store, models, bundle, usage.Record, func(string) {
		if budget != nil && budget.HandleBoundary() {
			return
		}
		// 预算止损优先于验收暂停；停靠点命中则短路本轮派发
		if pauser.HandleBoundary() {
			return
		}
		if router != nil {
			router.Dispatch()
		}
	}, func(agent, reason string, consecutive int32) {
		if onGuardBlock != nil {
			onGuardBlock(agent, reason, consecutive)
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
		webSearch:         webSearchTool,
		skillStore:        skillStore,
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
	// 预算哨兵订阅子代理边界事件执行停机；Dispatcher 由工具执行链同步触发，
	// 不再通过事件订阅抢占下一轮模型调用。
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
		// 计费盲区告警：模型不报 usage 时成本恒 0，预算永不触发——保险丝没接上必须喊人。
		usage.SetOnMissingUsage(func() {
			const blind = "预算盲区: 模型未返回 usage 数据，成本统计为 0，预算上限不会触发（自定义模型请确认注册表价格或上游 include_usage）"
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: blind, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: "budget", Level: "warn", Title: "ainovel: 预算", Body: blind})
		})
	}
	// 停靠点哨兵：执行用户预约的"重写完暂停"指令，事件+notify 成对（架构 §2.3）。
	// abort 注入必须自带 notify：暂停把 lifecycle 置 paused，waitDone 的 run_end
	// 通知会因 wasRunning=false 跳过——不在这里发，挂机用户在最该被叫回来的
	// 时刻收不到任何推送（abortWithEvent 本身只发屏内事件）。
	h.pauser = NewPausePointSentinel(store,
		func(reason string) {
			h.abortWithEvent(reason, "info")
			h.notifier.Send(notify.Notification{Kind: "pause_point", Level: "info", Title: "ainovel: 验收停靠点", Body: reason})
		},
		func(level, summary string) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
			h.notifier.Send(notify.Notification{Kind: "pause_point", Level: level, Title: "ainovel: 验收停靠点", Body: summary})
		},
	)
	pauser = h.pauser
	h.router = flow.NewDispatcher(coordinator, store)
	router = h.router
	// 重复指令告警：纯 telemetry，挂机时"模型可能在原地打转"值得喊人看一眼。
	// 事件流与 notify 成对发出——notify 只是屏内事件的离屏副本（架构 §2.3）。
	h.router.SetOnRepeat(func(agent, task string, n int) {
		body := fmt.Sprintf("同一指令已第 %d 次下达（%s）：%s", n, agent, task)
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "指令重复: " + body, Level: "warn"})
		h.notifier.Send(notify.Notification{Kind: "repeat", Level: "warn", Title: "ainovel: 指令重复", Body: body})
	})
	// StopGuard 拦截浮出：blocked 是高频自愈动作，只进屏内事件流（推送会刷屏）；
	// escalated / hard_stop 意味着本轮子任务报废重派，事件+notify 成对发出（架构 §2.3）。
	// 没有这层，拦截只进日志，用户在 TUI 上只看到"卡顿+token 变快"（issue #75）。
	onGuardBlock = func(agent, reason string, n int32) {
		switch reason {
		case "escalated":
			body := fmt.Sprintf("%s 连续 %d 次空转未落盘必要产物，本轮任务终止，交回 Coordinator 裁定", agent, n)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: "StopGuard 升级: " + body, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: "stop_guard", Level: "warn", Title: "ainovel: StopGuard", Body: body})
		case "hard_stop":
			body := fmt.Sprintf("%s 遭 provider 拒答（safety/content_filter），本轮任务立即终止", agent)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: "StopGuard 升级: " + body, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: "stop_guard", Level: "warn", Title: "ainovel: StopGuard", Body: body})
		default: // blocked
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent,
				Summary: fmt.Sprintf("StopGuard: %s 未完成必要产物就试图结束，已拦截催促（连续第 %d 次）", agent, n), Level: "info"})
		}
	}

	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		slog.Error("初始化运行元信息失败", "module", "boot", "err", err)
	}

	return h, nil
}

// ── 生命周期 ──

// PrepareUserRules 在新建模式下生成本书用户规则快照（启动侧确定性，不经 Coordinator、不进主创作 Run）。
//
// 入参是用户的**原始**创作要求（未经 BuildStartPrompt 包装）——归一化要的是用户规则本身，
// 不是启动脚手架。入口须在 StartPrepared 之前调用一次（quick/cocreate 两条新建路径都走这里）。
//
// 归一化失败只降级不报错（增强路径）；只有快照无法落盘才返回 error 中止开书——
// 后续运行将没有稳定事实源（见设计 §失败与降级）。
func (h *Host) PrepareUserRules(rawPrompt string) error {
	svc := userrules.NewService(h.store, h.models.Default, rules.DefaultOptions())
	snap, err := svc.Build(context.Background(), rawPrompt)
	if err != nil {
		return fmt.Errorf("用户规则快照落盘失败，无法继续: %w", err)
	}
	logUserRulesSnapshot(snap)
	return nil
}

// ensureUserRules 惰性确保快照存在（老书无快照时按 system_defaults + rules 文件生成）。
// 恢复路径调用，让老书也能拿到 rules 文件的归一化结果。
func (h *Host) ensureUserRules() {
	svc := userrules.NewService(h.store, h.models.Default, rules.DefaultOptions())
	snap, err := svc.GetOrBuild(context.Background())
	if err != nil {
		slog.Warn("用户规则快照读取/生成失败，运行时将退到内置默认", "module", "rules", "err", err)
		return
	}
	logUserRulesSnapshot(snap)
}

// logUserRulesSnapshot 启动回显：让用户看到系统把规则理解成了什么（复用日志，不新增机制）。
func logUserRulesSnapshot(snap *rules.Snapshot) {
	if snap == nil {
		return
	}
	words := "未设置"
	if w := snap.Structured.ChapterWords; w != nil {
		words = fmt.Sprintf("%d-%d", w.Min, w.Max)
	}
	slog.Info("用户规则快照",
		"module", "rules",
		"status", string(snap.Status),
		"来源", snap.Sources,
		"章节字数", words,
		"禁用短语", len(snap.Structured.ForbiddenPhrases),
		"疲劳词", len(snap.Structured.FatigueWords),
	)
	if snap.Status == rules.StatusDegraded {
		slog.Warn("部分规则未能解析，已按 raw preferences 运行（可重新生成快照）",
			"module", "rules", "uncertain", snap.Uncertain)
	}
}

// StartPrepared 使用已编排完成的启动 prompt 开始创作。
func (h *Host) StartPrepared(promptText string) error {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("阶段共创进行中，请先结束共创")
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

	slog.Info("开始创作", "module", "host", "prompt_len", len(promptText))
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "开始创作", Level: "info"})
	h.observer.setAborting(false)
	// 先重置重复追踪并启用路由，再启动 Prompt，避免首轮事件先于 Enable 抵达
	h.router.ResetRepeat()
	h.router.Enable()
	if err := h.coordinator.Prompt(context.Background(), promptText); err != nil {
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
	if h.cocreating {
		h.mu.Unlock()
		return "", fmt.Errorf("阶段共创进行中，请先结束共创")
	}
	h.mu.Unlock()

	prompt, label, err := buildResumePrompt(h.store)
	if err != nil {
		return "", err
	}
	if label == "" {
		return "", nil // 新建模式，无恢复
	}
	if err := h.budget.Refuse(); err != nil {
		return "", err
	}

	slog.Info("恢复创作", "module", "host", "label", label)
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "恢复创作: " + label, Level: "info"})
	for _, w := range h.store.CheckConsistency() {
		slog.Warn("一致性告警", "module", "host", "detail", w)
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "一致性告警: " + w, Level: "warn"})
	}
	// 老书无快照时惰性生成（按 system_defaults + rules 文件归一化）；已有则廉价读取。
	h.ensureUserRules()
	h.refreshWriterRestore()
	h.observer.setAborting(false)
	h.router.ResetRepeat()
	h.router.Enable()
	if err := h.coordinator.Prompt(context.Background(), prompt); err != nil {
		return "", fmt.Errorf("resume prompt: %w", err)
	}
	// PendingSteer 已随恢复 prompt 交付给 Coordinator，清除以免下次 Resume 重复注入同一条旧干预。
	// ClearHandledSteer 幂等：PendingSteer 为空 / Flow 非 steering 时均为 no-op。
	if err := h.store.ClearHandledSteer(); err != nil {
		slog.Warn("清除已处理的 PendingSteer 失败", "module", "host", "err", err)
	}
	// 停靠点对账：崩溃恰在"排空后、消费前"时，用户手动 Resume 即视为放行。
	h.pauser.ReconcileOnResume()
	// 主动派发一次首条指令，避免 Coordinator 对恢复 prompt 只回文字而 StopGuard 反复拦截。
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
// Coordinator 失去分类锚点而误派子代理（曾导致"改已写章节"被派给 writer 撞 edit_chapter 守卫）。
func interventionMsg(text string) agentcore.Message {
	return agentcore.UserMsg("[用户干预] " + text)
}

// Continue 用指定 prompt 继续。停机后用户在输入框输入时调用。
func (h *Host) Continue(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	h.mu.Lock()
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("阶段共创进行中，请先结束共创")
	}
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()

	h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[继续] " + text, Level: "info"})

	if running {
		h.coordinator.FollowUp(interventionMsg(text))
		return nil
	}
	// 停机后 → 注入并自动恢复（恢复 run 也受预算前置约束）
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	// 与 Resume 对称：若停机由预算/Esc 抢在停靠点消费之前，用户显式继续=放行，
	// 对账解除已满足的停靠点，避免恢复后首个边界立即再暂停。
	h.pauser.ReconcileOnResume()
	h.refreshWriterRestore()
	h.observer.setAborting(false)
	h.router.ResetRepeat()
	h.router.Enable()
	_, err := h.coordinator.Inject(interventionMsg(text))
	if err != nil {
		return fmt.Errorf("inject: %w", err)
	}
	// 与 Resume 对称：干预注入后主动派发一次当前路由指令。否则 Coordinator 只有
	// 用户文本没有 Host 指令，若它对干预只回文字，StopGuard 的拦截又要求执行
	// Host 指令，会陷入"无指令可执行"的僵局（继续后连续拦截直至熔断）。
	// 派发晚于 Inject：用户干预先入上下文，指令后到，干预优先级不受影响。
	h.router.Dispatch()
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

	msg := interventionMsg(text)
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
	return h.abortWithEvent("用户手动暂停当前创作", "warn")
}

// abortWithEvent 以指定原因事件执行暂停。预算停机与手动暂停共用同一停机机制，
// 仅事件文案不同（预算停机=用户预先签署的 Abort 指令，语义等同手动暂停）。
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
	// 失败事件，observer 凭此标志识别为 abort 衍生噪声并抑制。
	h.observer.setAborting(true)
	h.coordinator.Abort()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
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
	if h.budgetDetach != nil {
		h.budgetDetach()
		h.budgetDetach = nil
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
	// 运行中退出时 Close() 可能已 close(h.done)（AbortSilent 只 cancel 不 join 本 goroutine），
	// 末尾向 h.done 的发送会 panic；与 emitEvent 一致用 recover 兜住这段退出期竞态。
	defer func() { recover() }()
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
		h.notifier.Send(notify.Notification{
			Kind: "run_end", Level: "info", Title: "ainovel: 创作完成",
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
			summary := fmt.Sprintf("Coordinator 停止 (已完成 %d 章)", completed)
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

// runEndBody 组装 run_end 通知正文：书名 + 进度摘要 + 累计花费。
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
		ThinkingLevel:          h.cfg.ResolveReasoningEffort("default"),
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
		TotalCacheBreaks:       h.usage.OverallCacheBreaks(),
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
	h.normalizeThinkingLocked(role)
	if path := bootstrap.DefaultConfigPath(); path != "" {
		if err := bootstrap.SaveConfig(path, h.cfg); err != nil {
			slog.Warn("保存配置失败", "module", "host", "err", err)
		}
	}
	h.applyThinkingLocked(role)
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

// concreteThinkingRoles 是可应用推理强度的具体角色（与 agents.ApplyThinking 路由一致）。
// 调 default 时按各角色 ResolveReasoningEffort 逐个重新应用。
var concreteThinkingRoles = []string{"coordinator", "architect", "writer", "editor"}

// CurrentThinking 返回某角色当前生效的推理强度原始串（供 /model 面板同步当前值）。
func (h *Host) CurrentThinking(role string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.ResolveReasoningEffort(strings.ToLower(strings.TrimSpace(role)))
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
		parsed, _ := agents.ParseThinkingLevel(h.cfg.ReasoningEffort)
		for _, r := range concreteThinkingRoles {
			resolved, ok := agents.ResolveThinkingForModel(h.models.ForRole(r), parsed)
			if !ok || resolved != parsed {
				h.cfg.ReasoningEffort = string(resolved)
				return resolved
			}
		}
		h.cfg.ReasoningEffort = string(parsed)
		return parsed
	}

	_, hasRoleThinking := h.cfg.Roles[role]
	hasRoleThinking = hasRoleThinking && h.cfg.Roles[role].ReasoningEffort != ""
	parsed, _ := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(role))
	resolved, _ := agents.ResolveThinkingForModel(h.models.ForRole(role), parsed)
	if !hasRoleThinking {
		if resolved != parsed {
			h.cfg.ReasoningEffort = string(resolved)
		}
		return resolved
	}
	if h.cfg.Roles == nil {
		h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
	}
	rc := h.cfg.Roles[role]
	rc.ReasoningEffort = string(resolved)
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
			lv, _ := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(r))
			h.thinkingApplier(r, lv)
		}
		return
	}
	lv, _ := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(role))
	h.thinkingApplier(role, lv)
}

// SetRoleThinking 设置某角色（或 default）的推理强度：校验→持久化→联动 live agent→事件。
// 镜像 SwitchModel 的结构；与模型选择正交，可单独调整。level 为空 = 不覆盖（继承）。
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
	// 持久化：具体角色写 Roles[role].ReasoningEffort，default/"" 写顶层 ReasoningEffort。
	if role == "" || role == "default" {
		h.cfg.ReasoningEffort = string(parsed)
	} else {
		if h.cfg.Roles == nil {
			h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := h.cfg.Roles[role]
		rc.ReasoningEffort = string(parsed)
		h.cfg.Roles[role] = rc
	}
	if path := bootstrap.DefaultConfigPath(); path != "" {
		if err := bootstrap.SaveConfig(path, h.cfg); err != nil {
			slog.Warn("保存配置失败", "module", "host", "err", err)
		}
	}

	// 联动 live：具体角色直接应用；default 则遍历各具体角色按 ResolveReasoningEffort 重新应用
	// （已被角色级覆盖的保留自身，未覆盖的吃上新默认）。
	h.applyThinkingLocked(role)

	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	shown := string(parsed)
	if shown == "" {
		shown = "默认(继承)"
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("推理强度已切换：%s → %s", logRole, shown),
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
	return coCreateStream(ctx, h.models, h.store.Sessions, coCreateSystemPrompt, history, onProgress, h.webSearch)
}

// StageCoCreateStream 阶段共创：在已写内容的基础上规划后续方向。
// 系统提示 = 阶段 prompt + 当前故事状态摘要，让助手知道"已经写了什么"。
func (h *Host) StageCoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, h.store.Sessions, stageSystemPrompt(h.store), history, onProgress, h.webSearch)
}

// stagePlanPrefix 把共创产出的"后续方向 brief"包装成一条阶段规划干预，交 Coordinator 裁定。
// 只贴 [阶段规划] 事实标记 + 中性陈述，不写死"怎么落地"——具体路由（compass / architect /
// save_user_rules）交给 coordinator.md 的「阶段规划」判据，避免与 prompt 形成第二真相源、
// 也不堵死风格类要求走 user_rules（守"分类裁定归 LLM"）。Continue 再叠加 [用户干预] 前缀。
const stagePlanPrefix = "[阶段规划] 我暂停创作，和共创助手一起梳理了下面的后续方向，请按你的干预分类裁定如何落地，然后继续创作。后续方向如下：\n\n"

// PauseForCoCreate 进入阶段共创：置共创占用标记，运行中则一并暂停 coordinator。
// 返回 false 表示无法进入（全书已完成或已在共创中），调用方忽略即可。
// 占用标记在共创窗口内堵住 import/simulate/start/resume/continue 的并发介入——
// 运行中暂停后 lifecycle=paused，现有 ==running 互斥失效，靠该标记补缺；
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

	// 运行中复用 abortWithEvent 停机（running→paused + setAborting + Abort + 事件），与手动
	// 暂停同序、不另抄一遍；已停止（idle/paused）只置标记，规划完经 Continue 续跑。
	if running {
		h.abortWithEvent("进入阶段共创，创作已暂停", "info")
	} else {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "进入阶段共创", Level: "info"})
	}
	return true
}

// ResumeFromCoCreate 结束阶段共创：把共创产出的后续方向作为干预注入并恢复创作。
// 清占用标记后复用 Continue 的停机注入路径（受预算前置约束）。
// 注：draft 为空时提前返回、不清标记是有意的（共创尚未结束）；TUI 侧 canStart() 守卫
// 与此处用同一"非空"判据，保证该路径不可达，cocreating 不会因此泄漏。
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

	// PauseForCoCreate 的 Abort 是异步的：恢复前等旧 run 收敛，回到与手动暂停后 Continue
	// 一致的"真停机"前提，避免把续跑指令 steer 进正在退出的旧 run。非运行态进共创（未
	// Abort）时 coordinator 本就 idle，WaitForIdle 立即返回。
	h.coordinator.WaitForIdle()

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "阶段共创完成，已注入后续方向并恢复创作", Level: "info"})
	return h.Continue(stagePlanPrefix + draft)
}

// CancelCoCreate 放弃阶段共创：清占用标记，保持暂停态（用户可在输入框继续或重启 Resume）。
func (h *Host) CancelCoCreate() {
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return
	}
	h.cocreating = false
	h.mu.Unlock()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "已退出阶段共创，创作保持暂停（可在输入框继续）", Level: "info"})
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
	if err := h.guardExclusive("导入"); err != nil {
		return nil, err
	}

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

// Simulate 读取 simulate 目录并生成或增量更新仿写画像。
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

// ImportSimulationProfile 导入此前生成的仿写画像。
func (h *Host) ImportSimulationProfile(ctx context.Context, path string) (<-chan sim.Event, error) {
	if err := h.guardExclusive("导入仿写画像"); err != nil {
		return nil, err
	}
	return sim.RunImport(ctx, h.store, path)
}

// guardExclusive 检查独占占用：coordinator 运行中或阶段共创窗口内时拒绝会改写状态的入口
// （import/simulate）。补上 paused 期间只查 ==running 的并发缺口。
func (h *Host) guardExclusive(action string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.lifecycle == lifecycleRunning:
		return fmt.Errorf("coordinator 运行中，请先暂停后再%s", action)
	case h.cocreating:
		return fmt.Errorf("阶段共创进行中，请先结束共创后再%s", action)
	}
	return nil
}

// Export 导出已完成章节为外部文件（当前仅支持 TXT）。
//
// 与 ImportFrom 不同：导出是只读操作（不动 Progress / Checkpoint），
// 因此**不要求 Coordinator 空闲**——写作中途也可以随时导出"现阶段成品"。
// 只读到 Progress.CompletedChapters + 章节终稿 + 大纲 + premise 的一致快照。
func (h *Host) Export(ctx context.Context, opts exp.Options) (*exp.Result, error) {
	return exp.Run(ctx, exp.Deps{Store: h.store}, opts)
}

// SkillStore 返回跨书 skill 库的引用。nil 表示 skill 库未启用
// （路径解析失败或被禁用），调用方应优雅降级。
func (h *Host) SkillStore() *skills.Store {
	if h == nil {
		return nil
	}
	return h.skillStore
}

// SkillInject 把 /skill-name 用户消息展开为最终发给 LLM 的文本。
// 调用方（TUI / cocreate）先用 skills.ParseSkillRef 解析原始输入，
// 命中再调本方法。返回 (展开后文本, 是否命中, 错误提示)。
//
// host 自身不解析输入——拆分"解析"和"展开"两个职责让 cocreate 等场景
// 可以独立测试解析逻辑。
func (h *Host) SkillInject(req skills.InjectRequest) skills.InjectResult {
	if h == nil {
		return skills.InjectResult{
			Expanded: false,
			Hint:     "host 未初始化",
		}
	}
	return h.skillStore.InjectSkill(req)
}

// ── 素材收集（项目级素材库）──

// MaterialsCollect 触发一次素材收集：单轮 LLM 调用产出 8-15 条候选素材，
// 调用方（TUI）展示候选给用户筛选，选中条目走 MaterialsApprove 落盘。
//
// onProgress 在流式事件到达时被回调（kind=thinking/reply），可用于渲染加载动画。
// webSearch 是否可用取决于 Provider 配置；不可用时纯靠模型自身知识。
func (h *Host) MaterialsCollect(ctx context.Context, userPrompt string, onProgress func(kind, text string)) ([]MaterialsCandidate, string, error) {
	if h == nil {
		return nil, "", fmt.Errorf("host 未初始化")
	}
	return materialsCollect(ctx, h.models, userPrompt, onProgress, h.webSearch)
}

// MaterialsApprove 把用户筛选后的候选批量落盘到 meta/materials.json。
// 调用方应只传用户实际选中的条目；本小姐不做二次校验（信任 TUI 已经过滤）。
// 返回每条的最终 ID，便于 TUI 在事件流提示"保存了哪几条"。
func (h *Host) MaterialsApprove(items []MaterialsCandidate) ([]domain.MaterialItem, error) {
	if h == nil {
		return nil, fmt.Errorf("host 未初始化")
	}
	if len(items) == 0 {
		return nil, nil
	}
	conv := make([]domain.MaterialItem, 0, len(items))
	for _, c := range items {
		conv = append(conv, domain.MaterialItem{
			Category: c.Category,
			Title:    c.Title,
			Content:  c.Content,
			Source:   c.Source,
		})
	}
	return h.store.Materials.AddBatch(conv)
}

// MaterialsList 列出当前项目素材库。便于 TUI 在收集前展示已有素材。
func (h *Host) MaterialsList() ([]domain.MaterialItem, error) {
	if h == nil {
		return nil, fmt.Errorf("host 未初始化")
	}
	lib, err := h.store.Materials.Load()
	if err != nil {
		return nil, err
	}
	return lib.Items, nil
}

// MaterialsRemove 按 ID 删除单条素材。
func (h *Host) MaterialsRemove(id string) error {
	if h == nil {
		return fmt.Errorf("host 未初始化")
	}
	return h.store.Materials.Remove(id)
}

// LoadPremise 返回本书的 premise（Markdown 字符串）。空表示尚未规划。
// 给 TUI 在 /materials 不带参数时复用规划前提作收集 prompt。
func (h *Host) LoadPremise() (string, error) {
	if h == nil {
		return "", fmt.Errorf("host 未初始化")
	}
	return h.store.Outline.LoadPremise()
}
