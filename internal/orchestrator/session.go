package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// session 封装一次创作会话的运行时控制面。
// 它统一管理事件订阅、恢复判断和用户干预注入。
type session struct {
	coordinator *agentcore.Agent
	store       *storepkg.Store
	provider    string
	emit        emitFn
	onDelta     deltaFn
	onClear     clearFn

	lastProgressSummary string
	agentExt            *jsonFieldExtractor
	taskExt             *jsonFieldExtractor
	subFilter           *streamFilter
	reminders           *reminderEngine
	pendingClear        bool
}

func newSession(coordinator *agentcore.Agent, store *storepkg.Store, provider string, emit emitFn, onDelta deltaFn, onClear clearFn) *session {
	return &session{
		coordinator: coordinator,
		store:       store,
		provider:    provider,
		emit:        emit,
		onDelta:     onDelta,
		onClear:     onClear,
		agentExt:    newFieldExtractor("agent"),
		taskExt:     newFieldExtractor("task"),
		subFilter:   newStreamFilter("content"),
		reminders:   newReminderEngine(store),
	}
}

func (s *session) bind() {
	s.coordinator.Subscribe(s.handleEvent)
}

func (s *session) handleEvent(ev agentcore.Event) {
	switch ev.Type {
	case agentcore.EventToolExecStart:
		s.handleToolExecStart(ev)
	case agentcore.EventToolExecUpdate:
		s.handleToolExecUpdate(ev)
	case agentcore.EventMessageStart:
		s.handleMessageStart()
	case agentcore.EventMessageUpdate:
		s.handleMessageUpdate(ev)
	case agentcore.EventToolExecEnd:
		s.handleToolExecEnd(ev)
	case agentcore.EventMessageEnd:
		s.handleMessageEnd(ev)
	case agentcore.EventError:
		s.handleProviderError(ev)
	case agentcore.EventRetry:
		s.handleRetry(ev)
	}
}

func (s *session) recovery() recoveryResult {
	progress, _ := s.store.Progress.Load()
	runMeta, _ := s.store.RunMeta.Load()
	return applyHandoffToRecovery(s.store, determineRecovery(progress, runMeta, s.store))
}

func (s *session) persistSteer(text string) {
	slog.Info("用户干预", "module", "steer", "text", text)
	if err := s.store.RunMeta.AppendSteerEntry(domain.SteerEntry{
		Input:     text,
		Timestamp: time.Now().Format(time.RFC3339),
	}); err != nil {
		slog.Error("追加干预记录失败", "module", "steer", "err", err)
	}
	if err := s.store.RunMeta.SetPendingSteer(text); err != nil {
		slog.Error("设置待处理干预失败", "module", "steer", "err", err)
	}
	if err := s.store.Progress.SetFlow(domain.FlowSteering); err != nil {
		slog.Error("设置流程状态失败", "module", "steer", "err", err)
	}
}

func (s *session) submitSteer(text string) {
	s.persistSteer(text)
	runMeta, err := s.store.RunMeta.Load()
	if err != nil {
		slog.Warn("读取运行元信息失败", "module", "steer", "err", err)
	}
	guidance := planningTierGuidance(runMeta)
	message := fmt.Sprintf("[用户干预] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。", text)
	if guidance != "" {
		message += "\n" + guidance
	}
	s.coordinator.Steer(agentcore.UserMsg(message))
}

func (s *session) finalizeSteerIfIdle() {
	runMeta, _ := s.store.RunMeta.Load()
	progress, _ := s.store.Progress.Load()
	if runMeta == nil || runMeta.PendingSteer == "" || progress == nil {
		return
	}
	if progress.Flow != domain.FlowSteering {
		return
	}
	s.clearHandledSteer()
}

func (s *session) executePolicyActions(actions []policyAction, emit emitFn) {
	for _, action := range actions {
		switch action.Kind {
		case actionEmitNotice:
			if emit != nil {
				category := action.Category
				if category == "" {
					category = "SYSTEM"
				}
				emit(UIEvent{
					Time:     time.Now(),
					Category: category,
					Summary:  action.Summary,
					Level:    action.Level,
				})
			}
		case actionFollowUp:
			s.coordinator.FollowUp(agentcore.UserMsg(action.Message))
		case actionSetFlow:
			if err := s.store.Progress.SetFlow(action.Flow); err != nil {
				slog.Error("设置流程状态失败", "module", "host", "flow", action.Flow, "err", err)
			}
		case actionSetPendingRewrites:
			if err := s.store.Progress.SetPendingRewrites(action.Chapters, action.Reason); err != nil {
				slog.Error("设置待处理章节失败", "module", "host", "chapters", action.Chapters, "err", err)
			}
		case actionCompleteRewrite:
			if err := s.store.Progress.CompleteRewrite(action.Chapter); err != nil {
				slog.Error("完成重写标记失败", "module", "host", "chapter", action.Chapter, "err", err)
				continue
			}
			updated, _ := s.store.Progress.Load()
			if updated != nil && len(updated.PendingRewrites) == 0 {
				s.saveCheckpoint("rewrite-done")
				if emit != nil {
					emit(UIEvent{
						Time:     time.Now(),
						Category: "SYSTEM",
						Summary:  "所有重写/打磨已完成",
						Level:    "success",
					})
				}
			}
		case actionClearHandledSteer:
			s.clearHandledSteer()
		case actionSaveCheckpoint:
			s.saveCheckpoint(action.Label)
		case actionSaveHandoff:
			if err := saveHandoffSnapshot(s.store, action.Label); err != nil {
				slog.Error("保存交接包失败", "module", "host", "label", action.Label, "err", err)
			}
		case actionMarkComplete:
			if err := s.store.Progress.MarkComplete(); err != nil {
				slog.Error("标记完成失败", "module", "host", "err", err)
			}
		}
	}
}

// handleSubAgentDone 在每次 SubAgent 调用完成后读取文件系统信号，注入确定性任务。
// 返回 true 表示检测到 commit 信号（Writer 正常完成）。
func (s *session) handleSubAgentDone(emit emitFn) bool {
	result, err := s.store.Signals.LoadAndClearLastCommit()
	if err != nil || result == nil {
		return false
	}

	slog.Info("章节提交信号", "module", "host", "chapter", result.Chapter, "words", result.WordCount)
	if emit != nil {
		emit(UIEvent{
			Time:     time.Now(),
			Category: "SYSTEM",
			Summary:  fmt.Sprintf("第 %d 章已提交：%d 字", result.Chapter, result.WordCount),
			Level:    "success",
		})
	}

	progress, _ := s.store.Progress.Load()
	runMeta, _ := s.store.RunMeta.Load()
	actions := evaluateCommitPolicy(progress, runMeta, result)
	s.executePolicyActions(actions, emit)
	return true
}

// handleEditorDone 在 Editor SubAgent 完成后读取审阅信号。
func (s *session) handleEditorDone(emit emitFn) {
	review, err := s.store.Signals.LoadAndClearLastReview()
	if err != nil {
		slog.Error("加载审阅信号失败", "module", "host", "err", err)
		return
	}
	if review == nil {
		return
	}

	criticalN := review.CriticalCount()
	slog.Info("审阅信号", "module", "host",
		"verdict", review.Verdict, "issues", len(review.Issues),
		"critical", criticalN, "errors", review.ErrorCount())

	if review.Verdict == "accept" && criticalN > 0 {
		slog.Warn("critical 问题但 verdict=accept，强制升级为 rewrite", "module", "host", "critical", criticalN)
		review.Verdict = "rewrite"
	}
	runMeta, _ := s.store.RunMeta.Load()
	actions := evaluateReviewPolicy(runMeta, review)
	s.executePolicyActions(actions, emit)
}

func (s *session) clearHandledSteer() {
	if err := s.store.ClearHandledSteer(); err != nil {
		slog.Error("清除干预状态失败", "module", "host", "err", err)
	}
}

func (s *session) saveCheckpoint(label string) {
	progress, _ := s.store.Progress.Load()
	if err := s.store.RunMeta.SaveCheckpoint(label, progress); err != nil {
		slog.Error("保存检查点失败", "module", "host", "label", label, "err", err)
	}
}

func (s *session) handleToolExecStart(ev agentcore.Event) {
	slog.Debug("工具开始", "module", "tool", "name", ev.Tool)
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: ev.Tool + ".start", Level: "info"})
	}
}

func (s *session) handleToolExecUpdate(ev agentcore.Event) {
	if delta, ok := parseStreamDelta(ev); ok {
		if s.onDelta != nil {
			if text := s.subFilter.Feed(delta); text != "" {
				s.emitDisplayDelta(text)
			}
		}
		return
	}
	if thinking, ok := parseThinkingDelta(ev); ok {
		if s.onDelta != nil {
			if text := s.subFilter.Feed(thinking); text != "" {
				s.emitDisplayDelta(text)
			}
		}
		return
	}
	if ev.Progress != nil && ev.Progress.Kind == agentcore.ProgressToolStart {
		if preview := toolStartPreview(ev.Progress.Tool, ev.Progress.Args); preview != "" && s.onDelta != nil {
			if text := s.subFilter.Feed(preview); text != "" {
				s.emitDisplayDelta(text)
			}
		}
	}
	if retry, ok := parseSubAgentRetry(ev); ok {
		slog.Warn("SubAgent 重试", "module", "tool", "summary", retry)
		if s.emit != nil {
			s.emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: retry, Level: "warn"})
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

func (s *session) handleMessageStart() {
	s.agentExt.Reset()
	s.taskExt.Reset()
	s.subFilter.Reset()
	s.pendingClear = true
}

func (s *session) handleMessageUpdate(ev agentcore.Event) {
	if ev.Delta == "" || s.onDelta == nil {
		return
	}
	if name := s.agentExt.Feed(ev.Delta); name != "" {
		s.emitDisplayDelta("\n▸ " + agentLabel(name) + "\n")
	}
	if text := s.taskExt.Feed(ev.Delta); text != "" {
		s.emitDisplayDelta(text)
	}
}

func (s *session) emitDisplayDelta(text string) {
	if text == "" || s.onDelta == nil {
		return
	}
	if s.pendingClear {
		if s.onClear != nil {
			s.onClear()
		}
		s.pendingClear = false
	}
	s.onDelta(text)
}

func (s *session) handleToolExecEnd(ev agentcore.Event) {
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
	if ev.Tool == "save_foundation" {
		slog.Debug("工具完成", "module", "tool", "name", ev.Tool, "result", truncateLog(string(ev.Result), 200))
		if s.emit != nil {
			s.emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: foundationResultSummary(ev.Result), Level: "info"})
		}
		return
	}

	slog.Debug("工具完成", "module", "tool", "name", ev.Tool, "result", truncateLog(string(ev.Result), 200))
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: ev.Tool + ".done", Level: "info"})
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
}

func (s *session) handleSubAgentEventEnd(ev agentcore.Event) {
	logSubAgentResult(ev.Result, s.emit)
	committed := s.handleSubAgentDone(s.emit)
	s.handleEditorDone(s.emit)
	s.reminders.observeSubAgentDone(s.store, committed)
	s.executePolicyActions(s.reminders.drain(), s.emit)
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
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: "novel_context.done", Level: "info"})
	}
}

func (s *session) handleMessageEnd(ev agentcore.Event) {
	if ev.Message != nil && ev.Message.GetRole() == agentcore.RoleAssistant {
		text := ev.Message.TextContent()
		if isUserCanceledText(text) {
			slog.Info("assistant 输出已取消提示",
				"module", "agent",
				"raw_text", text)
			return
		}
		slog.Debug("assistant", "module", "agent", "text", truncateLog(text, 100))
		if s.emit != nil {
			s.emit(UIEvent{Time: time.Now(), Category: "AGENT", Summary: truncateLog(text, 80), Level: "info"})
		}
	}
}

func (s *session) handleProviderError(ev agentcore.Event) {
	if ev.Err != nil && errors.Is(ev.Err, context.Canceled) {
		slog.Info("agent 已取消", "module", "agent", "provider", s.provider)
		return
	}
	slog.Error("provider 错误", "module", "agent", "provider", s.provider, "err", ev.Err)
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: fmt.Sprintf("[%s] %v", s.provider, ev.Err), Level: "error"})
	}
}

func (s *session) handleRetry(ev agentcore.Event) {
	if ev.RetryInfo == nil {
		return
	}
	slog.Warn("重试", "module", "agent", "attempt", ev.RetryInfo.Attempt,
		"max", ev.RetryInfo.MaxRetries, "err", ev.RetryInfo.Err)
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
			Summary: fmt.Sprintf("重试 (%d/%d): %v", ev.RetryInfo.Attempt, ev.RetryInfo.MaxRetries, ev.RetryInfo.Err),
			Level:   "warn"})
	}
}
