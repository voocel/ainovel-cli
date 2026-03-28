package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveHandoffPack 保存最新交接包到 meta/handoff.json。
func (s *Store) SaveHandoffPack(pack domain.HandoffPack) error {
	return s.writeJSON("meta/handoff.json", pack)
}

// LoadHandoffPack 读取最新交接包。
func (s *Store) LoadHandoffPack() (*domain.HandoffPack, error) {
	var pack domain.HandoffPack
	if err := s.readJSON("meta/handoff.json", &pack); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &pack, nil
}
