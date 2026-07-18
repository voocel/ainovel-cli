package ctxpack

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

const defaultStoreSummaryBudgetTokens = 7000

type writerStoreSummaryState struct {
	progress          *domain.Progress
	chapter           int
	currentOutline    *domain.OutlineEntry
	chapterPlan       *domain.ChapterPlan
	recentSummaries   []domain.ChapterSummary
	currentArcSummary *domain.ArcSummary
	currentVolSummary *domain.VolumeSummary
	snapshots         []domain.CharacterSnapshot
	foreshadow        []domain.ForeshadowEntry
	timeline          []domain.TimelineEvent
	styleRules        *domain.WritingStyleRules
	pendingReviews    []writerPendingReview
	warnings          []string
}

func (s *writerStoreSummaryState) warn(scope string, err error) {
	if s != nil && err != nil {
		s.warnings = append(s.warnings, fmt.Sprintf("%s 读取失败: %v", scope, err))
	}
}

type writerPendingReview struct {
	Chapter        int                 `json:"chapter"`
	Scope          string              `json:"scope"`
	Verdict        string              `json:"verdict"`
	Summary        string              `json:"summary,omitempty"`
	ContractMisses []string            `json:"contract_misses,omitempty"`
	Issues         []writerReviewIssue `json:"issues,omitempty"`
}

type writerReviewIssue struct {
	Type        string `json:"type,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion,omitempty"`
}

func buildWriterStoreSummaryText(s *store.Store, budgetTokens int) (string, bool, error) {
	state, ok, err := loadWriterStoreSummaryState(s)
	if err != nil || !ok {
		return "", ok, err
	}
	if budgetTokens <= 0 {
		budgetTokens = defaultStoreSummaryBudgetTokens
	}
	parts := renderWriterStoreSections(state, budgetTokens, writerStoreSummarySections(state))
	if len(parts) == 0 {
		return "", false, nil
	}
	return "以下内容来自小说持久化 store，用于在压缩后恢复写作上下文。\n\n" + strings.Join(parts, "\n\n"), true, nil
}

func buildWriterRestoreText(s *store.Store, budgetTokens int) (string, bool, error) {
	state, ok, err := loadWriterStoreSummaryState(s)
	if err != nil {
		return "", false, err
	}
	if !ok && s != nil {
		state, err = loadWriterRestoreState(s)
		if err != nil {
			return "", false, err
		}
	}
	if state == nil {
		return "", false, nil
	}
	if budgetTokens <= 0 {
		budgetTokens = restoreBudgetTokens
	}
	parts := renderWriterStoreSections(state, budgetTokens, writerRestoreSections(state))
	if len(parts) == 0 {
		return "", false, nil
	}
	return "<post-compact-context>\n" + strings.Join(parts, "\n\n") + "\n</post-compact-context>", true, nil
}

func loadWriterStoreSummaryState(s *store.Store) (*writerStoreSummaryState, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	progress, err := s.Progress.Load()
	if err != nil || progress == nil {
		return nil, false, err
	}

	chapter := progress.CurrentChapter
	if progress.InProgressChapter > 0 {
		chapter = progress.InProgressChapter
	}
	if chapter <= 1 {
		return nil, false, nil
	}

	profile := domain.NewContextProfile(progress.TotalChapters)
	if !progress.Layered {
		profile.Layered = false
	}

	state := &writerStoreSummaryState{
		progress: progress,
		chapter:  chapter,
	}

	state.chapterPlan, err = s.Drafts.LoadChapterPlan(chapter)
	if err != nil {
		state.warn("chapter_plan", err)
	}
	state.currentOutline, err = s.Outline.GetChapterOutline(chapter)
	if err != nil {
		state.warn("chapter_outline", err)
	}
	if state.currentOutline == nil {
		if outline, layeredErr := s.Outline.GetChapterFromLayered(chapter); layeredErr == nil {
			state.currentOutline = outline
		} else {
			state.warn("layered_chapter_outline", layeredErr)
		}
	}

	state.recentSummaries, err = s.Summaries.LoadRecentSummaries(chapter, profile.SummaryWindow)
	if err != nil {
		state.warn("recent_summaries", err)
	}
	state.snapshots, err = s.Characters.LoadLatestSnapshots()
	if err != nil {
		state.warn("character_snapshots", err)
	}
	state.foreshadow, err = s.World.LoadActiveForeshadow()
	if err != nil {
		state.warn("foreshadow", err)
	}
	state.timeline, err = s.World.LoadRecentTimeline(chapter, profile.TimelineWindow)
	if err != nil {
		state.warn("timeline", err)
	}
	state.styleRules, err = s.World.LoadStyleRules()
	if err != nil {
		state.warn("style_rules", err)
	}
	state.pendingReviews, err = loadPendingReviewsForStoreState(s, chapter)
	if err != nil {
		state.warn("pending_reviews", err)
	}

	loadLayeredSummariesForStoreState(s, progress, chapter, state)

	hasSummaries := len(state.recentSummaries) > 0 || state.currentArcSummary != nil || state.currentVolSummary != nil
	hasWorkingState := state.chapterPlan != nil || state.currentOutline != nil
	if !hasSummaries || !hasWorkingState {
		return nil, false, nil
	}
	return state, true, nil
}

func loadWriterRestoreState(s *store.Store) (*writerStoreSummaryState, error) {
	if s == nil {
		return nil, nil
	}
	progress, err := s.Progress.Load()
	if err != nil || progress == nil {
		return nil, err
	}

	chapter := progress.CurrentChapter
	if progress.InProgressChapter > 0 {
		chapter = progress.InProgressChapter
	}
	if chapter <= 0 {
		return nil, nil
	}

	profile := domain.NewContextProfile(progress.TotalChapters)
	if !progress.Layered {
		profile.Layered = false
	}

	state := &writerStoreSummaryState{
		progress: progress,
		chapter:  chapter,
	}
	state.chapterPlan, err = s.Drafts.LoadChapterPlan(chapter)
	state.warn("chapter_plan", err)
	state.currentOutline, err = s.Outline.GetChapterOutline(chapter)
	state.warn("chapter_outline", err)
	if state.currentOutline == nil {
		state.currentOutline, err = s.Outline.GetChapterFromLayered(chapter)
		state.warn("layered_chapter_outline", err)
	}
	state.snapshots, err = s.Characters.LoadLatestSnapshots()
	state.warn("character_snapshots", err)
	state.foreshadow, err = s.World.LoadActiveForeshadow()
	state.warn("foreshadow", err)
	state.pendingReviews, err = loadPendingReviewsForStoreState(s, chapter)
	state.warn("pending_reviews", err)
	state.styleRules, err = s.World.LoadStyleRules()
	state.warn("style_rules", err)
	state.timeline, err = s.World.LoadRecentTimeline(chapter, profile.TimelineWindow)
	state.warn("timeline", err)
	if chapter > 1 {
		state.recentSummaries, err = s.Summaries.LoadRecentSummaries(chapter, min(profile.SummaryWindow, 2))
		state.warn("recent_summaries", err)
	}
	loadLayeredSummariesForStoreState(s, progress, chapter, state)
	if isEmptySummarySection(state.chapterPlan) &&
		isEmptySummarySection(state.currentOutline) &&
		isEmptySummarySection(state.snapshots) &&
		isEmptySummarySection(state.pendingReviews) &&
		isEmptySummarySection(state.recentSummaries) &&
		isEmptySummarySection(state.foreshadow) {
		return nil, nil
	}
	return state, nil
}

type writerStoreSection struct {
	heading string
	data    any
}

func writerStoreProgressSection(state *writerStoreSummaryState) map[string]any {
	if state == nil || state.progress == nil {
		return nil
	}
	return map[string]any{
		"phase":               state.progress.Phase,
		"flow":                state.progress.Flow,
		"current_chapter":     state.chapter,
		"completed_chapters":  state.progress.CompletedChapters,
		"completed_count":     len(state.progress.CompletedChapters),
		"current_volume":      state.progress.CurrentVolume,
		"current_arc":         state.progress.CurrentArc,
		"in_progress_chapter": state.progress.InProgressChapter,
	}
}

func writerStoreSummarySections(state *writerStoreSummaryState) []writerStoreSection {
	return []writerStoreSection{
		{heading: "当前进度", data: writerStoreProgressSection(state)},
		{heading: "数据告警", data: state.warnings},
		{heading: "最近章节摘要", data: state.recentSummaries},
		{heading: "当前章节计划", data: state.chapterPlan},
		{heading: "当前章节大纲", data: state.currentOutline},
		{heading: "当前弧摘要", data: state.currentArcSummary},
		{heading: "当前卷摘要", data: state.currentVolSummary},
		{heading: "角色快照", data: state.snapshots},
		{heading: "活跃伏笔", data: state.foreshadow},
		{heading: "待修审稿问题", data: state.pendingReviews},
		{heading: "最近时间线", data: state.timeline},
		{heading: "风格规则", data: state.styleRules},
	}
}

func writerRestoreSections(state *writerStoreSummaryState) []writerStoreSection {
	return []writerStoreSection{
		{heading: "当前进度", data: writerStoreProgressSection(state)},
		{heading: "数据告警", data: state.warnings},
		{heading: "当前章节计划", data: state.chapterPlan},
		{heading: "当前章节大纲", data: state.currentOutline},
		{heading: "待修审稿问题", data: state.pendingReviews},
		{heading: "角色快照", data: state.snapshots},
		{heading: "最近章节摘要", data: state.recentSummaries},
		{heading: "活跃伏笔", data: state.foreshadow},
		{heading: "当前弧摘要", data: state.currentArcSummary},
		{heading: "当前卷摘要", data: state.currentVolSummary},
		{heading: "最近时间线", data: state.timeline},
		{heading: "风格规则", data: state.styleRules},
	}
}

func renderWriterStoreSections(state *writerStoreSummaryState, budgetTokens int, sections []writerStoreSection) []string {
	if state == nil || len(sections) == 0 || budgetTokens <= 0 {
		return nil
	}
	parts := make([]string, 0, len(sections))
	remaining := budgetTokens
	for _, sec := range sections {
		if isEmptySummarySection(sec.data) {
			continue
		}
		stop := appendJSONSection(&parts, sec.heading, sec.data, &remaining)
		if stop {
			break
		}
	}
	return parts
}

func loadPendingReviewsForStoreState(s *store.Store, chapter int) ([]writerPendingReview, error) {
	if s == nil || chapter <= 1 {
		return nil, nil
	}
	start := max(chapter-3, 1)
	pending := make([]writerPendingReview, 0, 4)
	for ch := chapter - 1; ch >= start; ch-- {
		review, err := s.World.LoadReview(ch)
		if err != nil {
			return pending, err
		}
		if compact, ok := compactPendingReview(review); ok {
			pending = append(pending, compact)
		}
	}
	global, err := s.World.LoadLastReview(chapter - 1)
	if err != nil {
		return pending, err
	}
	if compact, ok := compactPendingReview(global); ok {
		alreadyIncluded := false
		for _, item := range pending {
			if item.Chapter == compact.Chapter && item.Scope == compact.Scope {
				alreadyIncluded = true
				break
			}
		}
		if !alreadyIncluded {
			pending = append(pending, compact)
		}
	}
	return pending, nil
}

func compactPendingReview(review *domain.ReviewEntry) (writerPendingReview, bool) {
	if review == nil {
		return writerPendingReview{}, false
	}
	if review.Verdict == "accept" && len(review.Issues) == 0 && len(review.ContractMisses) == 0 {
		return writerPendingReview{}, false
	}
	item := writerPendingReview{
		Chapter: review.Chapter,
		Scope:   review.Scope,
		Verdict: review.Verdict,
		Summary: review.Summary,
	}
	if len(review.ContractMisses) > 0 {
		item.ContractMisses = append([]string(nil), review.ContractMisses[:min(len(review.ContractMisses), 5)]...)
	}
	if len(review.Issues) > 0 {
		limit := min(len(review.Issues), 5)
		item.Issues = make([]writerReviewIssue, 0, limit)
		for _, issue := range review.Issues[:limit] {
			item.Issues = append(item.Issues, writerReviewIssue{
				Type:        issue.Type,
				Severity:    issue.Severity,
				Description: issue.Description,
				Suggestion:  issue.Suggestion,
			})
		}
	}
	return item, true
}

func loadLayeredSummariesForStoreState(s *store.Store, progress *domain.Progress, chapter int, state *writerStoreSummaryState) {
	if s == nil || progress == nil || state == nil {
		return
	}
	if !progress.Layered {
		return
	}
	volume, arc := progress.CurrentVolume, progress.CurrentArc
	if volume <= 0 || arc <= 0 {
		if v, a, err := s.Outline.LocateChapter(chapter); err == nil {
			volume, arc = v, a
		} else if v, a, err := s.Outline.LocateChapter(max(chapter-1, 1)); err == nil {
			volume, arc = v, a
		} else {
			state.warn("chapter_location", err)
		}
	}
	if volume <= 0 {
		return
	}
	if sum, err := s.Summaries.LoadVolumeSummary(volume); err == nil {
		state.currentVolSummary = sum
	} else {
		state.warn("volume_summary", err)
	}
	if arc > 0 {
		if sum, err := s.Summaries.LoadArcSummary(volume, arc); err == nil {
			state.currentArcSummary = sum
		} else {
			state.warn("arc_summary", err)
		}
	}
}

func appendJSONSection(parts *[]string, heading string, data any, remaining *int) bool {
	if parts == nil || remaining == nil || *remaining <= 0 {
		return true
	}
	b, err := json.Marshal(data)
	if err != nil {
		return false
	}
	text := string(b)
	tokens := estimateCompactSectionTokens(heading, text)
	if tokens > *remaining {
		if *remaining <= 100 {
			return true
		}
		text = truncateJSONToTokens(b, *remaining-20)
		*parts = append(*parts, fmt.Sprintf("## %s\n%s [已截断]", heading, text))
		*remaining = 0
		return true
	}
	*parts = append(*parts, fmt.Sprintf("## %s\n%s", heading, text))
	*remaining -= tokens
	return false
}

func estimateCompactSectionTokens(heading, body string) int {
	section := fmt.Sprintf("## %s\n%s", heading, body)
	return corecontext.EstimateTokens(agentcore.UserMsg(section))
}

func isEmptySummarySection(data any) bool {
	if data == nil {
		return true
	}
	rv := reflect.ValueOf(data)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	case reflect.Slice, reflect.Map, reflect.Array, reflect.String:
		return rv.Len() == 0
	default:
		return false
	}
}
