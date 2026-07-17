package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CommitChapterTool 提交章节：加载正文 → 保存终稿 → 生成摘要 → 更新状态 → 更新进度。
type CommitChapterTool struct {
	store *store.Store
}

func NewCommitChapterTool(store *store.Store) *CommitChapterTool {
	return &CommitChapterTool{store: store}
}

// commitOutput 在 domain.CommitResult 之上嵌入扩展字段，保持 domain 包不依赖 rules。
// 由于嵌入字段会被 JSON marshaler 提升（promoted），序列化结果等同于扁平结构。
type commitOutput struct {
	domain.CommitResult
	RuleViolations []rules.Violation `json:"rule_violations,omitempty"`
}

// commitArgs 是提交 Saga 的规范化结构化载荷。首次执行把它与正文快照一起写入
// PendingCommit；崩溃恢复一律重放这份冻结意图，忽略新 Worker 生成的参数和草稿。
type commitArgs struct {
	Chapter             int                        `json:"chapter"`
	Summary             string                     `json:"summary"`
	Characters          []string                   `json:"characters"`
	KeyEvents           []string                   `json:"key_events"`
	TimelineEvents      []domain.TimelineEvent     `json:"timeline_events"`
	ForeshadowUpdates   []domain.ForeshadowUpdate  `json:"foreshadow_updates"`
	RelationshipChanges []domain.RelationshipEntry `json:"relationship_changes"`
	StateChanges        []domain.StateChange       `json:"state_changes"`
	CastIntros          []domain.CastIntro         `json:"cast_intros"`
	HookType            string                     `json:"hook_type"`
	DominantStrand      string                     `json:"dominant_strand"`
	Feedback            *domain.OutlineFeedback    `json:"feedback"`
}

func (t *CommitChapterTool) Name() string { return "commit_chapter" }
func (t *CommitChapterTool) Description() string {
	return "提交章节终稿。加载草稿正文保存为终稿，更新时间线、伏笔、关系、角色状态和进度。" +
		"返回结构化事实：next_chapter / review_required / arc_end / volume_end / needs_expansion / book_complete / flow 等"
}
func (t *CommitChapterTool) Label() string { return "提交章节" }

// 写工具（跨域可恢复 Saga：完整载荷→终稿/状态→进度→checkpoint），禁止并发。
func (t *CommitChapterTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *CommitChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

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
	feedbackSchema["description"] = "对后续大纲的建议对象；必须直接传 JSON object，不要传字符串化 JSON"
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("summary", schema.String("本章内容摘要（200字以内）")).Required(),
		schema.Property("characters", schema.Array("本章出场角色名", schema.String(""))).Required(),
		schema.Property("key_events", schema.Array("本章关键事件", schema.String(""))).Required(),
		schema.Property("timeline_events", schema.Array("本章时间线事件", timelineSchema)),
		schema.Property("foreshadow_updates", schema.Array("伏笔操作", foreshadowSchema)),
		schema.Property("relationship_changes", schema.Array("关系变化", relationshipSchema)),
		schema.Property("state_changes", schema.Array("角色/实体状态变化", stateChangeSchema)),
		schema.Property("cast_intros", schema.Array("本章首次引入且后续可能再出现的次要角色简介（不含主角及 characters.json 已有角色）", schema.Object(
			schema.Property("name", schema.String("角色名")).Required(),
			schema.Property("brief_role", schema.String("一句话定位（如：客栈老板/赌坊打手）")).Required(),
		))),
		schema.Property("hook_type", schema.Enum("章末钩子类型", "crisis", "mystery", "desire", "emotion", "choice")),
		schema.Property("dominant_strand", schema.Enum("本章主导叙事线", "quest", "fire", "constellation")),
		schema.Property("feedback", feedbackSchema),
	)
}

func (t *CommitChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var requested commitArgs
	if err := json.Unmarshal(args, &requested); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if requested.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	existingPending, err := t.store.Signals.LoadPendingCommit()
	if err != nil {
		return nil, fmt.Errorf("load pending commit: %w: %w", errs.ErrStoreRead, err)
	}
	if existingPending != nil && existingPending.Chapter != requested.Chapter {
		return nil, fmt.Errorf("存在未恢复的章节提交：第 %d 章（阶段 %s），请先恢复或重新提交该章: %w", existingPending.Chapter, existingPending.Stage, errs.ErrToolConflict)
	}
	if existingPending != nil {
		switch existingPending.Stage {
		case domain.CommitStageStarted, domain.CommitStageStateApplied, domain.CommitStageProgressMarked, domain.CommitStageSignalSaved:
		default:
			return nil, fmt.Errorf("pending commit 阶段非法: %q: %w", existingPending.Stage, errs.ErrToolConflict)
		}
	}

	a := requested
	if existingPending != nil && existingPending.Stage != domain.CommitStageProgressMarked && existingPending.Stage != domain.CommitStageSignalSaved {
		if len(existingPending.Payload) == 0 {
			return nil, fmt.Errorf("第 %d 章存在旧版未完成提交，但缺少可重放 payload；拒绝使用新生成参数覆盖，请从最近 checkpoint 恢复或人工核对 meta/pending_commit.json: %w",
				existingPending.Chapter, errs.ErrToolConflict)
		}
		if err := json.Unmarshal(existingPending.Payload, &a); err != nil {
			return nil, fmt.Errorf("decode pending commit payload: %w: %w", errs.ErrStoreRead, err)
		}
		if a.Chapter != existingPending.Chapter {
			return nil, fmt.Errorf("pending commit payload 章节不一致：记录=%d payload=%d: %w", existingPending.Chapter, a.Chapter, errs.ErrToolConflict)
		}
	}

	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil {
		return nil, fmt.Errorf("progress 未初始化: %w", errs.ErrToolPrecondition)
	}
	completed := slices.Contains(progress.CompletedChapters, a.Chapter)
	if existingPending != nil && (existingPending.Stage == domain.CommitStageProgressMarked || existingPending.Stage == domain.CommitStageSignalSaved) {
		if !completed {
			return nil, fmt.Errorf("pending commit 已到 %s，但 progress 未标记第 %d 章完成: %w", existingPending.Stage, a.Chapter, errs.ErrToolConflict)
		}
		return t.finishPendingCommit(*existingPending, progress)
	}

	if existingPending != nil && existingPending.Rewrite {
		if !completed {
			return nil, fmt.Errorf("返工提交要求第 %d 章已存在终稿: %w", a.Chapter, errs.ErrToolConflict)
		}
		return t.executeRewriteCommit(a, progress, *existingPending, true)
	}
	if existingPending == nil && completed {
		if slices.Contains(progress.PendingRewrites, a.Chapter) {
			content, err := t.validateRewriteDraft(a.Chapter, progress)
			if err != nil {
				return nil, err
			}
			payload, err := json.Marshal(a)
			if err != nil {
				return nil, fmt.Errorf("marshal rewrite payload: %w", err)
			}
			now := time.Now().Format(time.RFC3339)
			mode := "rewrite"
			if progress.Flow == domain.FlowPolishing {
				mode = "polish"
			}
			pending := domain.PendingCommit{Chapter: a.Chapter, Stage: domain.CommitStageStarted,
				Rewrite: true, RewriteMode: mode, Payload: payload, DraftContent: content,
				Summary: a.Summary, HookType: a.HookType,
				DominantStrand: a.DominantStrand, StartedAt: now, UpdatedAt: now}
			if err := t.store.Signals.SavePendingCommit(pending); err != nil {
				return nil, fmt.Errorf("save rewrite pending commit: %w: %w", errs.ErrStoreWrite, err)
			}
			return t.executeRewriteCommit(a, progress, pending, false)
		}
		return t.buildSkipResult(a.Chapter, progress)
	}

	// 新提交必须通过当前阶段/返工队列校验；已有普通 PendingCommit 是恢复协议，
	// 允许跨过“Progress 已先落盘/Phase 已完成”的中断窗口继续收尾。
	if existingPending == nil {
		if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
			// 队列冲突保持原样（已带 ErrToolConflict 分类）；其他 IO 错误归 Precondition。
			if errors.Is(err, errs.ErrToolConflict) {
				return nil, err
			}
			return nil, fmt.Errorf("章节当前不允许提交: %w: %w", errs.ErrToolPrecondition, err)
		}
	}

	// 分层模式越界拦截：必须先于任何写操作，否则越界 commit 会把章节文件、摘要、
	// Progress 都改坏。boundary 复用给下方第 6b 步算弧/卷信号。
	var boundary *store.ArcBoundary
	if progress.Layered {
		b, bErr := t.store.Outline.CheckArcBoundary(a.Chapter)
		if bErr != nil {
			return nil, fmt.Errorf("弧边界检测失败 chapter=%d: %w: %w", a.Chapter, errs.ErrStoreRead, bErr)
		}
		if b == nil {
			return nil, fmt.Errorf(
				"第 %d 章不在分层大纲范围内：写作必须先 expand_arc 扩展弧或 append_volume 追加卷；若全书已完结请调 save_foundation type=complete_book: %w",
				a.Chapter, errs.ErrToolPrecondition)
		}
		boundary = b
	}

	// 1. 冻结章节正文。首次提交从草稿读取并随 PendingCommit 一起落盘；恢复时
	// 只使用该快照，避免新 Worker 在重试前覆盖 draft 后形成“旧事实 + 新正文”。
	var content string
	if existingPending != nil {
		content = existingPending.DraftContent
		if content == "" {
			return nil, fmt.Errorf("第 %d 章未完成提交缺少 draft_content，无法证明恢复正文与原提交一致: %w",
				a.Chapter, errs.ErrToolConflict)
		}
	} else {
		var loadErr error
		content, _, loadErr = t.store.Drafts.LoadChapterContent(a.Chapter)
		if loadErr != nil {
			return nil, fmt.Errorf("load chapter content: %w: %w", errs.ErrStoreRead, loadErr)
		}
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", a.Chapter, errs.ErrToolPrecondition)
	}
	wordCount := utf8.RuneCountInString(content)

	var pending domain.PendingCommit
	if existingPending != nil {
		pending = *existingPending
	} else {
		payload, err := json.Marshal(a)
		if err != nil {
			return nil, fmt.Errorf("marshal commit payload: %w", err)
		}
		now := time.Now().Format(time.RFC3339)
		pending = domain.PendingCommit{
			Chapter: a.Chapter, Stage: domain.CommitStageStarted, Payload: payload, DraftContent: content,
			Summary: a.Summary, HookType: a.HookType, DominantStrand: a.DominantStrand,
			StartedAt: now, UpdatedAt: now,
		}
		if err := t.store.Signals.SavePendingCommit(pending); err != nil {
			return nil, fmt.Errorf("save pending commit: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// StageStarted 可能表示尚未写任何工件，也可能在状态增量中途崩溃；完整载荷
	// 的所有操作都必须幂等，因此统一重放。StageStateApplied 则直接进入 Progress。
	if pending.Stage == domain.CommitStageStarted {
		// 2. 保存终稿
		if err := t.store.Drafts.SaveFinalChapter(a.Chapter, content); err != nil {
			return nil, fmt.Errorf("save final chapter: %w: %w", errs.ErrStoreWrite, err)
		}

		// 3. 保存摘要
		summary := domain.ChapterSummary{
			Chapter: a.Chapter, Summary: a.Summary, Characters: a.Characters, KeyEvents: a.KeyEvents,
		}
		if err := t.store.Summaries.SaveSummary(summary); err != nil {
			return nil, fmt.Errorf("save summary: %w: %w", errs.ErrStoreWrite, err)
		}

		// 4. 更新状态增量
		if len(a.TimelineEvents) > 0 {
			for i := range a.TimelineEvents {
				a.TimelineEvents[i].Chapter = a.Chapter
			}
			if err := t.store.World.AppendTimelineEvents(a.TimelineEvents); err != nil {
				return nil, fmt.Errorf("append timeline: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		if len(a.ForeshadowUpdates) > 0 {
			if err := t.store.World.UpdateForeshadow(a.Chapter, a.ForeshadowUpdates); err != nil {
				return nil, fmt.Errorf("update foreshadow: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		if len(a.RelationshipChanges) > 0 {
			for i := range a.RelationshipChanges {
				a.RelationshipChanges[i].Chapter = a.Chapter
			}
			if err := t.store.World.UpdateRelationships(a.RelationshipChanges); err != nil {
				return nil, fmt.Errorf("update relationships: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		if len(a.StateChanges) > 0 {
			for i := range a.StateChanges {
				a.StateChanges[i].Chapter = a.Chapter
			}
			if err := t.store.World.AppendStateChanges(a.StateChanges); err != nil {
				return nil, fmt.Errorf("append state changes: %w: %w", errs.ErrStoreWrite, err)
			}
		}

		// 4b. 累加配角名册：本章出场的非核心角色进 cast_ledger，供 novel_context 召回。
		// 失败时只 warn 不阻断 commit——名册是次要数据，可通过下一章 commit 自愈。
		if len(a.Characters) > 0 {
			coreNames, err := loadCoreCharacterNameSet(t.store)
			if err != nil {
				return nil, fmt.Errorf("load core characters for cast ledger: %w: %w", errs.ErrStoreRead, err)
			}
			if err := t.store.Cast.MergeAppearances(a.Chapter, a.Characters, a.CastIntros, coreNames); err != nil {
				slog.Warn("配角名册累加失败，跳过", "module", "commit", "chapter", a.Chapter, "err", err)
			}
		}

		pending.Stage = domain.CommitStageStateApplied
		pending.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := t.store.Signals.SavePendingCommit(pending); err != nil {
			return nil, fmt.Errorf("update pending commit stage: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// 5. 更新进度
	if !completed {
		if err := t.store.Progress.MarkChapterComplete(a.Chapter, wordCount, a.HookType, a.DominantStrand); err != nil {
			return nil, fmt.Errorf("mark chapter complete: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// 6. 判断是否需要审阅
	progress, err = t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	completedCount := 0
	if progress != nil {
		completedCount = len(progress.CompletedChapters)
	}

	// 6b. 长篇模式弧/卷信号：boundary 已在入口前置校验，Layered 时保证非 nil
	var arcEnd, volumeEnd, needsExpansion, needsNewVolume bool
	var vol, arc, nextVol, nextArc int
	if progress != nil && progress.Layered && boundary != nil {
		arcEnd = boundary.IsArcEnd
		volumeEnd = boundary.IsVolumeEnd
		vol = boundary.Volume
		arc = boundary.Arc
		needsExpansion = boundary.NeedsExpansion
		needsNewVolume = boundary.NeedsNewVolume
		nextVol = boundary.NextVolume
		nextArc = boundary.NextArc
		if err := t.store.Progress.UpdateVolumeArc(vol, arc); err != nil {
			return nil, fmt.Errorf("update volume/arc: %w: %w", errs.ErrStoreWrite, err)
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
		// (feedback 同时持久化到反馈池,见下方 persistFeedback——返回值只是镜像,
		// architect 经 novel_context 消费的是 store 事实)
		ArcEnd:         arcEnd,
		VolumeEnd:      volumeEnd,
		Volume:         vol,
		Arc:            arc,
		NeedsExpansion: needsExpansion,
		NeedsNewVolume: needsNewVolume,
		NextVolume:     nextVol,
		NextArc:        nextArc,
	}

	// 8. 完成态判定：非分层写完最后一章 / 分层最终卷最后一章 → MarkComplete
	bookComplete, err := t.applyCompletion(&result, progress)
	if err != nil {
		return nil, err
	}
	if bookComplete {
		result.BookComplete = true
	}
	latestProgress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress after completion: %w: %w", errs.ErrStoreRead, err)
	}
	if latestProgress != nil {
		result.Flow = string(latestProgress.Flow)
	}

	// 8.5 反馈池:writer 对大纲的反馈落盘,architect 下次结构操作经 novel_context
	// 消费(仅返回值会随 run 结束丢失)。附属事实 best-effort,不阻断提交。
	// 仅分层书持久化:非分层书没有结构操作,落盘只会制造永远无消费者的垃圾事实
	// (返回值镜像仍保留,诊断可见)。
	layered := progress != nil && progress.Layered
	if layered && a.Feedback != nil && (strings.TrimSpace(a.Feedback.Deviation) != "" || strings.TrimSpace(a.Feedback.Suggestion) != "") {
		if err := t.store.Outline.AppendOutlineFeedback(store.ChapterFeedback{
			Chapter: a.Chapter, Deviation: a.Feedback.Deviation, Suggestion: a.Feedback.Suggestion,
		}); err != nil {
			slog.Warn("大纲反馈落盘失败", "module", "tools", "chapter", a.Chapter, "err", err)
		}
	}

	// 机械规则是输出的一部分，必须在 ProgressMarked 前固化，恢复时直接返回同一输出。
	violations := t.checkRules(content)
	output, err := json.Marshal(commitOutput{CommitResult: result, RuleViolations: violations})
	if err != nil {
		return nil, fmt.Errorf("marshal commit output: %w", err)
	}

	pending.Stage = domain.CommitStageProgressMarked
	pending.Result = &result
	pending.Output = output
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("update pending commit result: %w: %w", errs.ErrStoreWrite, err)
	}

	// 9. 追加 checkpoint。必须先于清除 pending_commit，确保重启后可见的
	// pending_commit 总能驱动重跑补齐缺失 checkpoint。
	if err := t.appendCommitCheckpoint(a.Chapter); err != nil {
		return nil, fmt.Errorf("checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
	}
	pending.Stage = domain.CommitStageSignalSaved
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("update pending commit checkpoint stage: %w: %w", errs.ErrStoreWrite, err)
	}

	// 10. 清除进度中间状态
	if err := t.store.Progress.ClearInProgress(); err != nil {
		return nil, fmt.Errorf("clear in-progress: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Signals.ClearPendingCommit(); err != nil {
		return nil, fmt.Errorf("clear pending commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 持久化违规事实:editor 评审经 novel_context 消费(返回值只是镜像——
	// writer 在 commit 后立即硬停,返回值无人可读)。best-effort。
	if err := t.store.World.SaveRuleViolations(a.Chapter, violations); err != nil {
		slog.Warn("机械违规落盘失败", "module", "tools", "chapter", a.Chapter, "err", err)
	}
	return output, nil
}

// finishPendingCommit 收尾 ProgressMarked/SignalSaved 中断窗口。Checkpoint 追加按
// digest 幂等；只有 checkpoint 与中间态清理都成功后才删除恢复记录。
func (t *CommitChapterTool) finishPendingCommit(pending domain.PendingCommit, progress *domain.Progress) (json.RawMessage, error) {
	if pending.Stage == domain.CommitStageProgressMarked {
		if err := t.appendCommitCheckpoint(pending.Chapter); err != nil {
			return nil, fmt.Errorf("checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
		}
		pending.Stage = domain.CommitStageSignalSaved
		pending.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := t.store.Signals.SavePendingCommit(pending); err != nil {
			return nil, fmt.Errorf("update pending commit checkpoint stage: %w: %w", errs.ErrStoreWrite, err)
		}
	}
	if err := t.store.Progress.ClearInProgress(); err != nil {
		return nil, fmt.Errorf("clear in-progress: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Signals.ClearPendingCommit(); err != nil {
		return nil, fmt.Errorf("clear pending commit: %w: %w", errs.ErrStoreWrite, err)
	}
	if len(pending.Output) > 0 {
		return append(json.RawMessage(nil), pending.Output...), nil
	}
	if pending.Result != nil {
		return json.Marshal(pending.Result)
	}
	return t.buildSkipResult(pending.Chapter, progress)
}

func (t *CommitChapterTool) validateRewriteDraft(chapter int, progress *domain.Progress) (string, error) {
	content, _, err := t.store.Drafts.LoadChapterContent(chapter)
	if err != nil {
		return "", fmt.Errorf("rewrite: load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return "", fmt.Errorf("no content found for chapter %d: %w", chapter, errs.ErrToolPrecondition)
	}
	existingFinal, err := t.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return "", fmt.Errorf("rewrite: load final chapter: %w: %w", errs.ErrStoreRead, err)
	}
	if existingFinal == "" || existingFinal != content {
		return content, nil
	}
	mode := "重写"
	if progress != nil && progress.Flow == domain.FlowPolishing {
		mode = "打磨"
	}
	return "", fmt.Errorf("第 %d 章 drafts 与 chapters 内容完全相同，未检测到%s改动。请先调 draft_chapter(mode=write, chapter=%d) 写入%s后的新正文，再 commit_chapter: %w",
		chapter, mode, chapter, mode, errs.ErrToolPrecondition)
}

func (t *CommitChapterTool) appendCommitCheckpoint(chapter int) error {
	_, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(chapter), "commit",
		fmt.Sprintf("chapters/%02d.md", chapter),
	)
	return err
}

// checkRules 对章节正文做机械检查：内置产品底线 Lint（机制残留，始终执行）
// + 用户规则 Check（读本书快照的 structured；快照缺失退到内置默认，保证机械底线始终在）。
func (t *CommitChapterTool) checkRules(text string) []rules.Violation {
	violations := rules.Lint(text)
	structured := rules.SystemDefaults().Structured
	if snap, err := t.store.UserRules.Load(); err == nil && snap != nil {
		structured = snap.Structured
	}
	return append(violations, rules.Check(text, structured)...)
}

// executeRewriteCommit 处理打磨/重写章节的提交：覆盖终稿与摘要、更新字数、drain 队列。
// 跳过所有世界状态追加（timeline / foreshadow / relationship / state_changes）与弧边界检测，
// 这些已在章节原始提交时应用。
func (t *CommitChapterTool) executeRewriteCommit(a commitArgs, progress *domain.Progress, pending domain.PendingCommit, recovering bool) (json.RawMessage, error) {
	chapter := a.Chapter
	// 1. 只使用首次提交时冻结的返工正文，崩溃恢复不得采用随后被覆盖的 draft。
	content := pending.DraftContent
	if content == "" {
		return nil, fmt.Errorf("第 %d 章返工提交缺少 draft_content，无法安全恢复: %w", chapter, errs.ErrToolConflict)
	}
	wordCount := utf8.RuneCountInString(content)

	// 2. 硬校验：drafts 与现终稿完全相同 → 判定为未真正打磨/重写（writer 跳过了 draft_chapter）
	// 拒绝 commit，强制 writer 先调 draft_chapter(mode=write) 写入新版本。
	existingFinal, err := t.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return nil, fmt.Errorf("rewrite: load final chapter: %w: %w", errs.ErrStoreRead, err)
	}
	if !recovering && existingFinal != "" && existingFinal == content {
		mode := "重写"
		if progress != nil && progress.Flow == domain.FlowPolishing {
			mode = "打磨"
		}
		return nil, fmt.Errorf("第 %d 章 drafts 与 chapters 内容完全相同，未检测到%s改动。请先调 draft_chapter(mode=write, chapter=%d) 写入%s后的新正文，再 commit_chapter: %w",
			chapter, mode, chapter, mode, errs.ErrToolPrecondition)
	}

	if pending.Stage == domain.CommitStageStarted {
		// 3. 覆盖终稿与摘要；两者都是同载荷覆盖写，崩溃重放幂等。
		if err := t.store.Drafts.SaveFinalChapter(chapter, content); err != nil {
			return nil, fmt.Errorf("rewrite: save final chapter: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Summaries.SaveSummary(domain.ChapterSummary{
			Chapter: chapter, Summary: a.Summary, Characters: a.Characters, KeyEvents: a.KeyEvents,
		}); err != nil {
			return nil, fmt.Errorf("rewrite: save summary: %w: %w", errs.ErrStoreWrite, err)
		}
		pending.Stage = domain.CommitStageStateApplied
		pending.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := t.store.Signals.SavePendingCommit(pending); err != nil {
			return nil, fmt.Errorf("rewrite: update pending state stage: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// 4. 更新字数（MarkChapterComplete 对已完成章节是幂等的：replaces word count, slice.Contains 防止重复入队）
	if progress.Phase != domain.PhaseComplete {
		if err := t.store.Progress.MarkChapterComplete(chapter, wordCount, a.HookType, a.DominantStrand); err != nil {
			return nil, fmt.Errorf("rewrite: update word count: %w: %w", errs.ErrStoreWrite, err)
		}

		// 5. Drain 待处理队列；队列空时 CompleteRewrite 会自动把 flow 切回 writing
		if err := t.store.Progress.CompleteRewrite(chapter); err != nil {
			return nil, fmt.Errorf("rewrite: complete rewrite: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// 6. 读取 drain 后的 Progress 快照，作为事实返回
	mode := pending.RewriteMode
	if mode == "" {
		mode = "rewrite"
	}
	latest, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("rewrite: load progress after drain: %w: %w", errs.ErrStoreRead, err)
	}
	remaining := []int{}
	nextChapter := chapter + 1
	flow := string(domain.FlowWriting)
	if latest != nil {
		remaining = append(remaining, latest.PendingRewrites...)
		nextChapter = latest.NextChapter()
		flow = string(latest.Flow)
	}
	drained := len(remaining) == 0

	// 队列清空后再判完结：返工提交不经过主路径 applyCompletion，完结只能在此触发。
	//   - 分层 + 正向写作：layeredComplete 总判定（收官卷结构写完 / 未宣告走质量级）。
	//   - 分层 + reopen 返工（ReopenedFromComplete）：返工只改已有章、不增减结构，按结构完整
	//     即重新完结——若因返工扰动了某条线索就卡在 writing，终卷末会落到越界续写死循环。
	//   - 非分层：写满 TotalChapters 即完结（返工不增减章数，原本就满）。
	bookComplete := false
	if drained && latest != nil {
		reComplete := false
		switch {
		case latest.Layered && latest.ReopenedFromComplete:
			reComplete, err = layeredStructurallyComplete(t.store, latest)
		case latest.Layered:
			reComplete, err = layeredComplete(t.store, latest)
		default:
			reComplete = latest.TotalChapters > 0 && len(latest.CompletedChapters) >= latest.TotalChapters
		}
		if err != nil {
			return nil, fmt.Errorf("rewrite: evaluate completion: %w: %w", errs.ErrStoreRead, err)
		}
		if reComplete {
			if err := t.store.Progress.MarkComplete(); err != nil {
				return nil, fmt.Errorf("rewrite: mark complete: %w: %w", errs.ErrStoreWrite, err)
			}
			bookComplete = true
			p, err := t.store.Progress.Load()
			if err != nil {
				return nil, fmt.Errorf("rewrite: reload completed progress: %w: %w", errs.ErrStoreRead, err)
			}
			if p != nil {
				flow = string(p.Flow)
			}
		}
	}

	// 同主路径：rewrite/polish 也做机械检查并持久化(重写后落新记录,旧违规视为已清)
	violations := t.checkRules(content)
	output, err := json.Marshal(map[string]any{
		"chapter": chapter, "rewritten": true, "mode": mode, "word_count": wordCount,
		"remaining_queue": remaining, "queue_drained": drained, "next_chapter": nextChapter,
		"flow": flow, "book_complete": bookComplete, "rule_violations": violations,
	})
	if err != nil {
		return nil, fmt.Errorf("rewrite: marshal output: %w", err)
	}
	pending.Stage = domain.CommitStageProgressMarked
	pending.Output = output
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("rewrite: update pending progress stage: %w: %w", errs.ErrStoreWrite, err)
	}

	// 7. Checkpoint 后再标 signal_saved，最后清理 PendingCommit。
	if err := t.appendCommitCheckpoint(chapter); err != nil {
		return nil, fmt.Errorf("rewrite: checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
	}
	pending.Stage = domain.CommitStageSignalSaved
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("rewrite: update pending checkpoint stage: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Progress.ClearInProgress(); err != nil {
		return nil, fmt.Errorf("rewrite: clear in-progress: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Signals.ClearPendingCommit(); err != nil {
		return nil, fmt.Errorf("rewrite: clear pending commit: %w: %w", errs.ErrStoreWrite, err)
	}

	if err := t.store.World.SaveRuleViolations(chapter, violations); err != nil {
		slog.Warn("机械违规落盘失败", "module", "tools", "chapter", chapter, "err", err)
	}
	return output, nil
}

// buildSkipResult 为"章节已完成的重复提交"构造与正常 commit 对齐的事实返回。
// 协调者据此做后续决策（writer/editor/architect 派发），而不会因为拿到 prose 提示而幻觉。
func (t *CommitChapterTool) buildSkipResult(chapter int, progress *domain.Progress) (json.RawMessage, error) {
	_, wordCount, err := t.store.Drafts.LoadChapterContent(chapter)
	if err != nil {
		return nil, fmt.Errorf("load completed chapter: %w: %w", errs.ErrStoreRead, err)
	}

	result := domain.CommitResult{
		Chapter:     chapter,
		Committed:   true,
		WordCount:   wordCount,
		NextChapter: chapter + 1,
	}

	if progress != nil && progress.Layered {
		boundary, err := t.store.Outline.CheckArcBoundary(chapter)
		if err != nil {
			return nil, fmt.Errorf("check completed chapter boundary: %w: %w", errs.ErrStoreRead, err)
		}
		if boundary != nil {
			result.ArcEnd = boundary.IsArcEnd
			result.VolumeEnd = boundary.IsVolumeEnd
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

// loadCoreCharacterNameSet 加载 characters.json 中已有的角色名集合（含别名）。
// 用作 cast_ledger 的"已知核心"过滤集——核心角色不进次要名册。
// 加载失败时返回 nil（merge 时所有 characters 都进 ledger，可接受）。
func loadCoreCharacterNameSet(s *store.Store) (map[string]bool, error) {
	chars, err := s.Characters.Load()
	if err != nil {
		return nil, err
	}
	if len(chars) == 0 {
		return nil, nil
	}
	set := make(map[string]bool, len(chars)*2)
	for _, c := range chars {
		if c.Name != "" {
			set[c.Name] = true
		}
		for _, alias := range c.Aliases {
			if alias != "" {
				set[alias] = true
			}
		}
	}
	return set, nil
}

// applyCompletion 判断本次 commit 是否使全书完结，若是则 MarkComplete 并返回 true。
//   - 非分层：写完约定总章数即完结。
//   - 分层：架构师显式 save_foundation type=complete_book 是主路径；这里再加一道
//     确定性兜底（见 layeredComplete）——防止模型在终点既不 append_volume 也不
//     complete_book，导致"写手裸跑越界章节 → 越界守卫拦截 → 反复重试"的 livelock
//     （《凡骨》ch204..347 案例的根因）。
func (t *CommitChapterTool) applyCompletion(result *domain.CommitResult, progress *domain.Progress) (bool, error) {
	if progress == nil {
		return false, nil
	}
	if progress.Phase == domain.PhaseComplete {
		return true, nil
	}
	if progress.Layered {
		complete, err := layeredComplete(t.store, progress)
		if err != nil {
			return false, fmt.Errorf("evaluate layered completion: %w: %w", errs.ErrStoreRead, err)
		}
		if complete {
			if err := t.store.Progress.MarkComplete(); err != nil {
				return false, fmt.Errorf("mark book complete: %w: %w", errs.ErrStoreWrite, err)
			}
			return true, nil
		}
		return false, nil
	}
	if progress.TotalChapters > 0 && result.NextChapter > progress.TotalChapters {
		if err := t.store.Progress.MarkComplete(); err != nil {
			return false, fmt.Errorf("mark book complete: %w: %w", errs.ErrStoreWrite, err)
		}
		return true, nil
	}
	return false, nil
}

// ── 分层完结判定（包级：commit_chapter 与 save_volume_summary 两个触发点共用）──
//
// 完结检查永远发生在"最后一块事实落地"的工具里：
//   - 未宣告收官：末章 commit（layeredBookComplete 质量级）
//   - 已宣告收官：正向主路径的最后一块拼图是卷末收尾三连（评审→弧摘要→卷摘要），
//     故触发点在 save_volume_summary；返工 drain 后三连已齐时由 commit 触发。

// layeredStructurallyComplete 判定分层长篇是否"结构上写完"：返工队列空 + 无骨架弧待展开
// + 所有已展开章节都已写。这是确定性的终态事实，不含伏笔/长线等语义判断——用作"防终态
// 死循环"的安全网（返工排空后据此重新完结）。
func layeredStructurallyComplete(st *store.Store, progress *domain.Progress) (bool, error) {
	// 1. 返工队列必须清空
	if len(progress.PendingRewrites) > 0 {
		return false, nil
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return false, fmt.Errorf("load layered outline: %w", err)
	}
	if len(volumes) == 0 {
		return false, nil
	}
	// 2. 不能还有骨架弧待展开（计划内仍有内容要写）
	for i := range volumes {
		for j := range volumes[i].Arcs {
			if !volumes[i].Arcs[j].IsExpanded() {
				return false, nil
			}
		}
	}
	// 3. 已展开章节必须全部写完
	expanded := len(domain.FlattenOutline(volumes))
	return expanded > 0 && len(progress.CompletedChapters) >= expanded, nil
}

// finaleWrapped 收官卷的卷末收尾三连（弧评审/弧摘要/卷摘要）是否齐备。
// 收官完结不要求伏笔/长线归零，但必须等末弧过完编辑质量闸——结局是全书最要紧的部分，
// 完结不能抢在 editor 评审（可能入队返工）与摘要落盘之前。
func finaleWrapped(st *store.Store, progress *domain.Progress) (bool, error) {
	last := progress.LatestCompleted()
	if last <= 0 {
		return false, nil
	}
	b, err := st.Outline.CheckArcBoundary(last)
	if err != nil {
		return false, fmt.Errorf("check finale boundary: %w", err)
	}
	if b == nil || !b.IsArcEnd {
		return false, nil
	}
	hasReview, err := st.World.HasArcReview(last)
	if err != nil {
		return false, fmt.Errorf("load finale review: %w", err)
	}
	hasArcSummary, err := st.Summaries.HasArcSummary(b.Volume, b.Arc)
	if err != nil {
		return false, fmt.Errorf("load finale arc summary: %w", err)
	}
	hasVolumeSummary, err := st.Summaries.HasVolumeSummary(b.Volume)
	if err != nil {
		return false, fmt.Errorf("load finale volume summary: %w", err)
	}
	return hasReview && hasArcSummary && hasVolumeSummary, nil
}

// layeredComplete 分层正向写作的完结总判定：
//   - 已宣告收官卷（layered_outline 最后一卷带 final）→ 结构写完 + 卷末收尾三连齐备
//     即完结，不再要求伏笔/长线归零。收官卷整卷以收线为目标（架构师规划时已把长线/
//     伏笔分配进各弧），个别遗漏属编辑质量问题，不该把全书卡在终态之外——否则
//     estimated_scale 高估的书永远无法合法完本（140 章 stop guard 熔断案例的根因侧）。
//   - 未宣告 → 质量级 layeredBookComplete，防模型既不收官也不完本时在大纲耗尽处
//     过早收尾。
func layeredComplete(st *store.Store, progress *domain.Progress) (bool, error) {
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return false, fmt.Errorf("load layered outline: %w", err)
	}
	if domain.FinaleVolume(volumes) > 0 {
		structurallyComplete, err := layeredStructurallyComplete(st, progress)
		if err != nil || !structurallyComplete {
			return structurallyComplete, err
		}
		return finaleWrapped(st, progress)
	}
	return layeredBookComplete(st, progress)
}

// layeredBookComplete 用客观事实判断分层长篇是否真正写完，对照 architect-long.md 完结判定
// 清单里可量化的几项 + 结构性事实。结构完整之上再要求伏笔归零、长线收束——任一不满足都
// 让位给架构师继续 expand_arc / append_volume，绝不抢在故事没写完时收尾。无 compass 时保守
// 判为未完结。这是未宣告收官卷时的"质量级"完结判定，比 layeredStructurallyComplete 更严。
func layeredBookComplete(st *store.Store, progress *domain.Progress) (bool, error) {
	structurallyComplete, err := layeredStructurallyComplete(st, progress)
	if err != nil || !structurallyComplete {
		return structurallyComplete, err
	}
	// 4. 活跃伏笔必须归零（承诺已兑现）
	active, err := st.World.LoadActiveForeshadow()
	if err != nil {
		return false, fmt.Errorf("load active foreshadow: %w", err)
	}
	if len(active) > 0 {
		return false, nil
	}
	// 5. 指南针活跃长线必须收束（无 compass / 长线未清都交回架构师裁定）
	compass, err := st.Outline.LoadCompass()
	if err != nil {
		return false, fmt.Errorf("load compass: %w", err)
	}
	if compass == nil || len(compass.OpenThreads) > 0 {
		return false, nil
	}
	return true, nil
}
