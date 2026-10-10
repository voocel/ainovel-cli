package activity

import (
	"fmt"
	"testing"
	"time"
)

func TestTaskLifecycleRetryAndEviction(t *testing.T) {
	h := NewHub()
	e := Event{ProjectID: "book", RunID: "run", OperationID: "write", Kind: TaskStart, TaskLabel: "写作", At: time.Now()}
	h.Publish(e)
	e.OperationID, e.TaskLabel = "review", "审阅"
	h.Publish(e)
	e.Kind = TaskEnd
	h.Publish(e)
	s, _ := h.Snapshot("book")
	if len(s.Tasks) != 2 || s.Tasks[0].Done || !s.Tasks[1].Done || s.Tasks[1].Label != "审阅" {
		t.Fatalf("incorrect tasks: %+v", s.Tasks)
	}
	s.Tasks[0].Label = "mutated"
	e.Kind = TaskStart // 重试：同一任务再次开工
	h.Publish(e)
	s, _ = h.Snapshot("book")
	if s.Tasks[0].Label != "写作" || s.Tasks[1].Done {
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
	if len(s.Tasks) != 1 {
		t.Fatal("run change retained task history")
	}
}
