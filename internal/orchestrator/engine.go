package orchestrator

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// Engine 是独立于 UI 的会话执行内核。
// 它负责模型装配后的主循环、事件流、恢复、干预与生命周期管理。
type Engine struct {
	cfg         bootstrap.Config
	models      *bootstrap.ModelSet
	store       *storepkg.Store
	taskRT      *novelTaskRuntime
	scheduler   *taskScheduler
	agents      *agentBoard
	coordinator *agentcore.Agent
	session     *session
	askUser     *tools.AskUserTool
	events      chan UIEvent
	streamCh    chan string
	clearCh     chan struct{}
	done        chan struct{}
	mu          sync.Mutex
	controlMu   sync.Mutex
	running     bool
	closeOnce   sync.Once
}

const coordinatorRuntimeOwner = "runtime"

// NewEngine 创建与 UI 无关的会话执行内核。
func NewEngine(cfg bootstrap.Config, bundle assets.Bundle) (*Engine, error) {
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return nil, err
	}
	slog.Info("启动", "module", "boot", "provider", cfg.Provider, "model", cfg.ModelName, "output", cfg.OutputDir)

	store := storepkg.NewStore(cfg.OutputDir)
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}
	taskRT, err := newNovelTaskRuntime(store)
	if err != nil {
		return nil, fmt.Errorf("init task runtime: %w", err)
	}

	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return nil, fmt.Errorf("create models: %w", err)
	}
	slog.Info("模型就绪", "module", "boot", "summary", models.Summary())

	var compactEmit emitFn
	coordinator, askUser := BuildCoordinator(cfg, store, models, bundle, func(ev UIEvent) {
		if compactEmit != nil {
			compactEmit(ev)
		}
	})

	store.Signals.ClearStaleSignals()

	eng := &Engine{
		cfg:         cfg,
		models:      models,
		store:       store,
		taskRT:      taskRT,
		scheduler:   newTaskScheduler(taskRT),
		agents:      newAgentBoard(),
		coordinator: coordinator,
		askUser:     askUser,
		events:      make(chan UIEvent, 100),
		streamCh:    make(chan string, 256),
		clearCh:     make(chan struct{}, 4),
		done:        make(chan struct{}, 4),
	}
	compactEmit = eng.emit
	eng.session = newSession(coordinator, store, taskRT, eng.agents, cfg.Provider, eng.emit, eng.emitDelta, eng.emitClear, eng.submitControl)
	eng.session.bind()

	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		slog.Error("初始化运行元信息失败", "module", "boot", "err", err)
	}

	return eng, nil
}

// Dir 返回当前运行时的输出目录。
func (eng *Engine) Dir() string {
	return eng.store.Dir()
}

// AskUser 返回 ask_user 工具实例，供调用方注入交互 handler。
func (eng *Engine) AskUser() *tools.AskUserTool {
	return eng.askUser
}

// Stream 返回只读流式 token 通道。
func (eng *Engine) Stream() <-chan string {
	return eng.streamCh
}

// StreamClear 返回只读流式清空信号通道。
func (eng *Engine) StreamClear() <-chan struct{} {
	return eng.clearCh
}

func (eng *Engine) emitClear() {
	defer func() { recover() }()
	select {
	case eng.clearCh <- struct{}{}:
	default:
	}
}

func (eng *Engine) emitDelta(delta string) {
	defer func() { recover() }()
	select {
	case eng.streamCh <- delta:
	default:
		select {
		case <-eng.streamCh:
		default:
		}
		select {
		case eng.streamCh <- delta:
		default:
		}
	}
}

func (eng *Engine) emit(ev UIEvent) {
	if eng.store != nil && eng.store.Runtime != nil {
		priority := domain.RuntimePriorityBackground
		switch ev.Category {
		case "SYSTEM", "ERROR":
			priority = domain.RuntimePriorityControl
		}
		_, _ = eng.store.Runtime.AppendQueue(domain.RuntimeQueueItem{
			Time:     ev.Time,
			Kind:     domain.RuntimeQueueUIEvent,
			Priority: priority,
			Category: ev.Category,
			Summary:  ev.Summary,
			Payload:  ev,
		})
	}
	defer func() { recover() }()
	select {
	case eng.events <- ev:
	default:
		select {
		case <-eng.events:
		default:
		}
		select {
		case eng.events <- ev:
		default:
		}
	}
}

// Start 新建模式：初始化进度并启动 coordinator。
func (eng *Engine) Start(prompt string) error {
	return eng.StartPrepared(BuildStartPrompt(prompt))
}

// StartPrepared 使用已编排完成的启动 prompt 开始创作。
func (eng *Engine) StartPrepared(promptText string) error {
	eng.mu.Lock()
	if eng.running {
		eng.mu.Unlock()
		return fmt.Errorf("already running")
	}
	eng.mu.Unlock()

	promptText = strings.TrimSpace(promptText)
	if promptText == "" {
		return fmt.Errorf("prompt is required")
	}
	if eng.store != nil && eng.store.Runtime != nil {
		if err := eng.store.Runtime.Reset(); err != nil {
			return fmt.Errorf("reset runtime queue: %w", err)
		}
	}
	if err := eng.taskRT.Reset(); err != nil {
		return fmt.Errorf("reset tasks: %w", err)
	}
	eng.agents.ResetAll("待命")
	if err := eng.scheduler.SeedStartup(promptText); err != nil {
		return fmt.Errorf("seed foundation task: %w", err)
	}
	if err := eng.store.Progress.Init("", 0); err != nil {
		return fmt.Errorf("init progress: %w", err)
	}

	if err := eng.coordinator.Prompt(promptText); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	if _, err := eng.taskRT.Start(domain.TaskCoordinatorDecision, coordinatorRuntimeOwner, "协调整体创作", promptText, taskLocation{}); err != nil {
		return fmt.Errorf("start coordinator task: %w", err)
	}
	eng.agents.Start("coordinator", "", domain.TaskCoordinatorDecision, "正在协调整体创作")

	eng.mu.Lock()
	eng.running = true
	eng.mu.Unlock()

	go eng.waitDone()
	return nil
}

// Resume 恢复模式：根据 Progress/RunMeta 自动判断恢复类型并启动。
// 返回恢复标签（空字符串表示无法恢复，应走新建模式）。
func (eng *Engine) Resume() (string, error) {
	eng.mu.Lock()
	if eng.running {
		eng.mu.Unlock()
		return "", fmt.Errorf("already running")
	}
	eng.mu.Unlock()

	recovery := eng.session.recovery()
	if recovery.IsNew {
		return "", nil
	}
	progress, _ := eng.store.Progress.Load()
	runMeta, _ := eng.store.RunMeta.Load()
	if err := eng.scheduler.SeedRecovery(progress, runMeta); err != nil {
		return "", err
	}
	intent := domain.ControlIntent{
		Kind:      domain.ControlIntentResumePrompt,
		Priority:  domain.RuntimePriorityControl,
		Summary:   "恢复创作任务",
		Prompt:    recovery.PromptText,
		TaskKind:  domain.TaskCoordinatorDecision,
		TaskTitle: "恢复创作任务",
		TaskInput: recovery.PromptText,
		Payload: map[string]string{
			"label": recovery.Label,
		},
	}
	if _, err := eng.prepareResumeControl(intent, recovery); err != nil {
		return "", err
	}
	if err := eng.drainControlQueue(); err != nil {
		return "", err
	}
	return recovery.Label, nil
}

// Continue 使用指定 prompt 继续驱动 coordinator，适合无界面场景的后续动作。
func (eng *Engine) Continue(promptText string) error {
	eng.mu.Lock()
	if eng.running {
		eng.mu.Unlock()
		return fmt.Errorf("already running")
	}
	eng.mu.Unlock()

	promptText = strings.TrimSpace(promptText)
	if promptText == "" {
		return fmt.Errorf("prompt is required")
	}
	if _, err := eng.enqueueControl(domain.ControlIntent{
		Kind:      domain.ControlIntentResumePrompt,
		Priority:  domain.RuntimePriorityControl,
		Summary:   "继续协调小说任务",
		Prompt:    promptText,
		TaskKind:  domain.TaskCoordinatorDecision,
		TaskTitle: "继续协调小说任务",
		TaskInput: promptText,
	}); err != nil {
		return err
	}
	return eng.drainControlQueue()
}

// Abort 停止当前 coordinator 运行。
func (eng *Engine) Abort() bool {
	eng.mu.Lock()
	running := eng.running
	eng.mu.Unlock()
	if !running {
		return false
	}

	eng.coordinator.Abort()
	_ = eng.taskRT.CancelActive(coordinatorRuntimeOwner, "已暂停")
	eng.agents.Fail("coordinator", "已暂停")
	eng.emit(UIEvent{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  "用户手动暂停当前创作",
		Level:    "warn",
	})
	return true
}

// Steer 提交用户干预。
func (eng *Engine) Steer(text string) {
	eng.mu.Lock()
	wasRunning := eng.running
	eng.mu.Unlock()

	if wasRunning {
		eng.session.persistSteer(text)
		if _, err := eng.enqueueControl(domain.ControlIntent{
			Kind:      domain.ControlIntentSteerMessage,
			Priority:  domain.RuntimePriorityInterrupt,
			Summary:   "处理用户干预",
			Message:   text,
			TaskKind:  domain.TaskSteerApply,
			TaskTitle: "处理用户干预",
			TaskInput: text,
		}); err != nil {
			eng.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: "干预入队失败: " + err.Error(), Level: "error"})
			return
		}
		if err := eng.drainControlQueue(); err != nil {
			eng.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: "干预处理失败: " + err.Error(), Level: "error"})
			return
		}
	} else {
		eng.session.persistSteer(text)
		recovery := eng.session.recovery()
		promptText := recovery.PromptText
		if recovery.IsNew {
			promptText = fmt.Sprintf("[用户干预] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。", text)
		}
		slog.Info("agent 已停止，通过 Prompt 重启", "module", "steer", "recovery", recovery.Label)
		if _, err := eng.enqueueControl(domain.ControlIntent{
			Kind:      domain.ControlIntentResumePrompt,
			Priority:  domain.RuntimePriorityInterrupt,
			Summary:   "恢复并处理用户干预",
			Prompt:    promptText,
			TaskKind:  domain.TaskSteerApply,
			TaskTitle: "处理用户干预",
			TaskInput: text,
			Payload: map[string]string{
				"steer": text,
			},
		}); err != nil {
			eng.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: "恢复入队失败: " + err.Error(), Level: "error"})
			return
		}
		if err := eng.drainControlQueue(); err != nil {
			eng.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: "恢复失败: " + err.Error(), Level: "error"})
			return
		}
	}

	eng.emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: "干预已提交: " + truncateLog(text, 40), Level: "info"})
}

// Events 返回只读事件通道。
func (eng *Engine) Events() <-chan UIEvent {
	return eng.events
}

// ReplayQueue 返回指定序号之后的运行时队列项。
func (eng *Engine) ReplayQueue(afterSeq int64) ([]domain.RuntimeQueueItem, error) {
	if eng.store == nil || eng.store.Runtime == nil {
		return nil, nil
	}
	return eng.store.Runtime.LoadQueueAfter(afterSeq)
}

func (eng *Engine) enqueueControl(intent domain.ControlIntent) (domain.ControlIntent, error) {
	if eng.store == nil || eng.store.Runtime == nil {
		return intent, nil
	}
	queued, err := eng.store.Runtime.EnqueueControl(intent)
	if err != nil {
		return intent, err
	}
	_, _ = eng.store.Runtime.AppendQueue(domain.RuntimeQueueItem{
		Time:     time.Now(),
		Kind:     domain.RuntimeQueueControl,
		Priority: queued.Priority,
		Summary:  queued.Summary,
		Payload:  queued,
	})
	return queued, nil
}

func (eng *Engine) prepareResumeControl(intent domain.ControlIntent, recovery recoveryResult) (domain.ControlIntent, error) {
	if eng.store == nil || eng.store.Runtime == nil {
		return intent, nil
	}
	var dropKinds []domain.ControlIntentKind
	if recovery.ConsumesPendingSteer {
		dropKinds = append(dropKinds, domain.ControlIntentSteerMessage)
	}
	queued, err := eng.store.Runtime.PrependResumeControl(intent, dropKinds...)
	if err != nil {
		return intent, err
	}
	_, _ = eng.store.Runtime.AppendQueue(domain.RuntimeQueueItem{
		Time:     time.Now(),
		Kind:     domain.RuntimeQueueControl,
		Priority: queued.Priority,
		Summary:  queued.Summary,
		Payload:  queued,
	})
	return queued, nil
}

func (eng *Engine) submitControl(intent domain.ControlIntent) error {
	if _, err := eng.enqueueControl(intent); err != nil {
		return err
	}
	go func() {
		if err := eng.drainControlQueue(); err != nil {
			eng.emit(UIEvent{
				Time:     time.Now(),
				Category: "ERROR",
				Summary:  "控制队列处理失败: " + err.Error(),
				Level:    "error",
			})
		}
	}()
	return nil
}

func (eng *Engine) drainControlQueue() error {
	eng.controlMu.Lock()
	defer eng.controlMu.Unlock()

	for {
		if eng.store == nil || eng.store.Runtime == nil {
			return nil
		}
		intent, err := eng.store.Runtime.PeekControl()
		if err != nil {
			return err
		}
		if intent == nil {
			return nil
		}
		if err := eng.applyControlIntent(*intent); err != nil {
			return err
		}
		if err := eng.store.Runtime.DequeueControl(intent.ID); err != nil {
			return err
		}
	}
}

func (eng *Engine) applyControlIntent(intent domain.ControlIntent) error {
	switch intent.Kind {
	case domain.ControlIntentResumePrompt:
		if err := eng.coordinator.Prompt(intent.Prompt); err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
		if _, err := eng.taskRT.Start(intent.TaskKind, coordinatorRuntimeOwner, intent.TaskTitle, intent.TaskInput, taskLocation{}); err != nil {
			return fmt.Errorf("start coordinator task: %w", err)
		}
		eng.agents.Start("coordinator", "", intent.TaskKind, "正在"+intent.TaskTitle)
		eng.mu.Lock()
		wasRunning := eng.running
		eng.running = true
		eng.mu.Unlock()
		if !wasRunning {
			go eng.waitDone()
		}
		return nil
	case domain.ControlIntentSteerMessage:
		eng.session.dispatchSteer(intent.Message)
		return nil
	case domain.ControlIntentFollowUp:
		eng.coordinator.FollowUp(agentcore.UserMsg(intent.Message))
		return nil
	default:
		return fmt.Errorf("unknown control intent: %s", intent.Kind)
	}
}

// Done 返回完成信号通道。
func (eng *Engine) Done() <-chan struct{} {
	return eng.done
}

// Close 终止 coordinator 并关闭事件通道。
func (eng *Engine) Close() {
	eng.coordinator.AbortSilent()
	eng.session.finalizeSteerIfIdle()
	eng.closeOnce.Do(func() {
		close(eng.done)
		close(eng.events)
		close(eng.streamCh)
		close(eng.clearCh)
	})
}

func (eng *Engine) waitDone() {
	eng.coordinator.WaitForIdle()
	eng.session.finalizeSteerIfIdle()
	_ = eng.taskRT.CompleteActive(coordinatorRuntimeOwner)
	eng.mu.Lock()
	eng.running = false
	eng.mu.Unlock()
	eng.agents.Idle("coordinator", "待命")
	select {
	case eng.done <- struct{}{}:
	default:
	}
}
