package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/apperr"
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
	emit        emitFn
	onDelta     deltaFn
	onClear     clearFn
	continueRun func(string) error

	providerMu           sync.RWMutex
	provider             string
	providerSource       func() string
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

func newSession(coordinator *agentcore.Agent, store *storepkg.Store, taskRT *novelTaskRuntime, agents *agentBoard, provider string, emit emitFn, onDelta deltaFn, onClear clearFn, continueRun func(string) error, refreshWriterRestore func()) *session {
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
		continueRun:          continueRun,
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
	s.resetCurrentProvider()
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
		slog.Info("agent 已取消", "module", "agent", "provider", s.currentProvider())
		if s.taskRT != nil {
			_ = s.taskRT.CancelActive(coordinatorRuntimeOwner, "已暂停")
		}
		if s.agents != nil {
			s.agents.Idle("coordinator", "已暂停")
		}
		return
	}
	err := apperr.ClassifyProviderError(ev.Err, "orchestrator.provider_call")
	code := apperr.CodeOf(err)
	provider := s.currentProvider()
	summary := fmt.Sprintf("[%s][%s] %s", provider, code, apperr.Display(err))
	slog.Error("provider 错误", "module", "agent", "provider", provider, "code", code, "err", err)
	if s.taskRT != nil {
		_ = s.taskRT.FailActive(coordinatorRuntimeOwner, summary)
	}
	if s.agents != nil {
		s.agents.Fail("coordinator", summary)
	}
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "ERROR", Summary: summary, Level: "error"})
	}
	s.recorder.logTaskEvent("coordinator", "provider_error", "", summary, map[string]any{
		"provider_code": code,
	})
}

func (s *session) handleRetry(ev agentcore.Event) {
	if ev.RetryInfo == nil {
		return
	}
	err := apperr.ClassifyProviderError(ev.RetryInfo.Err, "orchestrator.provider_retry")
	code := apperr.CodeOf(err)
	slog.Warn("重试", "module", "agent", "attempt", ev.RetryInfo.Attempt,
		"max", ev.RetryInfo.MaxRetries, "code", code, "err", err)
	summary := fmt.Sprintf("重试 (%d/%d) [%s]: %s", ev.RetryInfo.Attempt, ev.RetryInfo.MaxRetries, code, apperr.Display(err))
	if s.emit != nil {
		s.emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
			Summary: summary,
			Level:   "warn"})
	}
	s.recorder.logTaskEvent("coordinator", "provider_retry", "", summary, map[string]any{
		"provider_code": code,
	})
}

func (s *session) currentProvider() string {
	s.providerMu.RLock()
	defer s.providerMu.RUnlock()
	return s.provider
}

func (s *session) setCurrentProvider(provider string) {
	if strings.TrimSpace(provider) == "" {
		return
	}
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	s.provider = provider
}

func (s *session) resetCurrentProvider() {
	if s.providerSource == nil {
		return
	}
	s.setCurrentProvider(s.providerSource())
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
