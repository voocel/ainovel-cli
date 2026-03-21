package orchestrator

import (
	"fmt"
	"log"
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
	log.Printf("[host] 基础设定不完整，缺失: %v", missing)
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

	log.Printf("[host] 章节提交信号：第 %d 章，%d 字",
		result.Chapter, result.WordCount)
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
		log.Printf("[host] outline_feedback: %s", result.Feedback.Deviation)
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
			log.Printf("[host] 警告：重写期间提交了非队列章节 %d，拒绝并提醒", result.Chapter)
			coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
				"[系统] 当前处于重写流程，但提交了非队列章节（第 %d 章）。请先完成待重写章节 %v 后再继续新章节。",
				result.Chapter, progress.PendingRewrites)))
			return true
		}
		if err := store.CompleteRewrite(result.Chapter); err != nil {
			log.Printf("[host] 完成重写标记失败: %v", err)
		}
		flushPendingSteer(store, coordinator, emit)
		updated, _ := store.LoadProgress()
		if updated != nil && len(updated.PendingRewrites) == 0 {
			log.Printf("[host] 所有重写/打磨已完成，恢复正常写作")
			saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
			saveCheckpoint(store, "rewrite-done")
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "SYSTEM", Summary: "所有重写/打磨已完成", Level: "success"})
			}
		} else if updated != nil {
			log.Printf("[host] 还有 %d 章待处理：%v", len(updated.PendingRewrites), updated.PendingRewrites)
			saveCheckpoint(store, fmt.Sprintf("ch%02d-commit", result.Chapter))
		}
		return true
	}

	// 确定性判断 1.5：长篇弧/卷边界处理
	if progress != nil && progress.Layered && result.ArcEnd {
		// 判断是否全书最后一弧
		isBookEnd := progress.TotalChapters > 0 && result.NextChapter > progress.TotalChapters

		if result.VolumeEnd {
			log.Printf("[host] 第 %d 卷第 %d 弧结束（卷结束），注入弧级+卷级评审指令", result.Volume, result.Arc)
			if err := store.SetFlow(domain.FlowReviewing); err != nil {
				log.Printf("[host] 设置审阅流程失败: %v", err)
			}
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
					Summary: fmt.Sprintf("第 %d 卷第 %d 弧结束（卷结束），触发评审", result.Volume, result.Arc), Level: "warn"})
			}

			tail := "完成后继续写下一卷。"
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
			log.Printf("[host] 第 %d 卷第 %d 弧结束，注入弧级评审指令", result.Volume, result.Arc)
			if err := store.SetFlow(domain.FlowReviewing); err != nil {
				log.Printf("[host] 设置审阅流程失败: %v", err)
			}
			if emit != nil {
				emit(UIEvent{Time: time.Now(), Category: "SYSTEM",
					Summary: fmt.Sprintf("第 %d 卷第 %d 弧结束，触发弧级评审", result.Volume, result.Arc), Level: "warn"})
			}
			coordinator.FollowUp(agentcore.UserMsg(fmt.Sprintf(
				"[系统] 第 %d 卷第 %d 弧结束。请依次：\n"+
					"1. 调用 editor 进行弧级评审（scope=arc，最新章节为第 %d 章）\n"+
					"2. 调用 editor 生成弧摘要和角色快照（save_arc_summary，volume=%d，arc=%d）\n"+
					"完成后继续写下一弧的章节。",
				result.Volume, result.Arc, result.Chapter, result.Volume, result.Arc)))
		}

		if isBookEnd {
			log.Printf("[host] 全书最后一弧，评审完成后将结束")
			if err := store.MarkComplete(); err != nil {
				log.Printf("[host] 标记完成失败: %v", err)
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

	// 确定性判断 1：全书完成（TotalChapters 由大纲自动设定）
	totalChapters := 0
	if progress != nil {
		totalChapters = progress.TotalChapters
	}
	if totalChapters > 0 && result.NextChapter > totalChapters {
		log.Printf("[host] 所有 %d 章已完成，注入完成指令", totalChapters)
		if err := store.MarkComplete(); err != nil {
			log.Printf("[host] 标记完成失败: %v", err)
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
		log.Printf("[host] review_required=true（%s），注入审阅指令", result.ReviewReason)
		if err := store.SetFlow(domain.FlowReviewing); err != nil {
			log.Printf("[host] 设置审阅流程失败: %v", err)
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
// 如果存在，提醒 Coordinator 重新调用 writer 完成提交。
func handleUncommittedDraft(coordinator *agentcore.Agent, store *storepkg.Store, emit emitFn) {
	progress, _ := store.LoadProgress()
	if progress == nil || progress.Phase == domain.PhaseComplete {
		return
	}
	// 确定下一个应该写的章节
	next := 1
	if progress.InProgressChapter > 0 {
		next = progress.InProgressChapter
	} else if len(progress.CompletedChapters) > 0 {
		next = progress.NextChapter()
	}
	// 检查该章节是否有草稿但未提交
	draft, _ := store.LoadDraft(next)
	if draft == "" {
		return
	}
	// 有草稿但没有 commit 信号
	log.Printf("[host] Writer 结束但第 %d 章草稿未提交", next)
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
		log.Printf("[host] 加载审阅信号失败: %v", err)
		return
	}
	if review == nil {
		return
	}

	criticalN := review.CriticalCount()
	log.Printf("[host] 审阅信号：verdict=%s，%d 个问题（critical=%d，error=%d）",
		review.Verdict, len(review.Issues), criticalN, review.ErrorCount())

	// 宿主兜底：如果 LLM 给了 accept 但存在 critical 问题，强制升级为 rewrite
	if review.Verdict == "accept" && criticalN > 0 {
		log.Printf("[host] 检测到 %d 个 critical 问题但 verdict=accept，强制升级为 rewrite", criticalN)
		review.Verdict = "rewrite"
	}

	chaptersInfo := ""
	if len(review.AffectedChapters) > 0 {
		chaptersInfo = fmt.Sprintf("受影响章节：%v。", review.AffectedChapters)
	}

	switch review.Verdict {
	case "rewrite":
		if err := store.SetPendingRewrites(review.AffectedChapters, review.Summary); err != nil {
			log.Printf("[host] 设置重写队列失败: %v", err)
		}
		if err := store.SetFlow(domain.FlowRewriting); err != nil {
			log.Printf("[host] 设置流程状态失败: %v", err)
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
			log.Printf("[host] 设置打磨队列失败: %v", err)
		}
		if err := store.SetFlow(domain.FlowPolishing); err != nil {
			log.Printf("[host] 设置流程状态失败: %v", err)
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
			log.Printf("[host] 清除审阅状态失败: %v", err)
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
		log.Printf("[host] 保存检查点失败: %v", err)
	}
}
