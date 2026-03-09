package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/domain"
	"github.com/voocel/ainovel-cli/state"
	"github.com/voocel/ainovel-cli/tools"
)

// emitFn 是可选的 UIEvent 发射回调，用于向 TUI 转发结构化事件。
// CLI 模式下为 nil，Runtime 模式下指向 events channel。
type emitFn func(UIEvent)

// deltaFn 是可选的流式 token 回调，用于向 TUI 转发 LLM 生成的文字。
type deltaFn func(delta string)

// clearFn 是可选的流式缓冲清空回调，在新一轮 LLM 输出开始时触发。
type clearFn func()

// Run 启动小说创作流程（CLI 模式，阻塞直到完成）。
func Run(cfg Config, refs tools.References, prompts Prompts, styles map[string]string) error {
	cfg.FillDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}

	// 1. 初始化状态
	store := state.NewStore(cfg.OutputDir)
	if err := store.Init(); err != nil {
		return fmt.Errorf("init store: %w", err)
	}

	// 2. 创建模型
	model, err := createModel(cfg)
	if err != nil {
		return fmt.Errorf("create model: %w", err)
	}

	// 3. 组装 Coordinator
	coordinator, askUser := BuildCoordinator(cfg, store, model, refs, prompts, styles)
	askUser.SetHandler(cliAskUserHandler)

	// 4. 确定性控制面：事件监听 + FollowUp 注入
	registerSubscription(coordinator, store, nil, nil, nil)

	// 5. 初始化运行元信息（保留已有 SteerHistory）
	if err := store.InitRunMeta(cfg.Style, cfg.ModelName); err != nil {
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
		promptText := fmt.Sprintf("请创作一部小说，章节数量由你根据故事需要自行决定。要求如下：\n\n%s", cfg.Prompt)
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
func registerSubscription(coordinator *agentcore.Agent, store *state.Store, emit emitFn, onDelta deltaFn, onClear clearFn) {
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
					onDelta(delta)
				}
				return
			}
			summary := parseProgressSummary(ev)
			log.Printf("[progress] %s", summary)
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: summary, Level: "info"})
			}

		case agentcore.EventMessageStart:
			// 新一轮 LLM 输出开始，清空流式缓冲
			if onClear != nil {
				onClear()
			}

		case agentcore.EventMessageUpdate:
			// Coordinator 自身思考时的流式 token
			if ev.Delta != "" && onDelta != nil {
				onDelta(ev.Delta)
			}

		case agentcore.EventToolExecEnd:
			if ev.IsError {
				log.Printf("[tool:error] %s", ev.Tool)
				if emit != nil {
					emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: ev.Tool + " 执行失败", Level: "error"})
				}
				return
			}
			log.Printf("[tool:done] %s → %s", ev.Tool, truncateLog(string(ev.Result), 200))
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: ev.Tool + ".done", Level: "info"})
			}

			if ev.Tool == "subagent" {
				handleSubAgentDone(coordinator, store, emit)
				handleEditorDone(coordinator, store, emit)
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
			log.Printf("[error] %v", ev.Err)
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: fmt.Sprintf("%v", ev.Err), Level: "error"})
			}
		}
	})
}

// submitSteer 提交用户干预（CLI 和 Runtime 共用）。
func submitSteer(store *state.Store, coordinator *agentcore.Agent, text string) {
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
	coordinator.Steer(agentcore.UserMsg(fmt.Sprintf(
		"[用户干预] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。", text)))
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

	if progress.InProgressChapter > 0 {
		ch := progress.InProgressChapter
		scenes := len(progress.CompletedScenes)
		return recoveryResult{
			PromptText: fmt.Sprintf(
				"第 %d 章正在进行中，已完成 %d 个场景。请调用 writer 从场景 %d 继续写作。总共需要写 %d 章。",
				ch, scenes, scenes+1, progress.TotalChapters),
			Label: fmt.Sprintf("场景级恢复：第 %d 章已完成 %d 个场景", ch, scenes),
		}
	}

	if len(progress.PendingRewrites) > 0 {
		verb := "重写"
		if progress.Flow == domain.FlowPolishing {
			verb = "打磨"
		}
		return recoveryResult{
			PromptText: fmt.Sprintf(
				"有 %d 章待%s（受影响章节：%v）。原因：%s。请逐章调用 writer %s后继续正常写作。总共需要写 %d 章。",
				len(progress.PendingRewrites), verb, progress.PendingRewrites, progress.RewriteReason, verb, progress.TotalChapters),
			Label: fmt.Sprintf("%s恢复：%d 章待处理 %v", verb, len(progress.PendingRewrites), progress.PendingRewrites),
		}
	}

	if progress.Flow == domain.FlowReviewing {
		return recoveryResult{
			PromptText: fmt.Sprintf(
				"上次审阅中断，请重新调用 editor 对已完成章节进行全局审阅。已完成 %d 章，共 %d 字。总共需要写 %d 章。",
				len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters),
			Label: "审阅恢复：上次审阅中断",
		}
	}

	if progress.IsResumable() && runMeta != nil && runMeta.PendingSteer != "" {
		next := progress.NextChapter()
		return recoveryResult{
			PromptText: fmt.Sprintf(
				"从第 %d 章继续写作。之前已完成 %d 章，共 %d 字。总共需要写 %d 章。\n\n[用户干预-恢复] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。",
				next, len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters, runMeta.PendingSteer),
			Label: "Steer 恢复：上次干预未完成，重新注入",
		}
	}

	if progress.IsResumable() {
		next := progress.NextChapter()
		return recoveryResult{
			PromptText: fmt.Sprintf(
				"从第 %d 章继续写作。之前已完成 %d 章，共 %d 字。总共需要写 %d 章。",
				next, len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters),
			Label: fmt.Sprintf("恢复模式：从第 %d 章继续（已完成 %d 章，共 %d 字）",
				next, len(progress.CompletedChapters), progress.TotalWordCount),
		}
	}

	return recoveryResult{IsNew: true}
}

// handleSubAgentDone 在每次 SubAgent 调用完成后读取文件系统信号，注入确定性任务。
func handleSubAgentDone(coordinator *agentcore.Agent, store *state.Store, emit emitFn) {
	result, err := store.LoadLastCommit()
	if err != nil || result == nil {
		return
	}
	if err := store.ClearLastCommit(); err != nil {
		log.Printf("[host] 清除 commit 信号失败: %v", err)
	}

	log.Printf("[host] 章节提交信号：第 %d 章，%d 字，%d 个场景",
		result.Chapter, result.WordCount, result.SceneCount)
	if emit != nil {
		emit(UIEvent{
			Time:     time.Now(),
			Category: "SYSTEM",
			Summary:  fmt.Sprintf("第 %d 章已提交：%d 字，%d 个场景", result.Chapter, result.WordCount, result.SceneCount),
			Level:    "success",
		})
	}

	// 确定性判断 0：正在重写/打磨流程中
	progress, _ := store.LoadProgress()
	if progress != nil && (progress.Flow == domain.FlowRewriting || progress.Flow == domain.FlowPolishing) {
		if !slices.Contains(progress.PendingRewrites, result.Chapter) {
			log.Printf("[host] 警告：重写期间提交了非队列章节 %d，拒绝并提醒", result.Chapter)
			coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
				"[系统] 当前处于重写流程，但提交了非队列章节（第 %d 章）。请先完成待重写章节 %v 后再继续新章节。",
				result.Chapter, progress.PendingRewrites)))
			return
		}
		if err := store.CompleteRewrite(result.Chapter); err != nil {
			log.Printf("[host] 完成重写标记失败: %v", err)
		}
		clearHandledSteer(store)
		updated, _ := store.LoadProgress()
		if updated != nil && len(updated.PendingRewrites) == 0 {
			log.Printf("[host] 所有重写/打磨已完成，恢复正常写作")
			saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
			saveCheckpoint(store, "rewrite-done")
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: "所有重写/打磨已完成", Level: "success"})
			}
		} else if updated != nil {
			log.Printf("[host] 还有 %d 章待处理：%v", len(updated.PendingRewrites), updated.PendingRewrites)
			saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
		}
		return
	}

	// 确定性判断 1：全书完成（TotalChapters 由大纲自动设定）
	totalChapters := 0
	if progress != nil {
		totalChapters = progress.TotalChapters
	}
	if totalChapters > 0 && result.NextChapter > totalChapters {
		log.Printf("[host] 所有 %d 章已完成，注入完成指令", totalChapters)
		if err := store.MarkComplete(); err != nil {
			log.Printf("[host] 标记完成失败: %v", err)
		}
		clearHandledSteer(store)
		saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: fmt.Sprintf("全部 %d 章已完成", totalChapters), Level: "success"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统] 全部 %d 章已写完。请总结全书并结束。不要再调用 writer。",
			totalChapters)))
		return
	}

	// 确定性判断 2：需要全局审阅
	if result.ReviewRequired {
		log.Printf("[host] review_required=true（%s），注入审阅指令", result.ReviewReason)
		if err := store.SetFlow(domain.FlowReviewing); err != nil {
			log.Printf("[host] 设置审阅流程失败: %v", err)
		}
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: "review_required=true " + result.ReviewReason, Level: "warn"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统] review_required=true，%s。请调用 editor 对已完成章节进行全局审阅，然后根据审阅结果决定继续写第 %d 章还是修正已有章节。",
			result.ReviewReason, result.NextChapter)))
	}
	clearHandledSteer(store)
	saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
}

// handleEditorDone 在 Editor SubAgent 完成后读取审阅信号。
func handleEditorDone(coordinator *agentcore.Agent, store *state.Store, emit emitFn) {
	review, err := store.LoadLastReviewSignal()
	if err != nil {
		log.Printf("[host] 加载审阅信号失败: %v", err)
		return
	}
	if review == nil {
		return
	}
	if err := store.ClearLastReview(); err != nil {
		log.Printf("[host] 清除审阅信号失败: %v", err)
	}

	log.Printf("[host] 审阅信号：verdict=%s，%d 个问题", review.Verdict, len(review.Issues))

	chaptersInfo := ""
	if len(review.AffectedChapters) > 0 {
		chaptersInfo = fmt.Sprintf("受影响章节：%v。", review.AffectedChapters)
	}

	switch review.Verdict {
	case "rewrite":
		if err := store.SetPendingRewrites(review.AffectedChapters, review.Summary); err != nil {
			log.Printf("[host] 设置重写队列失败: %v", err)
		}
		if err := store.SetFlow(domain.FlowRewriting); err != nil {
			log.Printf("[host] 设置流程状态失败: %v", err)
		}
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "REVIEW",
				Summary: fmt.Sprintf("verdict=rewrite affected=%v", review.AffectedChapters), Level: "warn"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统] Editor 审阅结论：rewrite。%s%s请逐章调用 writer 重写受影响章节，全部完成后继续正常写作。",
			review.Summary, chaptersInfo)))
	case "polish":
		if err := store.SetPendingRewrites(review.AffectedChapters, review.Summary); err != nil {
			log.Printf("[host] 设置打磨队列失败: %v", err)
		}
		if err := store.SetFlow(domain.FlowPolishing); err != nil {
			log.Printf("[host] 设置流程状态失败: %v", err)
		}
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "REVIEW",
				Summary: fmt.Sprintf("verdict=polish affected=%v", review.AffectedChapters), Level: "warn"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统] Editor 审阅结论：polish。%s%s请逐章调用 writer 打磨受影响章节，全部完成后继续正常写作。",
			review.Summary, chaptersInfo)))
	default:
		if err := store.SetFlow(domain.FlowWriting); err != nil {
			log.Printf("[host] 清除审阅状态失败: %v", err)
		}
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "REVIEW", Summary: "verdict=accept 审阅通过", Level: "success"})
		}
	}
	clearHandledSteer(store)
	saveCheckpoint(store, fmt.Sprintf("review-ch%02d-%s", review.Chapter, review.Verdict))
	if emit != nil {
		emit(UIEvent{Time: time.Now(), Category: "CHECK",
			Summary: fmt.Sprintf("saved review-ch%02d-%s", review.Chapter, review.Verdict), Level: "info"})
	}
}

func saveCheckpoint(store *state.Store, label string) {
	if err := store.SaveCheckpoint(label); err != nil {
		log.Printf("[host] 保存检查点失败: %v", err)
	}
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
		Agent string `json:"agent"`
		Tool  string `json:"tool"`
		Turn  int    `json:"turn"`
		Error bool   `json:"error"`
	}
	if err := json.Unmarshal(ev.Result, &data); err != nil {
		return truncateLog(string(ev.Result), 60)
	}
	if data.Tool != "" {
		if data.Error {
			return fmt.Sprintf("%s → %s (error)", data.Agent, data.Tool)
		}
		return fmt.Sprintf("%s → %s", data.Agent, data.Tool)
	}
	if data.Turn > 0 {
		return fmt.Sprintf("%s turn %d", data.Agent, data.Turn)
	}
	return truncateLog(string(ev.Result), 60)
}

func truncateLog(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func clearHandledSteer(store *state.Store) {
	if err := store.ClearPendingSteer(); err != nil {
		log.Printf("[host] 清除待处理干预失败: %v", err)
	}
	progress, _ := store.LoadProgress()
	if progress != nil && progress.Flow == domain.FlowSteering {
		if err := store.SetFlow(domain.FlowWriting); err != nil {
			log.Printf("[host] 重置流程状态失败: %v", err)
		}
	}
}

func finalizeSteerIfIdle(store *state.Store) {
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

// createModel 根据 provider 创建对应的 LLM 模型。
func createModel(cfg Config) (agentcore.ChatModel, error) {
	var baseURL []string
	if cfg.BaseURL != "" {
		baseURL = append(baseURL, cfg.BaseURL)
	}
	switch cfg.Provider {
	case "anthropic":
		return llm.NewAnthropicModel(cfg.ModelName, cfg.APIKey, baseURL...)
	case "gemini":
		return llm.NewGeminiModel(cfg.ModelName, cfg.APIKey, baseURL...)
	default:
		return llm.NewOpenAIModel(cfg.ModelName, cfg.APIKey, baseURL...)
	}
}

// cliAskUserHandler 是 CLI 模式下的交互式选择器，上下键选择，回车确认。
func cliAskUserHandler(_ context.Context, questions []tools.Question) (*tools.AskUserResponse, error) {
	resp := &tools.AskUserResponse{
		Answers: make(map[string]string),
		Notes:   make(map[string]string),
	}
	for _, q := range questions {
		m := newSelectModel(q)
		p := tea.NewProgram(m, tea.WithOutput(os.Stderr))
		final, err := p.Run()
		if err != nil {
			return resp, err
		}
		result := final.(selectModel)
		if result.cancelled {
			continue
		}
		resp.Answers[q.Question] = result.answer
		if result.isCustom {
			resp.Notes[q.Question] = result.answer
		}
	}
	return resp, nil
}

// ---------- 交互式选择器（bubbletea mini program）----------

var (
	selectCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	selectDescStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	selectHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	selectInputStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
)

type selectModel struct {
	question  tools.Question
	items     []string // label 列表，最后一项是"自由输入"
	descs     []string // 描述列表
	cursor    int
	answer    string
	isCustom  bool
	cancelled bool
	typing    bool   // 是否进入自由输入模式
	input     string // 自由输入缓冲
}

func newSelectModel(q tools.Question) selectModel {
	items := make([]string, 0, len(q.Options)+1)
	descs := make([]string, 0, len(q.Options)+1)
	for _, opt := range q.Options {
		items = append(items, opt.Label)
		descs = append(descs, opt.Description)
	}
	items = append(items, "自由输入")
	descs = append(descs, "以上都不合适，我自己写")
	return selectModel{question: q, items: items, descs: descs}
}

func (m selectModel) Init() tea.Cmd { return nil }

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.typing {
		return m.updateTyping(msg)
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			if m.cursor == len(m.items)-1 {
				m.typing = true
				return m, nil
			}
			m.answer = m.items[m.cursor]
			return m, tea.Quit
		case "q", "esc", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m selectModel) updateTyping(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			text := strings.TrimSpace(m.input)
			if text == "" {
				return m, nil
			}
			m.answer = text
			m.isCustom = true
			return m, tea.Quit
		case "esc":
			m.typing = false
			m.input = ""
			return m, nil
		case "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "backspace":
			if len(m.input) > 0 {
				runes := []rune(m.input)
				m.input = string(runes[:len(runes)-1])
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.input += string(msg.Runes)
			} else if msg.Type == tea.KeySpace {
				m.input += " "
			}
		}
	}
	return m, nil
}

func (m selectModel) View() string {
	var b strings.Builder
	b.WriteString(selectHeaderStyle.Render(fmt.Sprintf("[%s] %s", m.question.Header, m.question.Question)))
	b.WriteString("\n\n")

	for i, item := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = selectCursorStyle.Render("❯ ")
		}
		label := item
		if i == m.cursor {
			label = selectCursorStyle.Render(item)
		}
		desc := selectDescStyle.Render(" " + m.descs[i])
		b.WriteString(fmt.Sprintf("%s%s%s\n", cursor, label, desc))
	}

	if m.typing {
		b.WriteString("\n")
		b.WriteString(selectInputStyle.Render("  ✎ "))
		b.WriteString(m.input)
		b.WriteString(selectCursorStyle.Render("▌"))
		b.WriteString(selectDescStyle.Render("  (Enter 确认, Esc 返回)"))
	} else {
		b.WriteString(selectDescStyle.Render("\n  ↑↓ 选择  Enter 确认  Esc 取消"))
	}

	return b.String()
}
