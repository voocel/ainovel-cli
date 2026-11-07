package host

import (
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/progress"
	"github.com/voocel/litellm"
)

// handleWorkerEvent 用 AgentLoop 的真实边界投影模型响应与工具调用：
// MessageStart 开 MODEL 行，MessageDelta 流出正文/思考/工具参数，MessageEnd 收
// MODEL 行；ToolStart/ToolEnd 同步 TOOL 行；Retry 单列一行。其余生命周期事件
// （TurnEnd/Compaction/RunEnd）由 Engine 直接处理，这里不投影。
func (o *observer) handleWorkerEvent(agent string, ev agentcore.Event) {
	switch e := ev.(type) {
	case agentcore.MessageStart:
		o.startModelResponse(agent)
	case agentcore.MessageDelta:
		o.handleDelta(agent, e.Event)
	case agentcore.MessageEnd:
		if e.Message.Role == litellm.RoleAssistant {
			o.finishModelResponse(agent)
			o.clearStreamTool(agent)
		}
	case agentcore.ToolStart:
		o.handleToolUpdate(progress.Event{
			Kind:  progress.KindToolStart,
			Agent: agent,
			Tool:  e.Call.Name,
			Args:  e.Call.Args,
		})
	case agentcore.ToolEnd:
		if e.Result.IsError {
			o.handleToolUpdate(progress.Event{
				Kind:    progress.KindToolError,
				Agent:   agent,
				Tool:    e.Call.Name,
				Message: e.Result.Text(),
				IsError: true,
			})
			return
		}
		o.handleToolUpdate(progress.Event{Kind: progress.KindToolEnd, Agent: agent, Tool: e.Call.Name})
	case agentcore.ToolUpdate:
		// 工具自己报告的进度（如 llmretry 的重试），不是我们的进度包则忽略。
		p, ok := progress.From(e.Progress)
		if !ok {
			return
		}
		if p.Agent == "" {
			p.Agent = agent
		}
		if p.Tool == "" {
			p.Tool = e.Call.Name
		}
		o.handleToolUpdate(p)
	case agentcore.Retry:
		msg := ""
		if e.Err != nil {
			msg = e.Err.Error()
		}
		o.emitRetryRow(agent, e.Attempt, 0, e.Delay, msg)
	}
}

// handleDelta 把一次流式事件投影到流面板：正文、思考、工具参数分流。
func (o *observer) handleDelta(agent string, ev litellm.Event) {
	switch e := ev.(type) {
	case litellm.BlockStart:
		switch b := e.Block.(type) {
		case litellm.TextBlock:
			o.emitTextDelta(agent, b.Text)
		case litellm.ReasoningBlock:
			o.emitThinkingDelta(agent, b.Text)
		case litellm.ToolUseBlock:
			o.setStreamTool(agent, e.Index, b.Name)
			if b.Name != "" {
				o.updateModelState(agent, "生成 "+b.Name)
			}
			o.emitToolArgsDelta(agent, b.Name, b.Arguments)
		}
	case litellm.TextDelta:
		o.emitTextDelta(agent, e.Text)
	case litellm.ReasoningDelta:
		o.emitThinkingDelta(agent, e.Text)
	case litellm.ToolUseDelta:
		o.emitToolArgsDelta(agent, o.streamTool(agent, e.Index), e.Arguments)
	}
}

func (o *observer) emitTextDelta(agent, text string) {
	if text == "" {
		return
	}
	o.updateModelState(agent, "生成回复")
	o.emitStreamDelta(text, false)
}

func (o *observer) emitThinkingDelta(agent, text string) {
	if text == "" {
		return
	}
	o.updateModelState(agent, "思考中")
	o.emitStreamDelta(text, true)
}

// emitToolArgsDelta 只把已知长内容工具（draft_chapter 等）的参数按字段流出，
// 其余工具的参数 JSON 丢弃，避免刷屏。
func (o *observer) emitToolArgsDelta(agent, tool, args string) {
	if tool == "" || args == "" {
		return
	}
	o.handleToolUpdate(progress.Event{
		Kind:      progress.KindToolDelta,
		DeltaKind: progress.DeltaToolCall,
		Agent:     agent,
		Tool:      tool,
		Delta:     args,
	})
}

// ── 流式工具块的归属 ──
//
// litellm 按 Index 编号块，ToolUseDelta 只带 Index，工具名要回查 BlockStart。
// 并行调用会交错，所以按 agent 分别记录，MessageEnd 后清理。

func (o *observer) setStreamTool(agent string, index int, tool string) {
	if tool == "" {
		return
	}
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	if o.streamTools == nil {
		o.streamTools = make(map[string]map[int]string)
	}
	m := o.streamTools[agent]
	if m == nil {
		m = make(map[int]string)
		o.streamTools[agent] = m
	}
	m[index] = tool
}

func (o *observer) streamTool(agent string, index int) string {
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	return o.streamTools[agent][index]
}

func (o *observer) clearStreamTool(agent string) {
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	delete(o.streamTools, agent)
}

func (o *observer) startModelResponse(agent string) {
	now := time.Now()
	call := &activeCall{id: nextEventID(), start: now, summary: "等待模型", depth: 1}
	o.modelStarts[agent] = call
	o.emitAndLog(Event{
		ID:       call.id,
		Time:     now,
		Category: "MODEL",
		Agent:    agent,
		Summary:  call.summary,
		Level:    "info",
		Depth:    call.depth,
	})
}

func (o *observer) updateModelState(agent, summary string) {
	call := o.modelStarts[agent]
	if call == nil || summary == "" || call.summary == summary {
		return
	}
	call.summary = summary
	o.emitEv(Event{
		ID:       call.id,
		Time:     call.start,
		Category: "MODEL",
		Agent:    agent,
		Summary:  summary,
		Level:    "info",
		Depth:    call.depth,
	})
}

func (o *observer) finishModelResponse(agent string) {
	call := o.modelStarts[agent]
	if call == nil {
		return
	}
	delete(o.modelStarts, agent)
	call.summary = "模型响应"
	// MODEL 是实时观测事件，只写日志并投递 UI，不进入 runtime queue。
	o.emitEv(Event{
		ID:         call.id,
		Time:       call.start,
		FinishedAt: time.Now(),
		Category:   "MODEL",
		Agent:      agent,
		Summary:    call.summary,
		Level:      "success",
		Depth:      call.depth,
		Duration:   time.Since(call.start),
	})
}
