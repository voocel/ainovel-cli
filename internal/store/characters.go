package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// CharacterStore 管理角色档案和状态快照。
type CharacterStore struct {
	io      *IO
	outline *OutlineStore // 只读依赖，用于快照遍历
}

func NewCharacterStore(io *IO, outline *OutlineStore) *CharacterStore {
	return &CharacterStore{io: io, outline: outline}
}

// Save 同时保存 characters.json 和 characters.md（原子写入）。
func (s *CharacterStore) Save(chars []domain.Character) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("characters.json", chars); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("characters.md", renderCharacters(chars))
	})
}

// Load 从 characters.json 读取角色档案。
func (s *CharacterStore) Load() ([]domain.Character, error) {
	var chars []domain.Character
	if err := s.io.ReadJSON("characters.json", &chars); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return chars, nil
}

// SaveSnapshots 保存角色状态快照到 meta/snapshots/v{vol}a{arc}.json。
func (s *CharacterStore) SaveSnapshots(volume, arc int, snapshots []domain.CharacterSnapshot) error {
	return s.io.WriteJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), snapshots)
}

// LoadSnapshots 读取指定卷弧的角色快照。
func (s *CharacterStore) LoadSnapshots(volume, arc int) ([]domain.CharacterSnapshot, error) {
	var snapshots []domain.CharacterSnapshot
	if err := s.io.ReadJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), &snapshots); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return snapshots, nil
}

// LoadLatestSnapshots 加载最近一次角色快照（按卷弧倒序查找）。
func (s *CharacterStore) LoadLatestSnapshots() ([]domain.CharacterSnapshot, error) {
	// 大纲缺失→(nil,nil) 已在 LoadLayeredOutline 内消化;损坏必须上抛，否则角色快照
	// 会随分层大纲损坏一起静默消失，写作丢失角色连续性却无人知晓。
	volumes, err := s.outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
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

// LoadSnapshotsBefore 读取目标章之前已结束弧的最近快照；当前弧的快照包含
// 该弧写完后的状态，不能用作弧内旧章节的前置事实。无历史快照时返回 nil。
func (s *CharacterStore) LoadSnapshotsBefore(chapter int) ([]domain.CharacterSnapshot, error) {
	volumes, err := s.outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	var prior [][2]int
	endChapter := 0
scan:
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			endChapter += len(arc.Chapters)
			if endChapter >= chapter {
				break scan
			}
			if len(arc.Chapters) > 0 {
				prior = append(prior, [2]int{volume.Index, arc.Index})
			}
		}
	}
	for i := len(prior) - 1; i >= 0; i-- {
		snapshots, err := s.LoadSnapshots(prior[i][0], prior[i][1])
		if err != nil {
			return nil, err
		}
		if len(snapshots) > 0 {
			return snapshots, nil
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
