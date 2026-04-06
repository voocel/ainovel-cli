package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

func (s *session) handleToolExecStart(ev agentcore.Event) {
	slog.Debug("工具开始", "module", "tool", "name", ev.Tool)
	s.trackTaskStart(ev)
	if ev.Tool == "subagent" {
		if inv, ok := parseSubagentInvocation(ev.Args); ok {
			owner := canonicalAgentName(inv.Agent)
			s.recorder.logTaskEvent(owner, "task_start", ev.Tool, inv.Task, map[string]any{
				"agent": inv.Agent,
			})
		}
	}
	if ev.Tool == "ask_user" {
		summary, payload := summarizeAskUserRequest(ev.Args)
		s.recorder.logTaskEvent("coordinator", "ask_user_request", ev.Tool, summary, payload)
		if s.emit != nil {
			s.emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "info"})
		}
		return
	}
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: ev.Tool + ".start", Level: "info"})
	}
}

func (s *session) handleToolExecUpdate(ev agentcore.Event) {
	if progress, ok := parseToolProgress(ev); ok {
		s.trackAgentProgress(progress)
		s.recorder.logTaskEvent(progress.Agent, "tool_progress", progress.Tool, progressSummaryLabel(progress), progress)
	}
	if progress, ok := parseContextProgress(ev); ok {
		s.trackAgentContext(progress)
		s.recorder.logTaskEvent(progress.Agent, "context", "", fmt.Sprintf("%s %.1f%%", progress.Scope, progress.Percent), progress)
	}
	if ev.Progress != nil && ev.Progress.Kind == agentcore.ProgressTurnCounter {
		s.trackAgentTurn(ev.Progress.Agent, ev.Progress.Turn)
		s.recorder.logTaskEvent(ev.Progress.Agent, "turn", "", fmt.Sprintf("turn %d", ev.Progress.Turn), map[string]any{"turn": ev.Progress.Turn})
	}
	if delta, ok := parseStreamDelta(ev); ok {
		if s.onDelta != nil {
			if text := s.subFilter.Feed(delta); text != "" {
				agent := ""
				if ev.Progress != nil {
					agent = ev.Progress.Agent
				}
				s.emitDisplayDelta(agent, text)
			}
		}
		return
	}
	if thinking, ok := parseThinkingDelta(ev); ok {
		if s.onDelta != nil {
			if text := s.subFilter.Feed(incrementalThinkingDelta(s.lastThinkingText, thinking)); text != "" {
				agent := ""
				if ev.Progress != nil {
					agent = ev.Progress.Agent
				}
				s.emitDisplayDelta(agent, text)
			}
		}
		s.lastThinkingText = thinking
		return
	}
	if ev.Progress != nil && ev.Progress.Kind == agentcore.ProgressToolStart {
		if preview := toolStartPreview(ev.Progress.Tool, ev.Progress.Args); preview != "" && s.onDelta != nil {
			if text := s.subFilter.Feed(preview); text != "" {
				s.emitDisplayDelta(ev.Progress.Agent, text)
			}
		}
	}
	if retry, ok := parseSubAgentRetry(ev); ok {
		slog.Warn("SubAgent 重试", "module", "tool", "summary", retry)
		if s.emit != nil {
			s.emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: retry, Level: "warn"})
		}
		if ev.Progress != nil {
			s.recorder.logTaskEvent(ev.Progress.Agent, "retry", "", retry, nil)
		}
		return
	}

	summary := parseProgressSummary(ev)
	if summary == "" || summary == s.lastProgressSummary {
		return
	}
	if progress, ok := parseToolProgress(ev); ok {
		s.reminders.observeToolProgress(progress)
		s.executePolicyActions(s.reminders.drain(), s.emit)
	}
	s.lastProgressSummary = summary
	slog.Debug("进度", "module", "tool", "summary", summary)
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: summary, Level: "info"})
	}
}

func (s *session) handleToolExecEnd(ev agentcore.Event) {
	s.trackTaskEnd(ev)
	s.lastProgressSummary = ""
	if ev.IsError {
		s.handleToolExecError(ev)
		return
	}
	if ev.Tool == "subagent" {
		s.handleSubAgentEventEnd(ev)
		return
	}
	if ev.Tool == "novel_context" {
		s.handleNovelContextEnd(ev)
		return
	}
	if ev.Tool == "ask_user" {
		s.handleAskUserEnd(ev)
		return
	}
	if s.refreshWriterRestore != nil && toolAffectsWriterRestore(ev.Tool) {
		s.refreshWriterRestore()
	}
	if ev.Tool == "save_foundation" {
		if s.taskRT != nil {
			_ = s.taskRT.AttachOutputRef("architect", foundationOutputRef(ev.Result))
		}
		slog.Debug("工具完成", "module", "tool", "name", ev.Tool, "result", truncateLog(string(ev.Result), 200))
		if s.emit != nil {
			s.emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: foundationResultSummary(ev.Result), Level: "info"})
		}
		s.recorder.logTaskEvent("architect", "tool_done", ev.Tool, foundationResultSummary(ev.Result), nil)
		return
	}

	slog.Debug("工具完成", "module", "tool", "name", ev.Tool, "result", truncateLog(string(ev.Result), 200))
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: ev.Tool + ".done", Level: "info"})
	}
	s.recorder.logTaskEvent("", "tool_done", ev.Tool, ev.Tool+".done", nil)
}

func toolAffectsWriterRestore(tool string) bool {
	switch tool {
	case "plan_chapter", "save_foundation", "commit_chapter":
		return true
	default:
		return false
	}
}

func (s *session) handleToolExecError(ev agentcore.Event) {
	detail := extractToolErrorText(ev.Result)
	if ev.Tool == "subagent" && isUserCanceledText(detail) {
		slog.Info("subagent 工具已取消",
			"module", "tool",
			"name", ev.Tool,
			"raw_detail", detail,
			"raw_result", string(ev.Result))
		return
	}
	slog.Error("工具执行失败", "module", "tool", "name", ev.Tool, "detail", truncateLog(detail, 120))
	if ev.Tool != "subagent" {
		s.reminders.observeToolFailure(ev.Tool, detail)
		s.executePolicyActions(s.reminders.drain(), s.emit)
	}
	if s.emit != nil {
		summary := ev.Tool + " 执行失败"
		if detail != "" {
			summary += ": " + truncateLog(detail, 80)
		}
		s.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: summary, Level: "error"})
	}
	s.recorder.logTaskEvent("", "tool_error", ev.Tool, truncateLog(detail, 120), nil)
}

func (s *session) handleSubAgentEventEnd(ev agentcore.Event) {
	logSubAgentResult(ev.Result, s.emit)
	committed := s.handleSubAgentDone(s.emit)
	s.handleEditorDone(s.emit)
	s.reminders.observeSubAgentDone(s.store, committed)
	s.executePolicyActions(s.reminders.drain(), s.emit)
	s.runOperationalDiag()
}

func (s *session) handleNovelContextEnd(ev agentcore.Event) {
	if summary := extractLoadingSummary(ev.Result); summary != "" {
		slog.Info("上下文加载", "module", "tool", "summary", summary)
		if s.emit != nil {
			s.emit(UIEvent{Time: time.Now(), Category: "CONTEXT", Summary: summary, Level: "info"})
		}
	} else {
		slog.Debug("上下文加载", "module", "tool", "result", truncateLog(string(ev.Result), 200))
	}
	if evidence := extractContextBuildEvidence(ev.Result); evidence != nil {
		owner := s.contextEvidenceOwner(evidence)
		evidence.Agent = owner
		s.recorder.recordEvidence(owner, "context_build", contextEvidenceSummary(*evidence), *evidence)
	}
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: "novel_context.done", Level: "info"})
	}
}

func (s *session) handleAskUserEnd(ev agentcore.Event) {
	answer := extractAskUserAnswer(ev.Result)
	summary := "用户已完成补充信息"
	if answer != "" {
		summary = truncateLog(answer, 80)
	}
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "info"})
	}
	payload := map[string]any{}
	if answer != "" {
		payload["answer"] = answer
	}
	s.recorder.logTaskEvent("coordinator", "ask_user_answer", ev.Tool, truncateLog(summary, 120), payload)
}

func summarizeAskUserRequest(args json.RawMessage) (string, map[string]any) {
	var payload struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	summary := "等待用户补充关键信息"
	if err := json.Unmarshal(args, &payload); err != nil || len(payload.Questions) == 0 {
		return summary, map[string]any{}
	}

	questions := make([]map[string]any, 0, len(payload.Questions))
	headers := make([]string, 0, len(payload.Questions))
	for _, q := range payload.Questions {
		header := strings.TrimSpace(q.Header)
		if header == "" {
			header = strings.TrimSpace(q.Question)
		}
		if header != "" {
			headers = append(headers, header)
		}
		options := make([]string, 0, len(q.Options))
		for _, opt := range q.Options {
			label := strings.TrimSpace(opt.Label)
			if label != "" {
				options = append(options, label)
			}
		}
		questions = append(questions, map[string]any{
			"header":       q.Header,
			"question":     q.Question,
			"multi_select": q.MultiSelect,
			"options":      options,
		})
	}

	summary = fmt.Sprintf("等待用户补充关键信息（%d 个问题）", len(payload.Questions))
	if len(headers) > 0 {
		summary += "：" + truncateLog(strings.Join(headers, "、"), 24)
	}
	return summary, map[string]any{"questions": questions}
}

func extractAskUserAnswer(result json.RawMessage) string {
	var text string
	if err := json.Unmarshal(result, &text); err == nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(string(result))
}
