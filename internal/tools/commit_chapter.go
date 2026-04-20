package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
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
	return "提交章节终稿。加载草稿正文保存为终稿，更新时间线、伏笔、关系、角色状态和进度。" +
		"返回结构化事实：next_chapter / review_required / arc_end / volume_end / needs_expansion / book_complete / flow 等"
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
	if t.store.Progress.IsChapterCompleted(a.Chapter) {
		// 清理可能残留的 PendingCommit（崩溃发生在 ProgressMarked 之后、ClearPendingCommit 之前）
		if pending, _ := t.store.Signals.LoadPendingCommit(); pending != nil && pending.Chapter == a.Chapter {
			_ = t.store.Signals.ClearPendingCommit()
		}
		// 打磨/重写路径：章节虽已完成，但仍在 pending_rewrites 中，允许覆盖并 drain 队列
		progress, _ := t.store.Progress.Load()
		if progress != nil && slices.Contains(progress.PendingRewrites, a.Chapter) {
			return t.executeRewriteCommit(a.Chapter, a.Summary, a.Characters, a.KeyEvents,
				a.HookType, a.DominantStrand, progress)
		}
		return t.buildSkipResult(a.Chapter, progress)
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
	if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
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
	var arcEnd, volumeEnd, isFinalVolume, needsExpansion, needsNewVolume bool
	var vol, arc, nextVol, nextArc int
	if progress != nil && progress.Layered {
		boundary, bErr := t.store.Outline.CheckArcBoundary(a.Chapter)
		if bErr != nil {
			slog.Error("弧边界检测失败，降级为非弧结束", "module", "commit", "chapter", a.Chapter, "err", bErr)
		} else if boundary == nil {
			slog.Warn("章节不在分层大纲中，降级为非弧结束", "module", "commit", "chapter", a.Chapter)
		} else {
			arcEnd = boundary.IsArcEnd
			volumeEnd = boundary.IsVolumeEnd
			isFinalVolume = boundary.IsFinalVolume
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
		IsFinalVolume:  isFinalVolume,
		Volume:         vol,
		Arc:            arc,
		NeedsExpansion: needsExpansion,
		NeedsNewVolume: needsNewVolume,
		NextVolume:     nextVol,
		NextArc:        nextArc,
	}

	// 8. 完成态判定：非分层写完最后一章 / 分层最终卷最后一章 → MarkComplete
	if t.applyCompletion(&result, progress) {
		result.BookComplete = true
	}
	if p, _ := t.store.Progress.Load(); p != nil {
		result.Flow = string(p.Flow)
	}

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

// executeRewriteCommit 处理打磨/重写章节的提交：覆盖终稿与摘要、更新字数、drain 队列。
// 跳过所有世界状态追加（timeline / foreshadow / relationship / state_changes）与弧边界检测，
// 这些已在章节原始提交时应用。
func (t *CommitChapterTool) executeRewriteCommit(
	chapter int,
	summary string,
	characters, keyEvents []string,
	hookType, dominantStrand string,
	progress *domain.Progress,
) (json.RawMessage, error) {
	// 1. 加载打磨后的正文
	content, wordCount, err := t.store.Drafts.LoadChapterContent(chapter)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreReadFailed, "tools.commit_chapter.rewrite.load_content", "load chapter content")
	}
	if content == "" {
		return nil, apperr.New(
			apperr.CodeToolPreconditionFailed,
			"tools.commit_chapter.rewrite.load_content",
			fmt.Sprintf("no content found for chapter %d", chapter),
		)
	}

	// 2. 硬校验：drafts 与现终稿完全相同 → 判定为未真正打磨/重写（writer 跳过了 draft_chapter）
	// 拒绝 commit，强制 writer 先调 draft_chapter(mode=write) 写入新版本。
	existingFinal, _ := t.store.Drafts.LoadChapterText(chapter)
	if existingFinal != "" && existingFinal == content {
		mode := "重写"
		if progress != nil && progress.Flow == domain.FlowPolishing {
			mode = "打磨"
		}
		return nil, apperr.New(
			apperr.CodeToolPreconditionFailed,
			"tools.commit_chapter.rewrite.no_changes",
			fmt.Sprintf("第 %d 章 drafts 与 chapters 内容完全相同，未检测到%s改动。请先调 draft_chapter(mode=write, chapter=%d) 写入%s后的新正文，再 commit_chapter。",
				chapter, mode, chapter, mode),
		)
	}

	// 3. 覆盖终稿
	if err := t.store.Drafts.SaveFinalChapter(chapter, content); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.rewrite.save_final", "save final chapter")
	}

	// 3. 覆盖摘要
	if err := t.store.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:    chapter,
		Summary:    summary,
		Characters: characters,
		KeyEvents:  keyEvents,
	}); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.rewrite.save_summary", "save summary")
	}

	// 4. 更新字数（MarkChapterComplete 对已完成章节是幂等的：replaces word count, slice.Contains 防止重复入队）
	if err := t.store.Progress.MarkChapterComplete(chapter, wordCount, hookType, dominantStrand); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.rewrite.mark_complete", "update word count")
	}

	// 5. Drain 待处理队列；队列空时 CompleteRewrite 会自动把 flow 切回 writing
	if err := t.store.Progress.CompleteRewrite(chapter); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeStoreWriteFailed, "tools.commit_chapter.rewrite.complete_rewrite", "complete rewrite")
	}

	// 6. Checkpoint
	_, _ = t.store.Checkpoints.Append(
		domain.ChapterScope(chapter), "commit",
		fmt.Sprintf("chapters/ch%02d.md", chapter), "",
	)

	// 7. 读取 drain 后的 Progress 快照，作为事实返回
	mode := "rewrite"
	if progress.Flow == domain.FlowPolishing {
		mode = "polish"
	}
	latest, _ := t.store.Progress.Load()
	remaining := []int{}
	nextChapter := chapter + 1
	flow := string(domain.FlowWriting)
	if latest != nil {
		remaining = append(remaining, latest.PendingRewrites...)
		nextChapter = latest.NextChapter()
		flow = string(latest.Flow)
	}
	drained := len(remaining) == 0

	return json.Marshal(map[string]any{
		"chapter":         chapter,
		"rewritten":       true,
		"mode":            mode,
		"word_count":      wordCount,
		"remaining_queue": remaining,
		"queue_drained":   drained,
		"next_chapter":    nextChapter,
		"flow":            flow,
	})
}

// buildSkipResult 为"章节已完成的重复提交"构造与正常 commit 对齐的事实返回。
// 协调者据此做后续决策（writer/editor/architect 派发），而不会因为拿到 prose 提示而幻觉。
func (t *CommitChapterTool) buildSkipResult(chapter int, progress *domain.Progress) (json.RawMessage, error) {
	_, wordCount, _ := t.store.Drafts.LoadChapterContent(chapter)

	result := domain.CommitResult{
		Chapter:     chapter,
		Committed:   true,
		WordCount:   wordCount,
		NextChapter: chapter + 1,
	}

	if progress != nil && progress.Layered {
		if boundary, _ := t.store.Outline.CheckArcBoundary(chapter); boundary != nil {
			result.ArcEnd = boundary.IsArcEnd
			result.VolumeEnd = boundary.IsVolumeEnd
			result.IsFinalVolume = boundary.IsFinalVolume
			result.Volume = boundary.Volume
			result.Arc = boundary.Arc
			result.NeedsExpansion = boundary.NeedsExpansion
			result.NeedsNewVolume = boundary.NeedsNewVolume
			result.NextVolume = boundary.NextVolume
			result.NextArc = boundary.NextArc
		}
		result.ReviewRequired, result.ReviewReason = domain.ShouldArcReview(result.ArcEnd, result.VolumeEnd, result.Volume, result.Arc)
	} else if progress != nil {
		result.ReviewRequired, result.ReviewReason = domain.ShouldReview(len(progress.CompletedChapters))
	}

	if progress != nil {
		if progress.Phase == domain.PhaseComplete {
			result.BookComplete = true
		}
		result.Flow = string(progress.Flow)
	}

	return json.Marshal(result)
}

// applyCompletion 检查本次 commit 是否使整本书完成；若是则 MarkComplete。
// 返回 true 表示全书已完成。
func (t *CommitChapterTool) applyCompletion(result *domain.CommitResult, progress *domain.Progress) bool {
	if progress == nil {
		return false
	}
	// 非分层模式：写完约定总章数
	if !progress.Layered && progress.TotalChapters > 0 && result.NextChapter > progress.TotalChapters {
		_ = t.store.Progress.MarkComplete()
		return true
	}
	// 分层模式：最终卷最后一弧结束 = 全书完成
	if result.ArcEnd && result.VolumeEnd && result.IsFinalVolume {
		_ = t.store.Progress.MarkComplete()
		return true
	}
	return false
}
