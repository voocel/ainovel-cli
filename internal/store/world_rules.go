package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveWorldRules 全量写入 world_rules.json + world_rules.md。
func (s *Store) SaveWorldRules(rules []domain.WorldRule) error {
	if err := s.writeJSON("world_rules.json", rules); err != nil {
		return err
	}
	return s.writeMarkdown("world_rules.md", renderWorldRules(rules))
}

// LoadWorldRules 读取世界规则。
func (s *Store) LoadWorldRules() ([]domain.WorldRule, error) {
	var rules []domain.WorldRule
	if err := s.readJSON("world_rules.json", &rules); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return rules, nil
}

func renderWorldRules(rules []domain.WorldRule) string {
	grouped := make(map[string][]domain.WorldRule)
	var order []string
	for _, r := range rules {
		cat := r.Category
		if cat == "" {
			cat = "other"
		}
		if _, exists := grouped[cat]; !exists {
			order = append(order, cat)
		}
		grouped[cat] = append(grouped[cat], r)
	}

	var b strings.Builder
	b.WriteString("# 世界观规则\n\n")
	for _, cat := range order {
		fmt.Fprintf(&b, "## %s\n\n", cat)
		for _, r := range grouped[cat] {
			fmt.Fprintf(&b, "- **规则**：%s\n", r.Rule)
			if r.Boundary != "" {
				fmt.Fprintf(&b, "  - 边界：%s\n", r.Boundary)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
