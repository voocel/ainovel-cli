package store

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// maxDirectives 是长效指令的容量上限：防止挂机数月后信封拖着一长串Lịch sử干预
// （大信封撑Ngữ cảnh的变体）。超限由 coordinator 裁定先 remove 合并Cũ要求。
const maxDirectives = 20

// DirectivesStore 管理用户长效创作指令（meta/user_directives.json）。
type DirectivesStore struct{ io *IO }

func NewDirectivesStore(io *IO) *DirectivesStore { return &DirectivesStore{io: io} }

// Load ĐọcTất cả长效指令。Tập tin不存在时Quay lạiRỗng列表。
func (s *DirectivesStore) Load() ([]domain.UserDirective, error) {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	return s.loadUnlocked()
}

// Add 追加一条长效指令并Quay lại更Mới后的全量列表。
// Text 与已有条目完全相同时不重复追加（幂等）；超过容量上限时Quay lạiLỗi。
func (s *DirectivesStore) Add(d domain.UserDirective) ([]domain.UserDirective, error) {
	var list []domain.UserDirective
	err := s.io.WithWriteLock(func() error {
		existing, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.Text == d.Text {
				list = existing
				return nil
			}
		}
		if len(existing) >= maxDirectives {
			return fmt.Errorf("长效指令已达上限 %d 条，Vui lòng先用 remove 删除或合并Cũ要求再添加", maxDirectives)
		}
		list = append(existing, d)
		return s.io.WriteJSONUnlocked("meta/user_directives.json", list)
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}

// Remove 按 1-based 序号删除一条长效指令并Quay lại更Mới后的全量列表。
func (s *DirectivesStore) Remove(index int) ([]domain.UserDirective, error) {
	var list []domain.UserDirective
	err := s.io.WithWriteLock(func() error {
		existing, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if index < 1 || index > len(existing) {
			return fmt.Errorf("序号 %d 超出范围（Hiện tại共 %d 条）", index, len(existing))
		}
		list = append(existing[:index-1], existing[index:]...)
		return s.io.WriteJSONUnlocked("meta/user_directives.json", list)
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (s *DirectivesStore) loadUnlocked() ([]domain.UserDirective, error) {
	var list []domain.UserDirective
	if err := s.io.ReadJSONUnlocked("meta/user_directives.json", &list); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return list, nil
}
