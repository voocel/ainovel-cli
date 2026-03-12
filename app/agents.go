package app

import (
	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/state"
	"github.com/voocel/ainovel-cli/tools"
)

// BuildCoordinator 组装 Coordinator Agent 及其 SubAgent。
// 返回 Agent 和 AskUserTool（供调用方注入 handler）。
func BuildCoordinator(
	cfg Config,
	store *state.Store,
	model agentcore.ChatModel,
	refs tools.References,
	prompts Prompts,
	styles map[string]string,
) (*agentcore.Agent, *tools.AskUserTool) {
	// 共享工具
	contextTool := tools.NewContextTool(store, refs, cfg.Style)
	askUser := tools.NewAskUserTool()

	// Architect SubAgent 工具
	architectTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveFoundationTool(store),
	}

	// Writer SubAgent 工具（V1: +polish_chapter +check_consistency）
	writerTools := []agentcore.Tool{
		contextTool,
		tools.NewPlanChapterTool(store),
		tools.NewWriteSceneTool(store),
		tools.NewPolishChapterTool(store),
		tools.NewCheckConsistencyTool(store),
		tools.NewCommitChapterTool(store),
	}

	// Editor SubAgent 工具
	editorTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveReviewTool(store),
		tools.NewSaveArcSummaryTool(store),
		tools.NewSaveVolumeSummaryTool(store),
	}

	architectShort := agentcore.SubAgentConfig{
		Name:         "architect_short",
		Description:  "短篇规划师：为单卷、单冲突、高密度故事生成紧凑设定与扁平大纲",
		Model:        model,
		SystemPrompt: prompts.ArchitectShort,
		Tools:        architectTools,
		MaxTurns:     10,
	}

	architectMid := agentcore.SubAgentConfig{
		Name:         "architect_mid",
		Description:  "中篇规划师：为多阶段但篇幅受控的故事生成可推进的设定与阶段化大纲",
		Model:        model,
		SystemPrompt: prompts.ArchitectMid,
		Tools:        architectTools,
		MaxTurns:     12,
	}

	architectLong := agentcore.SubAgentConfig{
		Name:         "architect_long",
		Description:  "长篇规划师：为连载型、可持续升级的故事生成分层设定与卷弧大纲",
		Model:        model,
		SystemPrompt: prompts.ArchitectLong,
		Tools:        architectTools,
		MaxTurns:     14,
	}

	// 动态拼接风格指令到 Writer prompt
	writerPrompt := prompts.Writer
	if style, ok := styles[cfg.Style]; ok {
		writerPrompt += "\n\n" + style
	}

	writer := agentcore.SubAgentConfig{
		Name:         "writer",
		Description:  "场景写作者：逐场景完成一章的创作，包含打磨和一致性检查",
		Model:        model,
		SystemPrompt: writerPrompt,
		Tools:        writerTools,
		MaxTurns:     25,
	}

	editor := agentcore.SubAgentConfig{
		Name:         "editor",
		Description:  "全局审阅者：发现跨章结构问题，输出审阅结果",
		Model:        model,
		SystemPrompt: prompts.Editor,
		Tools:        editorTools,
		MaxTurns:     10,
	}

	subagentTool := agentcore.NewSubAgentTool(architectShort, architectMid, architectLong, writer, editor)

	agent := agentcore.NewAgent(
		agentcore.WithModel(model),
		agentcore.WithSystemPrompt(prompts.Coordinator),
		agentcore.WithTools(subagentTool, contextTool, askUser),
		agentcore.WithMaxTurns(60),
	)
	return agent, askUser
}
