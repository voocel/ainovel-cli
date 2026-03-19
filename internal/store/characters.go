package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveCharacters 同时保存 characters.json 和 characters.md。
func (s *Store) SaveCharacters(chars []domain.Character) error {
	if err := s.writeJSON("characters.json", chars); err != nil {
		return err
	}
	return s.writeMarkdown("characters.md", renderCharacters(chars))
}

// LoadCharacters 从 characters.json 读取角色档案。
func (s *Store) LoadCharacters() ([]domain.Character, error) {
	var chars []domain.Character
	if err := s.readJSON("characters.json", &chars); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return chars, nil
}

// SaveCharacterSnapshots 保存角色状态快照到 meta/snapshots/v{vol}a{arc}.json。
func (s *Store) SaveCharacterSnapshots(volume, arc int, snapshots []domain.CharacterSnapshot) error {
	return s.writeJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), snapshots)
}

// LoadSnapshots 读取指定卷弧的角色快照。
func (s *Store) LoadSnapshots(volume, arc int) ([]domain.CharacterSnapshot, error) {
	var snapshots []domain.CharacterSnapshot
	if err := s.readJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), &snapshots); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return snapshots, nil
}

// LoadLatestSnapshots 加载最近一次角色快照（按卷弧倒序查找）。
// 从分层大纲获取实际卷弧数量，避免盲扫。
func (s *Store) LoadLatestSnapshots() ([]domain.CharacterSnapshot, error) {
	volumes, _ := s.LoadLayeredOutline()
	if len(volumes) == 0 {
		return nil, nil
	}
	for vi := len(volumes) - 1; vi >= 0; vi-- {
		v := volumes[vi]
		for ai := len(v.Arcs) - 1; ai >= 0; ai-- {
			snaps, err := s.LoadSnapshots(v.Index, v.Arcs[ai].Index)
			if err != nil {
				return nil, err
			}
			if len(snaps) > 0 {
				return snaps, nil
			}
		}
	}
	return nil, nil
}

func renderCharacters(chars []domain.Character) string {
	var b strings.Builder
	b.WriteString("# 角色档案\n\n")
	for _, c := range chars {
		fmt.Fprintf(&b, "## %s（%s）\n\n", c.Name, c.Role)
		fmt.Fprintf(&b, "%s\n\n", c.Description)
		if c.Arc != "" {
			fmt.Fprintf(&b, "**角色弧线**：%s\n\n", c.Arc)
		}
		if len(c.Traits) > 0 {
			fmt.Fprintf(&b, "**特征**：%s\n\n", strings.Join(c.Traits, "、"))
		}
	}
	return b.String()
}
