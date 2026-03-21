package orchestrator

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/voocel/agentcore"
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
	log.Printf("[boot] provider=%s model=%s output=%s", cfg.Provider, cfg.ModelName, cfg.OutputDir)

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
	log.Printf("[boot] models: %s", models.Summary())

	// 3. 组装 Coordinator
	coordinator, askUser := BuildCoordinator(cfg, store, models, bundle)
	askUser.SetHandler(cliAskUserHandler)

	// 4. 确定性控制面：事件监听 + FollowUp 注入
	registerSubscription(coordinator, store, cfg.Provider, nil, nil, nil)

	// 5. 初始化运行元信息（保留已有 SteerHistory）
	if err := store.InitRunMeta(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		log.Printf("[warn] 初始化运行元信息失败: %v", err)
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
	recovery := determineRecovery(progress, runMeta)

	if recovery.IsNew {
		if err := store.InitProgress(cfg.NovelName, 0); err != nil {
			return fmt.Errorf("init progress: %w", err)
		}
		log.Printf("新建模式：%s", cfg.NovelName)
		promptText := fmt.Sprintf(
			"请创作一部小说，章节数量由你根据故事需要自行决定。若题材与冲突天然适合长篇连载，请优先规划为分层长篇结构，而不是压缩成短篇式梗概。要求如下：\n\n%s",
			cfg.Prompt,
		)
		if err := coordinator.Prompt(promptText); err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
	} else {
		log.Printf("%s", recovery.Label)
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
		log.Printf("创作完成：%d 章，共 %d 字，输出目录：%s",
			len(finalProgress.CompletedChapters), finalProgress.TotalWordCount, store.Dir())
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
			log.Printf("[tool:start] %s", ev.Tool)
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: ev.Tool + ".start", Level: "info"})
			}

		case agentcore.EventToolExecUpdate:
			// 区分流式 delta 和进度摘要
			if delta, ok := parseStreamDelta(ev); ok {
				if onDelta != nil {
					if text := subFilter.Feed(delta); text != "" {
						onDelta(text)
					}
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
			log.Printf("[progress] %s", summary)
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: summary, Level: "info"})
			}

		case agentcore.EventMessageStart:
			// 新一轮 LLM 输出开始，重置提取器 + 清空流式缓冲
			agentExt.Reset()
			taskExt.Reset()
			subFilter.Reset()
			if onClear != nil {
				onClear()
			}

		case agentcore.EventMessageUpdate:
			// Coordinator 的流式 token：先提取 agent 名称做标题，再提取 task 内容
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
				if detail != "" {
					log.Printf("[tool:error] %s → %s", ev.Tool, detail)
				} else {
					log.Printf("[tool:error] %s", ev.Tool)
				}
				if emit != nil {
					summary := ev.Tool + " 执行失败"
					if detail != "" {
						summary += "： " + truncateLog(detail, 80)
					}
					emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: summary, Level: "error"})
				}
				return
			}

			// subagent 结果：提取 usage 和 error，单独记录
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

			// novel_context：提取加载摘要替代原始 JSON
			if ev.Tool == "novel_context" {
				if summary := extractLoadingSummary(ev.Result); summary != "" {
					log.Printf("[tool:done] novel_context → %s", summary)
					if emit != nil {
						emit(UIEvent{Time: time.Now(), Category: "CONTEXT", Summary: summary, Level: "info"})
					}
				} else {
					log.Printf("[tool:done] novel_context → %s", truncateLog(string(ev.Result), 200))
				}
				if emit != nil {
					emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: "novel_context.done", Level: "info"})
				}
				break
			}

			// 其他工具：保持原样
			log.Printf("[tool:done] %s → %s", ev.Tool, truncateLog(string(ev.Result), 200))
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: ev.Tool + ".done", Level: "info"})
			}

		case agentcore.EventMessageEnd:
			if ev.Message != nil && ev.Message.GetRole() == agentcore.RoleAssistant {
				text := truncateLog(ev.Message.TextContent(), 300)
				log.Printf("[assistant] %s", text)
				if emit != nil {
					emit(UIEvent{Time: time.Now(), Category: "AGENT", Summary: truncateLog(ev.Message.TextContent(), 80), Level: "info"})
				}
			}

		case agentcore.EventError:
			log.Printf("[error][provider=%s] %v", provider, ev.Err)
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: fmt.Sprintf("[%s] %v", provider, ev.Err), Level: "error"})
			}
		}
	})
}

func planningTierGuidance(runMeta *domain.RunMeta) string {
	if runMeta == nil {
		return ""
	}
	switch runMeta.PlanningTier {
	case domain.PlanningTierShort:
		return "当前规划级别：short。如需调整设定或重做大纲，优先调用 architect_short。"
	case domain.PlanningTierMid:
		return "当前规划级别：mid。如需调整设定或重做大纲，优先调用 architect_mid。"
	case domain.PlanningTierLong:
		return "当前规划级别：long。如需调整设定或重做大纲，优先调用 architect_long，并保持分层大纲的一致性。"
	default:
		return ""
	}
}

// submitSteer 提交用户干预（CLI 和 Runtime 共用）。
func submitSteer(store *storepkg.Store, coordinator *agentcore.Agent, text string) {
	log.Printf("[steer] 用户干预: %s", text)
	if err := store.AppendSteerEntry(domain.SteerEntry{
		Input:     text,
		Timestamp: time.Now().Format(time.RFC3339),
	}); err != nil {
		log.Printf("[warn] 追加干预记录失败: %v", err)
	}
	if err := store.SetPendingSteer(text); err != nil {
		log.Printf("[warn] 设置待处理干预失败: %v", err)
	}
	if err := store.SetFlow(domain.FlowSteering); err != nil {
		log.Printf("[warn] 设置流程状态失败: %v", err)
	}
	runMeta, err := store.LoadRunMeta()
	if err != nil {
		log.Printf("[warn] 读取运行元信息失败: %v", err)
	}
	guidance := planningTierGuidance(runMeta)
	message := fmt.Sprintf("[用户干预] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。", text)
	if guidance != "" {
		message += "\n" + guidance
	}
	coordinator.Steer(agentcore.UserMsg(fmt.Sprintf(
		"%s", message)))
}

// recoveryResult 恢复链的判断结果。
type recoveryResult struct {
	PromptText string // 恢复时的 Prompt 文本
	Label      string // 恢复类型描述（供日志和 TUI 显示）
	IsNew      bool   // true 表示新建模式
}

// determineRecovery 根据 Progress 和 RunMeta 判断恢复类型和 Prompt 文本。
// 章节总数完全来自 Progress.TotalChapters（由大纲自动设定），不再由外部传入。
func determineRecovery(progress *domain.Progress, runMeta *domain.RunMeta) recoveryResult {
	if progress == nil {
		return recoveryResult{IsNew: true}
	}
	guidance := planningTierGuidance(runMeta)
	withGuidance := func(prompt string) string {
		if guidance == "" {
			return prompt
		}
		return prompt + "\n" + guidance
	}

	// 规划阶段崩溃：premise 或 outline 已保存但基础设定未完成
	if progress.Phase == domain.PhasePremise || progress.Phase == domain.PhaseOutline {
		return recoveryResult{
			PromptText: withGuidance(
				"上次在规划阶段中断。请调用 novel_context 检查当前基础设定状态，补全缺失的设定项（premise/outline/characters/world_rules），然后开始写作。"),
			Label: fmt.Sprintf("恢复：规划阶段（%s）", progress.Phase),
		}
	}

	if progress.InProgressChapter > 0 {
		ch := progress.InProgressChapter
		return recoveryResult{
			PromptText: withGuidance(fmt.Sprintf(
				"第 %d 章正在进行中，已有部分草稿。请调用 writer 继续完成该章（可用 read_chapter 读取已有草稿）。总共需要写 %d 章。",
				ch, progress.TotalChapters)),
			Label: fmt.Sprintf("恢复：第 %d 章进行中", ch),
		}
	}

	if len(progress.PendingRewrites) > 0 {
		verb := "重写"
		if progress.Flow == domain.FlowPolishing {
			verb = "打磨"
		}
		return recoveryResult{
			PromptText: withGuidance(fmt.Sprintf(
				"有 %d 章待%s（受影响章节：%v）。原因：%s。请逐章调用 writer %s后继续正常写作。总共需要写 %d 章。",
				len(progress.PendingRewrites), verb, progress.PendingRewrites, progress.RewriteReason, verb, progress.TotalChapters)),
			Label: fmt.Sprintf("%s恢复：%d 章待处理 %v", verb, len(progress.PendingRewrites), progress.PendingRewrites),
		}
	}

	if progress.Flow == domain.FlowReviewing {
		return recoveryResult{
			PromptText: withGuidance(fmt.Sprintf(
				"上次审阅中断，请重新调用 editor 对已完成章节进行全局审阅。已完成 %d 章，共 %d 字。总共需要写 %d 章。",
				len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters)),
			Label: "审阅恢复：上次审阅中断",
		}
	}

	// FlowSteering 但 PendingSteer 已丢失（崩溃导致不一致），重置为正常写作
	if progress.Flow == domain.FlowSteering && (runMeta == nil || runMeta.PendingSteer == "") {
		if progress.IsResumable() {
			next := progress.NextChapter()
			return recoveryResult{
				PromptText: withGuidance(fmt.Sprintf(
					"从第 %d 章继续写作。之前已完成 %d 章，共 %d 字。总共需要写 %d 章。",
					next, len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters)),
				Label: fmt.Sprintf("恢复模式：从第 %d 章继续（干预状态已重置）", next),
			}
		}
	}

	if progress.IsResumable() && runMeta != nil && runMeta.PendingSteer != "" {
		next := progress.NextChapter()
		return recoveryResult{
			PromptText: withGuidance(fmt.Sprintf(
				"从第 %d 章继续写作。之前已完成 %d 章，共 %d 字。总共需要写 %d 章。\n\n[用户干预-恢复] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。",
				next, len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters, runMeta.PendingSteer)),
			Label: "Steer 恢复：上次干预未完成，重新注入",
		}
	}

	if progress.IsResumable() {
		next := progress.NextChapter()
		return recoveryResult{
			PromptText: withGuidance(fmt.Sprintf(
				"从第 %d 章继续写作。之前已完成 %d 章，共 %d 字。总共需要写 %d 章。",
				next, len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters)),
			Label: fmt.Sprintf("恢复模式：从第 %d 章继续（已完成 %d 章，共 %d 字）",
				next, len(progress.CompletedChapters), progress.TotalWordCount),
		}
	}

	return recoveryResult{IsNew: true}
}


// parseStreamDelta 从 EventToolExecUpdate 中提取流式 delta 文本。
// 如果事件是 SubAgent 转发的 token delta（含 "delta" 字段），返回文本和 true。
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
	// subagent 的 thinking 更新属于高频内部推理，不适合刷到事件流面板。
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
		log.Printf("[tool:done] subagent → (empty)")
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
		log.Printf("[tool:done] subagent → %s", truncateLog(string(result), 200))
		return
	}

	// 记录 usage
	u := data.Usage
	log.Printf("[usage] input=%d output=%d cache_read=%d turns=%d tools=%d",
		u.Input, u.Output, u.CacheRead, u.Turns, u.Tools)

	if data.Error != "" {
		log.Printf("[subagent:error] %s", data.Error)
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "ERROR",
				Summary: "subagent: " + truncateLog(data.Error, 80), Level: "error"})
		}
		return
	}

	log.Printf("[tool:done] subagent → %s", truncateLog(data.Output, 200))
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

// agentLabel 将内部 agent 名称映射为用户友好的标签。
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

func clearHandledSteer(store *storepkg.Store) {
	if err := store.ClearHandledSteer(); err != nil {
		log.Printf("[host] 清除干预状态失败: %v", err)
	}
}

// flushPendingSteer 清除干预状态，如果有未处理的干预则追加 FollowUp 提醒 Coordinator。
// 用于 SubAgent 完成后，确保用户干预不会被 Host 的系统 FollowUp 淹没。
func flushPendingSteer(store *storepkg.Store, coordinator *agentcore.Agent, emit emitFn) {
	meta, _ := store.LoadRunMeta()
	if meta != nil && meta.PendingSteer != "" {
		log.Printf("[host] 检测到未处理的用户干预，追加提醒：%s", meta.PendingSteer)
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
				Summary: "提醒 Coordinator 处理用户干预", Level: "info"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统-重要] 用户在写作期间提交了干预指令：「%s」。请优先处理此干预（可能需要修改设定或重写章节），然后再继续后续写作。",
			meta.PendingSteer)))
	}
	clearHandledSteer(store)
}

func finalizeSteerIfIdle(store *storepkg.Store) {
	runMeta, _ := store.LoadRunMeta()
	progress, _ := store.LoadProgress()
	if runMeta == nil || runMeta.PendingSteer == "" || progress == nil {
		return
	}
	if progress.Flow != domain.FlowSteering {
		return
	}
	clearHandledSteer(store)
}

