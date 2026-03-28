package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveLastCommit 保存最近一次 commit 结果到 meta/last_commit.json。
// 用于宿主程序读取结构化信号。
func (s *Store) SaveLastCommit(result domain.CommitResult) error {
	return s.writeJSON("meta/last_commit.json", result)
}

// LoadLastCommit 读取最近一次 commit 结果。
func (s *Store) LoadLastCommit() (*domain.CommitResult, error) {
	var r domain.CommitResult
	if err := s.readJSON("meta/last_commit.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// LoadAndClearLastCommit 原子性读取并清除 commit 信号，防止 TOCTOU 竞态。
func (s *Store) LoadAndClearLastCommit() (*domain.CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r domain.CommitResult
	if err := s.readJSONUnlocked("meta/last_commit.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = s.removeFileUnlocked("meta/last_commit.json")
	return &r, nil
}

// ClearLastCommit 清除 commit 信号文件，防止重复消费。
func (s *Store) ClearLastCommit() error {
	return s.removeFile("meta/last_commit.json")
}

// SavePendingCommit 保存待恢复的章节提交状态。
func (s *Store) SavePendingCommit(pending domain.PendingCommit) error {
	return s.writeJSON("meta/pending_commit.json", pending)
}

// LoadPendingCommit 读取待恢复的章节提交状态。
func (s *Store) LoadPendingCommit() (*domain.PendingCommit, error) {
	var pending domain.PendingCommit
	if err := s.readJSON("meta/pending_commit.json", &pending); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &pending, nil
}

// ClearPendingCommit 清除待恢复的章节提交状态。
func (s *Store) ClearPendingCommit() error {
	return s.removeFile("meta/pending_commit.json")
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

// ClearStaleSignals 清理残留的信号文件。
// 在进程重启时调用，防止上次崩溃遗留的瞬时信号被误消费。
// pending_commit 用于恢复，不在这里清理。
func (s *Store) ClearStaleSignals() {
	_ = s.removeFile("meta/last_commit.json")
	_ = s.removeFile("meta/last_review.json")
}
