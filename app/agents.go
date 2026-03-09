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

	// Editor SubAgent 工具（V1）
	editorTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveReviewTool(store),
	}

	architect := agentcore.SubAgentConfig{
		Name:         "architect",
		Description:  "世界构建师：生成小说前提、大纲和角色档案",
		Model:        model,
		SystemPrompt: prompts.Architect,
		Tools:        architectTools,
		MaxTurns:     10,
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

	subagentTool := agentcore.NewSubAgentTool(architect, writer, editor)

	agent := agentcore.NewAgent(
		agentcore.WithModel(model),
		agentcore.WithSystemPrompt(prompts.Coordinator),
		agentcore.WithTools(subagentTool, contextTool, askUser),
		agentcore.WithMaxTurns(60),
	)
	return agent, askUser
}
