package activity

import "time"

// Task is one real capability execution, not an inferred agent or queued job.
type Task struct {
	OperationID, Label string
	Phase              Kind
	Tool, Err          string
	Attempt            int
	Done               bool
	StartedAt, EndedAt time.Time
	Usage              UsageTotals
}

func (s *Snapshot) foldTask(e Event) {
	index := -1
	for i := range s.Tasks {
		if s.Tasks[i].OperationID == e.OperationID {
			index = i
			break
		}
	}
	if e.Kind == TaskStart {
		if index < 0 {
			// Retain recent executions while never evicting an active task.
			if len(s.Tasks) >= 32 {
				for i, task := range s.Tasks {
					if task.Done {
						s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
						break
					}
				}
			}
			s.Tasks = append(s.Tasks, Task{OperationID: e.OperationID})
			index = len(s.Tasks) - 1
		}
		t := &s.Tasks[index]
		t.Label, t.StartedAt, t.Attempt = e.TaskLabel, e.At, e.Attempt
		t.Done, t.Err, t.Tool, t.EndedAt = false, "", "", time.Time{}
	}
	if index < 0 {
		return
	}
	t := &s.Tasks[index]
	switch e.Kind {
	case TaskStart, TurnStart, Thinking, Text, Prose, Retry, ToolStart, ToolDelta:
		t.Phase = e.Kind
		if e.Kind == ToolStart || e.Kind == ToolDelta {
			t.Tool = e.Tool
		}
	case TaskEnd:
		t.Done, t.EndedAt, t.Err, t.Phase = true, e.At, e.Err, TaskEnd
	case Usage:
		t.Usage.add(e.Usage)
	}
}
