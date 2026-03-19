package store

import (
	"fmt"
	"os"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveRunMeta 保存运行元信息到 meta/run.json。
func (s *Store) SaveRunMeta(meta domain.RunMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveRunMetaUnlocked(meta)
}

// LoadRunMeta 读取运行元信息。
func (s *Store) LoadRunMeta() (*domain.RunMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadRunMetaUnlocked()
}

func (s *Store) loadRunMetaUnlocked() (*domain.RunMeta, error) {
	var meta domain.RunMeta
	if err := s.readJSONUnlocked("meta/run.json", &meta); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &meta, nil
}

func (s *Store) saveRunMetaUnlocked(meta domain.RunMeta) error {
	return s.writeJSONUnlocked("meta/run.json", meta)
}

// InitRunMeta 初始化或更新运行元信息，保留已有的 SteerHistory。
func (s *Store) InitRunMeta(style, provider, model string) error {
	return s.withWriteLock(func() error {
		existing, err := s.loadRunMetaUnlocked()
		if err != nil {
			return err
		}
		meta := domain.RunMeta{
			StartedAt: time.Now().Format(time.RFC3339),
			Provider:  provider,
			Style:     style,
			Model:     model,
		}
		if existing != nil {
			meta.SteerHistory = existing.SteerHistory
			meta.PendingSteer = existing.PendingSteer
			meta.PlanningTier = existing.PlanningTier
		}
		return s.saveRunMetaUnlocked(meta)
	})
}

// AppendSteerEntry 追加用户干预记录到 meta/run.json。
func (s *Store) AppendSteerEntry(entry domain.SteerEntry) error {
	return s.withWriteLock(func() error {
		meta, err := s.loadRunMetaUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.SteerHistory = append(meta.SteerHistory, entry)
		return s.saveRunMetaUnlocked(*meta)
	})
}

// SetPendingSteer 记录未完成的 Steer 指令，用于中断恢复。
func (s *Store) SetPendingSteer(input string) error {
	return s.withWriteLock(func() error {
		meta, err := s.loadRunMetaUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.PendingSteer = input
		return s.saveRunMetaUnlocked(*meta)
	})
}

// ClearPendingSteer 清除已处理的 Steer 指令。
func (s *Store) ClearPendingSteer() error {
	return s.withWriteLock(func() error {
		meta, err := s.loadRunMetaUnlocked()
		if err != nil {
			return err
		}
		if meta == nil || meta.PendingSteer == "" {
			return nil
		}
		meta.PendingSteer = ""
		return s.saveRunMetaUnlocked(*meta)
	})
}

// SetPlanningTier 记录当前作品采用的规划级别。
func (s *Store) SetPlanningTier(tier domain.PlanningTier) error {
	return s.withWriteLock(func() error {
		meta, err := s.loadRunMetaUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.PlanningTier = tier
		return s.saveRunMetaUnlocked(*meta)
	})
}

// SaveCheckpoint 保存当前进度快照到 meta/checkpoints/。
func (s *Store) SaveCheckpoint(label string) error {
	p, err := s.LoadProgress()
	if err != nil || p == nil {
		return err
	}
	ts := time.Now().Format("20060102-150405")
	rel := fmt.Sprintf("meta/checkpoints/%s-%s.json", ts, label)
	return s.writeJSON(rel, p)
}
