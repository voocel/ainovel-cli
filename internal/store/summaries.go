package store

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SaveSummary 保存章节摘要到 summaries/{ch}.json。
func (s *Store) SaveSummary(sum domain.ChapterSummary) error {
	return s.writeJSON(fmt.Sprintf("summaries/%02d.json", sum.Chapter), sum)
}

// LoadSummary 读取指定章节的摘要。
func (s *Store) LoadSummary(chapter int) (*domain.ChapterSummary, error) {
	var sum domain.ChapterSummary
	if err := s.readJSON(fmt.Sprintf("summaries/%02d.json", chapter), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadRecentSummaries 加载 current 章之前最近 count 章的摘要。
func (s *Store) LoadRecentSummaries(current, count int) ([]domain.ChapterSummary, error) {
	var result []domain.ChapterSummary
	start := max(current-count, 1)
	for ch := start; ch < current; ch++ {
		sum, err := s.LoadSummary(ch)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// LoadAllSummaries 加载 current 章之前的所有摘要（短篇全量模式）。
func (s *Store) LoadAllSummaries(current int) ([]domain.ChapterSummary, error) {
	return s.LoadRecentSummaries(current, current)
}

// SaveArcSummary 保存弧级摘要到 summaries/arc-v{vol}a{arc}.json。
func (s *Store) SaveArcSummary(sum domain.ArcSummary) error {
	return s.writeJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", sum.Volume, sum.Arc), sum)
}

// LoadArcSummary 读取指定弧的摘要。
func (s *Store) LoadArcSummary(volume, arc int) (*domain.ArcSummary, error) {
	var sum domain.ArcSummary
	if err := s.readJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", volume, arc), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadArcSummaries 加载一卷内所有已有弧摘要。
// 从分层大纲获取实际弧数，无分层大纲时扫描到首个缺失为止。
func (s *Store) LoadArcSummaries(volume int) ([]domain.ArcSummary, error) {
	maxArc := s.arcCountForVolume(volume)
	var result []domain.ArcSummary
	for arc := 1; arc <= maxArc; arc++ {
		sum, err := s.LoadArcSummary(volume, arc)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// SaveVolumeSummary 保存卷级摘要到 summaries/vol-v{vol}.json。
func (s *Store) SaveVolumeSummary(sum domain.VolumeSummary) error {
	return s.writeJSON(fmt.Sprintf("summaries/vol-v%02d.json", sum.Volume), sum)
}

// LoadVolumeSummary 读取指定卷的摘要。
func (s *Store) LoadVolumeSummary(volume int) (*domain.VolumeSummary, error) {
	var sum domain.VolumeSummary
	if err := s.readJSON(fmt.Sprintf("summaries/vol-v%02d.json", volume), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadAllVolumeSummaries 加载所有已有卷摘要。
// 从分层大纲获取实际卷数，无分层大纲时扫描到首个缺失为止。
func (s *Store) LoadAllVolumeSummaries() ([]domain.VolumeSummary, error) {
	maxVol := s.volumeCount()
	var result []domain.VolumeSummary
	for vol := 1; vol <= maxVol; vol++ {
		sum, err := s.LoadVolumeSummary(vol)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// volumeCount 从分层大纲获取卷数，无大纲时返回安全上限。
func (s *Store) volumeCount() int {
	volumes, err := s.LoadLayeredOutline()
	if err == nil && len(volumes) > 0 {
		return len(volumes)
	}
	return 20
}

// arcCountForVolume 从分层大纲获取指定卷的弧数，无大纲时返回安全上限。
func (s *Store) arcCountForVolume(volume int) int {
	volumes, err := s.LoadLayeredOutline()
	if err == nil {
		for _, v := range volumes {
			if v.Index == volume {
				return len(v.Arcs)
			}
		}
	}
	return 20
}
