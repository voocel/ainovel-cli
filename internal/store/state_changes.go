package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// AppendStateChanges 追加角色状态变化到 meta/state_changes.json。
func (s *Store) AppendStateChanges(changes []domain.StateChange) error {
	return s.withWriteLock(func() error {
		var existing []domain.StateChange
		if err := s.readJSONUnlocked("meta/state_changes.json", &existing); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		return s.writeJSONUnlocked("meta/state_changes.json", append(existing, changes...))
	})
}

// LoadStateChanges 读取全部状态变化记录。
func (s *Store) LoadStateChanges() ([]domain.StateChange, error) {
	var changes []domain.StateChange
	if err := s.readJSON("meta/state_changes.json", &changes); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return changes, nil
}
