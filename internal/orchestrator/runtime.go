package orchestrator

import (
	"errors"
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
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// UIEvent 是 TUI 消费的结构化事件。
type UIEvent struct {
	Time     time.Time
	Category string // TOOL / SYSTEM / REVIEW / CHECK / ERROR / AGENT
	Summary  string
	Level    string // info / warn / error / success
}

// UISnapshot 是 TUI 渲染所需的聚合状态快照。
type UISnapshot struct {
	Provider          string
	NovelName         string
	ModelName         string
	Style             string
	StatusLabel       string // READY / RUNNING / REVIEW / REWRITE / COMPLETE / ERROR
	Phase             string
	Flow              string
	CurrentChapter    int
	TotalChapters     int
	CompletedCount    int
	TotalWordCount    int
	InProgressChapter int
	PendingRewrites   []int
	RewriteReason     string
	PendingSteer      string
	RecoveryLabel     string // 恢复类型描述，空表示新建
	IsRunning         bool

	// 基础设定
	Premise          string            // 前提概要
	Outline          []OutlineSnapshot // 大纲（每章标题 + 核心事件）
	Characters       []string          // 角色列表（名字 + 身份）
	Layered          bool              // 是否分层模式（滚动规划）
	CurrentVolumeArc string            // 当前卷弧位置（如"第1卷·第1弧"）
	NextVolumeTitle  string            // 下一卷预览标题（如有）
	CompassDirection string            // 终局方向
	CompassScale     string            // 预估规模

	// 详情区
	LastCommitSummary  string
	LastReviewSummary  string
	LastCheckpointName string
	RecentSummaries    []string
}

// OutlineSnapshot 是大纲条目的展示摘要。
type OutlineSnapshot struct {
	Chapter   int
	Title     string
	CoreEvent string
}

// Runtime 封装协调器生命周期，提供 TUI 所需的非阻塞接口。
type Runtime struct {
	cfg         bootstrap.Config
	models      *bootstrap.ModelSet
	store       *storepkg.Store
	coordinator *agentcore.Agent
	session     *session
	askUser     *tools.AskUserTool
	events      chan UIEvent
	streamCh    chan string   // 流式 token channel（独立于 events，避免淹没事件日志）
	clearCh     chan struct{} // 流式缓冲清空信号
	done        chan struct{}
	mu          sync.Mutex
	running     bool
	closeOnce   sync.Once
	doneOnce    sync.Once
}

// Dir 返回当前运行时的输出目录。
func (rt *Runtime) Dir() string {
	return rt.store.Dir()
}

// AskUser 返回 ask_user 工具实例，供 TUI 注入交互 handler。
func (rt *Runtime) AskUser() *tools.AskUserTool {
	return rt.askUser
}

// NewRuntime 创建 Runtime：初始化 store/model/coordinator，注册事件订阅，但不启动 Prompt。
func NewRuntime(cfg bootstrap.Config, bundle assets.Bundle) (*Runtime, error) {
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

	// 压缩回调需要 emit，但 rt 尚未创建，用闭包延迟绑定
	var compactEmit emitFn
	coordinator, askUser := BuildCoordinator(cfg, store, models, bundle, func(ev UIEvent) {
		if compactEmit != nil {
			compactEmit(ev)
		}
	})

	// 清理上次崩溃可能遗留的信号文件
	store.Signals.ClearStaleSignals()

	rt := &Runtime{
		cfg:         cfg,
		models:      models,
		store:       store,
		coordinator: coordinator,
		askUser:     askUser,
		events:      make(chan UIEvent, 100),
		streamCh:    make(chan string, 256),
		clearCh:     make(chan struct{}, 4),
		done:        make(chan struct{}),
	}
	compactEmit = rt.emit
	rt.session = newSession(coordinator, store, cfg.Provider, rt.emit, rt.emitDelta, rt.emitClear)

	// 注册事件订阅：确定性控制 + UIEvent 转发 + 流式 delta 转发
	rt.session.bind()

	// 初始化运行元信息
	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		slog.Error("初始化运行元信息失败", "module", "boot", "err", err)
	}

	return rt, nil
}

// Stream 返回只读流式 token 通道。
func (rt *Runtime) Stream() <-chan string {
	return rt.streamCh
}

// StreamClear 返回只读流式清空信号通道。
func (rt *Runtime) StreamClear() <-chan struct{} {
	return rt.clearCh
}

// emitClear 发送流式缓冲清空信号，非阻塞。
func (rt *Runtime) emitClear() {
	defer func() { recover() }()
	select {
	case rt.clearCh <- struct{}{}:
	default:
	}
}

// emitDelta 向流式通道发送 token，非阻塞（满时丢弃旧数据）。
func (rt *Runtime) emitDelta(delta string) {
	defer func() { recover() }()
	select {
	case rt.streamCh <- delta:
	default:
		// 满了就丢弃最旧的再写入
		select {
		case <-rt.streamCh:
		default:
		}
		select {
		case rt.streamCh <- delta:
		default:
		}
	}
}

// emit 向事件通道发送事件，非阻塞（满时丢弃最旧事件）。
func (rt *Runtime) emit(ev UIEvent) {
	defer func() { recover() }() // 防止 channel 关闭后写入 panic
	select {
	case rt.events <- ev:
	default:
		select {
		case <-rt.events:
		default:
		}
		select {
		case rt.events <- ev:
		default:
		}
	}
}

// Start 新建模式：初始化进度并启动 coordinator。
func (rt *Runtime) Start(prompt string) error {
	rt.mu.Lock()
	if rt.running {
		rt.mu.Unlock()
		return fmt.Errorf("already running")
	}
	rt.mu.Unlock()

	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := rt.store.Progress.Init(rt.cfg.NovelName, 0); err != nil {
		return fmt.Errorf("init progress: %w", err)
	}

	promptText := buildStartPrompt(prompt)
	if err := rt.coordinator.Prompt(promptText); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}

	rt.mu.Lock()
	rt.running = true
	rt.done = make(chan struct{})
	rt.doneOnce = sync.Once{}
	rt.mu.Unlock()

	go rt.waitDone()
	return nil
}

// Resume 恢复模式：根据 Progress/RunMeta 自动判断恢复类型并启动。
// 返回恢复标签（空字符串表示无法恢复，应走新建模式）。
func (rt *Runtime) Resume() (string, error) {
	rt.mu.Lock()
	if rt.running {
		rt.mu.Unlock()
		return "", fmt.Errorf("already running")
	}
	rt.mu.Unlock()

	recovery := rt.session.recovery()

	if recovery.IsNew {
		return "", nil
	}

	if err := rt.coordinator.Prompt(recovery.PromptText); err != nil {
		return "", fmt.Errorf("prompt: %w", err)
	}

	rt.mu.Lock()
	rt.running = true
	rt.done = make(chan struct{})
	rt.doneOnce = sync.Once{}
	rt.mu.Unlock()

	go rt.waitDone()
	return recovery.Label, nil
}

// Continue 使用指定 prompt 继续驱动 coordinator，适合无界面场景的后续动作。
func (rt *Runtime) Continue(promptText string) error {
	rt.mu.Lock()
	if rt.running {
		rt.mu.Unlock()
		return fmt.Errorf("already running")
	}
	rt.mu.Unlock()

	promptText = strings.TrimSpace(promptText)
	if promptText == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := rt.coordinator.Prompt(promptText); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}

	rt.mu.Lock()
	rt.running = true
	rt.done = make(chan struct{})
	rt.doneOnce = sync.Once{}
	rt.mu.Unlock()

	go rt.waitDone()
	return nil
}

// Steer 提交用户干预。
// 如果 coordinator 正在运行，通过 Steer 注入消息。
// 如果 coordinator 已停止（出错），通过 Prompt 重启 agent 循环。
func (rt *Runtime) Steer(text string) {
	rt.mu.Lock()
	wasRunning := rt.running
	rt.mu.Unlock()

	if wasRunning {
		rt.session.submitSteer(text)
	} else {
		// agent 循环已停止，持久化干预后通过 Prompt 重启
		rt.session.persistSteer(text)
		recovery := rt.session.recovery()
		promptText := recovery.PromptText
		if recovery.IsNew {
			promptText = fmt.Sprintf("[用户干预] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。", text)
		}
		slog.Info("agent 已停止，通过 Prompt 重启", "module", "steer", "recovery", recovery.Label)
		if err := rt.coordinator.Prompt(promptText); err != nil {
			slog.Error("重启 Prompt 失败", "module", "steer", "err", err)
			rt.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: "恢复失败: " + err.Error(), Level: "error"})
			return
		}
	}

	rt.emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: "干预已提交: " + truncateLog(text, 40), Level: "info"})

	rt.mu.Lock()
	if !rt.running {
		rt.running = true
		rt.done = make(chan struct{})
		rt.doneOnce = sync.Once{}
		go rt.waitDone()
	}
	rt.mu.Unlock()
}

// Snapshot 读取 store 聚合为状态快照。
func (rt *Runtime) Snapshot() UISnapshot {
	rt.mu.Lock()
	currentProvider, currentModel, _ := rt.models.CurrentSelection("default")
	snap := UISnapshot{
		NovelName: rt.cfg.NovelName,
		Provider:  currentProvider,
		ModelName: currentModel,
		Style:     rt.cfg.Style,
		IsRunning: rt.running,
	}
	rt.mu.Unlock()

	progress, _ := rt.store.Progress.Load()
	if progress != nil {
		snap.Phase = string(progress.Phase)
		snap.Flow = string(progress.Flow)
		snap.CurrentChapter = progress.CurrentChapter
		snap.TotalChapters = progress.TotalChapters
		snap.CompletedCount = len(progress.CompletedChapters)
		snap.TotalWordCount = progress.TotalWordCount
		snap.InProgressChapter = progress.InProgressChapter
		snap.PendingRewrites = progress.PendingRewrites
		snap.RewriteReason = progress.RewriteReason
	}

	runMeta, _ := rt.store.RunMeta.Load()
	if runMeta != nil {
		snap.PendingSteer = runMeta.PendingSteer
	}

	// 状态标签映射
	snap.StatusLabel = rt.deriveStatusLabel(progress, snap.IsRunning)

	// 恢复标签
	recovery := rt.session.recovery()
	if !recovery.IsNew {
		snap.RecoveryLabel = recovery.Label
	}

	// 详情区
	rt.fillDetails(&snap, progress)

	return snap
}

// ConfiguredProviders 返回已配置的 provider 列表。
func (rt *Runtime) ConfiguredProviders() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	providers := make([]string, 0, len(rt.cfg.Providers))
	for name := range rt.cfg.Providers {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers
}

// ConfiguredModels 返回某个 provider 下已配置的模型列表。
func (rt *Runtime) ConfiguredModels(provider string) []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	models := rt.cfg.CandidateModels(provider)
	return append([]string(nil), models...)
}

// CurrentModelSelection 返回指定 role 当前生效的 provider/model。
// role 为空或 "default" 时返回默认模型。
func (rt *Runtime) CurrentModelSelection(role string) (provider, model string, explicit bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.models.CurrentSelection(role)
}

// SwitchModel 热切换默认模型或指定角色模型。
// role 为空或 "default" 时更新默认模型；否则写入角色级会话覆盖。
func (rt *Runtime) SwitchModel(role, provider, model string) error {
	role = strings.TrimSpace(role)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		return fmt.Errorf("provider 不能为空")
	}
	if model == "" {
		return fmt.Errorf("model 不能为空")
	}
	if role == "" {
		role = "default"
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	if err := rt.models.Swap(role, provider, model); err != nil {
		return err
	}

	if role == "default" {
		rt.cfg.Provider = provider
		rt.cfg.ModelName = model
		if err := rt.store.RunMeta.Init(rt.cfg.Style, provider, model); err != nil {
			slog.Warn("更新运行元信息失败", "module", "runtime", "err", err)
		}
	} else {
		if rt.cfg.Roles == nil {
			rt.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rt.cfg.Roles[role] = bootstrap.RoleConfig{
			Provider: provider,
			Model:    model,
		}
	}

	// 持久化到全局配置文件（增量合并，不覆盖无关字段）
	if err := persistModelChange(role, provider, model, rt.cfg); err != nil {
		slog.Warn("持久化模型配置失败", "module", "runtime", "err", err)
	}

	summary := fmt.Sprintf("模型已切换：%s -> %s/%s", roleLabel(role), provider, model)
	slog.Info("模型切换", "module", "runtime", "role", role, "provider", provider, "model", model)
	rt.emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "success"})
	return nil
}

func buildStartPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	return "请根据以下创作要求开始创作一部小说。章节数量由你根据故事需要自行决定；若题材与冲突天然适合长篇连载，请优先规划为分层长篇结构，而不是压缩成短篇式梗概。\n\n[创作要求]\n" +
		prompt +
		"\n\n若某些细节未明确，请在不违背用户方向的前提下自行补全。"
}

// persistModelChange 增量更新全局配置文件中的模型设置。
// 从磁盘读取现有配置，仅覆盖 provider/model 或 roles 字段，再写回。
func persistModelChange(role, provider, model string, baseline bootstrap.Config) error {
	cfgPath := bootstrap.DefaultConfigPath()
	if cfgPath == "" {
		return fmt.Errorf("无法确定配置文件路径")
	}
	return persistModelChangeToPath(cfgPath, role, provider, model, baseline)
}

func persistModelChangeToPath(cfgPath, role, provider, model string, baseline bootstrap.Config) error {
	cfg, err := bootstrap.LoadConfigFile(cfgPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("读取全局配置失败: %w", err)
		}
		cfg = baseline
	}

	if role == "default" {
		cfg.Provider = provider
		cfg.ModelName = model
	} else {
		if cfg.Roles == nil {
			cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		cfg.Roles[role] = bootstrap.RoleConfig{Provider: provider, Model: model}
	}

	// 确保目标 provider 的凭证配置存在。
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]bootstrap.ProviderConfig)
	}
	if _, ok := cfg.Providers[provider]; !ok {
		if pc, exists := baseline.Providers[provider]; exists {
			cfg.Providers[provider] = pc
		}
	}

	return bootstrap.SaveConfig(cfgPath, cfg)
}

func roleLabel(role string) string {
	switch role {
	case "", "default":
		return "默认"
	case "coordinator":
		return "Coordinator"
	case "architect":
		return "Architect"
	case "writer":
		return "Writer"
	case "editor":
		return "Editor"
	default:
		return role
	}
}

// Events 返回只读事件通道。
func (rt *Runtime) Events() <-chan UIEvent {
	return rt.events
}

// Done 返回完成信号通道。
func (rt *Runtime) Done() <-chan struct{} {
	return rt.done
}

// Close 终止 coordinator 并关闭事件通道。
func (rt *Runtime) Close() {
	rt.coordinator.AbortSilent()
	rt.session.finalizeSteerIfIdle()
	rt.closeOnce.Do(func() {
		close(rt.events)
		close(rt.streamCh)
		close(rt.clearCh)
	})
}

func (rt *Runtime) waitDone() {
	rt.coordinator.WaitForIdle()
	rt.session.finalizeSteerIfIdle()
	rt.mu.Lock()
	rt.running = false
	rt.mu.Unlock()
	rt.doneOnce.Do(func() {
		close(rt.done)
	})
}

func (rt *Runtime) deriveStatusLabel(progress *domain.Progress, isRunning bool) string {
	if progress == nil {
		return "READY"
	}
	if progress.Phase == domain.PhaseComplete {
		return "COMPLETE"
	}
	if !isRunning {
		return "READY"
	}
	switch progress.Flow {
	case domain.FlowReviewing:
		return "REVIEW"
	case domain.FlowRewriting, domain.FlowPolishing:
		return "REWRITE"
	default:
		return "RUNNING"
	}
}

func (rt *Runtime) fillDetails(snap *UISnapshot, progress *domain.Progress) {
	// 基础设定
	if premise, _ := rt.store.Outline.LoadPremise(); premise != "" {
		snap.Premise = truncateLog(premise, 80)
	}
	if outline, _ := rt.store.Outline.LoadOutline(); len(outline) > 0 {
		for _, e := range outline {
			snap.Outline = append(snap.Outline, OutlineSnapshot{
				Chapter: e.Chapter, Title: e.Title, CoreEvent: e.CoreEvent,
			})
		}
	}
	// 分层模式：加载指南针 + 下一卷预览
	if progress != nil && progress.Layered {
		snap.Layered = true
		snap.CurrentVolumeArc = fmt.Sprintf("第%d卷·第%d弧", progress.CurrentVolume, progress.CurrentArc)
		if compass, _ := rt.store.Outline.LoadCompass(); compass != nil {
			snap.CompassDirection = compass.EndingDirection
			snap.CompassScale = compass.EstimatedScale
		}
		if volumes, _ := rt.store.Outline.LoadLayeredOutline(); len(volumes) > 0 {
			for _, v := range volumes {
				if v.Index > progress.CurrentVolume {
					snap.NextVolumeTitle = v.Title
					break
				}
			}
		}
	}
	if chars, _ := rt.store.Characters.Load(); len(chars) > 0 {
		for _, c := range chars {
			label := c.Name
			if c.Role != "" {
				label += "（" + c.Role + "）"
			}
			snap.Characters = append(snap.Characters, label)
		}
	}

	// 最近 commit：从 progress 的已完成章节 + 摘要推算（信号文件是一次性的，不可靠）
	if progress != nil && len(progress.CompletedChapters) > 0 {
		lastCh := progress.CompletedChapters[len(progress.CompletedChapters)-1]
		wordCount := progress.ChapterWordCounts[lastCh]
		snap.LastCommitSummary = fmt.Sprintf("第%d章 %d字", lastCh, wordCount)
	}

	// 最近 review
	currentCh := 1
	if progress != nil && len(progress.CompletedChapters) > 0 {
		currentCh = progress.CompletedChapters[len(progress.CompletedChapters)-1]
	}
	if review, err := rt.store.World.LoadLastReview(currentCh); err == nil && review != nil {
		snap.LastReviewSummary = fmt.Sprintf("verdict=%s %d个问题", review.Verdict, len(review.Issues))
		if len(review.AffectedChapters) > 0 {
			snap.LastReviewSummary += fmt.Sprintf(" 影响%v", review.AffectedChapters)
		}
	}

	// 最近 checkpoint
	snap.LastCheckpointName = rt.latestCheckpoint()

	// 最近两章摘要
	if progress != nil {
		for i := len(progress.CompletedChapters) - 1; i >= 0 && len(snap.RecentSummaries) < 2; i-- {
			ch := progress.CompletedChapters[i]
			if summary, err := rt.store.Summaries.LoadSummary(ch); err == nil && summary != nil {
				snap.RecentSummaries = append(snap.RecentSummaries,
					fmt.Sprintf("第%d章: %s", ch, truncateLog(summary.Summary, 50)))
			}
		}
	}
}

func (rt *Runtime) latestCheckpoint() string {
	dir := filepath.Join(rt.store.Dir(), "meta", "checkpoints")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() > entries[j].Name()
	})
	name := entries[0].Name()
	if ext := filepath.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	return name
}
