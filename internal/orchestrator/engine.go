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
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// Engine 是独立于 UI 的会话执行内核。
// 它负责模型装配后的主循环、事件流、恢复、干预与生命周期管理。
type Engine struct {
	cfg         bootstrap.Config
	models      *bootstrap.ModelSet
	store       *storepkg.Store
	coordinator *agentcore.Agent
	session     *session
	askUser     *tools.AskUserTool
	events      chan UIEvent
	streamCh    chan string
	clearCh     chan struct{}
	done        chan struct{}
	mu          sync.Mutex
	running     bool
	closeOnce   sync.Once
}

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
		coordinator: coordinator,
		askUser:     askUser,
		events:      make(chan UIEvent, 100),
		streamCh:    make(chan string, 256),
		clearCh:     make(chan struct{}, 4),
		done:        make(chan struct{}, 4),
	}
	compactEmit = eng.emit
	eng.session = newSession(coordinator, store, cfg.Provider, eng.emit, eng.emitDelta, eng.emitClear)
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
	eng.mu.Lock()
	if eng.running {
		eng.mu.Unlock()
		return fmt.Errorf("already running")
	}
	eng.mu.Unlock()

	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := eng.store.Progress.Init(eng.cfg.NovelName, 0); err != nil {
		return fmt.Errorf("init progress: %w", err)
	}

	promptText := buildStartPrompt(prompt)
	if err := eng.coordinator.Prompt(promptText); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}

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
	if err := eng.coordinator.Prompt(recovery.PromptText); err != nil {
		return "", fmt.Errorf("prompt: %w", err)
	}

	eng.mu.Lock()
	eng.running = true
	eng.mu.Unlock()

	go eng.waitDone()
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
	if err := eng.coordinator.Prompt(promptText); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}

	eng.mu.Lock()
	eng.running = true
	eng.mu.Unlock()

	go eng.waitDone()
	return nil
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
		eng.session.submitSteer(text)
	} else {
		eng.session.persistSteer(text)
		recovery := eng.session.recovery()
		promptText := recovery.PromptText
		if recovery.IsNew {
			promptText = fmt.Sprintf("[用户干预] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。", text)
		}
		slog.Info("agent 已停止，通过 Prompt 重启", "module", "steer", "recovery", recovery.Label)
		if err := eng.coordinator.Prompt(promptText); err != nil {
			slog.Error("重启 Prompt 失败", "module", "steer", "err", err)
			eng.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: "恢复失败: " + err.Error(), Level: "error"})
			return
		}
	}

	eng.emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: "干预已提交: " + truncateLog(text, 40), Level: "info"})

	eng.mu.Lock()
	if !eng.running {
		eng.running = true
		go eng.waitDone()
	}
	eng.mu.Unlock()
}

// Events 返回只读事件通道。
func (eng *Engine) Events() <-chan UIEvent {
	return eng.events
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
	eng.mu.Lock()
	eng.running = false
	eng.mu.Unlock()
	select {
	case eng.done <- struct{}{}:
	default:
	}
}
