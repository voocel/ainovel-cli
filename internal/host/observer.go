package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
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
		emitEv: emitEv,
		emitD:  emitD,
		emitC:  emitC,
		store:  s,
		agents: make(map[string]*agentState),
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
	slog.Log(context.Background(), level, ev.Summary, "module", "event", "category", ev.Category)

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
		if ev.Delta != "" {
			o.emitD(ev.Delta)
		}
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
			errEv := Event{
				Time:     time.Now(),
				Category: "ERROR",
				Summary:  truncate(ev.Err.Error(), 120),
				Level:    "error",
			}
			o.emitEv(errEv)
			o.persistEvent(errEv)
		}
	}
}

func (o *observer) handleToolStart(ev agentcore.Event) {
	if ev.Tool == "" {
		return
	}
	agent := agentFromEvent(ev)
	toolName := displayToolName(ev.Tool, ev.Args)
	summary := fmt.Sprintf("%s → %s", agent, toolName)

	// subagent 调用: 解析具体的 agent 名和任务描述
	if ev.Tool == "subagent" {
		if sub := parseSubagentArgs(ev.Args); sub.agent != "" {
			summary = fmt.Sprintf("%s → %s", agent, sub.agent)
			if sub.task != "" {
				summary += "（" + truncate(sub.task, 30) + "）"
			}
		}
	}

	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = ev.Tool
		a.summary = summary
	})

	toolEv := Event{
		Time:     time.Now(),
		Category: "TOOL",
		Summary:  summary,
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
			o.emitD(ev.Progress.Delta)
		}
	case agentcore.ProgressToolStart:
		// 子代理内部的工具调用（如 writer → draft_chapter）
		if ev.Progress.Agent != "" && ev.Progress.Tool != "" {
			toolName := displayToolName(ev.Progress.Tool, ev.Progress.Args)
			subEv := Event{
				Time:     time.Now(),
				Category: "TOOL",
				Summary:  fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName),
				Level:    "info",
			}
			o.emitEv(subEv)
			o.persistEvent(subEv)
			o.updateAgent(ev.Progress.Agent, func(a *agentState) {
				a.state = "working"
				a.tool = ev.Progress.Tool
				a.summary = subEv.Summary
			})
		}
	case agentcore.ProgressThinking:
		// 思考文本暂不转发
	case agentcore.ProgressRetry:
		retryEv := Event{
			Time:     time.Now(),
			Category: "SYSTEM",
			Summary: fmt.Sprintf("%s 重试 (%d/%d): %s",
				ev.Progress.Agent, ev.Progress.Attempt, ev.Progress.MaxRetries,
				truncate(ev.Progress.Message, 80)),
			Level: "warn",
		}
		o.emitEv(retryEv)
		o.persistEvent(retryEv)
	case agentcore.ProgressToolError:
		msg := ev.Progress.Message
		if msg == "" {
			msg = "unknown error"
		}
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  fmt.Sprintf("%s → %s 错误: %s", ev.Progress.Agent, ev.Progress.Tool, truncate(msg, 100)),
			Level:    "error",
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
	case agentcore.ProgressContext:
		o.handleContextProgress(ev)
	}
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

	ctxEv := Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("%s 上下文 %.0f%% (%d/%d) 策略: %s", agent, payload.Percent, payload.Tokens, payload.ContextWindow, payload.Strategy),
		Level:    "info",
	}
	if payload.Percent > 85 {
		ctxEv.Level = "warn"
	}
	o.emitEv(ctxEv)
	o.persistEvent(ctxEv)

	o.updateAgent(agent, func(a *agentState) {
		a.context = AgentContextSnapshot{
			Tokens:        payload.Tokens,
			ContextWindow: payload.ContextWindow,
			Percent:       payload.Percent,
			Scope:         payload.Scope,
			Strategy:      payload.Strategy,
		}
	})
}

func (o *observer) handleToolEnd(ev agentcore.Event) {
	agent := agentFromEvent(ev)
	o.updateAgent(agent, func(a *agentState) {
		a.tool = ""
	})

	if ev.IsError {
		summary := fmt.Sprintf("%s → %s 失败", agent, ev.Tool)
		if len(ev.Result) > 0 {
			errText := string(ev.Result)
			if len(errText) > 120 {
				errText = errText[:120] + "..."
			}
			summary += ": " + errText
		}
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  summary,
			Level:    "error",
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
		return
	}

	// 成功完成的关键工具 — 提取有意义的摘要
	if successEv := o.toolSuccessEvent(ev); successEv != nil {
		o.emitEv(*successEv)
		o.persistEvent(*successEv)
	}
}

// toolSuccessEvent 从工具成功返回值中提取关键信息生成事件。
func (o *observer) toolSuccessEvent(ev agentcore.Event) *Event {
	if len(ev.Result) == 0 {
		return nil
	}
	var payload map[string]any
	if json.Unmarshal(ev.Result, &payload) != nil {
		return nil
	}

	switch ev.Tool {
	case "subagent":
		// subagent 完成: 不在这里处理,由 coordinator 的下一个 turn 驱动
		return nil

	default:
		// 从 subagent 内部上报的结果不走这里（走 ProgressToolStart/Error）
		return nil
	}
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
		var p struct{ Type string `json:"type"` }
		if json.Unmarshal(args, &p) == nil && p.Type != "" {
			return fmt.Sprintf("%s[%s]", tool, p.Type)
		}
	case "commit_chapter", "plan_chapter", "draft_chapter", "check_consistency":
		var p struct{ Chapter int `json:"chapter"` }
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
		var p struct{ Chapter int `json:"chapter"` }
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
