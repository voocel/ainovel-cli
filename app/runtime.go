package app

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/domain"
	"github.com/voocel/ainovel-cli/state"
	"github.com/voocel/ainovel-cli/tools"
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
	CompletedScenes   int
	PendingRewrites   []int
	RewriteReason     string
	PendingSteer      string
	RecoveryLabel     string // 恢复类型描述，空表示新建
	IsRunning         bool

	// 详情区
	LastCommitSummary  string
	LastReviewSummary  string
	LastCheckpointName string
	RecentSummaries    []string
}

// Runtime 封装协调器生命周期，提供 TUI 所需的非阻塞接口。
type Runtime struct {
	cfg         Config
	store       *state.Store
	coordinator *agentcore.Agent
	events      chan UIEvent
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

// NewRuntime 创建 Runtime：初始化 store/model/coordinator，注册事件订阅，但不启动 Prompt。
func NewRuntime(cfg Config, refs tools.References, prompts Prompts, styles map[string]string) (*Runtime, error) {
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return nil, err
	}

	store := state.NewStore(cfg.OutputDir)
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}

	model, err := createModel(cfg)
	if err != nil {
		return nil, fmt.Errorf("create model: %w", err)
	}

	coordinator := BuildCoordinator(cfg, store, model, refs, prompts, styles)

	rt := &Runtime{
		cfg:         cfg,
		store:       store,
		coordinator: coordinator,
		events:      make(chan UIEvent, 100),
		done:        make(chan struct{}),
	}

	// 注册事件订阅：确定性控制 + UIEvent 转发
	registerSubscription(coordinator, store, cfg.MaxChapters, rt.emit)

	// 初始化运行元信息
	if err := store.InitRunMeta(cfg.Style, cfg.ModelName); err != nil {
		log.Printf("[warn] 初始化运行元信息失败: %v", err)
	}

	return rt, nil
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

	if err := rt.store.InitProgress(rt.cfg.NovelName, rt.cfg.MaxChapters); err != nil {
		return fmt.Errorf("init progress: %w", err)
	}

	promptText := fmt.Sprintf("请创作一部 %d 章的小说。要求如下：\n\n%s", rt.cfg.MaxChapters, prompt)
	if err := rt.coordinator.Prompt(promptText); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}

	rt.mu.Lock()
	rt.running = true
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

	progress, _ := rt.store.LoadProgress()
	runMeta, _ := rt.store.LoadRunMeta()
	recovery := determineRecovery(progress, runMeta, rt.cfg.MaxChapters)

	if recovery.IsNew {
		return "", nil
	}

	if err := rt.coordinator.Prompt(recovery.PromptText); err != nil {
		return "", fmt.Errorf("prompt: %w", err)
	}

	rt.mu.Lock()
	rt.running = true
	rt.mu.Unlock()

	go rt.waitDone()
	return recovery.Label, nil
}

// Steer 提交用户干预。
func (rt *Runtime) Steer(text string) {
	submitSteer(rt.store, rt.coordinator, text)
	rt.emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: "干预已提交: " + truncateLog(text, 40), Level: "info"})
}

// Snapshot 读取 store 聚合为状态快照。
func (rt *Runtime) Snapshot() UISnapshot {
	snap := UISnapshot{
		NovelName: rt.cfg.NovelName,
		ModelName: rt.cfg.ModelName,
		Style:     rt.cfg.Style,
	}

	rt.mu.Lock()
	snap.IsRunning = rt.running
	rt.mu.Unlock()

	progress, _ := rt.store.LoadProgress()
	if progress != nil {
		snap.Phase = string(progress.Phase)
		snap.Flow = string(progress.Flow)
		snap.CurrentChapter = progress.CurrentChapter
		snap.TotalChapters = progress.TotalChapters
		snap.CompletedCount = len(progress.CompletedChapters)
		snap.TotalWordCount = progress.TotalWordCount
		snap.InProgressChapter = progress.InProgressChapter
		snap.CompletedScenes = len(progress.CompletedScenes)
		snap.PendingRewrites = progress.PendingRewrites
		snap.RewriteReason = progress.RewriteReason
	}

	runMeta, _ := rt.store.LoadRunMeta()
	if runMeta != nil {
		snap.PendingSteer = runMeta.PendingSteer
	}

	// 状态标签映射
	snap.StatusLabel = rt.deriveStatusLabel(progress, snap.IsRunning)

	// 恢复标签
	recovery := determineRecovery(progress, runMeta, rt.cfg.MaxChapters)
	if !recovery.IsNew {
		snap.RecoveryLabel = recovery.Label
	}

	// 详情区
	rt.fillDetails(&snap, progress)

	return snap
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
	finalizeSteerIfIdle(rt.store)
	rt.closeOnce.Do(func() {
		close(rt.events)
	})
}

func (rt *Runtime) waitDone() {
	rt.coordinator.WaitForIdle()
	finalizeSteerIfIdle(rt.store)
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
	if review, err := rt.store.LoadLastReview(currentCh); err == nil && review != nil {
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
			if summary, err := rt.store.LoadSummary(ch); err == nil && summary != nil {
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
