package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/utils"
)

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

	streamThinking      bool
	lastThinkingByAgent   map[string]string   // agent → 最近的累积 thinking 文本（用于提取增量 delta）
	dispatchStarts        map[string]time.Time // dispatched agent → dispatch 开始时间
	currentDispatchTarget string               // 当前正在执行的 subagent 名（handleToolEnd 时 Args 可能为空）
	toolStarts            map[string]time.Time // agent → 当前工具开始时间
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
		dispatchStarts:      make(map[string]time.Time),
		toolStarts:          make(map[string]time.Time),
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

// persistEvent 将事件写入 runtime queue 和 slog 日志。
func (o *observer) persistEvent(ev Event) {
	// slog 日志 — 所有关键事件都记录,出问题时可追溯
	level := slog.LevelInfo
	switch ev.Level {
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.Log(context.Background(), level, ev.Summary, "module", "event", "category", ev.Category, "agent", ev.Agent)

	// runtime queue — headless 恢复用
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
		o.emitC()
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
	o.emitStreamDelta(ev.Delta, ev.DeltaKind == agentcore.DeltaThinking)
}

func (o *observer) handleToolStart(ev agentcore.Event) {
	if ev.Tool == "" {
		return
	}
	agent := agentFromEvent(ev)

	// subagent 调用 → DISPATCH 事件
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
		o.dispatchStarts[target] = time.Now()
		toolEv := Event{
			Time:     time.Now(),
			Category: "DISPATCH",
			Agent:    agent,
			Summary:  dispatchSummary,
			Level:    "info",
		}
		o.emitEv(toolEv)
		o.persistEvent(toolEv)
		return
	}

	// coordinator 自身工具
	toolName := displayToolName(ev.Tool, ev.Args)
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = ev.Tool
		a.summary = fmt.Sprintf("%s → %s", agent, toolName)
	})
	toolEv := Event{
		Time:     time.Now(),
		Category: "TOOL",
		Agent:    agent,
		Summary:  toolName,
		Level:    "info",
	}
	o.emitEv(toolEv)
	o.persistEvent(toolEv)
}

func (o *observer) handleToolUpdate(ev agentcore.Event) {
	if ev.Progress == nil {
		return
	}
	switch ev.Progress.Kind {
	case agentcore.ProgressToolDelta:
		if ev.Progress.Delta != "" {
			o.emitStreamDelta(ev.Progress.Delta, false)
		}
	case agentcore.ProgressToolStart:
		// 子代理内部的工具调用（如 writer → draft_chapter）
		if ev.Progress.Agent != "" && ev.Progress.Tool != "" {
			toolName := displayToolName(ev.Progress.Tool, ev.Progress.Args)
			o.toolStarts[ev.Progress.Agent] = time.Now()
			subEv := Event{
				Time:     time.Now(),
				Category: "TOOL",
				Agent:    ev.Progress.Agent,
				Summary:  toolName,
				Level:    "info",
				Depth:    1,
			}
			o.emitEv(subEv)
			o.persistEvent(subEv)
			o.updateAgent(ev.Progress.Agent, func(a *agentState) {
				a.state = "working"
				a.tool = ev.Progress.Tool
				a.summary = fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName)
			})
		}
	case agentcore.ProgressToolEnd:
		if ev.Progress.Agent != "" && ev.Progress.Tool != "" {
			var duration time.Duration
			if start, ok := o.toolStarts[ev.Progress.Agent]; ok {
				duration = time.Since(start)
				delete(o.toolStarts, ev.Progress.Agent)
			}
			if duration > 0 {
				toolName := ev.Progress.Tool
				doneEv := Event{
					Time:     time.Now(),
					Category: "DONE",
					Agent:    ev.Progress.Agent,
					Summary:  toolName,
					Level:    "info",
					Depth:    1,
					Duration: duration,
				}
				o.emitEv(doneEv)
				o.persistEvent(doneEv)
			}
		}
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
		delete(o.toolStarts, ev.Progress.Agent)
		msg := ev.Progress.Message
		if msg == "" {
			msg = "unknown error"
		}
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
	o.updateAgent(agent, func(a *agentState) {
		a.tool = ""
	})
	delete(o.lastThinkingByAgent, agent)

	// 计算 dispatch 耗时（handleToolEnd 的 ev.Args 可能为空，从 currentDispatchTarget 获取）
	var dispatchDuration time.Duration
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
		if start, ok := o.dispatchStarts[dispatchTarget]; ok {
			dispatchDuration = time.Since(start)
			delete(o.dispatchStarts, dispatchTarget)
		}
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
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Agent:    agent,
			Summary:  summary,
			Level:    "error",
			Depth:    depth,
			Duration: dispatchDuration,
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
		return
	}

	if errEv, fullErr := o.subagentResultErrorEvent(ev); errEv != nil {
		slog.Error(fullErr, "module", "agent", "tool", ev.Tool)
		errEv.Duration = dispatchDuration
		// 用 dispatchTarget 修正（subagentResultErrorEvent 解析 ev.Args 可能为空）
		if dispatchTarget != "" && dispatchTarget != "subagent" {
			errEv.Agent = dispatchTarget
		}
		o.emitEv(*errEv)
		o.persistEvent(*errEv)
		return
	}

	// subagent 成功完成 → DONE 事件
	if ev.Tool == "subagent" {
		doneEv := Event{
			Time:     time.Now(),
			Category: "DONE",
			Agent:    dispatchTarget,
			Level:    "success",
			Duration: dispatchDuration,
		}
		o.emitEv(doneEv)
		o.persistEvent(doneEv)
		return
	}

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
			Verdict string `json:"verdict"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			if p.Verdict != "" {
				return fmt.Sprintf("%s(第%d章·%s)", tool, p.Chapter, p.Verdict)
			}
			return fmt.Sprintf("%s(第%d章)", tool, p.Chapter)
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
