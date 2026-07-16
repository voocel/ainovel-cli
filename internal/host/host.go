package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/arbiter"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	modelreg "github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/ainovel-cli/internal/notify"
	"github.com/voocel/ainovel-cli/internal/rules"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
	"github.com/voocel/ainovel-cli/internal/userrules"
)

// Host 是运行时外壳:生命周期/干预入口/事件投影/模型管理。
// 调度与执行在 engine(确定性循环);语义裁定在 arbiter(LLM-as-function)。
type Host struct {
	cfg             bootstrap.Config
	bundle          assets.Bundle
	store           *storepkg.Store
	models          *bootstrap.ModelSet
	engine          *engine
	thinkingApplier agents.ApplyThinking // /model 调推理强度时联动各 Worker
	askUser         *tools.AskUserTool
	writerRestore   *ctxpack.WriterRestorePack
	userRules       *userrules.Service
	observer        *observer
	usage           *UsageTracker
	usageCancel     context.CancelFunc  // 停掉 autoSaveLoop 并触发最后一次 flush
	budget          *BudgetSentinel     // 预算政策；未启用为 nil（方法 nil 安全）
	gate            *ChapterAdvanceGate // 章节许可与一次性暂停的统一政策组件
	notifier        *notify.Notifier    // 无人值守告警；未启用为 nil（Send nil 安全）

	events   chan Event
	streamCh chan string
	done     chan struct{}

	mu         sync.Mutex
	lifecycle  lifecycle
	cocreating bool   // 阶段共创占用：paused 窗口内堵住 import/simulate/continue 的并发介入
	exclusive  string // 后台独占作业占用（导入/仿写）：非空表示某作业在跑，堵住并发独占入口
	// exclusiveCancel 是当前独占作业的取消函数：预算硬停/手动暂停须能停掉正在烧钱的
	// 导入，而不仅是 Engine——abortWithEvent 在 Engine 未运行时取消它（预算哨兵的
	// abort 回调与手动 Abort 共用同一停机机制）。releaseExclusive 一并清空。
	exclusiveCancel context.CancelFunc
	closeOnce       sync.Once

	interMu sync.Mutex // 干预裁定 FIFO 串行(同一时刻至多一次在途咨询)

	// runCtx 约束宿主侧的 LLM 裁定调用(启动裁定/干预分诊);Close 取消,
	// 避免退出时仍有裁定在途且无法中断。
	runCtx    context.Context
	runCancel context.CancelFunc
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
	// RunMeta 是所有控制语义的事实源，必须在构造模型/后台任务之前完成校验。
	// 未知 advance mode 直接返回结构化错误；禁止猜测降级后继续写盘。
	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		return nil, fmt.Errorf("init run meta: %w", err)
	}

	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return nil, fmt.Errorf("create models: %w", err)
	}
	slog.Info("模型就绪", "module", "boot", "summary", models.Summary())

	usage := NewUsageTracker(models, store)
	// 优先读 meta/usage.json；以下情况都走 sessions/*.jsonl 一次性回填：
	//   - 文件不存在（首次持久化前）
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

	// onGuardBlock 前置声明:h 构造后才能挂事件浮出闭包。
	var onGuardBlock func(agent, reason string, consecutive int32)
	workers, askUser, restore, applyThinking := agents.BuildWorkers(cfg, store, models, bundle, usage.Record,
		func(agent, reason string, consecutive int32) {
			if onGuardBlock != nil {
				onGuardBlock(agent, reason, consecutive)
			}
		})
	store.Signals.ClearStaleSignals()

	h := &Host{
		cfg:             cfg,
		bundle:          bundle,
		store:           store,
		models:          models,
		thinkingApplier: applyThinking,
		askUser:         askUser,
		writerRestore:   restore,
		userRules:       userrules.NewService(store, models.Default, rules.DefaultOptions()),
		usage:           usage,
		usageCancel:     usageCancel,
		events:          make(chan Event, 100),
		streamCh:        make(chan string, 256),
		done:            make(chan struct{}, 4),
		lifecycle:       lifecycleIdle,
	}
	h.runCtx, h.runCancel = context.WithCancel(context.Background())
	h.observer = newObserver(store, h.emitEvent, h.emitDelta, h.emitClear)
	// 宿主侧 Arbiter 与 Worker 共用同一条 ToolProgress → observer → 工作台链路。
	h.runCtx = agentcore.WithToolProgress(h.runCtx, h.observer.workerProgress)
	if cfg.Notify.IsEnabled() {
		h.notifier = notify.New(cfg.Notify.Command, cfg.Notify.Events)
	}
	// 预算哨兵:Engine 在每轮循环边界直接调用 HandleBoundary(不再经事件订阅)。
	if sentinel := NewBudgetSentinel(cfg.Budget,
		func() float64 { c, _, _, _, _ := usage.Totals(); return c },
		func(reason string) { h.abortWithEvent(reason, "error") },
		func(level, summary string) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
			h.notifier.Send(notify.Notification{Kind: notify.KindBudget, Level: level, Title: "ainovel: 预算", Body: summary})
		},
	); sentinel != nil {
		h.budget = sentinel
		usage.SetOnCost(sentinel.OnCost)
		// 计费盲区告警：模型不报 usage 时成本恒 0，预算永不触发——保险丝没接上必须喊人。
		usage.SetOnMissingUsage(func() {
			const blind = "预算盲区: 模型未返回 usage 数据，成本统计为 0，预算上限不会触发（自定义模型请确认注册表价格或上游 include_usage）"
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: blind, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: notify.KindBudget, Level: "warn", Title: "ainovel: 预算", Body: blind})
		})
	}
	// 统一前进闸门：执行一次性 hold，并阻止 review 模式下无许可的新章。
	h.gate = NewChapterAdvanceGate(store,
		func(reason string) {
			h.abortWithEvent(reason, "info")
			h.notifier.Send(notify.Notification{Kind: notify.KindAdvanceGate, Level: "info", Title: "ainovel: 等待验收", Body: reason})
		},
		func(level, summary string) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
			h.notifier.Send(notify.Notification{Kind: notify.KindAdvanceGate, Level: level, Title: "ainovel: 章节推进", Body: summary})
		},
	)
	// StopGuard 拦截浮出：blocked 是高频自愈动作，只进屏内事件流（推送会刷屏）；
	// escalated / hard_stop 意味着本轮子任务报废，事件+notify 成对发出（架构 §2.3）。
	onGuardBlock = func(agent, reason string, n int32) {
		switch reason {
		case "escalated":
			body := fmt.Sprintf("%s 连续 %d 次空转未落盘必要产物，本轮任务终止，交回 Engine 处理", agent, n)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: "StopGuard 升级: " + body, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: notify.KindStopGuard, Level: "warn", Title: "ainovel: StopGuard", Body: body})
		case "hard_stop":
			body := fmt.Sprintf("%s 遭 provider 拒答（safety/content_filter），本轮任务立即终止", agent)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: "StopGuard 升级: " + body, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: notify.KindStopGuard, Level: "warn", Title: "ainovel: StopGuard", Body: body})
		default: // blocked
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent,
				Summary: fmt.Sprintf("StopGuard: %s 未完成必要产物就试图结束，已拦截催促（连续第 %d 次）", agent, n), Level: "info"})
		}
	}
	// Engine:确定性执行引擎(docs/engine-rfc.md)。arbiter 用 Default 模型(过渡限制,
	// 见 engine-arbiter.md §4.2)。
	h.engine = &engine{
		store:           store,
		workers:         workers,
		arbiterModel:    newUsageTrackedModel(models.Default, "arbiter", usage.Record),
		failurePrompt:   bundle.Prompts.ArbiterFailure,
		planStartPrompt: bundle.Prompts.ArbiterPlanStart,
		style:           cfg.Style,
		// 同步重询:阻塞引擎循环一次裁定(数秒),换取"干预先于后续创作生效"。
		reconsult: h.handleIntervention,
		observer:  h.observer,
		budget:    h.budget,
		gate:      h.gate,
		refresh:   h.refreshWriterRestore,
		emitEvent: h.emitEvent,
		notify: func(kind, level, title, body string) {
			h.notifier.Send(notify.Notification{Kind: kind, Level: level, Title: title, Body: body})
		},
		onPause: func(summary string) { h.abortWithEvent(summary, "warn") },
		onDone:  h.runEnded,
	}

	return h, nil
}

// ── 生命周期 ──

// PrepareUserRules 在新建模式下生成本书用户规则快照（启动侧确定性，不进主创作 Run）。
//
// 入参是用户的**原始**创作要求（未经 BuildStartPrompt 包装）——归一化要的是用户规则本身，
// 不是启动脚手架。入口须在 StartPrepared 之前调用一次（quick/cocreate 两条新建路径都走这里）。
//
// 归一化失败只降级不报错（增强路径）；只有快照无法落盘才返回 error 中止开书——
// 后续运行将没有稳定事实源（见设计 §失败与降级）。
func (h *Host) PrepareUserRules(rawPrompt string) error {
	if err := h.refuseNewBookOverExisting(); err != nil {
		return err
	}
	svc := userrules.NewService(h.store, h.models.Default, rules.DefaultOptions())
	snap, err := svc.Build(context.Background(), rawPrompt)
	if err != nil {
		return fmt.Errorf("用户规则快照落盘失败，无法继续: %w", err)
	}
	logUserRulesSnapshot(snap)
	return nil
}

// ensureUserRules 在恢复路径确保快照存在；缺失时按
// system_defaults + rules 文件生成。
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
	slog.Info("用户规则快照",
		"module", "rules",
		"status", string(snap.Status),
		"来源", snap.Sources,
		"禁用短语", len(snap.Structured.ForbiddenPhrases),
		"疲劳词", len(snap.Structured.FatigueWords),
	)
	if snap.Status == rules.StatusDegraded {
		slog.Warn("部分规则未能解析，已按 raw preferences 运行（可重新生成快照）",
			"module", "rules", "uncertain", snap.Uncertain)
	}
}

// StartPrepared 用用户的**原始**创作要求开始创作:plan_start 裁定选规划师并扩充
// 需求，裁定结果先固化为
// 事实(PlanStartRecord)再启动 Engine——恢复永远依赖已落盘事实,不重做已有裁定。
// 输入事实(StartPrompt)在裁定之前落盘:裁定失败时它是引擎补裁的依据,
// 启动失败可从任何恢复入口(Resume/继续)自愈,不是死局。
func (h *Host) StartPrepared(rawRequirement string) error {
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

	rawRequirement = strings.TrimSpace(rawRequirement)
	if rawRequirement == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := h.refuseNewBookOverExisting(); err != nil {
		return err
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
	// 输入事实先于裁定落盘:裁定失败(模型故障等)后 StartPrompt 仍在,
	// 恢复/继续时引擎据此补裁(planStartFallback),启动失败不再是死局。
	if err := h.store.RunMeta.SetStartPrompt(rawRequirement); err != nil {
		return fmt.Errorf("记录创作需求: %w", err)
	}

	// 启动裁定:失败显式报错中止(启动期用户在场,报错优于猜测)。
	start := time.Now()
	decision, derr := runObservedDecision(h.observer, "启动裁定", func() (arbiter.PlanStartDecision, error) {
		return arbiter.DecidePlanStart(h.runCtx, h.arbiterModel(),
			h.bundle.Prompts.ArbiterPlanStart, rawRequirement, h.cfg.Style)
	})
	rec := storepkg.DecisionRecord{Kind: "plan_start", Decider: "arbiter", Input: rawRequirement,
		Reason: decision.Reason, DurationMs: time.Since(start).Milliseconds()}
	if derr == nil {
		if data, err := json.Marshal(decision); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	var recErr error
	if rec, recErr = h.store.Decisions.Append(rec); recErr != nil {
		slog.Warn("启动裁定审计落盘失败", "module", "host", "err", recErr)
	}
	if derr != nil {
		return fmt.Errorf("启动裁定失败: %w", derr)
	}
	if err := h.store.RunMeta.SetPlanStart(domain.PlanStartRecord{
		RawPrompt: rawRequirement, Planner: decision.Planner, PlannerTask: decision.Task, DecisionID: rec.ID,
	}); err != nil {
		return fmt.Errorf("记录启动裁定: %w", err)
	}

	slog.Info("开始创作", "module", "host", "planner", decision.Planner)
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
		Summary: fmt.Sprintf("开始创作（规划师: %s——%s）", decision.Planner, decision.Reason), Level: "info"})
	if !h.startEngine(&flow.Instruction{Agent: decision.Planner, Task: decision.Task, Reason: decision.Reason}) {
		return fmt.Errorf("Engine 已在运行或正在停止，无法启动新书")
	}
	return nil
}

// refuseNewBookOverExisting 拒绝在已有成章的书目录里开新书：StartPrepared 会重置
// checkpoints 与 progress，误触即静默清掉整本书的进度链（导入完成后停在欢迎页
// 误按 Enter 是最典型场景）。只看已完成章数——规划阶段/启动失败的残留没有成章，
// 放行以保留共创 Ctrl+S 同会话重试与恢复补裁的自愈路径。
func (h *Host) refuseNewBookOverExisting() error {
	progress, err := h.store.Progress.Load()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if progress == nil || len(progress.CompletedChapters) == 0 {
		return nil
	}
	name := strings.TrimSpace(progress.NovelName)
	if name == "" {
		name = "未定书名"
	}
	return fmt.Errorf("输出目录已有《%s》的 %d 章创作进度，新建会重置其进度与检查点：续写请走恢复入口（重启应用自动恢复），新书请更换输出目录",
		name, len(progress.CompletedChapters))
}

// startEngine 统一的引擎启动入口(Start/Resume/Continue/干预重启共用)。
// lifecycle 必须先于 goroutine 启动置为 running:引擎可能立即结束(完本/无路由),
// runEnded 会把 lifecycle 落到终态;若顺序颠倒,runEnded 先跑、这里再写 running,
// UI 将永远显示"运行中"而引擎实际已停。
func (h *Host) startEngine(initial *flow.Instruction) bool {
	// 跨重启门禁：存在未完成导入工作区时，禁止普通 Engine 消费半发布状态（RFC §12.5）。
	if active, done := imp.ResumeStatus(h.store); active && !done {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: "存在未完成的外部小说导入，请先执行 /import 恢复完成后再继续创作"})
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// 后台独占作业（导入/仿写）进行中时，引擎不得抢跑，避免与其写入竞争。这是所有引擎启动路径
	// （Resume/Continue 重启/自动接力/next）的统一 backstop——入口守卫是第一道，这里是最后一道。
	if h.exclusive != "" {
		return false
	}
	// lifecycle 可能已经是 paused，但旧 Engine goroutine 仍在执行退出 defer。
	// 必须同时核对 Engine 真状态；否则会把 lifecycle 改回 running，而 start
	// 实际 no-op，随后旧 runEnded 又把它落成 idle。
	if h.engine.isRunning() {
		return false
	}
	h.observer.setAborting(false)
	previous := h.lifecycle
	h.lifecycle = lifecycleRunning
	if !h.engine.start(initial) {
		h.lifecycle = previous
		return false
	}
	return true
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
	if h.exclusive != "" {
		ex := h.exclusive
		h.mu.Unlock()
		return "", fmt.Errorf("%s进行中，请先完成后再恢复创作", ex)
	}
	h.mu.Unlock()

	label, err := resumeLabel(h.store)
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
	// 确保用户规则快照存在；已有则廉价读取。
	h.ensureUserRules()
	h.refreshWriterRestore()
	// 待处理干预(停机期留下的/裁定期崩溃残留的)必须先于引擎续跑裁定——
	// 否则引擎可能抢在裁定前继续写出与干预相悖的章节。同步执行(阻塞数秒可接受,
	// UI 已显示"恢复创作");doIntervention 成功后自行清除 PendingSteer 并按
	// restart=true 拉起引擎。无待处理干预 → 直接续跑。
	if meta, _ := h.store.RunMeta.Load(); meta != nil && meta.PendingSteer != "" {
		h.doIntervention(meta.PendingSteer, true)
		// 裁定失败(已回显)时也要恢复续跑——书不能因一条无法理解的旧干预卡死。
		if !h.engine.isRunning() {
			if err := h.budget.Refuse(); err == nil {
				if !h.startEngine(nil) {
					return label, fmt.Errorf("Engine 正在完成上一轮停止，请稍后重试恢复")
				}
			}
		}
	} else {
		// 只恢复事实,不恢复会话(RFC §6):Engine 从 store 重算路由续跑。
		if !h.startEngine(nil) {
			return label, fmt.Errorf("Engine 正在完成上一轮停止，请稍后重试恢复")
		}
	}
	// lifecycle 由 startEngine / runEnded 管理,此处不再覆写——
	// 引擎立即结束(完本等)时覆写会把终态改回 running。
	return label, nil
}

// handleIntervention 用户干预的统一裁定路径:Collect → Decide → 执行。
// FIFO 串行(同一时刻至多一次在途咨询);answer/rules 即时执行,控制态动作
// (hold/reopen/dispatch)引擎运行中排队边界提交、停机时立即执行。
// restart=true(Continue 语义)时干预处理完确保引擎运行。
func (h *Host) handleIntervention(text string) {
	h.doIntervention(text, false)
}

func (h *Host) doIntervention(text string, restart bool) {
	h.interMu.Lock()
	defer h.interMu.Unlock()

	// 崩溃保护:裁定前先持久化(PendingSteer),成功应用或已当面回显失败后原子清除
	// (ClearHandledSteer 同时复位 FlowSteering)。裁定期间崩溃 → 下次 Resume 重放。
	if err := h.store.RunMeta.SetPendingSteer(text); err != nil {
		slog.Warn("干预持久化失败(继续裁定,但崩溃保护失效)", "module", "host", "err", err)
	}
	clearPending := func() {
		if err := h.store.ClearHandledSteer(); err != nil {
			slog.Warn("清除已处理干预失败", "module", "host", "err", err)
		}
	}

	facts := arbiter.CollectInterventionFacts(h.store)
	facts.Running = h.engine.isRunning()

	start := time.Now()
	decision, derr := runObservedDecision(h.observer, "用户干预裁定", func() (arbiter.InterventionDecision, error) {
		return arbiter.DecideIntervention(h.runCtx, h.arbiterModel(),
			h.bundle.Prompts.ArbiterIntervention, facts, text)
	})

	rec := storepkg.DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: text,
		Reason: decision.Reason, DurationMs: time.Since(start).Milliseconds()}
	if cp := h.store.Checkpoints.LatestGlobal(); cp != nil {
		rec.CheckpointSeq = cp.Seq
	}
	if data, err := json.Marshal(facts); err == nil {
		rec.Facts = data
	}
	if derr == nil {
		if data, err := json.Marshal(decision); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	if _, err := h.store.Decisions.Append(rec); err != nil {
		slog.Warn("裁定审计落盘失败", "module", "host", "err", err)
	}

	if derr != nil {
		// 宁可不动,不可误动:不产生任何写入。调用错误与
		// 输出校验错误共用同一 error 通道,必须原样回显,不得统一伪装成"未能理解"。
		// 已当面告知 → 清除 pending(否则下次 Resume 会自动重放同一条失败干预)。
		h.emitEvent(newInterventionFailureEvent(derr))
		clearPending()
		return
	}

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "裁定: " + decision.Reason, Level: "info"})
	if decision.Answer != "" {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: decision.Answer, Level: "info"})
	}
	// 任一动作持久化失败 → 保留 PendingSteer(恢复时整条重放重新裁定;
	// hold/reopen 幂等、dispatch 经新事实重询,重放安全)。
	actionsFailed := false
	if decision.Rules != "" {
		if snap, _, err := h.userRules.AddRuntimeRule(h.runCtx, decision.Rules); err != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "写作规则落盘失败: " + err.Error(), Level: "error"})
			actionsFailed = true
		} else if snap != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "写作规则已更新并持久化", Level: "info"})
		}
	}

	if decision.Hold != nil || decision.Reopen != nil || decision.Dispatch != nil {
		op := controlOp{hold: decision.Hold, reopen: decision.Reopen, dispatch: decision.Dispatch, text: text, facts: facts}
		if !h.engine.enqueue(op) {
			// 引擎未运行:立即执行;持久化失败 → 保留 PendingSteer,恢复时重放整条干预。
			if err := h.engine.applyControlOp(context.Background(), op); err != nil {
				h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
					Summary: "干预动作执行失败,已保留;恢复/继续时将自动重试"})
				return
			}
			// reopen/dispatch 表达了继续创作的意图,拉起引擎。
			if decision.Reopen != nil || decision.Dispatch != nil {
				restart = true
			}
		}
	}
	if actionsFailed {
		// 保留 PendingSteer:恢复/继续时整条重放重新裁定。
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: "部分干预动作未成功,干预已保留;恢复/继续时自动重试"})
		return
	}
	// 动作已成功应用/入队,清除崩溃保护(入队后引擎侧失败或退出竞态由 engine
	// 回存 PendingSteer 兜底)。
	clearPending()

	if restart && !h.engine.isRunning() {
		if err := h.budget.Refuse(); err != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: err.Error(), Level: "warn"})
			return
		}
		h.refreshWriterRestore()
		if !h.startEngine(nil) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
				Summary: "Engine 正在完成上一轮停止；干预已保存，请稍后继续"})
		}
	}
}

func newInterventionFailureEvent(err error) Event {
	detail := err.Error()
	return Event{
		Time:     time.Now(),
		Category: "ERROR",
		Agent:    "arbiter",
		Summary:  "干预裁定失败：" + detail + "（未做任何修改）",
		Detail:   detail,
		Kind:     errorKind(err, detail),
		Level:    "error",
	}
}

// arbiterModel 返回带用量追踪的裁定模型(token/成本进预算与 usage 系统)。
func (h *Host) arbiterModel() agentcore.ChatModel {
	return newUsageTrackedModel(h.models.Default, "arbiter", h.usage.Record)
}

// Continue 停机后用户在输入框输入时调用:干预裁定 + 确保引擎重新运行。
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
	if h.exclusive != "" {
		ex := h.exclusive
		h.mu.Unlock()
		// 独占作业期间必须在裁定前挡住：否则 Arbiter 已改 PendingSteer/规则/控制态，引擎才被门禁拦下。
		return fmt.Errorf("%s进行中，请先完成后再继续创作", ex)
	}
	h.mu.Unlock()
	if err := h.budget.Refuse(); err != nil {
		return err
	}

	h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[继续] " + text, Level: "info"})
	go h.doIntervention(text, true)
	return nil
}

// SetAdvanceMode 确定性切换章节推进模式。它只写入用户运行意图，
// 不调用 Arbiter，也不隐式启动已经暂停的 Engine。
func (h *Host) SetAdvanceMode(mode domain.ChapterAdvanceMode) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	if err := h.store.RunMeta.SetAdvanceMode(mode); err != nil {
		return err
	}
	label := "自动推进"
	if mode == domain.ChapterAdvanceReview {
		label = "逐章验收"
	}
	summary := "章节推进模式已切换为" + label
	h.mu.Lock()
	state := h.lifecycle
	h.mu.Unlock()
	if mode == domain.ChapterAdvanceAuto && state != lifecycleRunning && state != lifecycleCompleted {
		summary += "；当前仍暂停，输入继续指令后恢复运行"
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "info"})
	return nil
}

// AdvanceOneChapter 在逐章验收模式下授权一个精确章节并启动 Engine。
func (h *Host) AdvanceOneChapter() error {
	h.interMu.Lock()
	defer h.interMu.Unlock()

	h.mu.Lock()
	running, cocreating, ex := h.lifecycle == lifecycleRunning, h.cocreating, h.exclusive
	h.mu.Unlock()
	if running || h.engine.isRunning() {
		return fmt.Errorf("创作仍在运行或正在完成暂停，请稍后再执行 /next")
	}
	if cocreating {
		return fmt.Errorf("阶段共创进行中，请先结束共创")
	}
	if ex != "" {
		return fmt.Errorf("%s进行中，请先完成后再执行 /next", ex)
	}
	meta, err := h.store.RunMeta.Load()
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("RunMeta 未初始化")
	}
	if meta.AdvanceMode != domain.ChapterAdvanceReview {
		return fmt.Errorf("/next 仅用于逐章验收模式，请先执行 /review on")
	}
	if meta.AdvanceHold != nil {
		return fmt.Errorf("仍有一次性暂停意图待处理（%s），请先恢复或完成当前干预", meta.AdvanceHold.Reason)
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	progress, err := h.store.Progress.Load()
	if err != nil {
		return err
	}
	if progress == nil || progress.Phase != domain.PhaseWriting {
		phase := "<nil>"
		if progress != nil {
			phase = string(progress.Phase)
		}
		return fmt.Errorf("当前阶段不能授权新章（phase=%s）", phase)
	}
	target := progress.NextChapter()
	if target <= 0 {
		return fmt.Errorf("无法从当前进度推导下一章")
	}
	if err := h.store.RunMeta.GrantAdvancePermit(target); err != nil {
		return err
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
		Summary: fmt.Sprintf("已放行第 %d 章；该章提交后会先完成必要的评审与弧/卷结构维护，再次等待放行", target), Level: "info"})
	h.refreshWriterRestore()
	if !h.startEngine(nil) {
		// 许可按章节号持久化且同目标幂等，调用方稍后重试不会重复授权。
		return fmt.Errorf("章节许可已保存，但 Engine 仍在完成上一轮停止；请稍后重试 /next")
	}
	return nil
}

// Steer 提交用户干预(运行中随时可用;停机时裁定后视动作决定是否拉起引擎)。
func (h *Host) Steer(text string) {
	h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[用户干预] " + text, Level: "info"})
	go h.handleIntervention(text)
}

// Abort 暂停当前引擎循环。
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
	cancelExclusive := h.exclusiveCancel
	h.mu.Unlock()
	if running {
		// 置位必须在 engine.abort 之前：cancel 传播会立刻引发 stream init / worker
		// 失败事件，observer 凭此标志识别为 abort 衍生噪声并抑制。
		h.observer.setAborting(true)
		h.engine.abort()
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
		return true
	}
	// Engine 未运行但独占作业（导入等）在跑：它同样在烧钱，预算硬停/手动暂停必须
	// 能停掉它——否则预算政策对导入形同虚设（docs/import-pipeline.md §13.1）。
	if cancelExclusive != nil {
		cancelExclusive()
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
		return true
	}
	return false
}

// Close 终止引擎并关闭事件通道。
//
// Usage 持久化语义：先取消 autoSaveLoop（它自行 flush 最后一次 dirty 状态），
// 再补一次同步 SaveNow 收尾。终止后 in-flight LLM 调用的最末几百 token
// 丢失由下次启动时 session jsonl replay 自动补回。
func (h *Host) Close() {
	h.observer.setAborting(true)
	if h.runCancel != nil {
		h.runCancel() // 中断在途的宿主侧裁定调用
	}
	h.engine.abort()
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

// runEnded 引擎循环结束(任何原因)时由 engine.onDone 回调:按 store 事实定终态。
//   - Phase=Complete  → 标记 completed，发"创作完成"事件
//   - 其它            → 标记 idle/paused，发"创作停止"事件
func (h *Host) runEnded() {
	// 退出期 Close() 可能已 close(h.done)，末尾发送会 panic;recover 兜住竞态。
	defer func() { recover() }()
	h.observer.finalize()

	h.mu.Lock()
	progress, _ := h.store.Progress.Load()
	if progress != nil && progress.Phase == domain.PhaseComplete {
		h.lifecycle = lifecycleCompleted
		// 完本收尾:确定性生成(store 已有全部事实,不花 LLM 调用;RFC 末节)。
		summary := completionSummary(h.store)
		h.mu.Unlock()
		slog.Info(summary, "module", "host")
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "success"})
		h.notifier.Send(notify.Notification{
			Kind: notify.KindRunEnd, Level: "info", Title: "ainovel: 创作完成",
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
			summary := fmt.Sprintf("引擎停止 (已完成 %d 章)", completed)
			slog.Warn(summary, "module", "host")
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "warn"})
			h.notifier.Send(notify.Notification{
				Kind: notify.KindRunEnd, Level: "warn", Title: "ainovel: 创作停止",
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
		snap.AdvanceMode = string(meta.AdvanceMode)
		snap.AdvancePermitChapter = meta.AdvancePermitChapter
		if meta.AdvanceHold != nil {
			snap.HasAdvanceHold = true
			snap.AdvanceHoldReason = meta.AdvanceHold.Reason
		}
	}

	snap.Agents = h.observer.agentSnapshots()
	h.fillContextStatus(&snap)
	snap.StatusLabel = deriveStatusLabel(snap)

	// 恢复标签
	// 恢复标签
	if label, err := resumeLabel(h.store); err == nil && label != "" {
		snap.RecoveryLabel = label
	}

	h.fillDetails(&snap, progress)

	return snap
}

// fillContextStatus 填充上下文健康度信息。
// 主循环无常驻 LLM 上下文；Worker 的上下文健康度经进度中继
// (ProgressContext)进入 observer 的 per-agent 快照,由 Agents 面板展示。
// 汇总字段留空,面板按 per-agent 数据渲染。
func (h *Host) fillContextStatus(_ *UISnapshot) {}

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

	// 无常驻上下文需要联动:writer/architect/editor 的 ContextManager 走
	// ContextManagerFactory,下次 spawn 自动按新模型窗口重建。

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
var concreteThinkingRoles = []string{"architect", "writer", "editor"}

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
	return coCreateStream(ctx, h.models, h.store.Sessions, coCreateSystemPrompt, history, onProgress)
}

// StageCoCreateStream 阶段共创：在已写内容的基础上规划后续方向。
// 系统提示 = 阶段 prompt + 当前故事状态摘要，让助手知道"已经写了什么"。
func (h *Host) StageCoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, h.store.Sessions, stageSystemPrompt(h.store), history, onProgress)
}

// stagePlanPrefix 把共创产出的"后续方向 brief"包装成一条阶段规划干预，交 Arbiter 裁定。
// 只贴 [阶段规划] 事实标记 + 中性陈述，不写死"怎么落地"——具体路由（compass / architect /
// user_rules）交给 arbiter-intervention.md 的「阶段规划」判据，避免与 prompt 形成第二真相源、
// 也不堵死风格类要求走 user_rules（守"分类裁定归 LLM"）。Continue 再叠加 [用户干预] 前缀。
const stagePlanPrefix = "[阶段规划] 我暂停创作，和共创助手一起梳理了下面的后续方向，请按你的干预分类裁定如何落地，然后继续创作。后续方向如下：\n\n"

// PauseForCoCreate 进入阶段共创：置共创占用标记，运行中则一并暂停 Engine。
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

	// PauseForCoCreate 的 abort 是异步的:等引擎循环真正收敛再继续,回到与手动
	// 暂停后 Continue 一致的"真停机"前提。共创窗口是人机交互时间尺度,短轮询无感。
	for h.engine.isRunning() {
		time.Sleep(20 * time.Millisecond)
	}

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

// ImportFrom 启动一次外部小说语义编译导入：ingest → segment → analyze → synthesize → publish。
// 模型只裁定开放语义（边界/事实/综合），Go 掌管坐标/覆盖/幂等；与 Engine 运行互斥，
// 导入完成后由 AdvanceHold 决定是否续写。
// 返回的事件通道由 imp.Run 关闭，调用方负责消费（满则丢弃以防阻塞管线协程）。
func (h *Host) ImportFrom(ctx context.Context, opts imp.Options) (<-chan imp.Event, error) {
	// 预算启动前置检查与 Start/Resume/Continue 同一纪律：导入是全流程模型调用，
	// 预算已超时不得启动（§13.1「纳入现有预算哨兵」）。
	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := h.acquireExclusive("导入"); err != nil {
		return nil, err
	}
	// 登记取消函数：预算硬停/手动暂停经 abortWithEvent 取消导入自己的 context
	//（否则哨兵只会去暂停并未运行的 Engine，导入继续烧钱）。
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()

	deps := imp.Deps{
		Store:         h.store,
		CommitChapter: tools.NewCommitChapterTool(h.store),
		Segment:       h.importCaller("segment"),
		Analyze:       h.importCaller("analyze"),
		Synthesize:    h.importCaller("synthesize"),
		Prompts: imp.Prompts{
			Segment:    h.bundle.Prompts.ImportSegment,
			Analyze:    h.bundle.Prompts.ImportAnalyze,
			Synthesize: h.bundle.Prompts.ImportSynthesize,
			Range:      h.bundle.Prompts.ImportRange,
		},
	}
	ch, err := imp.Run(ctx, deps, opts)
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return h.superviseImport(ch, opts), nil
}

// ImportResumeHint 返回未完成导入的一行提示（无则空串），供 TUI 启动时主动告知（RFC §18.2）。
// 只在启动时调用一次：内部会重算工作区各工件的 InputDigest，不适合放进快照轮询。
func (h *Host) ImportResumeHint() string {
	return imp.ResumeSummary(h.store)
}

// importCaller 解析一个导入语义函数的模型档位（RFC §13.1）：roles 配置存在 import_<fn>
// 则用该档位（用量也记该角色的账），否则落 architect。这是调用配置，不改任何语义契约。
func (h *Host) importCaller(fn string) imp.Caller {
	role := "import_" + fn
	if _, _, explicit := h.models.CurrentSelection(role); !explicit {
		role = "architect"
	}
	model := newUsageTrackedModel(h.models.ForRole(role), role, h.usage.Record)
	return imp.Caller{Model: model, Runtime: h.importModelRuntime(role, model)}
}

// importModelRuntime 探测所选档位角色模型的调用能力，供 imp 双预算 / thinking 自适应使用（RFC §13/§21）。
// 探测失败的字段留零值，imp 侧回退保守默认，保证无能力信息也能正确运行。
// 不探测结构化输出能力：litellm 能力表是 provider 级，response_format 支持是模型级事实，
// 按 provider 级发送对不支持的模型是硬 400（见 imp/call.go callProfile 注释）。
// TODO(json-schema)：与全仓其它调用点统一改造 JSON Schema 模式时，在此按模型级核验能力。
func (h *Host) importModelRuntime(role string, model agentcore.ChatModel) imp.ModelRuntime {
	var rt imp.ModelRuntime
	_, name, _ := h.models.CurrentSelection(role)
	if name == "" {
		name = bootstrap.ModelName(model)
	}
	// context / completion 上限：registry 是唯一可信来源（被包装模型的 Info() 不含窗口）。
	rt.ContextTokens, _ = h.cfg.ResolveContextWindow(name)
	if entry, ok := modelreg.DefaultRegistry().Resolve(name); ok {
		rt.MaxOutputTokens = entry.MaxTokens
	}
	// thinking：按角色 reasoning effort 与模型能力 resolve；不支持则不发（与 arbiter 同策略）。
	if level, err := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(role)); err == nil {
		if resolved, ok := agents.ResolveThinkingForModel(model, level); ok {
			rt.Thinking = resolved
		}
	}
	return rt
}

// Simulate 读取 simulate 目录并生成或增量更新仿写画像。
func (h *Host) Simulate(ctx context.Context) (<-chan sim.Event, error) {
	if err := h.acquireExclusive("生成仿写画像"); err != nil {
		return nil, err
	}

	wd, err := os.Getwd()
	if err != nil {
		h.releaseExclusive()
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
	ch, err := sim.Run(ctx, deps, sim.Options{SourceDir: filepath.Join(wd, "simulate")})
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return superviseExclusive(h, ch), nil
}

// ImportSimulationProfile 导入此前生成的仿写画像。
func (h *Host) ImportSimulationProfile(ctx context.Context, path string) (<-chan sim.Event, error) {
	if err := h.acquireExclusive("导入仿写画像"); err != nil {
		return nil, err
	}
	ch, err := sim.RunImport(ctx, h.store, path)
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return superviseExclusive(h, ch), nil
}

// acquireExclusive 原子占用后台独占作业槽（import/simulate）：Engine 运行中、阶段共创窗口内、
// 或已有独占作业在跑时拒绝。成功即登记占用，作业结束须调 releaseExclusive 释放——否则两个导入
// 或导入+仿写会并发抢改同一状态。补上此前只查 ==running/cocreating、不登记作业本身的缺口。
func (h *Host) acquireExclusive(action string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	// engine.isRunning() 必查：Abort 先置 lifecycle=paused 再异步等 goroutine 退出，
	// 该窗口内 lifecycle 已非 running 但引擎仍可能在写 store（与启动门禁同一纪律）。
	case h.lifecycle == lifecycleRunning || h.engine.isRunning():
		return fmt.Errorf("创作引擎运行中或正在停止，请稍候再%s", action)
	case h.cocreating:
		return fmt.Errorf("阶段共创进行中，请先结束共创后再%s", action)
	case h.exclusive != "":
		return fmt.Errorf("%s进行中，请先完成后再%s", h.exclusive, action)
	}
	h.exclusive = action
	return nil
}

// releaseExclusive 释放后台独占作业槽（连同已登记的取消函数）。
func (h *Host) releaseExclusive() {
	h.mu.Lock()
	cancel := h.exclusiveCancel
	h.exclusive = ""
	h.exclusiveCancel = nil
	h.mu.Unlock()
	if cancel != nil {
		cancel() // 作业已结束：释放派生 context；对已退出的 runner 无副作用
	}
}

// superviseExclusive 转发独占作业事件，通道关闭（作业结束）时释放占用槽。
func superviseExclusive[T any](h *Host, src <-chan T) <-chan T {
	out := make(chan T, 32)
	go func() {
		defer close(out)
		defer h.releaseExclusive()
		for ev := range src {
			out <- ev
		}
	}()
	return out
}

// superviseImport 是"导入完成后是否接力"的唯一所有者：转发导入事件，成功完成时先释放独占槽、
// 再决定并执行接力，最后把真实接力结果写进 StageDone 事件的 Continued 字段。TUI 只据此渲染，
// 不再用本地 --continue 标志臆测运行态（消除 Runner/Host/TUI 三方各自解释导致的时序竞态）。
func (h *Host) superviseImport(src <-chan imp.Event, opts imp.Options) <-chan imp.Event {
	out := make(chan imp.Event, 32)
	go func() {
		defer close(out)
		released := false
		release := func() {
			if !released {
				released = true
				h.releaseExclusive()
			}
		}
		defer release()
		for ev := range src {
			if ev.Stage == imp.StageDone {
				release() // 先释放独占槽，接力的 startEngine 才能通过独占门禁
				ev.Continued = h.continueAfterImport(opts)
			}
			out <- ev
		}
	}()
	return out
}

// continueAfterImport 决定并执行 --continue 的真正自动接力，返回 Engine 是否已启动。
// 有效接力意图 = 本次 opts 或工作区持久化 intent（覆盖崩溃后无参数 /import 恢复的场景）；
// 仅 auto 推进模式接力，由自适应扩弧规划承接开放故事、或让已完结故事收尾；review 交用户 /next。
func (h *Host) continueAfterImport(opts imp.Options) bool {
	want := opts.ContinueAfter
	if !want {
		if in, err := imp.OpenWorkspace(h.store.Dir()).LoadIntent(); err == nil && in != nil {
			want = in.ContinueAfterImport
		}
	}
	if !want {
		return false
	}
	meta, err := h.store.RunMeta.Load()
	if err != nil || meta == nil {
		slog.Warn("导入自动接力读取 RunMeta 失败", "module", "host", "err", err)
		return false
	}
	if meta.AdvanceMode != domain.ChapterAdvanceAuto {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info",
			Summary: "导入完成；当前为逐章验收模式，输入继续或 /next 接力续写"})
		return false
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info", Summary: "导入完成，自动接力续写"})
	if !h.startEngine(nil) {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: "自动接力启动失败，请输入继续指令手动恢复"})
		return false
	}
	return true
}

// Export 导出已完成章节为外部文件（当前仅支持 TXT）。
//
// 与 ImportFrom 不同：导出是只读操作（不动 Progress / Checkpoint），
// 因此**不要求 Engine 停机**——写作中途也可以随时导出"现阶段成品"。
// 只读到 Progress.CompletedChapters + 章节终稿 + 大纲 + premise 的一致快照。
func (h *Host) Export(ctx context.Context, opts exp.Options) (*exp.Result, error) {
	return exp.Run(ctx, exp.Deps{Store: h.store}, opts)
}
