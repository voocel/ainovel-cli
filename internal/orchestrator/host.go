package orchestrator

import (
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// handleFoundationCheck 在 SubAgent 完成后检查基础设定是否完备。
// 如果 phase 仍在 premise（有 premise 但无 outline），注入确定性提醒。
func handleFoundationCheck(coordinator *agentcore.Agent, store *storepkg.Store, emit emitFn) {
	progress, _ := store.LoadProgress()
	if progress == nil {
		return
	}
	// 只在规划阶段检查（premise 已保存但 outline 未保存）
	if progress.Phase != domain.PhasePremise {
		return
	}
	var missing []string
	if o, _ := store.LoadOutline(); len(o) == 0 {
		missing = append(missing, "outline")
	}
	if c, _ := store.LoadCharacters(); len(c) == 0 {
		missing = append(missing, "characters")
	}
	if r, _ := store.LoadWorldRules(); len(r) == 0 {
		missing = append(missing, "world_rules")
	}
	if len(missing) == 0 {
		return
	}
	slog.Warn("基础设定不完整", "module", "host", "missing", missing)
	if emit != nil {
		emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
			Summary: fmt.Sprintf("基础设定不完整，缺失: %v", missing), Level: "warn"})
	}
	runMeta, _ := store.LoadRunMeta()
	guidance := planningTierGuidance(runMeta)
	msg := fmt.Sprintf(
		"[系统] 基础设定不完整，以下项目尚未保存：%v。请重新调用对应规划师补全这些设定。在基础设定全部完备前，不要调用 writer。",
		missing)
	if guidance != "" {
		msg += "\n" + guidance
	}
	coordinator.FollowUp(agentcore.UserMsg(msg))
}

// handleSubAgentDone 在每次 SubAgent 调用完成后读取文件系统信号，注入确定性任务。
// 返回 true 表示检测到 commit 信号（Writer 正常完成）。
func handleSubAgentDone(coordinator *agentcore.Agent, store *storepkg.Store, emit emitFn) bool {
	result, err := store.LoadAndClearLastCommit()
	if err != nil || result == nil {
		return false
	}

	slog.Info("章节提交信号", "module", "host", "chapter", result.Chapter, "words", result.WordCount)
	if emit != nil {
		emit(UIEvent{
			Time:     time.Now(),
			Category: "SYSTEM",
			Summary:  fmt.Sprintf("第 %d 章已提交：%d 字", result.Chapter, result.WordCount),
			Level:    "success",
		})
	}

	// outline_feedback 处理：Writer 反馈大纲偏离
	if result.Feedback != nil && result.Feedback.Deviation != "" {
		slog.Info("outline_feedback", "module", "host", "deviation", result.Feedback.Deviation)
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
				Summary: "Writer 反馈大纲偏离: " + truncateLog(result.Feedback.Deviation, 60), Level: "info"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统] Writer 在第 %d 章写作中发现大纲偏离。偏离：%s。建议：%s。请评估是否需要调整后续大纲。",
			result.Chapter, result.Feedback.Deviation, result.Feedback.Suggestion)))
	}

	// 确定性判断 0：正在重写/打磨流程中
	progress, _ := store.LoadProgress()
	if progress != nil && (progress.Flow == domain.FlowRewriting || progress.Flow == domain.FlowPolishing) {
		if !slices.Contains(progress.PendingRewrites, result.Chapter) {
			slog.Warn("重写期间提交了非队列章节", "module", "host", "chapter", result.Chapter)
			coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
				"[系统] 当前处于重写流程，但提交了非队列章节（第 %d 章）。请先完成待重写章节 %v 后再继续新章节。",
				result.Chapter, progress.PendingRewrites)))
			return true
		}
		if err := store.CompleteRewrite(result.Chapter); err != nil {
			slog.Error("完成重写标记失败", "module", "host", "err", err)
		}
		flushPendingSteer(store, coordinator, emit)
		updated, _ := store.LoadProgress()
		if updated != nil && len(updated.PendingRewrites) == 0 {
			slog.Info("所有重写/打磨已完成，恢复正常写作", "module", "host")
			saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
			saveCheckpoint(store, "rewrite-done")
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: "所有重写/打磨已完成", Level: "success"})
			}
		} else if updated != nil {
			slog.Info("还有待处理章节", "module", "host", "remaining", updated.PendingRewrites)
			saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
		}
		return true
	}

	// 确定性判断 1.5：长篇弧/卷边界处理
	if progress != nil && progress.Layered && result.ArcEnd {
		// 全书完成：无后续弧（包括骨架弧）
		isBookEnd := result.NextVolume == 0 && result.NextArc == 0

		// 根据是否需要展开下一卷/弧，生成尾部指令
		var expansionTail string
		if result.NeedsVolumeExpansion && !isBookEnd {
			expansionTail = fmt.Sprintf(
				"调用 architect_long 为第 %d 卷展开弧级结构和首弧章节（save_foundation type=expand_volume, volume=%d），然后继续写作。",
				result.NextVolume, result.NextVolume)
		} else if result.NeedsExpansion && !isBookEnd {
			expansionTail = fmt.Sprintf(
				"调用 architect_long 为第 %d 卷第 %d 弧展开详细章节规划（save_foundation type=expand_arc），然后继续写作。",
				result.NextVolume, result.NextArc)
		}

		if result.VolumeEnd {
			slog.Info("弧结束（卷结束），注入评审指令", "module", "host", "volume", result.Volume, "arc", result.Arc,
				"needs_expansion", result.NeedsExpansion)
			if err := store.SetFlow(domain.FlowReviewing); err != nil {
				slog.Error("设置审阅流程失败", "module", "host", "err", err)
			}
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
					Summary: fmt.Sprintf("第 %d 卷第 %d 弧结束（卷结束），触发评审", result.Volume, result.Arc), Level: "warn"})
			}

			tail := "完成后继续写下一卷。"
			if expansionTail != "" {
				tail = expansionTail
			}
			if isBookEnd {
				tail = "完成后总结全书并结束。不要再调用 writer。"
			}
			coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
				"[系统] 第 %d 卷第 %d 弧结束（卷结束）。请依次：\n"+
					"1. 调用 editor 进行弧级评审（scope=arc，最新章节为第 %d 章）\n"+
					"2. 调用 editor 生成弧摘要和角色快照（save_arc_summary，volume=%d，arc=%d）\n"+
					"3. 调用 editor 生成卷摘要（save_volume_summary，volume=%d）\n"+
					"%s",
				result.Volume, result.Arc, result.Chapter, result.Volume, result.Arc, result.Volume, tail)))
		} else {
			slog.Info("弧结束，注入弧级评审指令", "module", "host", "volume", result.Volume, "arc", result.Arc,
				"needs_expansion", result.NeedsExpansion)
			if err := store.SetFlow(domain.FlowReviewing); err != nil {
				slog.Error("设置审阅流程失败", "module", "host", "err", err)
			}
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
					Summary: fmt.Sprintf("第 %d 卷第 %d 弧结束，触发弧级评审", result.Volume, result.Arc), Level: "warn"})
			}

			tail := "完成后继续写下一弧的章节。"
			if expansionTail != "" {
				tail = expansionTail
			}
			coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
				"[系统] 第 %d 卷第 %d 弧结束。请依次：\n"+
					"1. 调用 editor 进行弧级评审（scope=arc，最新章节为第 %d 章）\n"+
					"2. 调用 editor 生成弧摘要和角色快照（save_arc_summary，volume=%d，arc=%d）\n"+
					"%s",
				result.Volume, result.Arc, result.Chapter, result.Volume, result.Arc, tail)))
		}

		if isBookEnd {
			slog.Info("全书最后一弧，评审完成后将结束", "module", "host")
			if err := store.MarkComplete(); err != nil {
				slog.Error("标记完成失败", "module", "host", "err", err)
			}
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
					Summary: fmt.Sprintf("全部 %d 章已完成，等待最终评审", progress.TotalChapters), Level: "success"})
			}
		}
		flushPendingSteer(store, coordinator, emit)
		saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
		return true
	}

	// 确定性判断 1：全书完成
	totalChapters := 0
	if progress != nil {
		totalChapters = progress.TotalChapters
	}
	if totalChapters > 0 && result.NextChapter > totalChapters {
		slog.Info("全书完成", "module", "host", "total", totalChapters)
		if err := store.MarkComplete(); err != nil {
			slog.Error("标记完成失败", "module", "host", "err", err)
		}
		flushPendingSteer(store, coordinator, emit)
		saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: fmt.Sprintf("全部 %d 章已完成", totalChapters), Level: "success"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统] 全部 %d 章已写完。请总结全书并结束。不要再调用 writer。",
			totalChapters)))
		return true
	}

	// 确定性判断 2：需要全局审阅
	if result.ReviewRequired {
		slog.Info("触发全局审阅", "module", "host", "reason", result.ReviewReason)
		if err := store.SetFlow(domain.FlowReviewing); err != nil {
			slog.Error("设置审阅流程失败", "module", "host", "err", err)
		}
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: "review_required=true " + result.ReviewReason, Level: "warn"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统] review_required=true，%s。请调用 editor 对已完成章节进行全局审阅，然后根据审阅结果决定继续写第 %d 章还是修正已有章节。",
			result.ReviewReason, result.NextChapter)))
		flushPendingSteer(store, coordinator, emit)
		saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
		return true
	}
	flushPendingSteer(store, coordinator, emit)
	saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
	return true
}

// handleUncommittedDraft 在 Writer 结束但没有 commit 时检测是否存在未提交的草稿。
func handleUncommittedDraft(coordinator *agentcore.Agent, store *storepkg.Store, emit emitFn) {
	progress, _ := store.LoadProgress()
	if progress == nil || progress.Phase == domain.PhaseComplete {
		return
	}
	next := 1
	if progress.InProgressChapter > 0 {
		next = progress.InProgressChapter
	} else if len(progress.CompletedChapters) > 0 {
		next = progress.NextChapter()
	}
	draft, _ := store.LoadDraft(next)
	if draft == "" {
		return
	}
	slog.Warn("Writer 结束但草稿未提交", "module", "host", "chapter", next)
	if emit != nil {
		emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
			Summary: fmt.Sprintf("第 %d 章有草稿但未提交", next), Level: "warn"})
	}
	coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
		"[系统] Writer 结束但第 %d 章草稿未提交。请重新调用 writer 完成该章的自审和提交（commit_chapter）。",
		next)))
}

// handleEditorDone 在 Editor SubAgent 完成后读取审阅信号。
func handleEditorDone(coordinator *agentcore.Agent, store *storepkg.Store, emit emitFn) {
	review, err := store.LoadAndClearLastReview()
	if err != nil {
		slog.Error("加载审阅信号失败", "module", "host", "err", err)
		return
	}
	if review == nil {
		return
	}

	criticalN := review.CriticalCount()
	slog.Info("审阅信号", "module", "host",
		"verdict", review.Verdict, "issues", len(review.Issues),
		"critical", criticalN, "errors", review.ErrorCount())

	// 宿主兜底：如果 LLM 给了 accept 但存在 critical 问题，强制升级为 rewrite
	if review.Verdict == "accept" && criticalN > 0 {
		slog.Warn("critical 问题但 verdict=accept，强制升级为 rewrite", "module", "host", "critical", criticalN)
		review.Verdict = "rewrite"
	}

	chaptersInfo := ""
	if len(review.AffectedChapters) > 0 {
		chaptersInfo = fmt.Sprintf("受影响章节：%v。", review.AffectedChapters)
	}

	switch review.Verdict {
	case "rewrite":
		if err := store.SetPendingRewrites(review.AffectedChapters, review.Summary); err != nil {
			slog.Error("设置重写队列失败", "module", "host", "err", err)
		}
		if err := store.SetFlow(domain.FlowRewriting); err != nil {
			slog.Error("设置流程状态失败", "module", "host", "err", err)
		}
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "REVIEW",
				Summary: fmt.Sprintf("verdict=rewrite affected=%v", review.AffectedChapters), Level: "warn"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统] Editor 审阅结论：rewrite。%s%s请逐章调用 writer 重写受影响章节，全部完成后继续正常写作。",
			review.Summary, chaptersInfo)))
	case "polish":
		if err := store.SetPendingRewrites(review.AffectedChapters, review.Summary); err != nil {
			slog.Error("设置打磨队列失败", "module", "host", "err", err)
		}
		if err := store.SetFlow(domain.FlowPolishing); err != nil {
			slog.Error("设置流程状态失败", "module", "host", "err", err)
		}
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "REVIEW",
				Summary: fmt.Sprintf("verdict=polish affected=%v", review.AffectedChapters), Level: "warn"})
		}
		coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
			"[系统] Editor 审阅结论：polish。%s%s请逐章调用 writer 打磨受影响章节，全部完成后继续正常写作。",
			review.Summary, chaptersInfo)))
	default:
		if err := store.SetFlow(domain.FlowWriting); err != nil {
			slog.Error("清除审阅状态失败", "module", "host", "err", err)
		}
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "REVIEW", Summary: "verdict=accept 审阅通过", Level: "success"})
		}
	}
	flushPendingSteer(store, coordinator, emit)
	saveCheckpoint(store, fmt.Sprintf("review-ch%02d-%s", review.Chapter, review.Verdict))
	if emit != nil {
		emit(UIEvent{Time: time.Now(), Category: "CHECK",
			Summary: fmt.Sprintf("saved review-ch%02d-%s", review.Chapter, review.Verdict), Level: "info"})
	}
}

func saveCheckpoint(store *storepkg.Store, label string) {
	if err := store.SaveCheckpoint(label); err != nil {
		slog.Error("保存检查点失败", "module", "host", "label", label, "err", err)
	}
}
