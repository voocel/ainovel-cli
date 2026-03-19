package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveTimeline 全量写入 timeline.json + timeline.md。
func (s *Store) SaveTimeline(events []domain.TimelineEvent) error {
	if err := s.writeJSON("timeline.json", events); err != nil {
		return err
	}
	return s.writeMarkdown("timeline.md", renderTimeline(events))
}

// LoadTimeline 读取时间线。
func (s *Store) LoadTimeline() ([]domain.TimelineEvent, error) {
	var events []domain.TimelineEvent
	if err := s.readJSON("timeline.json", &events); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return events, nil
}

// AppendTimelineEvents 追加时间线事件。
func (s *Store) AppendTimelineEvents(newEvents []domain.TimelineEvent) error {
	return s.withWriteLock(func() error {
		var existing []domain.TimelineEvent
		if err := s.readJSONUnlocked("timeline.json", &existing); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		all := append(existing, newEvents...)
		if err := s.writeJSONUnlocked("timeline.json", all); err != nil {
			return err
		}
		return s.writeFileUnlocked("timeline.md", []byte(renderTimeline(all)))
	})
}

// LoadRecentTimeline 返回最近 window 章内的时间线事件（chapter >= current-window）。
func (s *Store) LoadRecentTimeline(current, window int) ([]domain.TimelineEvent, error) {
	all, err := s.LoadTimeline()
	if err != nil {
		return nil, err
	}
	minCh := max(current-window, 1)
	var filtered []domain.TimelineEvent
	for _, e := range all {
		if e.Chapter >= minCh {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

func renderTimeline(events []domain.TimelineEvent) string {
	var b strings.Builder
	b.WriteString("# 时间线\n\n")
	for _, e := range events {
		chars := ""
		if len(e.Characters) > 0 {
			chars = "（" + strings.Join(e.Characters, "、") + "）"
		}
		fmt.Fprintf(&b, "- **第 %d 章 [%s]**：%s%s\n", e.Chapter, e.Time, e.Event, chars)
	}
	return b.String()
}
