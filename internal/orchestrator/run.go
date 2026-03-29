package orchestrator

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/memory"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// emitFn 是可选的 UIEvent 发射回调，用于向 TUI 转发结构化事件。
// CLI 模式下为 nil，Runtime 模式下指向 events channel。
type emitFn func(UIEvent)

// deltaFn 是可选的流式 token 回调，用于向 TUI 转发 LLM 生成的文字。
type deltaFn func(delta string)

// clearFn 是可选的流式缓冲清空回调，在新一轮 LLM 输出开始时触发。
type clearFn func()

// Run 启动小说创作流程（CLI 模式，阻塞直到完成）。
func Run(cfg bootstrap.Config, bundle assets.Bundle) error {
	cfg.FillDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	slog.Info("启动", "module", "boot", "provider", cfg.Provider, "model", cfg.ModelName, "output", cfg.OutputDir)

	// 1. 初始化状态
	store := storepkg.NewStore(cfg.OutputDir)
	if err := store.Init(); err != nil {
		return fmt.Errorf("init store: %w", err)
	}

	// 1.5 日志写入文件（CLI 模式同时输出到 stderr 和日志文件）
	if cleanup := setupFileLogger(store.Dir()); cleanup != nil {
		defer cleanup()
	}

	// 1.6 清理上次崩溃可能遗留的信号文件
	store.Signals.ClearStaleSignals()

	// 2. 创建模型集合
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return fmt.Errorf("create models: %w", err)
	}
	slog.Info("模型就绪", "module", "boot", "summary", models.Summary())

	// 3. 组装 Coordinator
	coordinator, askUser := BuildCoordinator(cfg, store, models, bundle, nil)
	askUser.SetHandler(cliAskUserHandler)
	sess := newSession(coordinator, store, cfg.Provider, nil, nil, nil)

	// 4. 确定性控制面：事件监听 + FollowUp 注入
	sess.bind()

	// 5. 初始化运行元信息（保留已有 SteerHistory）
	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		slog.Error("初始化运行元信息失败", "module", "boot", "err", err)
	}

	// 6. Steer 协程：stdin 读取用户干预
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text == "" {
				continue
			}
			sess.submitSteer(text)
		}
	}()

	// 7. 恢复或启动
	recovery := sess.recovery()

	if recovery.IsNew {
		if err := store.Progress.Init(cfg.NovelName, 0); err != nil {
			return fmt.Errorf("init progress: %w", err)
		}
		slog.Info("新建模式", "module", "boot", "novel", cfg.NovelName)
		promptText := fmt.Sprintf(
			"请创作一部小说，章节数量由你根据故事需要自行决定。若题材与冲突天然适合长篇连载，请优先规划为分层长篇结构，而不是压缩成短篇式梗概。要求如下：\n\n%s",
			cfg.Prompt,
		)
		if err := coordinator.Prompt(promptText); err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
	} else {
		slog.Info("恢复模式", "module", "boot", "label", recovery.Label)
		if err := coordinator.Prompt(recovery.PromptText); err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
	}

	// 8. 等待完成
	coordinator.WaitForIdle()
	sess.finalizeSteerIfIdle()

	// 9. 输出结果
	finalProgress, _ := store.Progress.Load()
	if finalProgress != nil {
		slog.Info("创作完成", "module", "boot",
			"chapters", len(finalProgress.CompletedChapters),
			"words", finalProgress.TotalWordCount,
			"output", store.Dir())
	}
	return nil
}

// parseSubAgentRetry 从 EventToolExecUpdate 中提取 SubAgent 转发的重试信息。
func parseSubAgentRetry(ev agentcore.Event) (string, bool) {
	if ev.Progress == nil || ev.Progress.Kind != agentcore.ProgressRetry {
		return "", false
	}
	msg := truncateLog(ev.Progress.Message, 80)
	return fmt.Sprintf("%s 重试 (%d/%d): %s", ev.Progress.Agent, ev.Progress.Attempt, ev.Progress.MaxRetries, msg), true
}

// parseStreamDelta 从 EventToolExecUpdate 中提取流式 delta 文本。
func parseStreamDelta(ev agentcore.Event) (string, bool) {
	if ev.Progress == nil || ev.Progress.Kind != agentcore.ProgressToolDelta || ev.Progress.Delta == "" {
		return "", false
	}
	return ev.Progress.Delta, true
}

// parseProgressSummary 从 EventToolExecUpdate 中提取可读摘要。
func parseProgressSummary(ev agentcore.Event) string {
	if ev.Progress == nil {
		return "progress"
	}
	if summary := summarizeStructuredProgress(ev.Progress); summary != "" || ev.Progress.Kind == agentcore.ProgressThinking {
		return summary
	}
	return "progress"
}

func summarizeStructuredProgress(progress *agentcore.ProgressPayload) string {
	if progress == nil {
		return ""
	}
	switch progress.Kind {
	case agentcore.ProgressThinking:
		return ""
	case agentcore.ProgressToolStart:
		if progress.Tool != "" {
			return fmt.Sprintf("%s → %s", progress.Agent, progress.Tool)
		}
	case agentcore.ProgressToolError:
		if progress.Tool != "" {
			if progress.Message != "" {
				return fmt.Sprintf("%s → %s (error: %s)", progress.Agent, progress.Tool, truncateLog(progress.Message, 120))
			}
			return fmt.Sprintf("%s → %s (error)", progress.Agent, progress.Tool)
		}
	case agentcore.ProgressTurnCounter:
		if progress.Agent != "" && progress.Turn > 0 {
			return fmt.Sprintf("%s turn %d", progress.Agent, progress.Turn)
		}
	case agentcore.ProgressSummary:
		if progress.Summary != "" {
			return progress.Summary
		}
	}
	return ""
}

func parseToolProgress(ev agentcore.Event) (toolProgress, bool) {
	if ev.Progress == nil {
		return toolProgress{}, false
	}
	switch ev.Progress.Kind {
	case agentcore.ProgressToolStart:
		if ev.Progress.Tool == "" {
			return toolProgress{}, false
		}
		return toolProgress{
			Agent:   ev.Progress.Agent,
			Tool:    ev.Progress.Tool,
			Message: ev.Progress.Message,
		}, true
	case agentcore.ProgressToolError:
		if ev.Progress.Tool == "" {
			return toolProgress{}, false
		}
		return toolProgress{
			Agent:   ev.Progress.Agent,
			Tool:    ev.Progress.Tool,
			Error:   true,
			Message: ev.Progress.Message,
		}, true
	default:
		return toolProgress{}, false
	}
}

// extractLoadingSummary 从 novel_context 的返回 JSON 中提取 _loading_summary 字段。
func extractLoadingSummary(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var data struct {
		Summary string `json:"_loading_summary"`
	}
	if err := json.Unmarshal(result, &data); err != nil {
		return ""
	}
	return data.Summary
}

// logSubAgentResult 从 subagent 结果中提取 usage 和 error，分别记录结构化日志。
func logSubAgentResult(result json.RawMessage, emit emitFn) {
	if len(result) == 0 {
		slog.Debug("subagent 返回空结果", "module", "tool")
		return
	}
	var data struct {
		Output string `json:"output"`
		Error  string `json:"error"`
		Usage  struct {
			Input      int     `json:"input"`
			Output     int     `json:"output"`
			CacheRead  int     `json:"cache_read"`
			CacheWrite int     `json:"cache_write"`
			Cost       float64 `json:"cost"`
			Turns      int     `json:"turns"`
			Tools      int     `json:"tools"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(result, &data); err != nil {
		slog.Debug("subagent 结果解析失败", "module", "tool", "raw", truncateLog(string(result), 200))
		return
	}

	u := data.Usage
	slog.Info("subagent usage", "module", "tool",
		"input", u.Input, "output", u.Output,
		"cache_read", u.CacheRead, "turns", u.Turns, "tools", u.Tools)

	if data.Error != "" {
		slog.Error("subagent 错误", "module", "tool", "err", data.Error)
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "ERROR",
				Summary: "subagent: " + truncateLog(data.Error, 80), Level: "error"})
		}
		return
	}

	slog.Debug("subagent 完成", "module", "tool", "output", truncateLog(data.Output, 200))
	if emit != nil {
		emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: "subagent.done", Level: "info"})
	}
}

func extractToolErrorText(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(result, &plain); err == nil {
		return plain
	}
	var obj struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(result, &obj); err == nil {
		switch {
		case obj.Error != "":
			return obj.Error
		case obj.Message != "":
			return obj.Message
		case obj.Detail != "":
			return obj.Detail
		}
	}
	return truncateLog(string(result), 160)
}

func agentLabel(name string) string {
	switch name {
	case "architect_short", "architect_mid", "architect_long":
		return "Architect 规划中"
	case "writer":
		return "Writer 创作中"
	case "editor":
		return "Editor 审阅中"
	default:
		return name
	}
}

func truncateLog(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// compactionCallback 创建上下文压缩的可观测回调，用于 slog 日志和 TUI 事件。
func compactionCallback(agent string, emit emitFn) func(memory.CompactionInfo) {
	return func(info memory.CompactionInfo) {
		slog.Warn("上下文压缩", "module", "compaction", "agent", agent,
			"tokens_before", info.TokensBefore, "tokens_after", info.TokensAfter,
			"msgs_before", info.MessagesBefore, "msgs_after", info.MessagesAfter,
			"compacted", info.CompactedCount, "kept", info.KeptCount,
			"split_turn", info.IsSplitTurn, "incremental", info.IsIncremental,
			"summary_runes", info.SummaryLen, "duration_ms", info.Duration.Milliseconds())

		if emit == nil {
			return
		}
		ratio := 0
		if info.TokensBefore > 0 {
			ratio = info.TokensAfter * 100 / info.TokensBefore
		}
		summary := fmt.Sprintf("%s 压缩: %d→%d tok (%d%%) %d→%d msgs 摘要%d字 耗时%s",
			agent, info.TokensBefore, info.TokensAfter, ratio,
			info.MessagesBefore, info.MessagesAfter,
			info.SummaryLen, info.Duration.Round(time.Millisecond))
		if info.IsSplitTurn {
			summary += " [split]"
		}
		if info.IsIncremental {
			summary += " [增量]"
		}
		emit(UIEvent{Time: time.Now(), Category: "COMPACT", Summary: summary, Level: "warn"})
	}
}
