package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveRelationships 全量写入 relationship_state.json + relationship_state.md。
func (s *Store) SaveRelationships(entries []domain.RelationshipEntry) error {
	if err := s.writeJSON("relationship_state.json", entries); err != nil {
		return err
	}
	return s.writeMarkdown("relationship_state.md", renderRelationships(entries))
}

// LoadRelationships 读取人物关系状态。
func (s *Store) LoadRelationships() ([]domain.RelationshipEntry, error) {
	var entries []domain.RelationshipEntry
	if err := s.readJSON("relationship_state.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// UpdateRelationships 合并关系变化。相同人物对的关系会被更新为最新值。
func (s *Store) UpdateRelationships(changes []domain.RelationshipEntry) error {
	return s.withWriteLock(func() error {
		var existing []domain.RelationshipEntry
		if err := s.readJSONUnlocked("relationship_state.json", &existing); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		idx := make(map[string]int, len(existing))
		for i, e := range existing {
			idx[pairKey(e.CharacterA, e.CharacterB)] = i
		}
		for _, c := range changes {
			key := pairKey(c.CharacterA, c.CharacterB)
			if i, ok := idx[key]; ok {
				existing[i].Relation = c.Relation
				existing[i].Chapter = c.Chapter
			} else {
				idx[key] = len(existing)
				existing = append(existing, c)
			}
		}
		if err := s.writeJSONUnlocked("relationship_state.json", existing); err != nil {
			return err
		}
		return s.writeFileUnlocked("relationship_state.md", []byte(renderRelationships(existing)))
	})
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

func renderRelationships(entries []domain.RelationshipEntry) string {
	var b strings.Builder
	b.WriteString("# 人物关系\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- **%s ↔ %s**：%s（第 %d 章）\n",
			e.CharacterA, e.CharacterB, e.Relation, e.Chapter)
	}
	return b.String()
}
