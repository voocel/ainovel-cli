package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/apperr"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CommitChapterTool 提交章节：加载正文 → 保存终稿 → 生成摘要 → 更新状态 → 更新进度。
type CommitChapterTool struct {
	store *store.Store
}

func NewCommitChapterTool(store *store.Store) *CommitChapterTool {
	return &CommitChapterTool{store: store}
}

func (t *CommitChapterTool) Name() string { return "commit_chapter" }
func (t *CommitChapterTool) Description() string {
	return "提交章节终稿。加载草稿正文，保存为终稿，同时更新时间线、伏笔、关系、角色状态。返回结构化信号"
}
func (t *CommitChapterTool) Label() string { return "提交章节" }

func (t *CommitChapterTool) Schema() map[string]any {
	timelineSchema := schema.Object(
		schema.Property("time", schema.String("故事内时间")).Required(),
		schema.Property("event", schema.String("事件描述")).Required(),
		schema.Property("characters", schema.Array("涉及角色", schema.String(""))),
	)
	foreshadowSchema := schema.Object(
		schema.Property("id", schema.String("伏笔 ID")).Required(),
		schema.Property("action", schema.Enum("操作", "plant", "advance", "resolve")).Required(),
		schema.Property("description", schema.String("伏笔描述（仅 plant 时必需）")),
	)
	relationshipSchema := schema.Object(
		schema.Property("character_a", schema.String("角色 A")).Required(),
		schema.Property("character_b", schema.String("角色 B")).Required(),
		schema.Property("relation", schema.String("当前关系描述")).Required(),
	)
	stateChangeSchema := schema.Object(
		schema.Property("entity", schema.String("角色名或实体名")).Required(),
		schema.Property("field", schema.String("变化属性")).Required(),
		schema.Property("old_value", schema.String("变化前的值")),
		schema.Property("new_value", schema.String("变化后的值")).Required(),
		schema.Property("reason", schema.String("变化原因")),
	)
	feedbackSchema := schema.Object(
		schema.Property("deviation", schema.String("偏离大纲的描述")).Required(),
		schema.Property("suggestion", schema.String("对后续大纲的调整建议")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("summary", schema.String("本章内容摘要（200字以内）")).Required(),
		schema.Property("characters", schema.Array("本章出场角色名", schema.String(""))).Required(),
		schema.Property("key_events", schema.Array("本章关键事件", schema.String(""))).Required(),
		schema.Property("timeline_events", schema.Array("本章时间线事件", timelineSchema)),
		schema.Property("foreshadow_updates", schema.Array("伏笔操作", foreshadowSchema)),
		schema.Property("relationship_changes", schema.Array("关系变化", relationshipSchema)),
		schema.Property("state_changes", schema.Array("角色/实体状态变化", stateChangeSchema)),
		schema.Property("hook_type", schema.Enum("章末钩子类型", "crisis", "mystery", "desire", "emotion", "choice")),
		schema.Property("dominant_strand", schema.Enum("本章主导叙事线", "quest", "fire", "constellation")),
		schema.Property("feedback", feedbackSchema),
	)
}

func (t *CommitChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter             int                        `json:"chapter"`
		Summary             string                     `json:"summary"`
		Characters          []string                   `json:"characters"`
		KeyEvents           []string                   `json:"key_events"`
		TimelineEvents      []domain.TimelineEvent     `json:"timeline_events"`
		ForeshadowUpdates   []domain.ForeshadowUpdate  `json:"foreshadow_updates"`
		RelationshipChanges []domain.RelationshipEntry `json:"relationship_changes"`
		StateChanges        []domain.StateChange       `json:"state_changes"`
		HookType            string                     `json:"hook_type"`
		DominantStrand      string                     `json:"dominant_strand"`
		Feedback            *domain.OutlineFeedback    `json:"feedback"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeToolArgsInvalid, "tools.commit_chapter.decode_args", "invalid args")
	}
	if a.Chapter <= 0 {
		return nil, apperr.New(apperr.CodeToolArgsInvalid, "tools.commit_chapter.validate_args", "chapter must be > 0")
	}
	existingPending, err := t.store.Signals.LoadPendingCommit()
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreReadFailed, "tools.commit_chapter.load_pending_commit", "load pending commit")
	}
	if existingPending != nil && existingPending.Chapter != a.Chapter {
		return nil, apperr.New(
			apperr.CodeToolConflict,
			"tools.commit_chapter.check_pending_commit",
			fmt.Sprintf("存在未恢复的章节提交：第 %d 章（阶段 %s），请先恢复或重新提交该章", existingPending.Chapter, existingPending.Stage),
		)
	}
	if err := t.store.Progress.ValidateChapterCommit(a.Chapter); err != nil {
		if apperr.CodeOf(err) != apperr.CodeUnknown {
			return nil, err
		}
		return nil, apperr.Wrap(err, apperr.CodeToolPreconditionFailed, "tools.commit_chapter.validate_commit", "章节当前不允许提交")
	}

	// 1. 加载章节正文
	content, wordCount, err := t.store.Drafts.LoadChapterContent(a.Chapter)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreReadFailed, "tools.commit_chapter.load_chapter_content", "load chapter content")
	}
	if content == "" {
		return nil, apperr.New(
			apperr.CodeToolPreconditionFailed,
			"tools.commit_chapter.load_chapter_content",
			fmt.Sprintf("no content found for chapter %d", a.Chapter),
		)
	}

	now := time.Now().Format(time.RFC3339)
	pending := domain.PendingCommit{
		Chapter:        a.Chapter,
		Stage:          domain.CommitStageStarted,
		Summary:        a.Summary,
		HookType:       a.HookType,
		DominantStrand: a.DominantStrand,
		StartedAt:      now,
		UpdatedAt:      now,
	}
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.save_pending_commit", "save pending commit")
	}

	// 2. 保存终稿
	if err := t.store.Drafts.SaveFinalChapter(a.Chapter, content); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.save_final_chapter", "save final chapter")
	}

	// 3. 保存摘要
	summary := domain.ChapterSummary{
		Chapter:    a.Chapter,
		Summary:    a.Summary,
		Characters: a.Characters,
		KeyEvents:  a.KeyEvents,
	}
	if err := t.store.Summaries.SaveSummary(summary); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.save_summary", "save summary")
	}

	// 4. 更新状态增量
	if len(a.TimelineEvents) > 0 {
		for i := range a.TimelineEvents {
			a.TimelineEvents[i].Chapter = a.Chapter
		}
		if err := t.store.World.AppendTimelineEvents(a.TimelineEvents); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.append_timeline", "append timeline")
		}
	}
	if len(a.ForeshadowUpdates) > 0 {
		if err := t.store.World.UpdateForeshadow(a.Chapter, a.ForeshadowUpdates); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.update_foreshadow", "update foreshadow")
		}
	}
	if len(a.RelationshipChanges) > 0 {
		for i := range a.RelationshipChanges {
			a.RelationshipChanges[i].Chapter = a.Chapter
		}
		if err := t.store.World.UpdateRelationships(a.RelationshipChanges); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.update_relationships", "update relationships")
		}
	}
	if len(a.StateChanges) > 0 {
		for i := range a.StateChanges {
			a.StateChanges[i].Chapter = a.Chapter
		}
		if err := t.store.World.AppendStateChanges(a.StateChanges); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.append_state_changes", "append state changes")
		}
	}
	pending.Stage = domain.CommitStageStateApplied
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.update_pending_commit_stage", "update pending commit stage")
	}

	// 5. 更新进度
	if err := t.store.Progress.MarkChapterComplete(a.Chapter, wordCount, a.HookType, a.DominantStrand); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.mark_chapter_complete", "mark chapter complete")
	}

	// 6. 判断是否需要审阅
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreReadFailed, "tools.commit_chapter.load_progress", "load progress")
	}
	completedCount := 0
	if progress != nil {
		completedCount = len(progress.CompletedChapters)
	}

	// 6b. 长篇模式：弧级边界检测
	var arcEnd, volumeEnd, needsExpansion, needsNewVolume bool
	var vol, arc, nextVol, nextArc int
	if progress != nil && progress.Layered {
		boundary, bErr := t.store.Outline.CheckArcBoundary(a.Chapter)
		if bErr != nil {
			slog.Warn("弧边界检测失败", "module", "commit", "chapter", a.Chapter, "err", bErr)
		} else if boundary != nil {
			arcEnd = boundary.IsArcEnd
			volumeEnd = boundary.IsVolumeEnd
			vol = boundary.Volume
			arc = boundary.Arc
			needsExpansion = boundary.NeedsExpansion
			needsNewVolume = boundary.NeedsNewVolume
			nextVol = boundary.NextVolume
			nextArc = boundary.NextArc
			_ = t.store.Progress.UpdateVolumeArc(vol, arc)
		}
	}

	var reviewRequired bool
	var reviewReason string
	if progress != nil && progress.Layered {
		reviewRequired, reviewReason = domain.ShouldArcReview(arcEnd, volumeEnd, vol, arc)
	} else {
		reviewRequired, reviewReason = domain.ShouldReview(completedCount)
	}

	// 7. 构造结构化信号
	result := domain.CommitResult{
		Chapter:        a.Chapter,
		Committed:      true,
		WordCount:      wordCount,
		NextChapter:    a.Chapter + 1,
		ReviewRequired: reviewRequired,
		ReviewReason:   reviewReason,
		HookType:       a.HookType,
		DominantStrand: a.DominantStrand,
		Feedback:       a.Feedback,
		ArcEnd:         arcEnd,
		VolumeEnd:      volumeEnd,
		Volume:         vol,
		Arc:            arc,
		NeedsExpansion: needsExpansion,
		NeedsNewVolume: needsNewVolume,
		NextVolume:     nextVol,
		NextArc:        nextArc,
	}

	// 8. 生成 [系统] 提示 — 替代 orchestrator/policy 层的 FollowUp
	result.SystemHints = t.buildSystemHints(&result, progress)

	pending.Stage = domain.CommitStageProgressMarked
	pending.Result = &result
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.update_pending_commit_result", "update pending commit result")
	}

	// 9. 清除进度中间状态
	if err := t.store.Progress.ClearInProgress(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.clear_in_progress", "clear in-progress")
	}
	if err := t.store.Signals.ClearPendingCommit(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.clear_pending_commit", "clear pending commit")
	}

	// 10. 追加 checkpoint
	_, _ = t.store.Checkpoints.Append(
		domain.ChapterScope(a.Chapter), "commit",
		fmt.Sprintf("chapters/ch%02d.md", a.Chapter), "",
	)

	return json.Marshal(result)
}

// buildSystemHints 内联原 orchestrator/policy/commit.go 的决策逻辑，
// 把确定性的下一步指令嵌入返回值，由 Coordinator 直接读取执行。
func (t *CommitChapterTool) buildSystemHints(result *domain.CommitResult, progress *domain.Progress) []string {
	var hints []string

	// Writer 大纲偏离反馈
	if result.Feedback != nil && result.Feedback.Deviation != "" {
		hints = append(hints, fmt.Sprintf(
			"[系统] writer_feedback: Writer 在第 %d 章发现大纲偏离。偏离：%s。建议：%s。请评估是否需要调用规划师调整后续大纲。",
			result.Chapter, result.Feedback.Deviation, result.Feedback.Suggestion))
	}

	// 重写/打磨流程中的章节完成
	if progress != nil && (progress.Flow == domain.FlowRewriting || progress.Flow == domain.FlowPolishing) {
		verb := "重写"
		if progress.Flow == domain.FlowPolishing {
			verb = "打磨"
		}
		remaining := removeInt(progress.PendingRewrites, result.Chapter)
		if len(remaining) > 0 {
			hints = append(hints, fmt.Sprintf(
				"[系统] %s完成: 第 %d 章已完成%s。剩余待处理章节: %v。请继续处理下一章。",
				verb, result.Chapter, verb, remaining))
		} else {
			hints = append(hints, fmt.Sprintf(
				"[系统] %s全部完成: 第 %d 章已完成%s。所有待%s章节已处理完毕，继续写第 %d 章。",
				verb, result.Chapter, verb, verb, result.NextChapter))
		}
		return hints
	}

	// 全书完成
	if progress != nil && progress.TotalChapters > 0 && result.NextChapter > progress.TotalChapters {
		hints = append(hints, fmt.Sprintf(
			"[系统] book_complete: 全部 %d 章已写完（共 %d 字）。请输出全书总结并结束，不要再调用 writer。",
			progress.TotalChapters, progress.TotalWordCount+result.WordCount))
		return hints
	}

	// 长篇弧/卷结束
	if result.ArcEnd {
		if result.VolumeEnd {
			hints = append(hints, fmt.Sprintf(
				"[系统] arc_end: 第 %d 卷第 %d 弧结束（卷结束）。请依次：1) 调用 editor 对本弧进行评审 2) 调用 editor 生成弧摘要 3) 调用 editor 生成卷摘要。",
				result.Volume, result.Arc))
		} else {
			hints = append(hints, fmt.Sprintf(
				"[系统] arc_end: 第 %d 卷第 %d 弧结束。请依次：1) 调用 editor 对本弧进行评审 2) 调用 editor 生成弧摘要。",
				result.Volume, result.Arc))
		}
		if result.NeedsNewVolume {
			hints = append(hints, "[系统] new_volume_required: 评审和摘要完成后，请调用 architect_long 自主规划下一卷（save_foundation type=append_volume），同时更新指南针（save_foundation type=update_compass），然后继续写作。")
		} else if result.NeedsExpansion {
			hints = append(hints, fmt.Sprintf(
				"[系统] expand_arc_required: 评审和摘要完成后，请调用 architect_long 为第 %d 卷第 %d 弧展开详细章节规划（save_foundation type=expand_arc），然后继续写作。",
				result.NextVolume, result.NextArc))
		}
		return hints
	}

	// 非弧结束的审阅触发
	if result.ReviewRequired {
		hints = append(hints, fmt.Sprintf(
			"[系统] review_required: %s。请调用 editor 进行全局审阅。",
			result.ReviewReason))
		return hints
	}

	// 默认：继续写下一章
	if progress != nil && progress.TotalChapters > 0 {
		hints = append(hints, fmt.Sprintf(
			"[系统] continue: 第 %d 章提交成功（%d 字）。请继续写第 %d 章（共 %d 章）。",
			result.Chapter, result.WordCount, result.NextChapter, progress.TotalChapters))
	}

	return hints
}

// removeInt 从 slice 中移除指定值。
func removeInt(s []int, v int) []int {
	var result []int
	for _, x := range s {
		if x != v {
			result = append(result, x)
		}
	}
	return result
}
