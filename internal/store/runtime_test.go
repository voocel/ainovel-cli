package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestRuntimeStoreAppendQueueAssignsSeq(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	first, err := store.Runtime.AppendQueue(domain.RuntimeQueueItem{
		Kind:     domain.RuntimeQueueUIEvent,
		Priority: domain.RuntimePriorityBackground,
		Summary:  "first",
	})
	if err != nil {
		t.Fatalf("AppendQueue first: %v", err)
	}
	second, err := store.Runtime.AppendQueue(domain.RuntimeQueueItem{
		Kind:     domain.RuntimeQueueControl,
		Priority: domain.RuntimePriorityControl,
		Summary:  "second",
	})
	if err != nil {
		t.Fatalf("AppendQueue second: %v", err)
	}
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("unexpected seq values: %d %d", first.Seq, second.Seq)
	}

	items, err := store.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[1].Summary != "second" {
		t.Fatalf("expected second item persisted, got %+v", items[1])
	}
}

func TestRuntimeStoreAppendTaskLog(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Runtime.AppendTaskLog("task-1", domain.RuntimeTaskLogEntry{
		Agent:   "writer",
		Event:   "stream",
		Summary: "开始落稿",
	}); err != nil {
		t.Fatalf("AppendTaskLog 1: %v", err)
	}
	if err := store.Runtime.AppendTaskLog("task-1", domain.RuntimeTaskLogEntry{
		Agent:   "writer",
		Event:   "tool",
		Tool:    "draft_chapter",
		Summary: "正文输出完成",
	}); err != nil {
		t.Fatalf("AppendTaskLog 2: %v", err)
	}

	entries, err := store.Runtime.LoadTaskLog("task-1")
	if err != nil {
		t.Fatalf("LoadTaskLog: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 task log entries, got %d", len(entries))
	}
	if entries[1].Tool != "draft_chapter" {
		t.Fatalf("expected tool persisted, got %+v", entries[1])
	}
}

func TestRuntimeStoreLoadQueueAfter(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	for _, summary := range []string{"one", "two", "three"} {
		if _, err := store.Runtime.AppendQueue(domain.RuntimeQueueItem{
			Kind:     domain.RuntimeQueueUIEvent,
			Priority: domain.RuntimePriorityBackground,
			Summary:  summary,
		}); err != nil {
			t.Fatalf("AppendQueue %s: %v", summary, err)
		}
	}

	items, err := store.Runtime.LoadQueueAfter(1)
	if err != nil {
		t.Fatalf("LoadQueueAfter: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items after seq 1, got %d", len(items))
	}
	if items[0].Summary != "two" || items[1].Summary != "three" {
		t.Fatalf("unexpected items: %+v", items)
	}
}

func TestRuntimeStoreReset(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	_, _ = store.Runtime.AppendQueue(domain.RuntimeQueueItem{
		Kind:     domain.RuntimeQueueUIEvent,
		Priority: domain.RuntimePriorityBackground,
		Summary:  "queued",
	})
	_ = store.Runtime.AppendTaskLog("task-1", domain.RuntimeTaskLogEntry{
		Event:   "stream_delta",
		Summary: "delta",
	})

	if err := store.Runtime.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	items, err := store.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue after reset: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty queue after reset, got %d", len(items))
	}

	logs, err := store.Runtime.LoadTaskLog("task-1")
	if err != nil {
		t.Fatalf("LoadTaskLog after reset: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected empty task log after reset, got %d", len(logs))
	}
}

func TestRuntimeStoreControlQueue(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	first, err := store.Runtime.EnqueueControl(domain.ControlIntent{
		Kind:      domain.ControlIntentResumePrompt,
		Priority:  domain.RuntimePriorityControl,
		Summary:   "恢复创作",
		TaskKind:  "coordinator_decision",
		TaskTitle: "恢复创作任务",
	})
	if err != nil {
		t.Fatalf("EnqueueControl first: %v", err)
	}
	_, err = store.Runtime.EnqueueControl(domain.ControlIntent{
		Kind:     domain.ControlIntentSteerMessage,
		Priority: domain.RuntimePriorityInterrupt,
		Summary:  "处理用户干预",
		Message:  "主角改成女性",
	})
	if err != nil {
		t.Fatalf("EnqueueControl second: %v", err)
	}

	peek, err := store.Runtime.PeekControl()
	if err != nil {
		t.Fatalf("PeekControl: %v", err)
	}
	if peek == nil || peek.ID != first.ID {
		t.Fatalf("expected first control at queue head, got %+v", peek)
	}

	if err := store.Runtime.DequeueControl(first.ID); err != nil {
		t.Fatalf("DequeueControl: %v", err)
	}
	intents, err := store.Runtime.LoadControls()
	if err != nil {
		t.Fatalf("LoadControls: %v", err)
	}
	if len(intents) != 1 || intents[0].Kind != domain.ControlIntentSteerMessage {
		t.Fatalf("unexpected remaining intents: %+v", intents)
	}
}

func TestRuntimeStorePrependResumeControl(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if _, err := store.Runtime.EnqueueControl(domain.ControlIntent{
		Kind:     domain.ControlIntentSteerMessage,
		Priority: domain.RuntimePriorityInterrupt,
		Summary:  "旧的干预",
		Message:  "主角改成女性",
	}); err != nil {
		t.Fatalf("EnqueueControl steer: %v", err)
	}
	if _, err := store.Runtime.EnqueueControl(domain.ControlIntent{
		Kind:      domain.ControlIntentResumePrompt,
		Priority:  domain.RuntimePriorityControl,
		Summary:   "旧的恢复",
		Prompt:    "old prompt",
		TaskKind:  "coordinator_decision",
		TaskTitle: "恢复创作任务",
	}); err != nil {
		t.Fatalf("EnqueueControl stale resume: %v", err)
	}
	resume, err := store.Runtime.PrependResumeControl(domain.ControlIntent{
		Kind:      domain.ControlIntentResumePrompt,
		Priority:  domain.RuntimePriorityControl,
		Summary:   "新的恢复",
		Prompt:    "new prompt",
		TaskKind:  "coordinator_decision",
		TaskTitle: "恢复创作任务",
	})
	if err != nil {
		t.Fatalf("PrependResumeControl: %v", err)
	}

	intents, err := store.Runtime.LoadControls()
	if err != nil {
		t.Fatalf("LoadControls: %v", err)
	}
	if len(intents) != 2 {
		t.Fatalf("expected 2 intents after prepend, got %d", len(intents))
	}
	if intents[0].ID != resume.ID || intents[0].Kind != domain.ControlIntentResumePrompt {
		t.Fatalf("expected new resume at queue head, got %+v", intents[0])
	}
	if intents[1].Kind != domain.ControlIntentSteerMessage {
		t.Fatalf("expected steer preserved after resume, got %+v", intents[1])
	}
}

func TestRuntimeStorePrependResumeControlDropsRequestedKinds(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if _, err := store.Runtime.EnqueueControl(domain.ControlIntent{
		Kind:     domain.ControlIntentSteerMessage,
		Priority: domain.RuntimePriorityInterrupt,
		Summary:  "处理用户干预",
		Message:  "主角改成女性",
	}); err != nil {
		t.Fatalf("EnqueueControl steer: %v", err)
	}
	resume, err := store.Runtime.PrependResumeControl(domain.ControlIntent{
		Kind:      domain.ControlIntentResumePrompt,
		Priority:  domain.RuntimePriorityControl,
		Summary:   "Steer 恢复",
		Prompt:    "resume prompt",
		TaskKind:  "coordinator_decision",
		TaskTitle: "恢复创作任务",
	}, domain.ControlIntentSteerMessage)
	if err != nil {
		t.Fatalf("PrependResumeControl: %v", err)
	}

	intents, err := store.Runtime.LoadControls()
	if err != nil {
		t.Fatalf("LoadControls: %v", err)
	}
	if len(intents) != 1 {
		t.Fatalf("expected 1 intent after pruning, got %d", len(intents))
	}
	if intents[0].ID != resume.ID || intents[0].Kind != domain.ControlIntentResumePrompt {
		t.Fatalf("expected resume at queue head, got %+v", intents[0])
	}
}
