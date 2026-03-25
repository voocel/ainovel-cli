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
	"github.com/voocel/ainovel-cli/internal/domain"
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
	store.ClearStaleSignals()

	// 2. 创建模型集合
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return fmt.Errorf("create models: %w", err)
	}
	slog.Info("模型就绪", "module", "boot", "summary", models.Summary())

	// 3. 组装 Coordinator
	coordinator, askUser := BuildCoordinator(cfg, store, models, bundle, nil)
	askUser.SetHandler(cliAskUserHandler)

	// 4. 确定性控制面：事件监听 + FollowUp 注入
	registerSubscription(coordinator, store, cfg.Provider, nil, nil, nil)

	// 5. 初始化运行元信息（保留已有 SteerHistory）
	if err := store.InitRunMeta(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
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
			submitSteer(store, coordinator, text)
		}
	}()

	// 7. 恢复或启动
	progress, _ := store.LoadProgress()
	runMeta, _ := store.LoadRunMeta()
	recovery := determineRecovery(progress, runMeta, store)

	if recovery.IsNew {
		if err := store.InitProgress(cfg.NovelName, 0); err != nil {
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
	finalizeSteerIfIdle(store)

	// 9. 输出结果
	finalProgress, _ := store.LoadProgress()
	if finalProgress != nil {
		slog.Info("创作完成", "module", "boot",
			"chapters", len(finalProgress.CompletedChapters),
			"words", finalProgress.TotalWordCount,
			"output", store.Dir())
	}
	return nil
}

// registerSubscription 注册 coordinator 事件订阅，包含确定性控制和可选的 UIEvent/Delta 转发。
func registerSubscription(coordinator *agentcore.Agent, store *storepkg.Store, provider string, emit emitFn, onDelta deltaFn, onClear clearFn) {
	var lastProgressSummary string
	agentExt := newFieldExtractor("agent")  // Coordinator → subagent 目标 agent 名称
	taskExt := newFieldExtractor("task")    // Coordinator → subagent 调度指令
	subFilter := newStreamFilter("content") // SubAgent：文本透传 + JSON 提取 content

	coordinator.Subscribe(func(ev agentcore.Event) {
		switch ev.Type {
		case agentcore.EventToolExecStart:
			slog.Debug("工具开始", "module", "tool", "name", ev.Tool)
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: ev.Tool + ".start", Level: "info"})
			}

		case agentcore.EventToolExecUpdate:
			if delta, ok := parseStreamDelta(ev); ok {
				if onDelta != nil {
					if text := subFilter.Feed(delta); text != "" {
						onDelta(text)
					}
				}
				return
			}
			// SubAgent 重试事件：独立处理，用 warn 级别
			if retry, ok := parseSubAgentRetry(ev); ok {
				slog.Warn("SubAgent 重试", "module", "tool", "summary", retry)
				if emit != nil {
					emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: retry, Level: "warn"})
				}
				return
			}
			summary := parseProgressSummary(ev)
			if summary == "" {
				return
			}
			if summary == lastProgressSummary {
				return
			}
			lastProgressSummary = summary
			slog.Debug("进度", "module", "tool", "summary", summary)
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: summary, Level: "info"})
			}

		case agentcore.EventMessageStart:
			agentExt.Reset()
			taskExt.Reset()
			subFilter.Reset()
			if onClear != nil {
				onClear()
			}

		case agentcore.EventMessageUpdate:
			if ev.Delta != "" && onDelta != nil {
				if name := agentExt.Feed(ev.Delta); name != "" {
					onDelta("\n▸ " + agentLabel(name) + "\n")
				}
				if text := taskExt.Feed(ev.Delta); text != "" {
					onDelta(text)
				}
			}

		case agentcore.EventToolExecEnd:
			lastProgressSummary = ""
			if ev.IsError {
				detail := extractToolErrorText(ev.Result)
				slog.Error("工具执行失败", "module", "tool", "name", ev.Tool, "detail", truncateLog(detail, 120))
				if emit != nil {
					summary := ev.Tool + " 执行失败"
					if detail != "" {
						summary += ": " + truncateLog(detail, 80)
					}
					emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: summary, Level: "error"})
				}
				return
			}

			if ev.Tool == "subagent" {
				logSubAgentResult(ev.Result, emit)
				handleFoundationCheck(coordinator, store, emit)
				committed := handleSubAgentDone(coordinator, store, emit)
				if !committed {
					handleUncommittedDraft(coordinator, store, emit)
				}
				handleEditorDone(coordinator, store, emit)
				break
			}

			if ev.Tool == "novel_context" {
				if summary := extractLoadingSummary(ev.Result); summary != "" {
					slog.Info("上下文加载", "module", "tool", "summary", summary)
					if emit != nil {
						emit(UIEvent{Time: time.Now(), Category: "CONTEXT", Summary: summary, Level: "info"})
					}
				} else {
					slog.Debug("上下文加载", "module", "tool", "result", truncateLog(string(ev.Result), 200))
				}
				if emit != nil {
					emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: "novel_context.done", Level: "info"})
				}
				break
			}

			slog.Debug("工具完成", "module", "tool", "name", ev.Tool, "result", truncateLog(string(ev.Result), 200))
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: ev.Tool + ".done", Level: "info"})
			}

		case agentcore.EventMessageEnd:
			if ev.Message != nil && ev.Message.GetRole() == agentcore.RoleAssistant {
				slog.Debug("assistant", "module", "agent", "text", truncateLog(ev.Message.TextContent(), 100))
				if emit != nil {
					emit(UIEvent{Time: time.Now(), Category: "AGENT", Summary: truncateLog(ev.Message.TextContent(), 80), Level: "info"})
				}
			}

		case agentcore.EventError:
			slog.Error("provider 错误", "module", "agent", "provider", provider, "err", ev.Err)
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: fmt.Sprintf("[%s] %v", provider, ev.Err), Level: "error"})
			}

		case agentcore.EventRetry:
			if ev.RetryInfo != nil {
				slog.Warn("重试", "module", "agent", "attempt", ev.RetryInfo.Attempt,
					"max", ev.RetryInfo.MaxRetries, "err", ev.RetryInfo.Err)
				if emit != nil {
					emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
						Summary: fmt.Sprintf("重试 (%d/%d): %v", ev.RetryInfo.Attempt, ev.RetryInfo.MaxRetries, ev.RetryInfo.Err),
						Level:   "warn"})
				}
			}
		}
	})
}

// persistSteer 将干预持久化到 store（不触发 coordinator）。
func persistSteer(store *storepkg.Store, text string) {
	slog.Info("用户干预", "module", "steer", "text", text)
	if err := store.AppendSteerEntry(domain.SteerEntry{
		Input:     text,
		Timestamp: time.Now().Format(time.RFC3339),
	}); err != nil {
		slog.Error("追加干预记录失败", "module", "steer", "err", err)
	}
	if err := store.SetPendingSteer(text); err != nil {
		slog.Error("设置待处理干预失败", "module", "steer", "err", err)
	}
	if err := store.SetFlow(domain.FlowSteering); err != nil {
		slog.Error("设置流程状态失败", "module", "steer", "err", err)
	}
}

// submitSteer 提交用户干预（CLI 和 Runtime 共用，仅在 agent 循环运行中时调用）。
func submitSteer(store *storepkg.Store, coordinator *agentcore.Agent, text string) {
	persistSteer(store, text)
	runMeta, err := store.LoadRunMeta()
	if err != nil {
		slog.Warn("读取运行元信息失败", "module", "steer", "err", err)
	}
	guidance := planningTierGuidance(runMeta)
	message := fmt.Sprintf("[用户干预] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。", text)
	if guidance != "" {
		message += "\n" + guidance
	}
	coordinator.Steer(agentcore.UserMsg(message))
}

// parseSubAgentRetry 从 EventToolExecUpdate 中提取 SubAgent 转发的重试信息。
func parseSubAgentRetry(ev agentcore.Event) (string, bool) {
	if len(ev.Result) == 0 {
		return "", false
	}
	var data struct {
		Agent      string `json:"agent"`
		Retry      bool   `json:"retry"`
		Attempt    int    `json:"attempt"`
		MaxRetries int    `json:"max_retries"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(ev.Result, &data); err != nil || !data.Retry {
		return "", false
	}
	msg := truncateLog(data.Error, 80)
	return fmt.Sprintf("%s 重试 (%d/%d): %s", data.Agent, data.Attempt, data.MaxRetries, msg), true
}

// parseStreamDelta 从 EventToolExecUpdate 中提取流式 delta 文本。
func parseStreamDelta(ev agentcore.Event) (string, bool) {
	if len(ev.Result) == 0 {
		return "", false
	}
	var data struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(ev.Result, &data); err != nil {
		return "", false
	}
	if data.Delta != "" {
		return data.Delta, true
	}
	return "", false
}

// parseProgressSummary 从 EventToolExecUpdate 中提取可读摘要。
func parseProgressSummary(ev agentcore.Event) string {
	if len(ev.Result) == 0 {
		return "progress"
	}
	var data struct {
		Agent    string `json:"agent"`
		Tool     string `json:"tool"`
		Turn     int    `json:"turn"`
		Error    bool   `json:"error"`
		Message  string `json:"message"`
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal(ev.Result, &data); err != nil {
		return truncateLog(string(ev.Result), 60)
	}
	if data.Thinking != "" && data.Tool == "" {
		return ""
	}
	if data.Tool != "" {
		if data.Error {
			if data.Message != "" {
				return fmt.Sprintf("%s → %s (error: %s)", data.Agent, data.Tool, truncateLog(data.Message, 120))
			}
			return fmt.Sprintf("%s → %s (error)", data.Agent, data.Tool)
		}
		return fmt.Sprintf("%s → %s", data.Agent, data.Tool)
	}
	if data.Turn > 0 {
		return fmt.Sprintf("%s turn %d", data.Agent, data.Turn)
	}
	return truncateLog(string(ev.Result), 60)
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
