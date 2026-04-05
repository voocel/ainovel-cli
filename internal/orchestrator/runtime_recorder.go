package orchestrator

import (
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type runtimeRecorder struct {
	runtime            *storepkg.RuntimeStore
	taskRT             *novelTaskRuntime
	pendingStreamTask  string
	pendingStreamAgent string
	pendingStreamText  strings.Builder
}

const streamDeltaFlushThreshold = 512

func newRuntimeRecorder(store *storepkg.Store, taskRT *novelTaskRuntime) *runtimeRecorder {
	var runtimeStore *storepkg.RuntimeStore
	if store != nil {
		runtimeStore = store.Runtime
	}
	return &runtimeRecorder{
		runtime: runtimeStore,
		taskRT:  taskRT,
	}
}

func (r *runtimeRecorder) appendQueue(item domain.RuntimeQueueItem) {
	if r == nil || r.runtime == nil {
		return
	}
	if item.Time.IsZero() {
		item.Time = time.Now()
	}
	_, _ = r.runtime.AppendQueue(item)
}

func (r *runtimeRecorder) logTaskEvent(owner, event, tool, summary string, payload any) {
	r.appendTaskLog(owner, domain.RuntimeTaskLogEntry{
		Event:   event,
		Tool:    tool,
		Summary: summary,
		Payload: payload,
	})
}

func (r *runtimeRecorder) appendTaskLog(owner string, entry domain.RuntimeTaskLogEntry) {
	if r == nil || r.runtime == nil {
		return
	}
	owner = canonicalAgentName(strings.TrimSpace(owner))
	if owner == "" {
		owner = inferredLogOwner(entry.Tool)
	}
	if owner == "" {
		owner = canonicalAgentName(strings.TrimSpace(entry.Agent))
	}
	if owner == "" {
		return
	}
	if entry.Agent == "" {
		entry.Agent = owner
	}
	if entry.TaskID == "" {
		if r.taskRT == nil {
			return
		}
		task, ok := r.taskRT.ActiveTask(owner)
		if !ok {
			return
		}
		entry.TaskID = task.ID
	}
	_ = r.runtime.AppendTaskLog(entry.TaskID, entry)
}

func (r *runtimeRecorder) activeTaskID(owner string) string {
	if r == nil || r.taskRT == nil {
		return ""
	}
	task, ok := r.taskRT.ActiveTask(owner)
	if !ok {
		return ""
	}
	return task.ID
}

func (r *runtimeRecorder) logControlAction(action policyAction) {
	summary := action.Summary
	if summary == "" {
		summary = action.Message
	}
	if summary == "" {
		summary = string(action.Kind)
	}
	r.appendQueue(domain.RuntimeQueueItem{
		Time:     time.Now(),
		Kind:     domain.RuntimeQueueControl,
		Priority: domain.RuntimePriorityControl,
		Category: action.Category,
		Summary:  summary,
		Payload:  action,
	})
}

func (r *runtimeRecorder) recordStreamClear(owner string) {
	owner = canonicalAgentName(owner)
	r.flushPendingStream()
	r.appendQueue(domain.RuntimeQueueItem{
		Time:     time.Now(),
		Kind:     domain.RuntimeQueueStreamClear,
		Priority: domain.RuntimePriorityBackground,
		TaskID:   r.activeTaskID(owner),
		Agent:    owner,
		Summary:  "stream.clear",
	})
	r.logTaskEvent(owner, "stream_clear", "", "开始新一轮输出", nil)
}

func (r *runtimeRecorder) recordStreamDelta(owner, text string) {
	if r == nil || text == "" {
		return
	}
	owner = canonicalAgentName(owner)
	taskID := r.activeTaskID(owner)
	if r.pendingStreamText.Len() > 0 && (r.pendingStreamAgent != owner || r.pendingStreamTask != taskID) {
		r.flushPendingStream()
	}
	if r.pendingStreamAgent == "" {
		r.pendingStreamAgent = owner
		r.pendingStreamTask = taskID
	}
	_, _ = r.pendingStreamText.WriteString(text)
	if r.pendingStreamText.Len() >= streamDeltaFlushThreshold {
		r.flushPendingStream()
	}
}

func (r *runtimeRecorder) flushPendingStream() {
	if r == nil || r.pendingStreamText.Len() == 0 {
		return
	}
	text := r.pendingStreamText.String()
	owner := r.pendingStreamAgent
	taskID := r.pendingStreamTask
	r.pendingStreamText.Reset()
	r.pendingStreamAgent = ""
	r.pendingStreamTask = ""
	summary := truncateLog(text, 120)
	r.appendQueue(domain.RuntimeQueueItem{
		Time:     time.Now(),
		Kind:     domain.RuntimeQueueStreamDelta,
		Priority: domain.RuntimePriorityBackground,
		TaskID:   taskID,
		Agent:    owner,
		Summary:  summary,
		Payload: map[string]any{
			"delta": text,
		},
	})
	r.appendTaskLog(owner, domain.RuntimeTaskLogEntry{
		TaskID:  taskID,
		Agent:   owner,
		Event:   "stream_delta",
		Summary: summary,
		Payload: map[string]any{
			"delta": text,
		},
	})
}

func inferredLogOwner(tool string) string {
	switch tool {
	case "save_foundation":
		return "architect"
	case "plan_chapter", "draft_chapter", "check_consistency", "commit_chapter":
		return "writer"
	case "save_review", "save_arc_summary", "save_volume_summary":
		return "editor"
	case "ask_user":
		return "coordinator"
	default:
		return ""
	}
}
