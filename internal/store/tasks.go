package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// TaskStore 管理运行时任务状态。
type TaskStore struct{ io *IO }

func NewTaskStore(io *IO) *TaskStore { return &TaskStore{io: io} }

// Load 返回全部任务记录。文件不存在时返回空切片。
func (s *TaskStore) Load() ([]domain.TaskRecord, error) {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	return s.loadUnlocked()
}

func (s *TaskStore) loadUnlocked() ([]domain.TaskRecord, error) {
	var tasks []domain.TaskRecord
	if err := s.io.ReadJSONUnlocked("meta/tasks.json", &tasks); err != nil {
		if os.IsNotExist(err) {
			return []domain.TaskRecord{}, nil
		}
		return nil, err
	}
	if tasks == nil {
		return []domain.TaskRecord{}, nil
	}
	return tasks, nil
}

// Save 覆盖保存全部任务记录。
func (s *TaskStore) Save(tasks []domain.TaskRecord) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.io.WriteJSONUnlocked("meta/tasks.json", tasks)
}

// Update 在单锁内原子更新任务列表。
func (s *TaskStore) Update(fn func([]domain.TaskRecord) ([]domain.TaskRecord, error)) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()

	tasks, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	updated, err := fn(tasks)
	if err != nil {
		return err
	}
	return s.io.WriteJSONUnlocked("meta/tasks.json", updated)
}
