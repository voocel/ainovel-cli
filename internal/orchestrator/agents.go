package orchestrator

import (
	"time"

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
			return newContextManager(contextManagerConfig{
				Model:            model,
				ContextWindow:    cfg.ContextWindow,
				ReserveTokens:    16384,
				KeepRecentTokens: 20000,
				Agent:            "writer",
				Emit:             emit,
				AppendBoundary:   runtimeAppender(store),
				ToolMicrocompact: &corecontext.ToolResultMicrocompactConfig{
					IdleThreshold: 5 * time.Minute,
				},
				ExtraStrategies: []corecontext.Strategy{
					NewStoreSummaryCompact(StoreSummaryCompactConfig{
						Store:            store,
						KeepRecentTokens: 20000,
					}),
				},
				Summary: &corecontext.FullSummaryConfig{
					PostSummaryHooks:    []corecontext.PostSummaryHook{restore.Hook()},
					SystemPrompt:        writerSummarySystemPrompt,
					SummaryPrompt:       writerSummaryPrompt,
					UpdateSummaryPrompt: writerUpdateSummaryPrompt,
					TurnPrefixPrompt:    writerTurnPrefixPrompt,
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
	}

	subagentTool := agentcore.NewSubAgentTool(architectShort, architectMid, architectLong, writer, editor)

	agent := agentcore.NewAgent(
		agentcore.WithModel(coordinatorModel),
		agentcore.WithSystemPrompt(bundle.Prompts.Coordinator),
		agentcore.WithTools(subagentTool, contextTool, askUser),
		agentcore.WithMaxTurns(200),
		agentcore.WithContextManager(newContextManager(contextManagerConfig{
			Model:            coordinatorModel,
			ContextWindow:    cfg.ContextWindow,
			ReserveTokens:    32000,
			KeepRecentTokens: 30000,
			Agent:            "coordinator",
			Emit:             emit,
			CommitOnProject:  true,
			AppendBoundary:   runtimeAppender(store),
		})),
	)
	return agent, askUser, restore
}

func runtimeAppender(s *store.Store) func(domain.RuntimeQueueItem) {
	return func(item domain.RuntimeQueueItem) {
		if s == nil || s.Runtime == nil {
			return
		}
		_, _ = s.Runtime.AppendQueue(item)
	}
}

// contextManagerConfig groups all parameters for newContextManager, replacing
// the 9-parameter function signature that accumulated over successive changes.
type contextManagerConfig struct {
	Model            agentcore.ChatModel
	ContextWindow    int
	ReserveTokens    int
	KeepRecentTokens int
	Agent            string
	Emit             emitFn
	CommitOnProject  bool
	AppendBoundary   func(domain.RuntimeQueueItem)
	Summary          *corecontext.FullSummaryConfig            // nil = defaults
	ToolMicrocompact *corecontext.ToolResultMicrocompactConfig // nil = defaults
	ExtraStrategies  []corecontext.Strategy
}

func newContextManager(cfg contextManagerConfig) agentcore.ContextManager {
	var sc corecontext.FullSummaryConfig
	if cfg.Summary != nil {
		sc = *cfg.Summary // copy, never mutate caller's struct
	}
	sc.Model = cfg.Model
	if sc.KeepRecentTokens <= 0 {
		sc.KeepRecentTokens = cfg.KeepRecentTokens
	}
	var tc corecontext.ToolResultMicrocompactConfig
	if cfg.ToolMicrocompact != nil {
		tc = *cfg.ToolMicrocompact
	}
	strategies := []corecontext.Strategy{
		corecontext.NewToolResultMicrocompact(tc),
		corecontext.NewLightTrim(corecontext.LightTrimConfig{}),
	}
	strategies = append(strategies, cfg.ExtraStrategies...)
	strategies = append(strategies, corecontext.NewFullSummary(sc))
	engine := corecontext.NewEngine(corecontext.EngineConfig{
		ContextWindow:   cfg.ContextWindow,
		ReserveTokens:   cfg.ReserveTokens,
		CommitOnProject: cfg.CommitOnProject,
		Strategies:      strategies,
	})
	callback := contextRewriteCallback(cfg.Agent, cfg.Emit, cfg.AppendBoundary)
	engine.SetProjectHook(callback)
	engine.SetRecoverHook(callback)
	return engine
}
