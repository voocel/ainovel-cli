package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveForeshadowLedger 全量写入 foreshadow_ledger.json + foreshadow_ledger.md。
func (s *Store) SaveForeshadowLedger(entries []domain.ForeshadowEntry) error {
	if err := s.writeJSON("foreshadow_ledger.json", entries); err != nil {
		return err
	}
	return s.writeMarkdown("foreshadow_ledger.md", renderForeshadow(entries))
}

// LoadForeshadowLedger 读取伏笔账本。
func (s *Store) LoadForeshadowLedger() ([]domain.ForeshadowEntry, error) {
	var entries []domain.ForeshadowEntry
	if err := s.readJSON("foreshadow_ledger.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// UpdateForeshadow 批量应用伏笔增量操作。
func (s *Store) UpdateForeshadow(chapter int, updates []domain.ForeshadowUpdate) error {
	return s.withWriteLock(func() error {
		var entries []domain.ForeshadowEntry
		if err := s.readJSONUnlocked("foreshadow_ledger.json", &entries); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		idx := make(map[string]int, len(entries))
		for i, e := range entries {
			idx[e.ID] = i
		}
		for _, u := range updates {
			switch u.Action {
			case "plant":
				idx[u.ID] = len(entries)
				entries = append(entries, domain.ForeshadowEntry{
					ID:          u.ID,
					Description: u.Description,
					PlantedAt:   chapter,
					Status:      "planted",
				})
			case "advance":
				if i, ok := idx[u.ID]; ok {
					entries[i].Status = "advanced"
				}
			case "resolve":
				if i, ok := idx[u.ID]; ok {
					entries[i].Status = "resolved"
					entries[i].ResolvedAt = chapter
				}
			}
		}
		if err := s.writeJSONUnlocked("foreshadow_ledger.json", entries); err != nil {
			return err
		}
		return s.writeFileUnlocked("foreshadow_ledger.md", []byte(renderForeshadow(entries)))
	})
}

// LoadActiveForeshadow 返回未回收的伏笔条目（status != "resolved"）。
func (s *Store) LoadActiveForeshadow() ([]domain.ForeshadowEntry, error) {
	all, err := s.LoadForeshadowLedger()
	if err != nil {
		return nil, err
	}
	var active []domain.ForeshadowEntry
	for _, e := range all {
		if e.Status != "resolved" {
			active = append(active, e)
		}
	}
	return active, nil
}

func renderForeshadow(entries []domain.ForeshadowEntry) string {
	var b strings.Builder
	b.WriteString("# 伏笔账本\n\n")
	for _, e := range entries {
		status := e.Status
		if e.ResolvedAt > 0 {
			status = fmt.Sprintf("已回收（第 %d 章）", e.ResolvedAt)
		}
		fmt.Fprintf(&b, "- **[%s]** %s — 埋设于第 %d 章，状态：%s\n",
			e.ID, e.Description, e.PlantedAt, status)
	}
	return b.String()
}
