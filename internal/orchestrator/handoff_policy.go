package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func shouldUseHandoff(progress *domain.Progress) bool {
	if progress == nil {
		return false
	}
	policy := domain.NewChapterMemoryPolicy(progress, domain.NewContextProfile(progress.TotalChapters), false)
	return policy.HandoffPreferred
}

func saveHandoffSnapshot(store *storepkg.Store, reason string) error {
	pack, err := buildHandoffPack(store, reason)
	if err != nil || pack == nil {
		return err
	}
	return store.SaveHandoffPack(*pack)
}

func buildHandoffPack(store *storepkg.Store, reason string) (*domain.HandoffPack, error) {
	if store == nil {
		return nil, nil
	}
	progress, err := store.LoadProgress()
	if err != nil || progress == nil {
		return nil, err
	}
	runMeta, err := store.LoadRunMeta()
	if err != nil {
		return nil, err
	}

	pack := &domain.HandoffPack{
		Reason:         reason,
		UpdatedAt:      time.Now().Format(time.RFC3339),
		NovelName:      progress.NovelName,
		Phase:          string(progress.Phase),
		Flow:           string(progress.Flow),
		NextChapter:    progress.NextChapter(),
		CompletedCount: len(progress.CompletedChapters),
		TotalChapters:  progress.TotalChapters,
		TotalWordCount: progress.TotalWordCount,
	}
	if runMeta != nil {
		pack.PlanningTier = string(runMeta.PlanningTier)
		pack.PendingSteer = runMeta.PendingSteer
	}
	policy := domain.NewChapterMemoryPolicy(progress, domain.NewContextProfile(progress.TotalChapters), false)
	pack.MemoryPolicy = &policy
	if len(progress.PendingRewrites) > 0 {
		pack.PendingRewrites = append([]int(nil), progress.PendingRewrites...)
		pack.RewriteReason = progress.RewriteReason
	}
	if len(progress.CompletedChapters) > 0 {
		lastCh := progress.CompletedChapters[len(progress.CompletedChapters)-1]
		wordCount := progress.ChapterWordCounts[lastCh]
		pack.LastCommit = fmt.Sprintf("第%d章已完成，约%d字。下一章=%d。", lastCh, wordCount, progress.NextChapter())
		if review, reviewErr := store.LoadLastReview(lastCh); reviewErr == nil && review != nil {
			pack.LastReview = fmt.Sprintf("最近审阅 verdict=%s，summary=%s", review.Verdict, truncateLog(review.Summary, 80))
		}
	}
	if summaries, err := store.LoadRecentSummaries(progress.NextChapter(), 3); err == nil {
		for _, summary := range summaries {
			pack.RecentSummaries = append(pack.RecentSummaries,
				fmt.Sprintf("第%d章：%s", summary.Chapter, truncateLog(summary.Summary, 80)))
		}
	}
	pack.Guidance = buildHandoffGuidance(progress, runMeta)
	return pack, nil
}

func buildHandoffGuidance(progress *domain.Progress, runMeta *domain.RunMeta) []string {
	var guidance []string
	guidance = append(guidance, "优先依赖 handoff pack、chapter contract、recent summaries 和 review，不要假设你还记得旧对话。")
	policy := domain.NewChapterMemoryPolicy(progress, domain.NewContextProfile(totalChapters(progress)), false)
	if progress != nil {
		if progress.Flow == domain.FlowReviewing {
			guidance = append(guidance, "当前处于审阅流程，先完成 editor 相关动作，再决定是否继续写新章。")
		}
		if progress.Flow == domain.FlowRewriting || progress.Flow == domain.FlowPolishing {
			guidance = append(guidance, "当前存在返工流程，先处理 pending rewrites，再继续推进新章节。")
		}
		if progress.Layered {
			guidance = append(guidance, "这是分层长篇，请优先参考卷/弧位置与结构化摘要维持连续性。")
		}
	}
	if policy.HandoffPreferred {
		guidance = append(guidance, fmt.Sprintf("当前记忆策略建议优先依赖结构化交接工件；连续只读探索阈值=%d。", policy.ReadOnlyThreshold))
	}
	if runMeta != nil && runMeta.PendingSteer != "" {
		guidance = append(guidance, "存在待处理用户干预，必须先评估其影响范围。")
	}
	return guidance
}

func applyHandoffToRecovery(store *storepkg.Store, recovery recoveryResult) recoveryResult {
	if store == nil || recovery.IsNew {
		return recovery
	}
	progress, _ := store.LoadProgress()
	if !shouldUseHandoff(progress) {
		return recovery
	}
	pack, err := store.LoadHandoffPack()
	if err != nil || pack == nil {
		pack, _ = buildHandoffPack(store, "recovery")
	}
	if pack == nil {
		return recovery
	}
	if pack.MemoryPolicy == nil {
		policy := domain.NewChapterMemoryPolicy(progress, domain.NewContextProfile(totalChapters(progress)), false)
		pack.MemoryPolicy = &policy
	}
	body := renderHandoffPack(*pack)
	if body == "" {
		return recovery
	}
	recovery.PromptText = body + "\n\n" + recovery.PromptText
	if recovery.Label != "" {
		recovery.Label += " + Handoff"
	} else {
		recovery.Label = "Handoff 恢复"
	}
	return recovery
}

func renderHandoffPack(pack domain.HandoffPack) string {
	var lines []string
	lines = append(lines, "[系统-Handoff] 以下内容来自结构化工件，请优先依赖这些信息继续工作，不要假设你还记得旧对话。")
	if pack.NovelName != "" {
		lines = append(lines, "作品："+pack.NovelName)
	}
	if pack.Phase != "" || pack.Flow != "" {
		lines = append(lines, fmt.Sprintf("阶段：phase=%s flow=%s", pack.Phase, pack.Flow))
	}
	if pack.NextChapter > 0 {
		lines = append(lines, fmt.Sprintf("推进位置：下一章=%d，已完成=%d/%d，总字数=%d", pack.NextChapter, pack.CompletedCount, pack.TotalChapters, pack.TotalWordCount))
	}
	if pack.PlanningTier != "" {
		lines = append(lines, "规划级别："+pack.PlanningTier)
	}
	if pack.PendingSteer != "" {
		lines = append(lines, "待处理干预："+pack.PendingSteer)
	}
	if len(pack.PendingRewrites) > 0 {
		lines = append(lines, fmt.Sprintf("待返工章节：%v，原因：%s", pack.PendingRewrites, pack.RewriteReason))
	}
	if pack.LastCommit != "" {
		lines = append(lines, "最近提交："+pack.LastCommit)
	}
	if pack.LastReview != "" {
		lines = append(lines, "最近审阅："+pack.LastReview)
	}
	if len(pack.RecentSummaries) > 0 {
		lines = append(lines, "最近摘要："+strings.Join(pack.RecentSummaries, " | "))
	}
	if pack.MemoryPolicy != nil {
		lines = append(lines, fmt.Sprintf(
			"记忆策略：mode=%s summary_window=%d timeline_window=%d layered=%t handoff_preferred=%t read_only_threshold=%d",
			pack.MemoryPolicy.Mode,
			pack.MemoryPolicy.SummaryWindow,
			pack.MemoryPolicy.TimelineWindow,
			pack.MemoryPolicy.LayeredSummaries,
			pack.MemoryPolicy.HandoffPreferred,
			pack.MemoryPolicy.ReadOnlyThreshold,
		))
	}
	if len(pack.Guidance) > 0 {
		lines = append(lines, "交接要求："+strings.Join(pack.Guidance, " "))
	}
	return strings.Join(lines, "\n")
}

func totalChapters(progress *domain.Progress) int {
	if progress == nil {
		return 0
	}
	return progress.TotalChapters
}
