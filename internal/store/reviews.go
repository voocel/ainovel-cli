package store

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveReview 保存审阅结果。scope=chapter 写 reviews/{ch}.json，scope=global 写 reviews/{ch}-global.json。
func (s *Store) SaveReview(r domain.ReviewEntry) error {
	rel := fmt.Sprintf("reviews/%02d.json", r.Chapter)
	if r.Scope == "global" {
		rel = fmt.Sprintf("reviews/%02d-global.json", r.Chapter)
	}
	return s.writeJSON(rel, r)
}

// LoadReview 读取章节审阅结果。
func (s *Store) LoadReview(chapter int) (*domain.ReviewEntry, error) {
	var r domain.ReviewEntry
	if err := s.readJSON(fmt.Sprintf("reviews/%02d.json", chapter), &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// SaveLastReview 保存最近一次审阅结果到 meta/last_review.json，供宿主读取。
func (s *Store) SaveLastReview(r domain.ReviewEntry) error {
	return s.writeJSON("meta/last_review.json", r)
}

// LoadLastReviewSignal 读取审阅信号文件。
func (s *Store) LoadLastReviewSignal() (*domain.ReviewEntry, error) {
	var r domain.ReviewEntry
	if err := s.readJSON("meta/last_review.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// ClearLastReview 清除审阅信号文件，防止重复消费。
func (s *Store) ClearLastReview() error {
	return s.removeFile("meta/last_review.json")
}

// LoadAndClearLastReview 原子性读取并清除审阅信号，防止 TOCTOU 竞态。
func (s *Store) LoadAndClearLastReview() (*domain.ReviewEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r domain.ReviewEntry
	if err := s.readJSONUnlocked("meta/last_review.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = s.removeFileUnlocked("meta/last_review.json")
	return &r, nil
}

// LoadLastReview 读取最近一次全局审阅。从 chapter 往前搜索。
func (s *Store) LoadLastReview(fromChapter int) (*domain.ReviewEntry, error) {
	for ch := fromChapter; ch >= 1; ch-- {
		var r domain.ReviewEntry
		if err := s.readJSON(fmt.Sprintf("reviews/%02d-global.json", ch), &r); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		return &r, nil
	}
	return nil, nil
}
