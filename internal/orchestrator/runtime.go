package orchestrator

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
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
	Agents            []AgentSnapshot
	Tasks             []TaskSnapshot

	// 上下文
	ContextTokens         int
	ContextWindow         int
	ContextPercent        float64
	ContextScope          string
	ContextStrategy       string
	ContextActiveMessages int
	ContextSummaryCount   int
	ContextCompactedCount int
	ContextKeptCount      int

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

// TaskSnapshot 是任务列表的展示投影。
type TaskSnapshot struct {
	ID        string
	Kind      string
	Owner     string
	Title     string
	Status    string
	Chapter   int
	Volume    int
	Arc       int
	Summary   string
	Tool      string
	OutputRef string
	UpdatedAt time.Time
}

// Runtime 是面向 TUI 的适配壳。
// 核心会话主循环位于 Engine；Runtime 只补充快照聚合和模型切换等界面能力。
type Runtime struct {
	*Engine
}

// ReplayDeltaText 从运行时队列项中提取可回放的流式文本。
func ReplayDeltaText(item domain.RuntimeQueueItem) string {
	if payload, ok := item.Payload.(map[string]any); ok {
		if text, ok := payload["delta"].(string); ok {
			return text
		}
	}
	return ""
}

// NewRuntime 创建面向 TUI 的运行时适配器。
func NewRuntime(cfg bootstrap.Config, bundle assets.Bundle) (*Runtime, error) {
	engine, err := NewEngine(cfg, bundle)
	if err != nil {
		return nil, err
	}
	return &Runtime{Engine: engine}, nil
}

// ReplayQueue 返回指定序号之后的运行时队列项。
func (rt *Runtime) ReplayQueue(afterSeq int64) ([]domain.RuntimeQueueItem, error) {
	return rt.Engine.ReplayQueue(afterSeq)
}

// Snapshot 读取 store 聚合为状态快照。
func (rt *Runtime) Snapshot() UISnapshot {
	rt.mu.Lock()
	currentProvider, currentModel, _ := rt.models.CurrentSelection("default")
	snap := UISnapshot{
		Provider:  currentProvider,
		ModelName: currentModel,
		Style:     rt.cfg.Style,
		IsRunning: rt.running,
	}
	rt.mu.Unlock()

	progress, _ := rt.store.Progress.Load()
	if rt.taskRT != nil {
		_ = rt.taskRT.Reconcile(progress)
	}
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
	}
	if snap.NovelName == "" {
		if premise, _ := rt.store.Outline.LoadPremise(); premise != "" {
			snap.NovelName = domain.ExtractNovelNameFromPremise(premise)
		}
	}

	runMeta, _ := rt.store.RunMeta.Load()
	if runMeta != nil {
		snap.PendingSteer = runMeta.PendingSteer
	}

	snap.Agents = rt.agents.Snapshot()
	snap.Tasks = rt.taskSnapshots()
	if snap.InProgressChapter == 0 {
		snap.InProgressChapter = activeChapterFromTasks(snap.Tasks)
	}
	rt.fillContextStatus(&snap)

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

func activeChapterFromTasks(tasks []TaskSnapshot) int {
	for _, task := range tasks {
		if task.Status != string(domain.TaskRunning) && task.Status != string(domain.TaskQueued) {
			continue
		}
		switch task.Kind {
		case string(domain.TaskChapterWrite), string(domain.TaskChapterRewrite), string(domain.TaskChapterPolish):
			if task.Chapter > 0 {
				return task.Chapter
			}
		}
	}
	return 0
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

func BuildStartPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	return "请根据以下创作要求开始创作一部小说。进入规划后，Premise 第一行必须输出 `# 书名`。章节数量由你根据故事需要自行决定；若题材与冲突天然适合长篇连载，请优先规划为分层长篇结构，而不是压缩成短篇式梗概。\n\n[创作要求]\n" +
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
	for _, task := range rt.taskSnapshots() {
		if task.Status != string(domain.TaskRunning) && task.Status != string(domain.TaskQueued) {
			continue
		}
		switch task.Kind {
		case string(domain.TaskChapterReview):
			return "REVIEW"
		case string(domain.TaskChapterRewrite), string(domain.TaskChapterPolish):
			return "REWRITE"
		}
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

func (rt *Runtime) taskSnapshots() []TaskSnapshot {
	if rt.taskRT == nil {
		return nil
	}
	tasks := rt.taskRT.Snapshot()
	if len(tasks) == 0 {
		return nil
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})
	out := make([]TaskSnapshot, 0, len(tasks))
	for _, task := range tasks {
		summary := task.Progress.Summary
		if summary == "" {
			summary = task.Progress.ToolSummary
		}
		out = append(out, TaskSnapshot{
			ID:        task.ID,
			Kind:      string(task.Kind),
			Owner:     task.Owner,
			Title:     task.Title,
			Status:    string(task.Status),
			Chapter:   task.Chapter,
			Volume:    task.Volume,
			Arc:       task.Arc,
			Summary:   summary,
			Tool:      task.Progress.Tool,
			OutputRef: task.OutputRef,
			UpdatedAt: task.UpdatedAt,
		})
	}
	return out
}

func (rt *Runtime) fillContextStatus(snap *UISnapshot) {
	if rt.coordinator == nil || snap == nil {
		return
	}
	if usage := rt.coordinator.ContextUsage(); usage != nil {
		snap.ContextTokens = usage.Tokens
		snap.ContextWindow = usage.ContextWindow
		snap.ContextPercent = usage.Percent
	}
	if ctx := rt.coordinator.ContextSnapshot(); ctx != nil {
		snap.ContextScope = ctx.Scope
		snap.ContextStrategy = ctx.LastStrategy
		snap.ContextActiveMessages = ctx.ActiveMessages
		snap.ContextSummaryCount = ctx.SummaryMessages
		snap.ContextCompactedCount = ctx.LastCompactedCount
		snap.ContextKeptCount = ctx.LastKeptCount
		if snap.ContextTokens == 0 && ctx.Usage != nil {
			snap.ContextTokens = ctx.Usage.Tokens
			snap.ContextWindow = ctx.Usage.ContextWindow
			snap.ContextPercent = ctx.Usage.Percent
		}
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
