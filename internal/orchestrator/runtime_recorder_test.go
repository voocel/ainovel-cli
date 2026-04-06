package orchestrator

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestRuntimeRecorderBatchesStreamDeltaUntilFlush(t *testing.T) {
	store := storepkg.NewStore(t.TempDir())
	recorder := newRuntimeRecorder(store, nil)

	recorder.recordStreamClear("writer")
	recorder.recordStreamDelta("writer", "第一段")
	recorder.recordStreamDelta("writer", "第二段")

	items, err := store.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue before flush: %v", err)
	}
	if len(items) != 1 || items[0].Kind != domain.RuntimeQueueStreamClear {
		t.Fatalf("expected only clear before flush, got %+v", items)
	}

	recorder.flushPendingStream()

	items, err = store.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue after flush: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected clear + delta after flush, got %d items", len(items))
	}
	if got := ReplayDeltaText(items[1]); got != "第一段第二段" {
		t.Fatalf("unexpected replay text: %q", got)
	}
}

func TestRuntimeRecorderFlushesPendingStreamBeforeNextClear(t *testing.T) {
	store := storepkg.NewStore(t.TempDir())
	recorder := newRuntimeRecorder(store, nil)

	recorder.recordStreamClear("writer")
	recorder.recordStreamDelta("writer", "上一轮输出")
	recorder.recordStreamClear("writer")

	items, err := store.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected clear, delta, clear; got %d items", len(items))
	}
	if items[0].Kind != domain.RuntimeQueueStreamClear || items[1].Kind != domain.RuntimeQueueStreamDelta || items[2].Kind != domain.RuntimeQueueStreamClear {
		t.Fatalf("unexpected queue order: %+v", items)
	}
	if got := ReplayDeltaText(items[1]); got != "上一轮输出" {
		t.Fatalf("unexpected replay text: %q", got)
	}
}

func TestRuntimeRecorderFlushesStreamDeltaAtThreshold(t *testing.T) {
	store := storepkg.NewStore(t.TempDir())
	recorder := newRuntimeRecorder(store, nil)
	chunk := strings.Repeat("a", streamDeltaFlushThreshold/2)

	recorder.recordStreamClear("writer")
	recorder.recordStreamDelta("writer", chunk)

	items, err := store.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue after first chunk: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected first chunk to stay buffered, got %d items", len(items))
	}

	recorder.recordStreamDelta("writer", chunk)

	items, err = store.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue after second chunk: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected threshold flush to write one delta item, got %d items", len(items))
	}
	if got := ReplayDeltaText(items[1]); got != chunk+chunk {
		t.Fatalf("unexpected replay text length: got %d want %d", len(got), len(chunk+chunk))
	}
}

func TestSessionFlushesPendingStreamOnMessageEnd(t *testing.T) {
	store := storepkg.NewStore(t.TempDir())
	sess := newSession(nil, store, nil, nil, "", nil, func(string) {}, func() {}, nil, nil)

	sess.handleMessageStart()
	sess.emitDisplayDelta("writer", "未结束的输出")

	items, err := store.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue before message end: %v", err)
	}
	if len(items) != 1 || items[0].Kind != domain.RuntimeQueueStreamClear {
		t.Fatalf("expected only clear before message end, got %+v", items)
	}

	sess.handleMessageEnd(agentcore.Event{})

	items, err = store.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue after message end: %v", err)
	}
	if len(items) != 2 || items[1].Kind != domain.RuntimeQueueStreamDelta {
		t.Fatalf("expected pending delta flushed on message end, got %+v", items)
	}
	if got := ReplayDeltaText(items[1]); got != "未结束的输出" {
		t.Fatalf("unexpected replay text after message end: %q", got)
	}
}
