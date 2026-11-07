package revision

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
)

type StyleIndex interface {
	ChapterCommitted(chapter int, text string)
}

var errPreparedStale = errors.New("修订分析已过期")

type Result struct {
	Changed  []int
	Applied  []int
	Analyses []domain.RevisionAnalysis
}

type Service struct {
	store      *store.Store
	model      llmcontract.Model
	prompt     string
	styleIndex StyleIndex
}

func NewService(st *store.Store, model llmcontract.Model, prompt string, styleIndex StyleIndex) *Service {
	return &Service{store: st, model: model, prompt: prompt, styleIndex: styleIndex}
}

func (s *Service) Sync(ctx context.Context) (*Result, error) {
	pending, err := s.store.Revisions.LoadPending()
	if err != nil {
		return nil, fmt.Errorf("读取修订恢复记录: %w", err)
	}
	if pending != nil {
		return s.applyPending(*pending)
	}

	changes, err := Scan(s.store)
	if err != nil {
		return nil, err
	}
	result := &Result{Changed: ChangedChapters(changes)}
	if len(changes) == 0 {
		return result, nil
	}

	items := make([]domain.PendingRevisionItem, 0, len(changes))
	proposedSummaries := make(map[int]domain.ChapterSummary, len(changes))
	for i := len(changes) - 1; i >= 0; i-- {
		change := changes[i]
		previous, err := s.store.ChapterRecords.Load(change.Chapter)
		if err != nil || previous == nil {
			if err == nil {
				err = fmt.Errorf("接纳记录不存在")
			}
			return nil, fmt.Errorf("读取第 %d 章基线: %w", change.Chapter, err)
		}
		downstream, err := s.downstreamSummaries(change.Chapter, proposedSummaries)
		if err != nil {
			return nil, err
		}
		analysis, err := Analyze(ctx, s.model, s.prompt, change, *previous, downstream)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		record := domain.ChapterRecord{
			Version: domain.ChapterRecordVersion, Chapter: change.Chapter, Revision: previous.Revision + 1,
			Origin: domain.ChapterOriginUser, Content: change.After, ContentSHA256: change.CurrentSHA256,
			Facts: analysis.Facts, StyleDelta: domain.MergeStyleDelta(previous.StyleDelta, analysis.StyleDelta), AcceptedAt: now,
		}
		items = append(items, domain.PendingRevisionItem{
			Chapter: change.Chapter, BaseSHA256: change.BaseSHA256, CurrentSHA256: change.CurrentSHA256,
			Record: record, Analysis: analysis,
		})
		proposedSummaries[change.Chapter] = domain.ChapterSummary{
			Chapter: change.Chapter, Title: analysis.Facts.Title, Summary: analysis.Facts.Summary,
			Characters: analysis.Facts.Characters, KeyEvents: analysis.Facts.KeyEvents,
		}
	}
	slices.SortFunc(items, func(a, b domain.PendingRevisionItem) int { return a.Chapter - b.Chapter })
	for _, item := range items {
		result.Analyses = append(result.Analyses, item.Analysis)
	}
	if err := s.validatePendingRecords(items); err != nil {
		return nil, fmt.Errorf("校验修订后的章节状态: %w", err)
	}
	now := time.Now()
	pending = &domain.PendingRevision{Stage: domain.RevisionStagePrepared, Items: items, StartedAt: now, UpdatedAt: now}
	if err := s.store.Revisions.SavePending(*pending); err != nil {
		return nil, fmt.Errorf("保存修订恢复记录: %w", err)
	}
	applied, err := s.applyPending(*pending)
	if err != nil {
		return nil, err
	}
	applied.Changed = result.Changed
	applied.Analyses = result.Analyses
	return applied, nil
}

func (s *Service) applyPending(pending domain.PendingRevision) (*Result, error) {
	switch pending.Stage {
	case domain.RevisionStagePrepared:
		if err := s.applyPreparedRecords(pending.Items); err != nil {
			if errors.Is(err, errPreparedStale) {
				if clearErr := s.store.Revisions.ClearPending(); clearErr != nil {
					return nil, fmt.Errorf("%v；清理过期修订分析失败: %w", err, clearErr)
				}
			}
			return nil, err
		}
		pending.Stage = domain.RevisionStageRecordsApplied
		pending.UpdatedAt = time.Now()
		if err := s.store.Revisions.SavePending(pending); err != nil {
			return nil, err
		}
		fallthrough
	case domain.RevisionStageRecordsApplied:
		progress, err := s.store.Progress.Load()
		if err != nil || progress == nil {
			if err == nil {
				err = fmt.Errorf("progress 未初始化")
			}
			return nil, err
		}
		records, err := s.store.ChapterRecords.LoadCompleted(progress.CompletedChapters)
		if err != nil {
			return nil, err
		}
		if err := NewProjector(s.store).Apply(records); err != nil {
			return nil, fmt.Errorf("重建章节派生状态: %w", err)
		}
		if len(pending.Items) > 0 {
			if err := s.store.InvalidateChapterAggregates(pending.Items[0].Chapter); err != nil {
				return nil, fmt.Errorf("失效修订后的高层派生状态: %w", err)
			}
		}
		for _, item := range pending.Items {
			if slices.Contains(progress.PendingRewrites, item.Chapter) {
				if err := s.store.Progress.CompleteRewrite(item.Chapter); err != nil {
					return nil, fmt.Errorf("完成第 %d 章人工返工: %w", item.Chapter, err)
				}
			}
		}
		for _, item := range pending.Items {
			analysis := item.Analysis
			if analysis.StoryChanged || analysis.OutlineImpact != nil || len(analysis.DownstreamIssues) > 0 {
				feedback := store.ChapterFeedback{
					Chapter: item.Chapter, StoryChanged: analysis.StoryChanged,
					ChangeSummary: analysis.ChangeSummary, DownstreamIssues: analysis.DownstreamIssues,
				}
				if analysis.OutlineImpact != nil {
					feedback.Deviation = analysis.OutlineImpact.Deviation
					feedback.Suggestion = analysis.OutlineImpact.Suggestion
				}
				if err := s.store.Outline.AppendOutlineFeedback(feedback); err != nil {
					return nil, fmt.Errorf("保存第 %d 章大纲影响: %w", item.Chapter, err)
				}
			}
		}
		pending.Stage = domain.RevisionStageProjectionsApplied
		pending.UpdatedAt = time.Now()
		if err := s.store.Revisions.SavePending(pending); err != nil {
			return nil, err
		}
		fallthrough
	case domain.RevisionStageProjectionsApplied:
		for _, item := range pending.Items {
			if _, err := s.store.Checkpoints.AppendArtifact(domain.ChapterScope(item.Chapter), "revision_sync", store.ChapterRecordPath(item.Chapter)); err != nil {
				return nil, fmt.Errorf("记录第 %d 章修订 checkpoint: %w", item.Chapter, err)
			}
		}
	default:
		return nil, fmt.Errorf("未知修订恢复阶段 %q", pending.Stage)
	}
	if err := s.store.Revisions.ClearPending(); err != nil {
		return nil, fmt.Errorf("清理修订恢复记录: %w", err)
	}
	result := &Result{}
	for _, item := range pending.Items {
		result.Applied = append(result.Applied, item.Chapter)
		result.Analyses = append(result.Analyses, item.Analysis)
		if s.styleIndex != nil {
			s.styleIndex.ChapterCommitted(item.Chapter, item.Record.Content)
		}
	}
	return result, nil
}

func (s *Service) applyPreparedRecords(items []domain.PendingRevisionItem) error {
	for _, item := range items {
		content, err := s.store.Drafts.LoadChapterText(item.Chapter)
		if err != nil {
			return fmt.Errorf("读取第 %d 章工作区正文: %w", item.Chapter, err)
		}
		if domain.ChapterContentSHA256(content) != item.CurrentSHA256 {
			return fmt.Errorf("%w：第 %d 章在分析后再次发生变化，请重新执行 /sync", errPreparedStale, item.Chapter)
		}
		current, err := s.store.ChapterRecords.Load(item.Chapter)
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("第 %d 章接纳记录不存在", item.Chapter)
		}
		switch {
		case sameChapterRecord(*current, item.Record):
			continue
		case current.ContentSHA256 == item.BaseSHA256 && current.Revision+1 == item.Record.Revision:
			if err := s.store.ChapterRecords.Save(item.Record); err != nil {
				return fmt.Errorf("接纳第 %d 章修订: %w", item.Chapter, err)
			}
		default:
			return fmt.Errorf("%w：第 %d 章接纳记录在分析后发生变化，请重新执行 /sync", errPreparedStale, item.Chapter)
		}
	}
	return nil
}

func sameChapterRecord(a, b domain.ChapterRecord) bool {
	return a.Version == b.Version && a.Chapter == b.Chapter && a.Revision == b.Revision &&
		a.Origin == b.Origin && a.Content == b.Content && a.ContentSHA256 == b.ContentSHA256 &&
		reflect.DeepEqual(a.Facts, b.Facts) && reflect.DeepEqual(a.StyleDelta, b.StyleDelta) &&
		a.AcceptedAt.Equal(b.AcceptedAt)
}

func (s *Service) validatePendingRecords(items []domain.PendingRevisionItem) error {
	progress, err := s.store.Progress.Load()
	if err != nil || progress == nil {
		if err == nil {
			err = fmt.Errorf("progress 未初始化")
		}
		return err
	}
	records, err := s.store.ChapterRecords.LoadCompleted(progress.CompletedChapters)
	if err != nil {
		return err
	}
	replacements := make(map[int]domain.ChapterRecord, len(items))
	for _, item := range items {
		replacements[item.Chapter] = item.Record
	}
	for i, record := range records {
		if replacement, ok := replacements[record.Chapter]; ok {
			records[i] = replacement
		}
	}
	return ValidateRecords(records)
}

func (s *Service) downstreamSummaries(chapter int, proposed map[int]domain.ChapterSummary) ([]domain.ChapterSummary, error) {
	progress, err := s.store.Progress.Load()
	if err != nil {
		return nil, err
	}
	if progress == nil {
		return nil, fmt.Errorf("progress 未初始化")
	}
	chapters := slices.Clone(progress.CompletedChapters)
	slices.Sort(chapters)
	var summaries []domain.ChapterSummary
	for _, current := range chapters {
		if current <= chapter {
			continue
		}
		if summary, ok := proposed[current]; ok {
			summaries = append(summaries, summary)
			continue
		}
		summary, err := s.store.Summaries.LoadSummary(current)
		if err != nil {
			return nil, fmt.Errorf("读取第 %d 章摘要: %w", current, err)
		}
		if summary == nil {
			return nil, fmt.Errorf("第 %d 章缺少摘要，无法完整分析后续影响", current)
		}
		summaries = append(summaries, *summary)
	}
	return summaries, nil
}
