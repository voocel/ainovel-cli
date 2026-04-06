package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// session 封装一次创作会话的运行时控制面。
// 它统一管理事件订阅、恢复判断和用户干预注入。
type session struct {
	coordinator *agentcore.Agent
	store       *storepkg.Store
	recorder    *runtimeRecorder
	taskRT      *novelTaskRuntime
	scheduler   *taskScheduler
	agents      *agentBoard
	provider    string
	emit        emitFn
	onDelta     deltaFn
	onClear     clearFn
	enqueueCtrl func(domain.ControlIntent) error

	lastProgressSummary  string
	lastThinkingText     string
	agentExt             *utils.JSONFieldExtractor
	taskExt              *utils.JSONFieldExtractor
	subFilter            *utils.StreamFilter
	reminders            *reminderEngine
	pendingClear         bool
	diagActionKeys       map[string]struct{}
	refreshWriterRestore func()
}

func newSession(coordinator *agentcore.Agent, store *storepkg.Store, taskRT *novelTaskRuntime, agents *agentBoard, provider string, emit emitFn, onDelta deltaFn, onClear clearFn, enqueueCtrl func(domain.ControlIntent) error, refreshWriterRestore func()) *session {
	return &session{
		coordinator:          coordinator,
		store:                store,
		recorder:             newRuntimeRecorder(store, taskRT),
		taskRT:               taskRT,
		scheduler:            newTaskScheduler(taskRT),
		agents:               agents,
		provider:             provider,
		emit:                 emit,
		onDelta:              onDelta,
		onClear:              onClear,
		enqueueCtrl:          enqueueCtrl,
		agentExt:             utils.NewFieldExtractor("agent"),
		taskExt:              utils.NewFieldExtractor("task"),
		subFilter:            utils.NewStreamFilter("content"),
		reminders:            newReminderEngine(store),
		diagActionKeys:       make(map[string]struct{}),
		refreshWriterRestore: refreshWriterRestore,
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

// ---------------------------------------------------------------------------
// Message & display handlers
// ---------------------------------------------------------------------------

func (s *session) handleMessageStart() {
	s.agentExt.Reset()
	s.taskExt.Reset()
	s.subFilter.Reset()
	s.lastThinkingText = ""
	s.pendingClear = true
}

func (s *session) handleMessageUpdate(ev agentcore.Event) {
	if ev.Delta == "" || s.onDelta == nil {
		return
	}
	if name := s.agentExt.Feed(ev.Delta); name != "" {
		s.emitDisplayDelta("coordinator", "\n▸ "+agentLabel(name)+"\n")
	}
	if text := s.taskExt.Feed(ev.Delta); text != "" {
		s.emitDisplayDelta("coordinator", text)
	}
}

func (s *session) emitDisplayDelta(agent, text string) {
	if text == "" || s.onDelta == nil {
		return
	}
	owner := canonicalAgentName(agent)
	if s.pendingClear {
		if s.onClear != nil {
			s.onClear()
		}
		s.recorder.recordStreamClear(owner)
		s.pendingClear = false
	}
	s.recorder.recordStreamDelta(owner, text)
	s.onDelta(text)
}

func (s *session) handleMessageEnd(ev agentcore.Event) {
	s.recorder.flushPendingStream()
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
		s.recorder.logTaskEvent("coordinator", "assistant_message", "", truncateLog(text, 120), nil)
	}
}

func (s *session) handleProviderError(ev agentcore.Event) {
	s.recorder.flushPendingStream()
	if ev.Err != nil && errors.Is(ev.Err, context.Canceled) {
		slog.Info("agent 已取消", "module", "agent", "provider", s.provider)
		if s.taskRT != nil {
			_ = s.taskRT.CancelActive(coordinatorRuntimeOwner, "已暂停")
		}
		if s.agents != nil {
			s.agents.Idle("coordinator", "已暂停")
		}
		return
	}
	slog.Error("provider 错误", "module", "agent", "provider", s.provider, "err", ev.Err)
	if s.taskRT != nil {
		_ = s.taskRT.FailActive(coordinatorRuntimeOwner, fmt.Sprintf("[%s] %v", s.provider, ev.Err))
	}
	if s.agents != nil {
		s.agents.Fail("coordinator", fmt.Sprintf("[%s] %v", s.provider, ev.Err))
	}
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: fmt.Sprintf("[%s] %v", s.provider, ev.Err), Level: "error"})
	}
	s.recorder.logTaskEvent("coordinator", "provider_error", "", fmt.Sprintf("[%s] %v", s.provider, ev.Err), nil)
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
	s.recorder.logTaskEvent("coordinator", "provider_retry", "", fmt.Sprintf("重试 (%d/%d): %v", ev.RetryInfo.Attempt, ev.RetryInfo.MaxRetries, ev.RetryInfo.Err), nil)
}

// ---------------------------------------------------------------------------
// Utility helpers
// ---------------------------------------------------------------------------

func canonicalAgentName(name string) string {
	switch {
	case strings.HasPrefix(name, "architect"):
		return "architect"
	case name == "writer":
		return "writer"
	case name == "editor":
		return "editor"
	case name == "coordinator":
		return "coordinator"
	default:
		return name
	}
}

func incrementalThinkingDelta(previous, current string) string {
	if current == "" {
		return ""
	}
	if previous == "" {
		return current
	}
	if current == previous {
		return ""
	}
	if strings.HasPrefix(current, previous) {
		return current[len(previous):]
	}
	return current
}
