package orchestrator

import (
	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// BuildCoordinator 组装 Coordinator Agent 及其 SubAgent。
// 返回 Agent、AskUserTool 和 writerRestorePack（供调用方在写章生命周期中 Refresh）。
func BuildCoordinator(
	cfg bootstrap.Config,
	store *store.Store,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
	emit emitFn,
) (*agentcore.Agent, *tools.AskUserTool, *writerRestorePack) {
	// 共享工具
	contextTool := tools.NewContextTool(store, bundle.References, cfg.Style)
	readChapter := tools.NewReadChapterTool(store)
	askUser := tools.NewAskUserTool()

	// Architect SubAgent 工具
	architectTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveFoundationTool(store),
	}

	// Writer SubAgent 工具：读写 + 规划 + 一致性检查 + 提交
	writerTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewPlanChapterTool(store),
		tools.NewDraftChapterTool(store),
		tools.NewCheckConsistencyTool(store),
		tools.NewCommitChapterTool(store),
	}

	// Editor SubAgent 工具：读原文 + 审阅 + 摘要
	editorTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewSaveReviewTool(store),
		tools.NewSaveArcSummaryTool(store),
		tools.NewSaveVolumeSummaryTool(store),
	}

	architectModel := models.ForRole("architect")
	writerModel := models.ForRole("writer")
	editorModel := models.ForRole("editor")
	coordinatorModel := models.ForRole("coordinator")

	architectShort := agentcore.SubAgentConfig{
		Name:         "architect_short",
		Description:  "短篇规划师：为单卷、单冲突、高密度故事生成紧凑设定与扁平大纲",
		Model:        architectModel,
		SystemPrompt: bundle.Prompts.ArchitectShort,
		Tools:        architectTools,
		MaxTurns:     10,
	}

	architectMid := agentcore.SubAgentConfig{
		Name:         "architect_mid",
		Description:  "中篇规划师：为多阶段但篇幅受控的故事生成可推进的设定与阶段化大纲",
		Model:        architectModel,
		SystemPrompt: bundle.Prompts.ArchitectMid,
		Tools:        architectTools,
		MaxTurns:     12,
	}

	architectLong := agentcore.SubAgentConfig{
		Name:         "architect_long",
		Description:  "长篇规划师：为连载型、可持续升级的故事生成分层设定与卷弧大纲",
		Model:        architectModel,
		SystemPrompt: bundle.Prompts.ArchitectLong,
		Tools:        architectTools,
		MaxTurns:     14,
	}

	// 动态拼接风格指令到 Writer prompt
	writerPrompt := bundle.Prompts.Writer
	if style, ok := bundle.Styles[cfg.Style]; ok {
		writerPrompt += "\n\n" + style
	}

	restore := &writerRestorePack{}
	restore.Refresh(store) // initial load

	writer := agentcore.SubAgentConfig{
		Name:         "writer",
		Description:  "创作者：自主完成一章的构思、写作、自审和提交",
		Model:        writerModel,
		SystemPrompt: writerPrompt,
		Tools:        writerTools,
		MaxTurns:     20,
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			return newContextManager(model, cfg.ContextWindow, 16384, 20000, "writer", emit, false, func(item domain.RuntimeQueueItem) {
				if store == nil || store.Runtime == nil {
					return
				}
				_, _ = store.Runtime.AppendQueue(item)
			}, restore.Hook())
		},
	}

	editor := agentcore.SubAgentConfig{
		Name:         "editor",
		Description:  "审阅者：阅读原文，从结构和审美两个层面发现问题",
		Model:        editorModel,
		SystemPrompt: bundle.Prompts.Editor,
		Tools:        editorTools,
		MaxTurns:     10,
	}

	subagentTool := agentcore.NewSubAgentTool(architectShort, architectMid, architectLong, writer, editor)

	agent := agentcore.NewAgent(
		agentcore.WithModel(coordinatorModel),
		agentcore.WithSystemPrompt(bundle.Prompts.Coordinator),
		agentcore.WithTools(subagentTool, contextTool, askUser),
		agentcore.WithMaxTurns(200),
		agentcore.WithContextManager(newContextManager(coordinatorModel, cfg.ContextWindow, 32000, 30000, "coordinator", emit, true, func(item domain.RuntimeQueueItem) {
			if store == nil || store.Runtime == nil {
				return
			}
			_, _ = store.Runtime.AppendQueue(item)
		})),
	)
	return agent, askUser, restore
}

func newContextManager(model agentcore.ChatModel, contextWindow, reserveTokens, keepRecentTokens int, agent string, emit emitFn, commitOnProject bool, appendBoundary func(domain.RuntimeQueueItem), hooks ...corecontext.PostSummaryHook) agentcore.ContextManager {
	engine := corecontext.NewEngine(corecontext.EngineConfig{
		ContextWindow:   contextWindow,
		ReserveTokens:   reserveTokens,
		CommitOnProject: commitOnProject,
		Strategies: []corecontext.Strategy{
			corecontext.NewToolResultMicrocompact(corecontext.ToolResultMicrocompactConfig{}),
			corecontext.NewLightTrim(corecontext.LightTrimConfig{}),
			corecontext.NewFullSummary(corecontext.FullSummaryConfig{
				Model:            model,
				KeepRecentTokens: keepRecentTokens,
				PostSummaryHooks: hooks,
			}),
		},
	})
	callback := contextRewriteCallback(agent, emit, appendBoundary)
	engine.SetProjectHook(callback)
	engine.SetRecoverHook(callback)
	return engine
}

