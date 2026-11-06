package host

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/progress"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// handleToolUpdate 处理 Worker 的进度中继(progress.Event):TOOL 行、流式正文、
// thinking、retry、context。Engine 经 observer.workerProgress 喂入。
func (o *observer) handleToolUpdate(ev progress.Event) {
	switch ev.Kind {
	case progress.KindToolDelta:
		if ev.Delta != "" {
			o.handleSubagentDelta(&ev)
		}
	case progress.KindToolStart:
		// Worker 内部的工具调用（如 writer → draft_chapter）。
		if ev.Agent == "" || ev.Tool == "" {
			break
		}
		toolName := displayToolName(ev.Tool, ev.Args)
		id := nextEventID()
		o.toolStarts[ev.Agent] = &activeCall{id: id, start: time.Now(), summary: toolName, depth: 1}
		o.emitAndLog(Event{
			ID:       id,
			Time:     time.Now(),
			Category: "TOOL",
			Agent:    ev.Agent,
			Summary:  toolName,
			Level:    "info",
			Depth:    1,
		})
		o.updateAgent(ev.Agent, func(a *agentState) {
			a.state = "working"
			a.tool = ev.Tool
			a.summary = fmt.Sprintf("%s → %s", ev.Agent, toolName)
		})
		o.emitFallbackStreamHeader(ev.Tool)
	case progress.KindToolEnd:
		delete(o.streamExtractors, ev.Agent)
		if ev.Agent == "" {
			return
		}
		call, ok := o.toolStarts[ev.Agent]
		if !ok {
			return
		}
		delete(o.toolStarts, ev.Agent)
		// 同 ID 更新事件：TUI 按 ID 定位原 TOOL 行，回填 FinishedAt / Duration。
		// Summary / Depth 也带上，保证 runtime queue replay 时能还原完整行。
		finishEv := Event{
			ID:         call.id,
			Time:       call.start,
			FinishedAt: time.Now(),
			Category:   "TOOL",
			Agent:      ev.Agent,
			Summary:    call.summary,
			Level:      "info",
			Depth:      call.depth,
			Duration:   time.Since(call.start),
		}
		o.emitEv(finishEv)
		o.persistEvent(finishEv)
	case progress.KindThinking:
		o.handleThinkingProgress(ev)
	case progress.KindRetry:
		// 只展示上游明确报告的实际等待时间，避免本地估算与真实退避节奏不一致。
		// Summary 不嵌静态延时——UI 依 RetryAt 逐秒倒计时；Detail/日志保留发出时的延时快照。
		o.emitRetryRow(ev.Agent, ev.Attempt, ev.MaxRetries, retryProgressDelay(&ev), ev.Message)
	case progress.KindToolError:
		delete(o.streamExtractors, ev.Agent)
		msg := ev.Message
		if msg == "" {
			msg = "unknown error"
		}
		// 如果有进行中的 TOOL 行，原地标记为失败并把完整错误放进 Detail。
		// 同一次失败只生成一个 ERROR 级事件，避免 TOOL 失败态与附加 ERROR
		// 详情在 tui.log 中被误读成两次独立故障。
		if call, ok := o.toolStarts[ev.Agent]; ok {
			delete(o.toolStarts, ev.Agent)
			detail := fmt.Sprintf("%s 错误: %s", ev.Tool, msg)
			finishEv := Event{
				ID:         call.id,
				Time:       call.start,
				FinishedAt: time.Now(),
				Failed:     true,
				Category:   "TOOL",
				Agent:      ev.Agent,
				Summary:    fmt.Sprintf("%s 错误: %s", call.summary, utils.TruncateRunes(msg, 100)),
				Detail:     detail,
				Kind:       errorKind(nil, msg),
				Level:      "error",
				Depth:      call.depth,
				Duration:   time.Since(call.start),
			}
			o.emitEv(finishEv)
			o.persistEvent(finishEv)
			return
		}
		// 极少数缺失 start 的进度流无法原地更新，保留独立 ERROR 事件暴露故障。
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Agent:    ev.Agent,
			Summary:  fmt.Sprintf("%s 错误: %s", ev.Tool, utils.TruncateRunes(msg, 100)),
			Detail:   fmt.Sprintf("%s 错误: %s", ev.Tool, msg),
			Kind:     errorKind(nil, msg),
			Level:    "error",
			Depth:    1,
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
	case progress.KindContext:
		o.handleContextProgress(ev)
	}
}

// emitRetryRow 发一行重试提示：同一 scope 的连续尝试原地更新同一行 (2/7)。
func (o *observer) emitRetryRow(agent string, attempt, maxRetries int, delay time.Duration, msg string) {
	retryEv := Event{
		ID:       o.retryEventID(agent, attempt),
		Time:     time.Now(),
		Category: "SYSTEM",
		Agent:    agent,
		Summary:  retryPrefix(attempt, maxRetries, 0) + utils.TruncateRunes(msg, 80),
		Detail:   retryPrefix(attempt, maxRetries, delay) + msg,
		Kind:     errorKind(nil, msg),
		Level:    "warn",
		Depth:    1,
	}
	if delay > 0 {
		retryEv.RetryAt = retryEv.Time.Add(delay)
	}
	o.emitEv(retryEv)
	o.persistEvent(retryEv)
}

func retryProgressDelay(p *progress.Event) time.Duration {
	if p == nil {
		return 0
	}
	if len(p.Meta) > 0 {
		var meta struct {
			DelayMS int64 `json:"retry_delay_ms"`
		}
		if json.Unmarshal(p.Meta, &meta) == nil && meta.DelayMS > 0 {
			return time.Duration(meta.DelayMS) * time.Millisecond
		}
	}
	return 0
}

func dispatchSummary(agent, task string) string {
	if agent == "" {
		agent = "subagent"
	}
	if task == "" {
		return agent
	}
	firstLine := strings.TrimSpace(strings.SplitN(task, "\n", 2)[0])
	if firstLine == "" {
		return agent
	}
	return agent + "（" + utils.TruncateRunes(firstLine, 30) + "）"
}

func dispatchDetail(task, reason string) string {
	var parts []string
	if strings.TrimSpace(reason) != "" {
		parts = append(parts, "派发原因: "+reason)
	}
	if strings.TrimSpace(task) != "" {
		parts = append(parts, "完整任务:\n"+task)
	}
	return strings.Join(parts, "\n")
}

func (o *observer) emitCallFinish(call *activeCall, category, agentName string, callErr error) {
	if call == nil {
		return
	}
	failed := callErr != nil
	level := "success"
	if failed {
		level = "error"
	}
	summary := call.summary
	detail := ""
	kind := ""
	if failed {
		detail = callErr.Error()
		kind = errorKind(callErr, detail)
		summary = fmt.Sprintf("%s 错误: %s", call.summary, utils.TruncateRunes(detail, 100))
	}
	finishEv := Event{
		ID:         call.id,
		Time:       call.start,
		FinishedAt: time.Now(),
		Failed:     failed,
		Category:   category,
		Agent:      agentName,
		Summary:    summary,
		Detail:     detail,
		Kind:       kind,
		Level:      level,
		Depth:      call.depth,
		Duration:   time.Since(call.start),
	}
	o.emitEv(finishEv)
	o.persistEvent(finishEv)
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
