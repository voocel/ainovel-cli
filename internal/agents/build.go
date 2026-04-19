package agents

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host/reminder"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// UsageRecorder 是 BuildCoordinator 可选的用量回调；签名与 OnMessage 一致，
// 每条 agent 消息都会调一次，由 Host 层负责聚合。nil 表示不追踪。
type UsageRecorder func(agentName string, msg agentcore.AgentMessage)

// BuildCoordinator 组装 Coordinator Agent 及其 SubAgent。
// 返回 Agent、AskUserTool 和 WriterRestorePack。
// Host 层通过 Agent.Subscribe 获取事件流,不再需要 emit 回调。
func BuildCoordinator(
	cfg bootstrap.Config,
	store *store.Store,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
	recordUsage UsageRecorder,
) (*agentcore.Agent, *tools.AskUserTool, *ctxpack.WriterRestorePack) {
	// 共享工具
	contextTool := tools.NewContextTool(store, bundle.References, cfg.Style)
	readChapter := tools.NewReadChapterTool(store)
	askUser := tools.NewAskUserTool()

	architectTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveFoundationTool(store),
	}
	writerTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewPlanChapterTool(store),
		tools.NewDraftChapterTool(store),
		tools.NewCheckConsistencyTool(store),
		tools.NewCommitChapterTool(store),
	}
	editorTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewSaveReviewTool(store),
		tools.NewSaveArcSummaryTool(store),
		tools.NewSaveVolumeSummaryTool(store),
	}

	// Provider failover 只记日志,不通知宿主
	reportFailover := func(ev bootstrap.FailoverEvent) {
		slog.Warn("provider 切换",
			"module", "agent",
			"role", ev.Role,
			"code", ev.Code,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err,
		)
	}

	architectModel := models.ForRoleWithFailover("architect", reportFailover)
	writerModel := models.ForRoleWithFailover("writer", reportFailover)
	editorModel := models.ForRoleWithFailover("editor", reportFailover)
	coordinatorModel := models.ForRoleWithFailover("coordinator", reportFailover)

	// Coordinator 的 ContextManager 在 Agent 构造时一次性生成，按启动模型解析。
	// 运行中 /model 切换到更小窗口的模型时，建议用户显式配置 context_window 兜底。
	_, coordinatorModelName, _ := models.CurrentSelection("coordinator")
	coordinatorContextWindow, coordinatorSource := cfg.ResolveContextWindow(coordinatorModelName)
	// Writer 的 ContextManager 由工厂每次调用重建，窗口随模型 swap 动态跟随（见下方工厂）。
	_, writerModelName, _ := models.CurrentSelection("writer")
	writerContextWindow, writerSource := cfg.ResolveContextWindow(writerModelName)
	logContextWindowChoice("coordinator", coordinatorModelName, coordinatorContextWindow, coordinatorSource)
	logContextWindowChoice("writer", writerModelName, writerContextWindow, writerSource)

	baseOnMsg := store.Sessions.SubAgentLogger()
	onMsg := func(agentName, task string, msg agentcore.AgentMessage) {
		baseOnMsg(agentName, task, msg)
		if recordUsage != nil {
			recordUsage(agentName, msg)
		}
	}
	baseCoordinatorLog := store.Sessions.CoordinatorLogger()
	coordinatorOnMessage := func(msg agentcore.AgentMessage) {
		baseCoordinatorLog(msg)
		if recordUsage != nil {
			recordUsage("coordinator", msg)
		}
	}

	architectStopGuardFactory := func(_, _ string) agentcore.StopGuard {
		return reminder.NewArchitectStopGuard(store)
	}
	architectShort := agentcore.SubAgentConfig{
		Name:             "architect_short",
		Description:      "短篇规划师：为单卷、单冲突、高密度故事生成紧凑设定与扁平大纲",
		Model:            architectModel,
		SystemPrompt:     bundle.Prompts.ArchitectShort,
		Tools:            architectTools,
		MaxTurns:         8,
		OnMessage:        onMsg,
		StopGuardFactory: architectStopGuardFactory,
	}
	architectMid := agentcore.SubAgentConfig{
		Name:             "architect_mid",
		Description:      "中篇规划师：为多阶段但篇幅受控的故事生成可推进的设定与阶段化大纲",
		Model:            architectModel,
		SystemPrompt:     bundle.Prompts.ArchitectMid,
		Tools:            architectTools,
		MaxTurns:         10,
		OnMessage:        onMsg,
		StopGuardFactory: architectStopGuardFactory,
	}
	architectLong := agentcore.SubAgentConfig{
		Name:             "architect_long",
		Description:      "长篇规划师：为连载型、可持续升级的故事生成分层设定与卷弧大纲",
		Model:            architectModel,
		SystemPrompt:     bundle.Prompts.ArchitectLong,
		Tools:            architectTools,
		MaxTurns:         14,
		OnMessage:        onMsg,
		StopGuardFactory: architectStopGuardFactory,
	}

	writerPrompt := bundle.Prompts.Writer
	if style, ok := bundle.Styles[cfg.Style]; ok {
		writerPrompt += "\n\n" + style
	}

	restore := &ctxpack.WriterRestorePack{}
	restore.Refresh(store)

	writer := agentcore.SubAgentConfig{
		Name:         "writer",
		Description:  "创作者：自主完成一章的构思、写作、自审和提交",
		Model:        writerModel,
		SystemPrompt: writerPrompt,
		Tools:        writerTools,
		MaxTurns:     20,
		OnMessage:    onMsg,
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return reminder.NewWriterStopGuard(store)
		},
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			// 每次 subagent(writer) 调用都会重建，从当前 runModel 读取最新模型名。
			// /model 切换 writer 后下一章自动用新窗口。
			window, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			return newContextManager(contextManagerConfig{
				Model:            model,
				ContextWindow:    window,
				ReserveTokens:    16384,
				KeepRecentTokens: 20000,
				Agent:            "writer",
				ToolMicrocompact: &corecontext.ToolResultMicrocompactConfig{
					IdleThreshold: 5 * time.Minute,
				},
				ExtraStrategies: []corecontext.Strategy{
					ctxpack.NewStoreSummaryCompact(ctxpack.StoreSummaryCompactConfig{
						Store:            store,
						KeepRecentTokens: 20000,
					}),
				},
				Summary: &corecontext.FullSummaryConfig{
					PostSummaryHooks:    []corecontext.PostSummaryHook{restore.Hook()},
					SystemPrompt:        ctxpack.WriterSummarySystemPrompt,
					SummaryPrompt:       ctxpack.WriterSummaryPrompt,
					UpdateSummaryPrompt: ctxpack.WriterUpdateSummaryPrompt,
					TurnPrefixPrompt:    ctxpack.WriterTurnPrefixPrompt,
				},
			})
		},
	}

	editor := agentcore.SubAgentConfig{
		Name:         "editor",
		Description:  "审阅者：阅读原文，从结构和审美两个层面发现问题",
		Model:        editorModel,
		SystemPrompt: bundle.Prompts.Editor,
		Tools:        editorTools,
		MaxTurns:     10,
		OnMessage:    onMsg,
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return reminder.NewEditorStopGuard(store)
		},
	}

	subagentTool := agentcore.NewSubAgentTool(architectShort, architectMid, architectLong, writer, editor)

	agent := agentcore.NewAgent(
		agentcore.WithModel(coordinatorModel),
		agentcore.WithSystemPrompt(bundle.Prompts.Coordinator),
		agentcore.WithTools(subagentTool, contextTool),
		agentcore.WithMaxTurns(1000),
		agentcore.WithOnMaxTurns(agentcore.MaxTurnsSoftRestart),
		agentcore.WithDefaultToolChoice("required"),
		agentcore.WithOnMessage(coordinatorOnMessage),
		agentcore.WithContextManager(newContextManager(contextManagerConfig{
			Model:            coordinatorModel,
			ContextWindow:    coordinatorContextWindow,
			ReserveTokens:    32000,
			KeepRecentTokens: 30000,
			Agent:            "coordinator",
			CommitOnProject:  true,
		})),
		agentcore.WithReminderGenerator(reminder.Aggregate(store, reminder.Default()...)),
		agentcore.WithStopGuard(reminder.NewStopGuard(store, nil)),
	)
	return agent, askUser, restore
}

// logContextWindowChoice 打印某个角色的窗口决策。source=default 时发 Warn 提示用户显式配置。
func logContextWindowChoice(role, model string, window int, source bootstrap.ContextWindowSource) {
	attrs := []any{"module", "agent", "role", role, "model", model, "window", window, "source", source}
	if source == bootstrap.CtxWindowDefault {
		slog.Warn("未识别的模型，使用默认上下文窗口（如实际不同请在 config 中设置 context_window）", attrs...)
		return
	}
	slog.Info("上下文窗口", attrs...)
}
