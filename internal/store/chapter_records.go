package store

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const chapterRecordsDir = "meta/chapter_records"

type ChapterRecordStore struct{ io *IO }

func NewChapterRecordStore(io *IO) *ChapterRecordStore { return &ChapterRecordStore{io: io} }

func ChapterRecordPath(chapter int) string {
	return fmt.Sprintf("%s/%06d.json", chapterRecordsDir, chapter)
}

func (s *ChapterRecordStore) Load(chapter int) (*domain.ChapterRecord, error) {
	var record domain.ChapterRecord
	if err := s.io.ReadJSON(ChapterRecordPath(chapter), &record); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := validateChapterRecord(record); err != nil {
		return nil, fmt.Errorf("读取第 %d 章接纳记录: %w", chapter, err)
	}
	if record.Chapter != chapter {
		return nil, fmt.Errorf("读取第 %d 章接纳记录: 记录章节号为 %d", chapter, record.Chapter)
	}
	return &record, nil
}

func (s *ChapterRecordStore) Save(record domain.ChapterRecord) error {
	if err := validateChapterRecord(record); err != nil {
		return err
	}
	return s.io.WriteJSON(ChapterRecordPath(record.Chapter), record)
}

// Prepare 构造下一版章节记录但不落盘，供跨记录不变量在写入前完成校验。
func (s *ChapterRecordStore) Prepare(chapter int, origin domain.ChapterOrigin, content string, facts domain.ChapterFacts, style domain.StyleDelta) (*domain.ChapterRecord, error) {
	existing, err := s.Load(chapter)
	if err != nil {
		return nil, err
	}
	record, _ := prepareChapterRecord(existing, chapter, origin, content, facts, style)
	return record, nil
}

func (s *ChapterRecordStore) Accept(chapter int, origin domain.ChapterOrigin, content string, facts domain.ChapterFacts, style domain.StyleDelta) (*domain.ChapterRecord, error) {
	existing, err := s.Load(chapter)
	if err != nil {
		return nil, err
	}
	record, changed := prepareChapterRecord(existing, chapter, origin, content, facts, style)
	if !changed {
		return record, nil
	}
	if err := s.Save(*record); err != nil {
		return nil, err
	}
	return record, nil
}

func prepareChapterRecord(existing *domain.ChapterRecord, chapter int, origin domain.ChapterOrigin, content string, facts domain.ChapterFacts, style domain.StyleDelta) (*domain.ChapterRecord, bool) {
	digest := domain.ChapterContentSHA256(content)
	revision := 1
	if existing != nil {
		// 覆盖旧记录时保住本章自己种下的伏笔：重写只换正文，不该抹掉这一章的埋设事实。
		facts.ForeshadowUpdates = domain.RestoreOwnPlants(existing.Facts.ForeshadowUpdates, facts.ForeshadowUpdates)
		if existing.ContentSHA256 == digest && existing.Origin == origin && reflect.DeepEqual(existing.Facts, facts) && reflect.DeepEqual(existing.StyleDelta, style) {
			return existing, false
		}
		revision = existing.Revision + 1
	}
	record := domain.ChapterRecord{
		Version:       domain.ChapterRecordVersion,
		Chapter:       chapter,
		Revision:      revision,
		Origin:        origin,
		Content:       domain.NormalizeChapterContent(content),
		ContentSHA256: digest,
		Facts:         facts,
		StyleDelta:    style,
		AcceptedAt:    time.Now(),
	}
	return &record, true
}

func (s *ChapterRecordStore) LoadCompleted(chapters []int) ([]domain.ChapterRecord, error) {
	numbers := slices.Clone(chapters)
	slices.Sort(numbers)
	records := make([]domain.ChapterRecord, 0, len(numbers))
	for _, chapter := range numbers {
		record, err := s.Load(chapter)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, fmt.Errorf("第 %d 章缺少接纳记录", chapter)
		}
		records = append(records, *record)
	}
	return records, nil
}

// LoadVersions 从已校验的接纳记录提取版本快照，顺序与 LoadCompleted 一致。
func (s *ChapterRecordStore) LoadVersions(chapters []int) ([]domain.ChapterVersion, error) {
	records, err := s.LoadCompleted(chapters)
	if err != nil {
		return nil, err
	}
	versions := make([]domain.ChapterVersion, 0, len(records))
	for _, record := range records {
		versions = append(versions, domain.ChapterVersion{
			Chapter: record.Chapter, Revision: record.Revision, ContentSHA256: record.ContentSHA256,
		})
	}
	return versions, nil
}

func validateChapterRecord(record domain.ChapterRecord) error {
	switch {
	case record.Version != domain.ChapterRecordVersion:
		return fmt.Errorf("unsupported chapter record version %d", record.Version)
	case record.Chapter <= 0:
		return fmt.Errorf("chapter must be > 0")
	case record.Revision <= 0:
		return fmt.Errorf("revision must be > 0")
	case record.Origin != domain.ChapterOriginGenerated && record.Origin != domain.ChapterOriginUser && record.Origin != domain.ChapterOriginLegacy:
		return fmt.Errorf("invalid chapter origin %q", record.Origin)
	case record.ContentSHA256 != domain.ChapterContentSHA256(record.Content):
		return fmt.Errorf("content digest mismatch")
	case record.AcceptedAt.IsZero():
		return fmt.Errorf("accepted_at is required")
	}
	return nil
}
