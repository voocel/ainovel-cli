package store

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SummaryStore 管理Chương、弧、卷Tóm tắt。
type SummaryStore struct {
	io      *IO
	outline *OutlineStore // 只读依赖，用于获取弧/卷数量
}

func NewSummaryStore(io *IO, outline *OutlineStore) *SummaryStore {
	return &SummaryStore{io: io, outline: outline}
}

// SaveSummary LưuTóm tắt chương到 summaries/{ch}.json。
func (s *SummaryStore) SaveSummary(sum domain.ChapterSummary) error {
	return s.io.WriteJSON(fmt.Sprintf("summaries/%02d.json", sum.Chapter), sum)
}

// LoadSummary Đọc指定Chương的Tóm tắt。
func (s *SummaryStore) LoadSummary(chapter int) (*domain.ChapterSummary, error) {
	var sum domain.ChapterSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/%02d.json", chapter), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadRecentSummaries 加载 current 章之前最近 count 章的Tóm tắt。
func (s *SummaryStore) LoadRecentSummaries(current, count int) ([]domain.ChapterSummary, error) {
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

// SaveArcSummary Lưu弧级Tóm tắt。
func (s *SummaryStore) SaveArcSummary(sum domain.ArcSummary) error {
	return s.io.WriteJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", sum.Volume, sum.Arc), sum)
}

// HasArcSummary Kiểm tra指定弧Có czy không已LưuTóm tắt。读Thất bại按"未Lưu"处理。
func (s *SummaryStore) HasArcSummary(volume, arc int) bool {
	sum, err := s.LoadArcSummary(volume, arc)
	return err == nil && sum != nil
}

// HasVolumeSummary Kiểm tra指定卷Có czy không已LưuTóm tắt。读Thất bại按"未Lưu"处理。
func (s *SummaryStore) HasVolumeSummary(volume int) bool {
	sum, err := s.LoadVolumeSummary(volume)
	return err == nil && sum != nil
}

// LoadArcSummary Đọc指定弧的Tóm tắt。
func (s *SummaryStore) LoadArcSummary(volume, arc int) (*domain.ArcSummary, error) {
	var sum domain.ArcSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", volume, arc), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadArcSummaries 加载一卷内所有已有弧Tóm tắt。
func (s *SummaryStore) LoadArcSummaries(volume int) ([]domain.ArcSummary, error) {
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

// SaveVolumeSummary Lưu卷级Tóm tắt。
func (s *SummaryStore) SaveVolumeSummary(sum domain.VolumeSummary) error {
	return s.io.WriteJSON(fmt.Sprintf("summaries/vol-v%02d.json", sum.Volume), sum)
}

// LoadVolumeSummary Đọc指定卷的Tóm tắt。
func (s *SummaryStore) LoadVolumeSummary(volume int) (*domain.VolumeSummary, error) {
	var sum domain.VolumeSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/vol-v%02d.json", volume), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadAllVolumeSummaries 加载所有已有卷Tóm tắt。
func (s *SummaryStore) LoadAllVolumeSummaries() ([]domain.VolumeSummary, error) {
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

// FindCharacterAppearances 批量查找多个角色的最后出场Chương号。
func (s *SummaryStore) FindCharacterAppearances(names []string, endChapter, recentWindow int) map[string]int {
	result := make(map[string]int, len(names))
	remaining := make(map[string]struct{}, len(names))
	for _, n := range names {
		remaining[n] = struct{}{}
	}
	for ch := endChapter - recentWindow; ch >= 1; ch-- {
		if len(remaining) == 0 {
			break
		}
		sum, err := s.LoadSummary(ch)
		if err != nil || sum == nil {
			continue
		}
		for _, c := range sum.Characters {
			if _, need := remaining[c]; need {
				result[c] = ch
				delete(remaining, c)
			}
		}
	}
	return result
}

func (s *SummaryStore) volumeCount() int {
	volumes, err := s.outline.LoadLayeredOutline()
	if err == nil && len(volumes) > 0 {
		return len(volumes)
	}
	return 20
}

func (s *SummaryStore) arcCountForVolume(volume int) int {
	volumes, err := s.outline.LoadLayeredOutline()
	if err == nil {
		for _, v := range volumes {
			if v.Index == volume {
				return len(v.Arcs)
			}
		}
	}
	return 20
}
