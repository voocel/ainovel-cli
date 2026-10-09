package activity

// Scope comes from the validated task input, never inferred from generated text.
type Scope struct {
	ChapterNumber int
	PlanNodeID    string
	ChapterIDs    []string
}

func (s Scope) clone() Scope {
	s.ChapterIDs = append([]string(nil), s.ChapterIDs...)
	return s
}

// Task 是一次真实的能力执行（不是推测的代理或排队的任务）：现场标题取正在执行的那一项，
// 输出块按它的作用范围归章。用量与用时不在这里，右栏「创作团队」从落盘的执行记录汇总。
type Task struct {
	OperationID, Label string
	Scope              Scope
	Done               bool
}

func (s *Snapshot) foldTask(e Event) {
	index := -1
	for i := range s.Tasks {
		if s.Tasks[i].OperationID == e.OperationID {
			index = i
			break
		}
	}
	switch e.Kind {
	case TaskStart:
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
		s.Tasks[index].Label, s.Tasks[index].Scope, s.Tasks[index].Done = e.TaskLabel, e.Scope.clone(), false
	case TaskEnd:
		if index >= 0 {
			s.Tasks[index].Done = true
		}
	}
}
