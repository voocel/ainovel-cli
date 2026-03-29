package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SignalStore 管理一次性信号文件（commit/review 结果、待恢复状态）。
type SignalStore struct{ io *IO }

func NewSignalStore(io *IO) *SignalStore { return &SignalStore{io: io} }

// SaveLastCommit 保存最近一次 commit 结果到 meta/last_commit.json。
func (s *SignalStore) SaveLastCommit(result domain.CommitResult) error {
	return s.io.WriteJSON("meta/last_commit.json", result)
}

// LoadLastCommit 读取最近一次 commit 结果。
func (s *SignalStore) LoadLastCommit() (*domain.CommitResult, error) {
	var r domain.CommitResult
	if err := s.io.ReadJSON("meta/last_commit.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// LoadAndClearLastCommit 原子性读取并清除 commit 信号，防止 TOCTOU 竞态。
func (s *SignalStore) LoadAndClearLastCommit() (*domain.CommitResult, error) {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	var r domain.CommitResult
	if err := s.io.ReadJSONUnlocked("meta/last_commit.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = s.io.RemoveFileUnlocked("meta/last_commit.json")
	return &r, nil
}

// ClearLastCommit 清除 commit 信号文件。
func (s *SignalStore) ClearLastCommit() error {
	return s.io.RemoveFile("meta/last_commit.json")
}

// SavePendingCommit 保存待恢复的章节提交状态。
func (s *SignalStore) SavePendingCommit(pending domain.PendingCommit) error {
	return s.io.WriteJSON("meta/pending_commit.json", pending)
}

// LoadPendingCommit 读取待恢复的章节提交状态。
func (s *SignalStore) LoadPendingCommit() (*domain.PendingCommit, error) {
	var pending domain.PendingCommit
	if err := s.io.ReadJSON("meta/pending_commit.json", &pending); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &pending, nil
}

// ClearPendingCommit 清除待恢复的章节提交状态。
func (s *SignalStore) ClearPendingCommit() error {
	return s.io.RemoveFile("meta/pending_commit.json")
}

// SaveLastReview 保存最近一次审阅结果到 meta/last_review.json。
func (s *SignalStore) SaveLastReview(r domain.ReviewEntry) error {
	return s.io.WriteJSON("meta/last_review.json", r)
}

// LoadLastReviewSignal 读取审阅信号文件。
func (s *SignalStore) LoadLastReviewSignal() (*domain.ReviewEntry, error) {
	var r domain.ReviewEntry
	if err := s.io.ReadJSON("meta/last_review.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// ClearLastReview 清除审阅信号文件。
func (s *SignalStore) ClearLastReview() error {
	return s.io.RemoveFile("meta/last_review.json")
}

// LoadAndClearLastReview 原子性读取并清除审阅信号。
func (s *SignalStore) LoadAndClearLastReview() (*domain.ReviewEntry, error) {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	var r domain.ReviewEntry
	if err := s.io.ReadJSONUnlocked("meta/last_review.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = s.io.RemoveFileUnlocked("meta/last_review.json")
	return &r, nil
}

// ClearStaleSignals 清理残留的信号文件（进程重启时调用）。
func (s *SignalStore) ClearStaleSignals() {
	_ = s.io.RemoveFile("meta/last_commit.json")
	_ = s.io.RemoveFile("meta/last_review.json")
}
