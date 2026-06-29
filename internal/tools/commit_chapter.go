package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CommitChapterTool NộpChương：加载Chính văn → Lưu终稿 → 生成Tóm tắt → 更MớiTrạng thái → 更MớiTiến độ。
type CommitChapterTool struct {
	store     *store.Store
	rulesOpts rules.LoadOptions // 可选；Rỗng LoadOptions 时不产生 rule_violations
}

func NewCommitChapterTool(store *store.Store) *CommitChapterTool {
	return &CommitChapterTool{store: store}
}

// WithRules 注入用户规则加载选项，使 rule_violations 中附带用户规则Kiểm traKết quả。
// 不调用此方法时仅执行内置底线 Lint（机制残留Kiểm tra，始终开启）。
func (t *CommitChapterTool) WithRules(opts rules.LoadOptions) *CommitChapterTool {
	t.rulesOpts = opts
	return t
}

// commitOutput 在 domain.CommitResult 之上嵌入扩展字段，保持 domain 包不依赖 rules。
// 由于嵌入字段会被 JSON marshaler 提升（promoted），序列化Kết quả等同于扁平结构。
type commitOutput struct {
	domain.CommitResult
	RuleViolations []rules.Violation `json:"rule_violations,omitempty"`
}

func (t *CommitChapterTool) Name() string { return "commit_chapter" }
func (t *CommitChapterTool) Description() string {
	return "NộpChương终稿。加载Bản nhápChính vănLưu为终稿，更Mới时间线、伏笔、关系、角色Trạng thái和Tiến độ。" +
		"Quay lại结构化事实：next_chapter / review_required / arc_end / volume_end / needs_expansion / book_complete / flow 等"
}
func (t *CommitChapterTool) Label() string { return "NộpChương" }

// 写工具（跨域原子操作：Bản nháp→终稿→Tóm tắt→Tiến độ→checkpoint），禁止并发。
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
		schema.Property("relation", schema.String("Hiện tại关系描述")).Required(),
	)
	stateChangeSchema := schema.Object(
		schema.Property("entity", schema.String("角色名或实体名")).Required(),
		schema.Property("field", schema.String("变化属性")).Required(),
		schema.Property("old_value", schema.String("变化前的值")),
		schema.Property("new_value", schema.String("变化后的值")).Required(),
		schema.Property("reason", schema.String("变化原因")),
	)
	feedbackSchema := schema.Object(
		schema.Property("deviation", schema.String("偏离Đại cương的描述")).Required(),
		schema.Property("suggestion", schema.String("对后续Đại cương的调整建议")).Required(),
	)
	feedbackSchema["description"] = "对后续大纲的建议对象；必须直接传 JSON object，不要传字符串化 JSON"
	return schema.Object(
		schema.Property("chapter", schema.Int("Chương号")).Required(),
		schema.Property("summary", schema.String("本章内容Tóm tắt（200字以内）")).Required(),
		schema.Property("characters", schema.Array("本章出场角色名", schema.String(""))).Required(),
		schema.Property("key_events", schema.Array("本章Sự kiện trọng tâm", schema.String(""))).Required(),
		schema.Property("timeline_events", schema.Array("本章Sự kiện thời gian", timelineSchema)),
		schema.Property("foreshadow_updates", schema.Array("伏笔操作", foreshadowSchema)),
		schema.Property("relationship_changes", schema.Array("Thay đổi quan hệ", relationshipSchema)),
		schema.Property("state_changes", schema.Array("角色/实体Thay đổi trạng thái", stateChangeSchema)),
		schema.Property("cast_intros", schema.Array("本章首次引入且后续可能再出现的次要角色简介（不含主角及 characters.json 已有角色）", schema.Object(
			schema.Property("name", schema.String("角色名")).Required(),
			schema.Property("brief_role", schema.String("一句话定位（如：客栈老板/赌坊打手）")).Required(),
		))),
		schema.Property("hook_type", schema.Enum("章末Loại móc", "crisis", "mystery", "desire", "emotion", "choice")),
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
		CastIntros          []domain.CastIntro         `json:"cast_intros"`
		HookType            string                     `json:"hook_type"`
		DominantStrand      string                     `json:"dominant_strand"`
		Feedback            *domain.OutlineFeedback    `json:"feedback"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if t.store.Progress.IsChapterCompleted(a.Chapter) {
		// 清理可能残留的 PendingCommit（崩溃发生在 ProgressMarked 之后、ClearPendingCommit 之前）
		if pending, _ := t.store.Signals.LoadPendingCommit(); pending != nil && pending.Chapter == a.Chapter {
			_ = t.store.Signals.ClearPendingCommit()
		}
		// 打磨/重写Đường dẫn：Chương虽Đã hoàn thành，但仍在 pending_rewrites 中，允许覆盖并 drain 队列
		progress, _ := t.store.Progress.Load()
		if progress != nil && slices.Contains(progress.PendingRewrites, a.Chapter) {
			return t.executeRewriteCommit(a.Chapter, a.Summary, a.Characters, a.KeyEvents,
				a.HookType, a.DominantStrand, progress)
		}
		return t.buildSkipResult(a.Chapter, progress)
	}
	existingPending, err := t.store.Signals.LoadPendingCommit()
	if err != nil {
		return nil, fmt.Errorf("load pending commit: %w: %w", errs.ErrStoreRead, err)
	}
	if existingPending != nil && existingPending.Chapter != a.Chapter {
		return nil, fmt.Errorf("存在未Phục hồi的ChươngNộp：第 %d 章（阶段 %s），Vui lòng先Phục hồi或重MớiNộp该章: %w", existingPending.Chapter, existingPending.Stage, errs.ErrToolConflict)
	}
	if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
		// 队列冲突保持原样（已带 ErrToolConflict 分类）；Khác IO Lỗi归 Precondition。
		if errors.Is(err, errs.ErrToolConflict) {
			return nil, err
		}
		return nil, fmt.Errorf("ChươngHiện tạiKhông允 phépNộp: %w: %w", errs.ErrToolPrecondition, err)
	}

	// 分层模式越界拦截：必须先于任何写操作，否则越界 commit 会把ChươngTập tin、Tóm tắt、
	// Progress 都改坏。boundary 复用给下方第 6b 步算弧/卷信号。
	var boundary *store.ArcBoundary
	if progress, perr := t.store.Progress.Load(); perr == nil && progress != nil && progress.Layered {
		b, bErr := t.store.Outline.CheckArcBoundary(a.Chapter)
		if bErr != nil {
			return nil, fmt.Errorf("弧边界检测Thất bại chapter=%d: %w: %w", a.Chapter, errs.ErrStoreRead, bErr)
		}
		if b == nil {
			return nil, fmt.Errorf(
				"第 %d 章不在分层Đại cương范围内：Viết必须先 expand_arc 扩展弧或 append_volume 追加卷；若全书已完结Vui lòng调 save_foundation type=complete_book: %w",
				a.Chapter, errs.ErrToolPrecondition)
		}
		boundary = b
	}

	// 1. 加载ChươngChính văn
	content, wordCount, err := t.store.Drafts.LoadChapterContent(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", a.Chapter, errs.ErrToolPrecondition)
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
		return nil, fmt.Errorf("save pending commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 2. Lưu终稿
	if err := t.store.Drafts.SaveFinalChapter(a.Chapter, content); err != nil {
		return nil, fmt.Errorf("save final chapter: %w: %w", errs.ErrStoreWrite, err)
	}

	// 3. LưuTóm tắt
	summary := domain.ChapterSummary{
		Chapter:    a.Chapter,
		Summary:    a.Summary,
		Characters: a.Characters,
		KeyEvents:  a.KeyEvents,
	}
	if err := t.store.Summaries.SaveSummary(summary); err != nil {
		return nil, fmt.Errorf("save summary: %w: %w", errs.ErrStoreWrite, err)
	}

	// 4. 更MớiTrạng thái增量
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
	// Thất bại时只 warn 不阻断 commit——名册是次要数据，可通过下一章 commit 自愈。
	if len(a.Characters) > 0 {
		coreNames := loadCoreCharacterNameSet(t.store)
		if err := t.store.Cast.MergeAppearances(a.Chapter, a.Characters, a.CastIntros, coreNames); err != nil {
			slog.Warn("配角名册累加Thất bại，Bỏ qua", "module", "commit", "chapter", a.Chapter, "err", err)
		}
	}

	pending.Stage = domain.CommitStageStateApplied
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("update pending commit stage: %w: %w", errs.ErrStoreWrite, err)
	}

	// 5. 更MớiTiến độ
	if err := t.store.Progress.MarkChapterComplete(a.Chapter, wordCount, a.HookType, a.DominantStrand); err != nil {
		return nil, fmt.Errorf("mark chapter complete: %w: %w", errs.ErrStoreWrite, err)
	}

	// 6. 判断Có czy khôngCần审阅
	progress, err := t.store.Progress.Load()
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
		_ = t.store.Progress.UpdateVolumeArc(vol, arc)
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

	// 8. Hoàn thành态判定：非分层写完最后一章 / 分层最终卷最后一章 → MarkComplete
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
		return nil, fmt.Errorf("update pending commit result: %w: %w", errs.ErrStoreWrite, err)
	}

	// 9. 清除Tiến độ中间Trạng thái
	if err := t.store.Progress.ClearInProgress(); err != nil {
		return nil, fmt.Errorf("clear in-progress: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Signals.ClearPendingCommit(); err != nil {
		return nil, fmt.Errorf("clear pending commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 10. 追加 checkpoint
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(a.Chapter), "commit",
		fmt.Sprintf("chapters/%02d.md", a.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 11. 机械规则Kiểm tra（仅返事实，不阻断）
	violations := t.checkRules(content, wordCount)
	return json.Marshal(commitOutput{CommitResult: result, RuleViolations: violations})
}

// checkRules 对ChươngChính văn做机械Kiểm tra：内置产品底线 Lint（机制残留，始终执行）
// + 用户规则 Check（rulesOpts 全Rỗng时 loader Quay lạiRỗng layers，checker 返 nil）。
func (t *CommitChapterTool) checkRules(text string, wordCount int) []rules.Violation {
	violations := rules.Lint(text)
	bundle := rules.Merge(rules.Load(t.rulesOpts))
	return append(violations, rules.Check(text, wordCount, bundle.Structured)...)
}

// executeRewriteCommit 处理打磨/Viết lại chương的Nộp：覆盖终稿与Tóm tắt、更Mới字数、drain 队列。
// Bỏ qua所有世界Trạng thái追加（timeline / foreshadow / relationship / state_changes）与弧边界检测，
// 这些已在Chương原始Nộp时Áp dụng。
func (t *CommitChapterTool) executeRewriteCommit(
	chapter int,
	summary string,
	characters, keyEvents []string,
	hookType, dominantStrand string,
	progress *domain.Progress,
) (json.RawMessage, error) {
	// 1. 加载打磨后的Chính văn
	content, wordCount, err := t.store.Drafts.LoadChapterContent(chapter)
	if err != nil {
		return nil, fmt.Errorf("rewrite: load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", chapter, errs.ErrToolPrecondition)
	}

	// 2. 硬校验：drafts 与现终稿完全相同 → 判定为未真正打磨/重写（writer Bỏ qua了 draft_chapter）
	// 拒绝 commit，强制 writer 先调 draft_chapter(mode=write) 写入MớiPhiên bản。
	existingFinal, _ := t.store.Drafts.LoadChapterText(chapter)
	if existingFinal != "" && existingFinal == content {
		mode := "重写"
		if progress != nil && progress.Flow == domain.FlowPolishing {
			mode = "打磨"
		}
		return nil, fmt.Errorf("第 %d 章 drafts 与 chapters 内容完全相同，未Phát hiện%s改动。Vui lòng先调 draft_chapter(mode=write, chapter=%d) 写入%s后的MớiChính văn，再 commit_chapter: %w",
			chapter, mode, chapter, mode, errs.ErrToolPrecondition)
	}

	// 3. 覆盖终稿
	if err := t.store.Drafts.SaveFinalChapter(chapter, content); err != nil {
		return nil, fmt.Errorf("rewrite: save final chapter: %w: %w", errs.ErrStoreWrite, err)
	}

	// 3. 覆盖Tóm tắt
	if err := t.store.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:    chapter,
		Summary:    summary,
		Characters: characters,
		KeyEvents:  keyEvents,
	}); err != nil {
		return nil, fmt.Errorf("rewrite: save summary: %w: %w", errs.ErrStoreWrite, err)
	}

	// 4. 更Mới字数（MarkChapterComplete 对Đã hoàn thànhChương是幂等的：replaces word count, slice.Contains 防止重复入队）
	if err := t.store.Progress.MarkChapterComplete(chapter, wordCount, hookType, dominantStrand); err != nil {
		return nil, fmt.Errorf("rewrite: update word count: %w: %w", errs.ErrStoreWrite, err)
	}

	// 5. Drain Chờ xử lý队列；队列Rỗng时 CompleteRewrite 会自动把 flow 切回 writing
	if err := t.store.Progress.CompleteRewrite(chapter); err != nil {
		return nil, fmt.Errorf("rewrite: complete rewrite: %w: %w", errs.ErrStoreWrite, err)
	}

	// 6. Checkpoint
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(chapter), "commit",
		fmt.Sprintf("chapters/%02d.md", chapter),
	); err != nil {
		return nil, fmt.Errorf("rewrite: checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 7. Đọc drain 后的 Progress Chụp，作为事实Quay lại
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

	// 队列清Rỗng后再判完结：返工Nộp不经过主Đường dẫn applyCompletion，完结只能在此触发。
	//   - 分层 + 正向Viết：用质量级 layeredBookComplete（要求线索收束），未满足让位架构师。
	//   - 分层 + reopen 返工（ReopenedFromComplete）：返工只改已有章、不增减结构，按结构完整
	//     即重Mới完结——若因返工扰动了某条线索就卡在 writing，终卷末会落到越界续写死循环。
	//   - 非分层：写满 TotalChapters 即完结（返工不增减章数，原本就满）。
	bookComplete := false
	if drained && latest != nil {
		reComplete := false
		switch {
		case latest.Layered && latest.ReopenedFromComplete:
			reComplete = t.layeredStructurallyComplete(latest)
		case latest.Layered:
			reComplete = t.layeredBookComplete(latest)
		default:
			reComplete = latest.TotalChapters > 0 && len(latest.CompletedChapters) >= latest.TotalChapters
		}
		if reComplete {
			if cerr := t.store.Progress.MarkComplete(); cerr == nil {
				bookComplete = true
				if p, _ := t.store.Progress.Load(); p != nil {
					flow = string(p.Flow)
				}
			}
		}
	}

	// 同主Đường dẫn：rewrite/polish 也做机械Kiểm tra并附 rule_violations
	violations := t.checkRules(content, wordCount)
	return json.Marshal(map[string]any{
		"chapter":         chapter,
		"rewritten":       true,
		"mode":            mode,
		"word_count":      wordCount,
		"remaining_queue": remaining,
		"queue_drained":   drained,
		"next_chapter":    nextChapter,
		"flow":            flow,
		"book_complete":   bookComplete,
		"rule_violations": violations,
	})
}

// buildSkipResult 为"ChươngĐã hoàn thành的重复Nộp"构造与正常 commit 对齐的事实Quay lại。
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
// 加载Thất bại时Quay lại nil（merge 时所有 characters 都进 ledger，可接受）。
func loadCoreCharacterNameSet(s *store.Store) map[string]bool {
	chars, err := s.Characters.Load()
	if err != nil || len(chars) == 0 {
		return nil
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
	return set
}

// applyCompletion 判断本次 commit Có czy không使全书完结，若是则 MarkComplete 并Quay lại true。
//   - 非分层：写完约定总章数即完结。
//   - 分层：架构师显式 save_foundation type=complete_book 是主Đường dẫn；这里再加一道
//     确定性兜底——当全书已客观满足完结条件（见 layeredBookComplete）时自动收尾。
//     防止Mô hình在终点既不 append_volume 也不 complete_book，导致"写手裸跑越界Chương →
//     越界守卫拦截 → 反复Thử lại"的 livelock（《凡骨》ch204..347 案例的根因）。
func (t *CommitChapterTool) applyCompletion(result *domain.CommitResult, progress *domain.Progress) bool {
	if progress == nil {
		return false
	}
	if progress.Layered {
		if t.layeredBookComplete(progress) {
			_ = t.store.Progress.MarkComplete()
			return true
		}
		return false
	}
	if progress.TotalChapters > 0 && result.NextChapter > progress.TotalChapters {
		_ = t.store.Progress.MarkComplete()
		return true
	}
	return false
}

// layeredStructurallyComplete 判定分层长篇Có czy không"结构上写完"：返工队列Rỗng + Không có骨架弧待Mở rộng
// + 所有已Mở rộngChương都已写。这是确定性的终态事实，不含伏笔/长线等语义判断——用作"防终态
// 死循环"的安全网（返工排Rỗng后据此重Mới完结）。
func (t *CommitChapterTool) layeredStructurallyComplete(progress *domain.Progress) bool {
	// 1. 返工队列必须清Rỗng
	if len(progress.PendingRewrites) > 0 {
		return false
	}
	volumes, err := t.store.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return false
	}
	// 2. 不能还有骨架弧待Mở rộng（计划内仍有内容要写）
	for i := range volumes {
		for j := range volumes[i].Arcs {
			if !volumes[i].Arcs[j].IsExpanded() {
				return false
			}
		}
	}
	// 3. 已Mở rộngChương必须Tất cả写完
	expanded := len(domain.FlattenOutline(volumes))
	return expanded > 0 && len(progress.CompletedChapters) >= expanded
}

// layeredBookComplete 用客观事实判断分层长篇Có czy không真正写完，对照 architect-long.md 完结判定
// 清单里可量化的几项 + 结构性事实。结构完整之上再要求伏笔归零、长线收束——任一不满足都
// 让位给架构师Tiếp tục expand_arc / append_volume，绝不抢在故事没写完时收尾。Không có compass 时保守
// 判为未完结。这是正向Viết的"质量级"完结判定，比 layeredStructurallyComplete 更严。
func (t *CommitChapterTool) layeredBookComplete(progress *domain.Progress) bool {
	if !t.layeredStructurallyComplete(progress) {
		return false
	}
	// 4. 活跃伏笔必须归零（承诺已兑现）
	if active, aerr := t.store.World.LoadActiveForeshadow(); aerr != nil || len(active) > 0 {
		return false
	}
	// 5. 指南针活跃长线必须收束（Không có compass / 长线未清都交回架构师裁定）
	compass, cerr := t.store.Outline.LoadCompass()
	if cerr != nil || compass == nil || len(compass.OpenThreads) > 0 {
		return false
	}
	return true
}
