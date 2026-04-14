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
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// BuildCoordinator 组装 Coordinator Agent 及其 SubAgent。
// 返回 Agent、AskUserTool 和 WriterRestorePack。
// Host 层通过 Agent.Subscribe 获取事件流,不再需要 emit 回调。
func BuildCoordinator(
	cfg bootstrap.Config,
	store *store.Store,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
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

	onMsg := store.Sessions.SubAgentLogger()

	architectShort := agentcore.SubAgentConfig{
		Name:         "architect_short",
		Description:  "短篇规划师：为单卷、单冲突、高密度故事生成紧凑设定与扁平大纲",
		Model:        architectModel,
		SystemPrompt: bundle.Prompts.ArchitectShort,
		Tools:        architectTools,
		MaxTurns:     8,
		OnMessage:    onMsg,
	}
	architectMid := agentcore.SubAgentConfig{
		Name:         "architect_mid",
		Description:  "中篇规划师：为多阶段但篇幅受控的故事生成可推进的设定与阶段化大纲",
		Model:        architectModel,
		SystemPrompt: bundle.Prompts.ArchitectMid,
		Tools:        architectTools,
		MaxTurns:     10,
		OnMessage:    onMsg,
	}
	architectLong := agentcore.SubAgentConfig{
		Name:         "architect_long",
		Description:  "长篇规划师：为连载型、可持续升级的故事生成分层设定与卷弧大纲",
		Model:        architectModel,
		SystemPrompt: bundle.Prompts.ArchitectLong,
		Tools:        architectTools,
		MaxTurns:     14,
		OnMessage:    onMsg,
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
		MaxTurns:     10,
		OnMessage:    onMsg,
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			return newContextManager(contextManagerConfig{
				Model:            model,
				ContextWindow:    cfg.ContextWindow,
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
	}

	subagentTool := agentcore.NewSubAgentTool(architectShort, architectMid, architectLong, writer, editor)

	agent := agentcore.NewAgent(
		agentcore.WithModel(coordinatorModel),
		agentcore.WithSystemPrompt(bundle.Prompts.Coordinator),
		agentcore.WithTools(subagentTool, contextTool),
		agentcore.WithMaxTurns(1000),
		agentcore.WithDefaultToolChoice("required"),
		agentcore.WithOnMessage(store.Sessions.CoordinatorLogger()),
		agentcore.WithContextManager(newContextManager(contextManagerConfig{
			Model:            coordinatorModel,
			ContextWindow:    cfg.ContextWindow,
			ReserveTokens:    32000,
			KeepRecentTokens: 30000,
			Agent:            "coordinator",
			CommitOnProject:  true,
		})),
	)
	return agent, askUser, restore
}
