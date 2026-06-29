package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SignalStore 管理一次性信号Tập tin（commit/review Kết quả、待Phục hồiTrạng thái）。
type SignalStore struct{ io *IO }

func NewSignalStore(io *IO) *SignalStore { return &SignalStore{io: io} }

// SaveLastCommit Lưu最近一次 commit Kết quả到 meta/last_commit.json。
func (s *SignalStore) SaveLastCommit(result domain.CommitResult) error {
	return s.io.WriteJSON("meta/last_commit.json", result)
}

// LoadLastCommit Đọc最近一次 commit Kết quả。
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

// LoadAndClearLastCommit 原子性Đọc并清除 commit 信号，防止 TOCTOU 竞态。
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

// ClearLastCommit 清除 commit 信号Tập tin。
func (s *SignalStore) ClearLastCommit() error {
	return s.io.RemoveFile("meta/last_commit.json")
}

// SavePendingCommit Lưu待Phục hồi的ChươngNộpTrạng thái。
func (s *SignalStore) SavePendingCommit(pending domain.PendingCommit) error {
	return s.io.WriteJSON("meta/pending_commit.json", pending)
}

// LoadPendingCommit Đọc待Phục hồi的ChươngNộpTrạng thái。
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

// ClearPendingCommit 清除待Phục hồi的ChươngNộpTrạng thái。
func (s *SignalStore) ClearPendingCommit() error {
	return s.io.RemoveFile("meta/pending_commit.json")
}

// SaveLastReview Lưu最近一次审阅Kết quả到 meta/last_review.json。
func (s *SignalStore) SaveLastReview(r domain.ReviewEntry) error {
	return s.io.WriteJSON("meta/last_review.json", r)
}

// LoadLastReviewSignal Đọc审阅信号Tập tin。
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

// ClearLastReview 清除审阅信号Tập tin。
func (s *SignalStore) ClearLastReview() error {
	return s.io.RemoveFile("meta/last_review.json")
}

// LoadAndClearLastReview 原子性Đọc并清除审阅信号。
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

// ClearStaleSignals 清理残留的信号Tập tin（进程重启时调用）。
func (s *SignalStore) ClearStaleSignals() {
	_ = s.io.RemoveFile("meta/last_commit.json")
	_ = s.io.RemoveFile("meta/last_review.json")
}
