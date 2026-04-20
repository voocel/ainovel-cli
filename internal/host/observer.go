package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// 单调递增的事件 ID 计数器；配合时间戳生成稳定 ID。
var eventIDCounter uint64

func nextEventID() string {
	return fmt.Sprintf("e%d", atomic.AddUint64(&eventIDCounter, 1))
}

// activeCall 记录一次正在进行的调用（TOOL / DISPATCH）的 ID、起点时间与 summary。
// summary 在完成事件时回填进 finish Event，保证 replay（runtime queue）能还原行内容。
type activeCall struct {
	id      string
	start   time.Time
	summary string
	depth   int
}

// observer 订阅 coordinator 事件流并投影到 Host 的输出通道。
// 它是纯观察者,不参与任何控制决策。
type observer struct {
	unsub   func()
	emitEv  func(Event)
	emitD   func(string)
	emitC   func()
	store   *storepkg.Store // 用于 runtime queue 持久化（ReplayQueue 消费）
	agents  map[string]*agentState
	agentMu sync.Mutex

	streamThinking        bool
	lastThinkingByAgent   map[string]string          // agent → 最近的累积 thinking 文本（用于提取增量 delta）
	dispatchStarts        map[string]*activeCall     // dispatched agent → 进行中的 DISPATCH 调用
	currentDispatchTarget string                     // 当前正在执行的 subagent 名（handleToolEnd 时 Args 可能为空）
	toolStarts            map[string]*activeCall     // agent → 进行中的 TOOL 调用
	streamExtractors      map[string]*agentExtractor // agent → 当前工具调用 JSON 参数的内容抽取器
	streamHasContent      bool                       // 当前 streamRound 是否已输出过内容（判断是否需要段落分隔）
	streamLastByte        byte                       // 最近一次流式输出的末字节（用于精确补齐换行）
}

// agentExtractor 记录某个 agent 当前正在抽取的工具名与抽取器实例。
// 工具名用于检测"新的工具调用开始了"，避免缓存被上一轮残留污染。
type agentExtractor struct {
	tool       string
	ext        *jsonFieldExtractor
	emittedAny bool // 本 extractor 是否已经产出过内容；用于首次输出前补段落分隔
}

type agentState struct {
	name    string
	state   string
	tool    string
	summary string
	turn    int
	context AgentContextSnapshot
	updated time.Time
}

func newObserver(coordinator *agentcore.Agent, s *storepkg.Store, emitEv func(Event), emitD func(string), emitC func()) *observer {
	o := &observer{
		emitEv:              emitEv,
		emitD:               emitD,
		emitC:               emitC,
		store:               s,
		agents:              make(map[string]*agentState),
		lastThinkingByAgent: make(map[string]string),
		dispatchStarts:      make(map[string]*activeCall),
		toolStarts:          make(map[string]*activeCall),
		streamExtractors:    make(map[string]*agentExtractor),
	}
	o.unsub = coordinator.Subscribe(o.handle)
	return o
}

func (o *observer) finalize() {
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	for _, a := range o.agents {
		a.state = "idle"
		a.tool = ""
	}
}

// emitAndLog 用于调用类事件的"开始"态：发给 TUI 但不写入 runtime queue，
// 避免 replay 时"开始一行、完成又一行"重复。slog 由 host.emitEvent 统一记录。
func (o *observer) emitAndLog(ev Event) {
	o.emitEv(ev)
}

// persistEvent 把事件写入 runtime queue（slog 由 host.emitEvent 统一记录）。
func (o *observer) persistEvent(ev Event) {
	if o.store == nil || o.store.Runtime == nil {
		return
	}
	priority := domain.RuntimePriorityBackground
	switch ev.Category {
	case "SYSTEM", "ERROR":
		priority = domain.RuntimePriorityControl
	}
	_, _ = o.store.Runtime.AppendQueue(domain.RuntimeQueueItem{
		Time:     ev.Time,
		Kind:     domain.RuntimeQueueUIEvent,
		Priority: priority,
		Category: ev.Category,
		Summary:  ev.Summary,
		Payload:  ev,
	})
}

func (o *observer) handle(ev agentcore.Event) {
	switch ev.Type {
	case agentcore.EventToolExecStart:
		o.handleToolStart(ev)
	case agentcore.EventToolExecUpdate:
		o.handleToolUpdate(ev)
	case agentcore.EventToolExecEnd:
		o.handleToolEnd(ev)
	case agentcore.EventMessageUpdate:
		o.handleMessageUpdate(ev)
	case agentcore.EventMessageEnd:
		o.streamClear()
	case agentcore.EventTurnStart:
		if ev.Progress != nil && ev.Progress.Kind == agentcore.ProgressTurnCounter {
			o.updateAgent(ev.Progress.Agent, func(a *agentState) {
				a.turn = ev.Progress.Turn
			})
		}
	case agentcore.EventRetry:
		if ev.RetryInfo != nil {
			msg := ""
			if ev.RetryInfo.Err != nil {
				msg = ev.RetryInfo.Err.Error()
			}
			retryEv := Event{
				Time:     time.Now(),
				Category: "SYSTEM",
				Summary:  fmt.Sprintf("重试 (%d/%d): %s", ev.RetryInfo.Attempt, ev.RetryInfo.MaxRetries, truncate(msg, 80)),
				Level:    "warn",
			}
			o.emitEv(retryEv)
			o.persistEvent(retryEv)
		}
	case agentcore.EventError:
		if ev.Err != nil {
			fullMsg := ev.Err.Error()
			slog.Error(fullMsg, "module", "agent", "category", "ERROR")
			errEv := Event{
				Time:     time.Now(),
				Category: "ERROR",
				Summary:  truncate(fullMsg, 120),
				Level:    "error",
			}
			o.emitEv(errEv)
			o.persistEvent(errEv)
		}
	}
}

func (o *observer) handleMessageUpdate(ev agentcore.Event) {
	if ev.Delta == "" {
		return
	}
	// Coordinator 的 tool-call 参数是给 subagent 的任务 JSON，没有可读内容，直接丢弃。
	if ev.DeltaKind == agentcore.DeltaToolCall {
		return
	}
	o.emitStreamDelta(ev.Delta, ev.DeltaKind == agentcore.DeltaThinking)
}

func (o *observer) handleToolStart(ev agentcore.Event) {
	if ev.Tool == "" {
		return
	}
	agent := agentFromEvent(ev)

	// subagent 调用 → DISPATCH 事件（进行中）
	if ev.Tool == "subagent" {
		sub := parseSubagentArgs(ev.Args)
		target := sub.agent
		if target == "" {
			target = "subagent"
		}
		dispatchSummary := target
		if sub.task != "" {
			dispatchSummary += "（" + truncate(sub.task, 30) + "）"
		}
		o.updateAgent(agent, func(a *agentState) {
			a.state = "working"
			a.tool = ev.Tool
			a.summary = fmt.Sprintf("%s → %s", agent, dispatchSummary)
		})
		o.currentDispatchTarget = target
		id := nextEventID()
		o.dispatchStarts[target] = &activeCall{id: id, start: time.Now(), summary: dispatchSummary}
		o.emitAndLog(Event{
			ID:       id,
			Time:     time.Now(),
			Category: "DISPATCH",
			Agent:    agent,
			Summary:  dispatchSummary,
			Level:    "info",
		})
		return
	}

	// coordinator 自身工具（进行中）
	toolName := displayToolName(ev.Tool, ev.Args)
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = ev.Tool
		a.summary = fmt.Sprintf("%s → %s", agent, toolName)
	})
	id := nextEventID()
	o.toolStarts[agent] = &activeCall{id: id, start: time.Now(), summary: toolName}
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "TOOL",
		Agent:    agent,
		Summary:  toolName,
		Level:    "info",
	})
}

func (o *observer) handleToolUpdate(ev agentcore.Event) {
	if ev.Progress == nil {
		return
	}
	switch ev.Progress.Kind {
	case agentcore.ProgressToolDelta:
		if ev.Progress.Delta != "" {
			o.handleSubagentDelta(ev.Progress)
		}
	case agentcore.ProgressToolStart:
		// 子代理内部的工具调用（如 writer → draft_chapter）。
		// 注意：TOOL 行可能已经在流式识别阶段被 handleSubagentDelta 提前发出。
		// 此处：若已发 → 只更新 summary（args 此时完整，能显示 "tool(第N章)"）；否则正常发。
		if ev.Progress.Agent == "" || ev.Progress.Tool == "" {
			break
		}
		toolName := displayToolName(ev.Progress.Tool, ev.Progress.Args)
		if call, ok := o.toolStarts[ev.Progress.Agent]; ok {
			if toolName != "" && toolName != call.summary {
				call.summary = toolName
				// 发 summary-only 更新事件（同 ID），TUI applyEvent 会合并
				o.emitEv(Event{
					ID:       call.id,
					Time:     call.start,
					Category: "TOOL",
					Agent:    ev.Progress.Agent,
					Summary:  toolName,
					Level:    "info",
					Depth:    call.depth,
				})
			}
			o.updateAgent(ev.Progress.Agent, func(a *agentState) {
				a.state = "working"
				a.tool = ev.Progress.Tool
				a.summary = fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName)
			})
			break
		}
		// 未提前发过 → 正常流程
		id := nextEventID()
		o.toolStarts[ev.Progress.Agent] = &activeCall{id: id, start: time.Now(), summary: toolName, depth: 1}
		o.emitAndLog(Event{
			ID:       id,
			Time:     time.Now(),
			Category: "TOOL",
			Agent:    ev.Progress.Agent,
			Summary:  toolName,
			Level:    "info",
			Depth:    1,
		})
		o.updateAgent(ev.Progress.Agent, func(a *agentState) {
			a.state = "working"
			a.tool = ev.Progress.Tool
			a.summary = fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName)
		})
	case agentcore.ProgressToolEnd:
		delete(o.streamExtractors, ev.Progress.Agent)
		if ev.Progress.Agent == "" {
			return
		}
		call, ok := o.toolStarts[ev.Progress.Agent]
		if !ok {
			return
		}
		delete(o.toolStarts, ev.Progress.Agent)
		// 同 ID 更新事件：TUI 按 ID 定位原 TOOL 行，回填 FinishedAt / Duration。
		// Summary / Depth 也带上，保证 runtime queue replay 时能还原完整行。
		finishEv := Event{
			ID:         call.id,
			Time:       call.start,
			FinishedAt: time.Now(),
			Category:   "TOOL",
			Agent:      ev.Progress.Agent,
			Summary:    call.summary,
			Level:      "info",
			Depth:      call.depth,
			Duration:   time.Since(call.start),
		}
		o.emitEv(finishEv)
		o.persistEvent(finishEv)
	case agentcore.ProgressThinking:
		o.handleThinkingProgress(ev)
	case agentcore.ProgressRetry:
		retryEv := Event{
			Time:     time.Now(),
			Category: "SYSTEM",
			Agent:    ev.Progress.Agent,
			Summary: fmt.Sprintf("重试 (%d/%d): %s",
				ev.Progress.Attempt, ev.Progress.MaxRetries,
				truncate(ev.Progress.Message, 80)),
			Level: "warn",
			Depth: 1,
		}
		o.emitEv(retryEv)
		o.persistEvent(retryEv)
	case agentcore.ProgressToolError:
		delete(o.streamExtractors, ev.Progress.Agent)
		msg := ev.Progress.Message
		if msg == "" {
			msg = "unknown error"
		}
		// 如果有进行中的 TOOL 行，原地标记为失败；否则独立追加 ERROR 行。
		if call, ok := o.toolStarts[ev.Progress.Agent]; ok {
			delete(o.toolStarts, ev.Progress.Agent)
			finishEv := Event{
				ID:         call.id,
				Time:       call.start,
				FinishedAt: time.Now(),
				Failed:     true,
				Category:   "TOOL",
				Agent:      ev.Progress.Agent,
				Summary:    call.summary,
				Level:      "error",
				Depth:      call.depth,
				Duration:   time.Since(call.start),
			}
			o.emitEv(finishEv)
			o.persistEvent(finishEv)
		}
		// 附加 ERROR 详情行（补充错误信息，便于排查）
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Agent:    ev.Progress.Agent,
			Summary:  fmt.Sprintf("%s 错误: %s", ev.Progress.Tool, truncate(msg, 100)),
			Level:    "error",
			Depth:    1,
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
	case agentcore.ProgressContext:
		o.handleContextProgress(ev)
	}
}

// handleSubagentDelta 分流 subagent 的文本与工具调用参数：
// - DeltaText 直接作为 markdown 流出
// - DeltaToolCall 只对已知的长内容工具（如 draft_chapter.content）抽取字段流出；其他工具的参数 JSON 全部丢弃
func (o *observer) handleSubagentDelta(p *agentcore.ProgressPayload) {
	if p.DeltaKind != agentcore.DeltaToolCall {
		o.emitStreamDelta(p.Delta, false)
		return
	}
	if p.Tool == "" {
		return // 工具名未就绪，下一个 delta 再试
	}

	// 流式识别到工具名时提前发 TOOL 进行中事件，让 spinner 覆盖整段 LLM 生成期间
	// （否则 draft_chapter 这类工具的"进行中"只在真实 Execute 的几十毫秒里显示）。
	// 真正的 ProgressToolStart 到来时识别到 toolStarts 已有记录，只会补齐 summary。
	o.ensureSubagentToolStarted(p.Agent, p.Tool)

	cur, ok := o.streamExtractors[p.Agent]
	// 工具名变了或上一轮已 Done（aborted / 已完成），一律重建
	if !ok || cur.tool != p.Tool || cur.ext.Done() {
		ext := newToolExtractor(p.Tool)
		if ext == nil {
			delete(o.streamExtractors, p.Agent)
			return
		}
		cur = &agentExtractor{tool: p.Tool, ext: ext}
		o.streamExtractors[p.Agent] = cur
	}
	if emitted := cur.ext.Feed(p.Delta); emitted != "" {
		if !cur.emittedAny {
			cur.emittedAny = true
			o.ensureStreamParagraphBreak()
		}
		o.emitStreamDelta(emitted, false)
	}
}

func (o *observer) handleThinkingProgress(ev agentcore.Event) {
	agent := ev.Progress.Agent
	thinking := ev.Progress.Thinking
	if agent == "" || thinking == "" {
		return
	}

	prev := o.lastThinkingByAgent[agent]
	delta := thinking
	if strings.HasPrefix(thinking, prev) {
		delta = thinking[len(prev):]
	}
	o.lastThinkingByAgent[agent] = thinking
	if delta == "" {
		return
	}
	o.emitStreamDelta(delta, true)
}

func (o *observer) handleContextProgress(ev agentcore.Event) {
	if ev.Progress == nil || len(ev.Progress.Meta) == 0 {
		return
	}
	var payload struct {
		Tokens        int     `json:"tokens"`
		ContextWindow int     `json:"context_window"`
		Percent       float64 `json:"percent"`
		Scope         string  `json:"scope"`
		Strategy      string  `json:"strategy"`
	}
	if json.Unmarshal(ev.Progress.Meta, &payload) != nil {
		return
	}

	agent := ev.Progress.Agent
	if agent == "" {
		agent = "coordinator"
	}

	// 更新 agent 快照（TUI 侧边栏始终可见）
	o.updateAgent(agent, func(a *agentState) {
		a.context = AgentContextSnapshot{
			Tokens:        payload.Tokens,
			ContextWindow: payload.ContextWindow,
			Percent:       payload.Percent,
			Scope:         payload.Scope,
			Strategy:      payload.Strategy,
		}
	})

	level := "info"
	if payload.Percent > 85 {
		level = "warn"
	}
	summary := fmt.Sprintf("%s 上下文 %.0f%% (%d/%d) 策略: %s", agent, payload.Percent, payload.Tokens, payload.ContextWindow, payload.Strategy)

	depth := 0
	if agent != "coordinator" {
		depth = 1
	}

	if payload.Strategy != "" {
		// 触发了压缩 → 事件流 + 日志
		ctxEv := Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: summary, Level: level, Depth: depth}
		o.emitEv(ctxEv)
		o.persistEvent(ctxEv)
	} else {
		// 普通使用率报告 → 仅日志
		slogLevel := slog.LevelInfo
		if level == "warn" {
			slogLevel = slog.LevelWarn
		}
		slog.Log(context.Background(), slogLevel, summary, "module", "context", "agent", agent)
	}
}

func (o *observer) handleToolEnd(ev agentcore.Event) {
	agent := agentFromEvent(ev)
	// 工具结束：把状态切回 idle，否则侧边栏会永远停在 working。
	// 子代理派遣结束时 dispatchTarget 的状态会在下方另行清除。
	o.updateAgent(agent, func(a *agentState) {
		a.tool = ""
		a.state = "idle"
	})
	delete(o.lastThinkingByAgent, agent)

	// 取出进行中的 DISPATCH 记录（handleToolEnd 的 ev.Args 可能为空，从 currentDispatchTarget 取）
	var dispatchCall *activeCall
	var dispatchTarget string
	if ev.Tool == "subagent" {
		dispatchTarget = o.currentDispatchTarget
		o.currentDispatchTarget = ""
		if dispatchTarget == "" {
			if sub := parseSubagentArgs(ev.Args); sub.agent != "" {
				dispatchTarget = sub.agent
			}
		}
		if dispatchTarget == "" {
			dispatchTarget = "subagent"
		}
		if call, ok := o.dispatchStarts[dispatchTarget]; ok {
			dispatchCall = call
			delete(o.dispatchStarts, dispatchTarget)
		}
		// 派遣结束：把子代理状态复位为 idle（成功/失败/错误路径都需要此清理）
		if dispatchTarget != "subagent" {
			o.updateAgent(dispatchTarget, func(a *agentState) {
				a.state = "idle"
				a.tool = ""
			})
		}
	}

	// 取出 coordinator 直接工具（非 subagent）的进行中记录（罕见，但保证一致性）
	var toolCall *activeCall
	if ev.Tool != "subagent" {
		if call, ok := o.toolStarts[agent]; ok {
			toolCall = call
			delete(o.toolStarts, agent)
		}
	}

	// 统一的调用完成态（成功/失败），通过同 ID 更新原行
	emitFinish := func(call *activeCall, category, agentName string, failed bool) {
		if call == nil {
			return
		}
		level := "success"
		if failed {
			level = "error"
		}
		finishEv := Event{
			ID:         call.id,
			Time:       call.start,
			FinishedAt: time.Now(),
			Failed:     failed,
			Category:   category,
			Agent:      agentName,
			Summary:    call.summary,
			Level:      level,
			Depth:      call.depth,
			Duration:   time.Since(call.start),
		}
		o.emitEv(finishEv)
		o.persistEvent(finishEv)
	}
	emitDispatchFinish := func(failed bool) {
		emitFinish(dispatchCall, "DISPATCH", dispatchTarget, failed)
	}
	emitToolFinish := func(failed bool) {
		emitFinish(toolCall, "TOOL", agent, failed)
	}
	// 兜底：若 subagent 结束时，该 subagent 内部还有未完成的 TOOL 调用（比如 ensureSubagentToolStarted
	// 提前发了进行中事件，但随后 abort/context cancel 让 ProgressToolEnd 没来），
	// 在这里强制发 finish，避免 TOOL 行永远"进行中"。状态跟随 dispatch 同步。
	flushOrphanSubagentTool := func(failed bool) {
		if dispatchTarget == "" {
			return
		}
		call, ok := o.toolStarts[dispatchTarget]
		if !ok {
			return
		}
		delete(o.toolStarts, dispatchTarget)
		delete(o.streamExtractors, dispatchTarget)
		emitFinish(call, "TOOL", dispatchTarget, failed)
	}

	if ev.IsError {
		depth := 0
		if agent != "coordinator" {
			depth = 1
		}
		summary := fmt.Sprintf("%s 失败", ev.Tool)
		if len(ev.Result) > 0 {
			errText := string(ev.Result)
			slog.Error(fmt.Sprintf("%s → %s: %s", agent, ev.Tool, errText), "module", "agent", "tool", ev.Tool)
			if len(errText) > 120 {
				errText = errText[:120] + "..."
			}
			summary += ": " + errText
		}
		flushOrphanSubagentTool(true)
		emitDispatchFinish(true)
		emitToolFinish(true)
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Agent:    agent,
			Summary:  summary,
			Level:    "error",
			Depth:    depth,
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
		return
	}

	if errEv, fullErr := o.subagentResultErrorEvent(ev); errEv != nil {
		slog.Error(fullErr, "module", "agent", "tool", ev.Tool)
		if dispatchTarget != "" && dispatchTarget != "subagent" {
			errEv.Agent = dispatchTarget
		}
		flushOrphanSubagentTool(true)
		emitDispatchFinish(true)
		o.emitEv(*errEv)
		o.persistEvent(*errEv)
		return
	}

	// subagent 成功完成 → 更新原 DISPATCH 行为完成态（带耗时）
	if ev.Tool == "subagent" {
		flushOrphanSubagentTool(false)
		emitDispatchFinish(false)
		return
	}

	// coordinator 直接工具成功完成
	emitToolFinish(false)
}

func (o *observer) emitStreamDelta(delta string, thinking bool) {
	if delta == "" {
		return
	}
	if thinking != o.streamThinking {
		o.emitD(utils.ThinkingSep)
		o.streamThinking = thinking
	}
	o.emitD(delta)
	o.streamHasContent = true
	o.streamLastByte = delta[len(delta)-1]
}

// ensureSubagentToolStarted 在流式识别到 tool_call 首次出现时，提前为该 agent
// 登记一次进行中的 TOOL 调用，使事件流的 spinner 覆盖"LLM 流式生成 tool_call
// 参数"这一段时间（通常占调用总耗时的 99%）。args 此时尚不完整，暂以纯工具名
// 为 summary；等真正的 ProgressToolStart 到来时会补齐带参数的 summary。
func (o *observer) ensureSubagentToolStarted(agent, tool string) {
	if agent == "" || tool == "" {
		return
	}
	if _, ok := o.toolStarts[agent]; ok {
		return // 已有进行中调用，幂等
	}
	id := nextEventID()
	o.toolStarts[agent] = &activeCall{
		id:      id,
		start:   time.Now(),
		summary: tool, // 先用纯工具名，ProgressToolStart 到来时可能更新为 tool(第N章)
		depth:   1,
	}
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "TOOL",
		Agent:    agent,
		Summary:  tool,
		Level:    "info",
		Depth:    1,
	})
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = tool
	})
	// 无 extractor 的工具（read_chapter / check_consistency 等）在流式面板上
	// 补一行 header，避免面板在这类调用期间空白让用户觉得卡住。
	// 有 extractor 的工具由 extractor 在首个字段匹配时自行输出 header。
	if _, has := toolDisplays[tool]; !has {
		o.ensureStreamParagraphBreak()
		o.emitStreamDelta(streamHeaderFallback(tool)+"\n", false)
	}
}

// streamHeaderFallback 为未配置 extractor 的工具生成流式 header 文本，
// 让用户即使对轻量读取类工具也能看到"在调用什么"。
func streamHeaderFallback(tool string) string {
	label := tool
	switch tool {
	case "read_chapter":
		label = "读章节"
	case "novel_context":
		label = "查询上下文"
	case "check_consistency":
		label = "一致性检查"
	case "ask_user":
		label = "向用户提问"
	}
	return "【" + label + "】"
}

// streamClear 通知 TUI 开启新一轮 streamRound，同时重置与段落分隔相关的状态。
// 逻辑上新 round 是"空 stream"，否则下一次首个 extractor emit 会误补前导空行。
func (o *observer) streamClear() {
	o.emitC()
	o.streamHasContent = false
	o.streamLastByte = 0
	// 上一轮的 subagent 结束前 ProgressToolEnd 已 delete，这里防御性清空。
	if len(o.streamExtractors) > 0 {
		o.streamExtractors = make(map[string]*agentExtractor)
	}
}

// ensureStreamParagraphBreak 在流式面板上插入一个段落分隔（空行），
// 用于新工具调用 / 新输出块前，避免与上一段内容挤在同一行。
// 若流为空则跳过；若末尾已是 '\n' 则只补一个换行构成空行，否则补两个。
func (o *observer) ensureStreamParagraphBreak() {
	if !o.streamHasContent {
		return
	}
	if o.streamLastByte == '\n' {
		o.emitD("\n")
	} else {
		o.emitD("\n\n")
	}
	o.streamLastByte = '\n'
}

func (o *observer) subagentResultErrorEvent(ev agentcore.Event) (*Event, string) {
	if ev.Tool != "subagent" || len(ev.Result) == 0 {
		return nil, ""
	}
	sub := parseSubagentArgs(ev.Args)
	errMsg := parseSubagentResultError(ev.Result)
	if errMsg == "" {
		return nil, ""
	}

	target := "subagent"
	if sub.agent != "" {
		target = sub.agent
	}
	fullErr := fmt.Sprintf("%s 失败: %s", target, errMsg)
	return &Event{
		Time:     time.Now(),
		Category: "ERROR",
		Agent:    "coordinator",
		Summary:  fmt.Sprintf("%s 失败: %s", target, truncate(errMsg, 120)),
		Level:    "error",
	}, fullErr
}

func (o *observer) updateAgent(name string, fn func(*agentState)) {
	if name == "" {
		return
	}
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	a, ok := o.agents[name]
	if !ok {
		a = &agentState{name: name, state: "idle"}
		o.agents[name] = a
	}
	fn(a)
	a.updated = time.Now()
}

func (o *observer) agentSnapshots() []AgentSnapshot {
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	snaps := make([]AgentSnapshot, 0, len(o.agents))
	for _, a := range o.agents {
		snaps = append(snaps, AgentSnapshot{
			Name:      a.name,
			State:     a.state,
			Summary:   a.summary,
			Tool:      a.tool,
			Turn:      a.turn,
			Context:   a.context,
			UpdatedAt: a.updated,
		})
	}
	return snaps
}

func agentFromEvent(ev agentcore.Event) string {
	if ev.Progress != nil && ev.Progress.Agent != "" {
		return ev.Progress.Agent
	}
	return "coordinator"
}

func displayToolName(tool string, args json.RawMessage) string {
	if len(args) == 0 {
		return tool
	}
	switch tool {
	case "save_foundation":
		var p struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(args, &p) == nil && p.Type != "" {
			return fmt.Sprintf("%s[%s]", tool, p.Type)
		}
	case "commit_chapter", "plan_chapter", "draft_chapter", "check_consistency":
		var p struct {
			Chapter int `json:"chapter"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			return fmt.Sprintf("%s(第%d章)", tool, p.Chapter)
		}
	case "save_review":
		var p struct {
			Chapter int    `json:"chapter"`
			Scope   string `json:"scope"`
			Verdict string `json:"verdict"`
		}
		if json.Unmarshal(args, &p) == nil {
			label := ""
			switch p.Scope {
			case "arc":
				label = "本弧"
			case "global":
				label = "全局"
			default:
				if p.Chapter > 0 {
					label = fmt.Sprintf("第%d章", p.Chapter)
				}
			}
			if label == "" {
				return tool
			}
			if p.Verdict != "" {
				return fmt.Sprintf("%s(%s·%s)", tool, label, p.Verdict)
			}
			return fmt.Sprintf("%s(%s)", tool, label)
		}
	case "novel_context":
		var p struct {
			Chapter int `json:"chapter"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			return fmt.Sprintf("%s(第%d章)", tool, p.Chapter)
		}
	case "read_chapter":
		var p struct {
			Chapter   int    `json:"chapter"`
			Source    string `json:"source"`
			Character string `json:"character"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			suffix := ""
			if p.Character != "" {
				suffix = "·" + p.Character + "对话"
			} else if p.Source == "draft" {
				suffix = "·草稿"
			}
			return fmt.Sprintf("%s(第%d章%s)", tool, p.Chapter, suffix)
		}
	}
	return tool
}

type subagentInvocation struct {
	agent string
	task  string
}

func parseSubagentResultError(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(result, &payload) != nil {
		return ""
	}
	return payload.Error
}

func parseSubagentArgs(args json.RawMessage) subagentInvocation {
	if len(args) == 0 {
		return subagentInvocation{}
	}
	var p struct {
		Agent string `json:"agent"`
		Task  string `json:"task"`
	}
	if json.Unmarshal(args, &p) == nil && p.Agent != "" {
		return subagentInvocation{agent: p.Agent, task: p.Task}
	}
	return subagentInvocation{}
}
