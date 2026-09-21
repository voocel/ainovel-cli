package activity

import (
	"fmt"
	"testing"
	"time"
)

func TestTaskLifecycleAndUsageAreIsolated(t *testing.T) {
	h := NewHub()
	e := Event{ProjectID: "book", RunID: "run", OperationID: "write", Kind: TaskStart, TaskKind: "write_chapter", TaskLabel: "写作", At: time.Now(), Attempt: 1}
	h.Publish(e)
	e.OperationID, e.TaskLabel = "review", "审阅"
	e.TaskKind = "review_range"
	h.Publish(e)
	e.Kind, e.Usage = Usage, UsageTotals{Input: 100, Output: 20, CacheRead: 60}
	h.Publish(e)
	e.Kind, e.Err = TaskEnd, "未提交结果"
	h.Publish(e)
	s, _ := h.Snapshot("book")
	if s.Tasks[0].Kind != "write_chapter" || s.Tasks[1].Kind != "review_range" {
		t.Fatal("task roles lost their execution identity")
	}
	if len(s.Tasks) != 2 || s.Tasks[0].Done || !s.Tasks[1].Done || s.Tasks[1].Usage.Input != 100 || s.Tasks[0].Usage.Input != 0 || s.Tasks[1].Err == "" {
		t.Fatalf("incorrect tasks: %+v", s.Tasks)
	}
	s.Tasks[0].Label = "mutated"
	e.Kind, e.Err, e.Attempt = TaskStart, "", 2
	h.Publish(e)
	s, _ = h.Snapshot("book")
	if s.Tasks[0].Label != "写作" || s.Tasks[1].Done || s.Tasks[1].Err != "" || s.Tasks[1].Attempt != 2 {
		t.Fatalf("snapshot/retry: %+v", s.Tasks)
	}
	for i := 0; i < 40; i++ {
		e.OperationID, e.Kind = fmt.Sprint(i), TaskStart
		h.Publish(e)
		e.Kind = TaskEnd
		h.Publish(e)
	}
	s, _ = h.Snapshot("book")
	if len(s.Tasks) != 32 || s.Tasks[0].OperationID != "write" || s.Tasks[1].OperationID != "review" {
		t.Fatal("eviction lost active tasks or grew history")
	}
	e.RunID = "new-run"
	e.Kind = TaskStart
	h.Publish(e)
	s, _ = h.Snapshot("book")
	if len(s.Tasks) != 1 || s.Tasks[0].Usage.Input != 0 {
		t.Fatal("run change retained task history")
	}
}
