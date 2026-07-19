package host

import (
	"context"
	"fmt"
	"strings"
	"time"

	"encoding/json"
	"github.com/voocel/agentcore"
	"log/slog"
)

func retryPrefix(attempt, maxRetries int, delay time.Duration) string {
	if maxRetries <= 0 {
		if text := formatRetryDelay(delay); text != "" {
			return fmt.Sprintf("重试 (第%d次，%s后): ", attempt, text)
		}
		return fmt.Sprintf("重试 (第%d次): ", attempt)
	}
	if text := formatRetryDelay(delay); text != "" {
		return fmt.Sprintf("重试 (%d/%d，%s后): ", attempt, maxRetries, text)
	}
	return fmt.Sprintf("重试 (%d/%d): ", attempt, maxRetries)
}

func formatRetryDelay(delay time.Duration) string {
	if delay <= 0 {
		return ""
	}
	seconds := int64(delay / time.Second)
	if delay%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return (time.Duration(seconds) * time.Second).String()
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
		return
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

	if payload.Strategy != "" {
		// 触发了压缩 → 事件流 + 日志
		ctxEv := Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: summary, Level: level, Depth: 1}
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
