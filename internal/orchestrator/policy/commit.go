package policy

import (
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	orchestratoraction "github.com/voocel/ainovel-cli/internal/orchestrator/action"
)

func commitFeedbackActions(snapshot snapshot) []orchestratoraction.Action {
	result := snapshot.Commit
	if result == nil || result.Feedback == nil || result.Feedback.Deviation == "" {
		return nil
	}
	return []orchestratoraction.Action{
		orchestratoraction.EmitNotice("SYSTEM", "Writer 反馈大纲偏离: "+truncateText(result.Feedback.Deviation, 60), "info"),
		orchestratoraction.FollowUp(fmt.Sprintf(
			"[系统] Writer 在第 %d 章写作中发现大纲偏离。偏离：%s。建议：%s。请评估是否需要调整后续大纲，处理完成后继续写第 %d 章。",
			result.Chapter, result.Feedback.Deviation, result.Feedback.Suggestion, result.NextChapter)),
	}
}

func commitRewriteFlowRule(snapshot snapshot) (bool, []orchestratoraction.Action) {
	progress := snapshot.Progress
	result := snapshot.Commit
	if progress == nil || result == nil {
		return false, nil
	}
	if progress.Flow != domain.FlowRewriting && progress.Flow != domain.FlowPolishing {
		return false, nil
	}
	if !slices.Contains(progress.PendingRewrites, result.Chapter) {
		return true, []orchestratoraction.Action{
			orchestratoraction.FollowUp(fmt.Sprintf(
				"[系统] 当前处于重写流程，但提交了非队列章节（第 %d 章）。请先完成待重写章节 %v 后再继续新章节。",
				result.Chapter, progress.PendingRewrites)),
		}
	}
	actions := []orchestratoraction.Action{orchestratoraction.CompleteRewrite(result.Chapter)}
	actions = append(actions, pendingSteerActions(snapshot.RunMeta)...)
	actions = append(actions,
		orchestratoraction.SaveCheckpointAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
		orchestratoraction.SaveHandoffAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
	)
	return true, actions
}

func commitLayeredArcEndRule(snapshot snapshot) (bool, []orchestratoraction.Action) {
	progress := snapshot.Progress
	result := snapshot.Commit
	if progress == nil || result == nil || !progress.Layered || !result.ArcEnd {
		return false, nil
	}

	isBookEnd := result.NextVolume == 0 && result.NextArc == 0 && !result.NeedsNewVolume

	var expansionTail string
	if result.NeedsNewVolume {
		expansionTail = "调用 architect_long 自主规划下一卷（save_foundation type=append_volume），参考终局方向和已写内容决定下一卷的方向和结构，同时更新指南针（save_foundation type=update_compass），然后继续写作。"
	} else if result.NeedsExpansion && !isBookEnd {
		expansionTail = fmt.Sprintf(
			"调用 architect_long 为第 %d 卷第 %d 弧展开详细章节规划（save_foundation type=expand_arc），然后继续写作。",
			result.NextVolume, result.NextArc)
	}

	var actions []orchestratoraction.Action
	actions = append(actions, orchestratoraction.SetFlow(domain.FlowReviewing))
	if result.VolumeEnd {
		actions = append(actions, orchestratoraction.EmitNotice("SYSTEM",
			fmt.Sprintf("第 %d 卷第 %d 弧结束（卷结束），触发评审", result.Volume, result.Arc), "warn"))

		tail := "完成后继续写下一卷。"
		if expansionTail != "" {
			tail = expansionTail
		}
		if isBookEnd {
			tail = "完成后总结全书并结束。不要再调用 writer。"
		}
		actions = append(actions, orchestratoraction.FollowUp(fmt.Sprintf(
			"[系统] 第 %d 卷第 %d 弧结束（卷结束）。请依次：\n"+
				"1. 调用 editor 进行弧级评审（scope=arc，最新章节为第 %d 章）\n"+
				"2. 调用 editor 生成弧摘要和角色快照（save_arc_summary，volume=%d，arc=%d）\n"+
				"3. 调用 editor 生成卷摘要（save_volume_summary，volume=%d）\n"+
				"%s",
			result.Volume, result.Arc, result.Chapter, result.Volume, result.Arc, result.Volume, tail)))
	} else {
		actions = append(actions, orchestratoraction.EmitNotice("SYSTEM",
			fmt.Sprintf("第 %d 卷第 %d 弧结束，触发弧级评审", result.Volume, result.Arc), "warn"))

		tail := "完成后继续写下一弧的章节。"
		if expansionTail != "" {
			tail = expansionTail
		}
		actions = append(actions, orchestratoraction.FollowUp(fmt.Sprintf(
			"[系统] 第 %d 卷第 %d 弧结束。请依次：\n"+
				"1. 调用 editor 进行弧级评审（scope=arc，最新章节为第 %d 章）\n"+
				"2. 调用 editor 生成弧摘要和角色快照（save_arc_summary，volume=%d，arc=%d）\n"+
				"%s",
			result.Volume, result.Arc, result.Chapter, result.Volume, result.Arc, tail)))
	}

	if isBookEnd {
		actions = append(actions,
			orchestratoraction.MarkComplete(),
			orchestratoraction.EmitNotice("SYSTEM", fmt.Sprintf("全部 %d 章已完成，等待最终评审", progress.TotalChapters), "success"),
		)
	}

	actions = append(actions, pendingSteerActions(snapshot.RunMeta)...)
	actions = append(actions,
		orchestratoraction.SaveCheckpointAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
		orchestratoraction.SaveHandoffAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
	)
	return true, actions
}

func commitBookCompleteRule(snapshot snapshot) (bool, []orchestratoraction.Action) {
	progress := snapshot.Progress
	result := snapshot.Commit
	if progress == nil || result == nil || progress.TotalChapters == 0 || result.NextChapter <= progress.TotalChapters {
		return false, nil
	}
	actions := []orchestratoraction.Action{orchestratoraction.MarkComplete()}
	actions = append(actions, pendingSteerActions(snapshot.RunMeta)...)
	actions = append(actions,
		orchestratoraction.SaveCheckpointAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
		orchestratoraction.SaveHandoffAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
		orchestratoraction.EmitNotice("SYSTEM", fmt.Sprintf("全部 %d 章已完成", progress.TotalChapters), "success"),
		orchestratoraction.FollowUp(fmt.Sprintf(
			"[系统] 全部 %d 章已写完。请总结全书并结束。不要再调用 writer。",
			progress.TotalChapters)),
	)
	return true, actions
}

func commitReviewRequiredRule(snapshot snapshot) (bool, []orchestratoraction.Action) {
	result := snapshot.Commit
	if result == nil || !result.ReviewRequired {
		return false, nil
	}
	actions := []orchestratoraction.Action{
		orchestratoraction.SetFlow(domain.FlowReviewing),
		orchestratoraction.EmitNotice("SYSTEM", "review_required=true "+result.ReviewReason, "warn"),
		orchestratoraction.FollowUp(fmt.Sprintf(
			"[系统] review_required=true，%s。请调用 editor 对已完成章节进行全局审阅，然后根据审阅结果决定继续写第 %d 章还是修正已有章节。",
			result.ReviewReason, result.NextChapter)),
	}
	actions = append(actions, pendingSteerActions(snapshot.RunMeta)...)
	actions = append(actions,
		orchestratoraction.SaveCheckpointAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
		orchestratoraction.SaveHandoffAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
	)
	return true, actions
}

func commitDefaultRule(snapshot snapshot) (bool, []orchestratoraction.Action) {
	result := snapshot.Commit
	if result == nil {
		return false, nil
	}
	actions := append([]orchestratoraction.Action{}, pendingSteerActions(snapshot.RunMeta)...)
	actions = append(actions,
		orchestratoraction.SaveCheckpointAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
		orchestratoraction.SaveHandoffAction(fmt.Sprintf("ch%02d-commit", result.Chapter)),
	)
	return true, actions
}
