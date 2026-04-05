package orchestrator

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type recoveryRule func(recoverySnapshot) (bool, recoveryResult)

type recoverySnapshot struct {
	Progress *domain.Progress
	RunMeta  *domain.RunMeta
	Store    *storepkg.Store
}

type recoveryEngine struct {
	rules []recoveryRule
}

var defaultRecoveryEngine = recoveryEngine{
	rules: []recoveryRule{
		recoveryPendingCommitRule,
		recoveryTaskRule,
		recoveryNewRule,
		recoveryPlanningRule,
		recoveryInProgressChapterRule,
		recoveryPendingRewriteRule,
		recoveryReviewingRule,
		recoverySteeringResetRule,
		recoveryPendingSteerRule,
		recoveryLayeredPlanningRule,
		recoveryResumableRule,
	},
}

func (e recoveryEngine) evaluate(snapshot recoverySnapshot) recoveryResult {
	for _, rule := range e.rules {
		if matched, result := rule(snapshot); matched {
			return result
		}
	}
	return recoveryResult{IsNew: true}
}

func (s recoverySnapshot) withGuidance(prompt string) string {
	guidance := planningTierGuidance(s.RunMeta)
	if guidance == "" {
		return prompt
	}
	return prompt + "\n" + guidance
}

func recoveryPendingCommitRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	if snapshot.Store == nil {
		return false, recoveryResult{}
	}
	pending, err := snapshot.Store.Signals.LoadPendingCommit()
	if err != nil {
		slog.Error("读取 pending_commit 失败", "module", "recovery", "err", err)
		return false, recoveryResult{}
	}
	if pending == nil {
		return false, recoveryResult{}
	}

	switch pending.Stage {
	case domain.CommitStageProgressMarked, domain.CommitStageSignalSaved:
		result, recErr := reconcileCommittedChapter(snapshot, pending)
		if recErr != nil {
			slog.Error("补齐章节提交失败", "module", "recovery", "chapter", pending.Chapter, "stage", pending.Stage, "err", recErr)
			return true, recoveryResult{
				PromptText: snapshot.withGuidance(pendingCommitManualPrompt(pending)),
				Label:      fmt.Sprintf("恢复：第 %d 章提交中断（%s）", pending.Chapter, pending.Stage),
			}
		}
		return true, result
	default:
		return true, recoveryResult{
			PromptText: snapshot.withGuidance(pendingCommitManualPrompt(pending)),
			Label:      fmt.Sprintf("恢复：第 %d 章提交中断（%s）", pending.Chapter, pending.Stage),
		}
	}
}

func recoveryNewRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	if snapshot.Progress != nil {
		return false, recoveryResult{}
	}
	return true, recoveryResult{IsNew: true}
}

func recoveryTaskRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	if snapshot.Store == nil {
		return false, recoveryResult{}
	}
	tasks, err := snapshot.Store.Tasks.Load()
	if err != nil || len(tasks) == 0 {
		return false, recoveryResult{}
	}

	var active *domain.TaskRecord
	for i := len(tasks) - 1; i >= 0; i-- {
		task := tasks[i]
		if task.IsTerminal() {
			continue
		}
		active = &task
		break
	}
	if active == nil {
		return false, recoveryResult{}
	}

	prompt, label := recoveryPromptFromTask(*active)
	if prompt == "" {
		return false, recoveryResult{}
	}
	return true, recoveryResult{
		PromptText: snapshot.withGuidance(prompt),
		Label:      label,
	}
}

func recoveryPlanningRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	progress := snapshot.Progress
	if progress == nil {
		return false, recoveryResult{}
	}
	if progress.Phase != domain.PhasePremise && progress.Phase != domain.PhaseOutline {
		return false, recoveryResult{}
	}
	return true, recoveryResult{
		PromptText: snapshot.withGuidance(
			"上次在规划阶段中断。请调用 novel_context 检查当前基础设定状态，补全缺失的设定项（premise/outline/characters/world_rules），然后开始写作。"),
		Label: fmt.Sprintf("恢复：规划阶段（%s）", progress.Phase),
	}
}

func recoveryInProgressChapterRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	progress := snapshot.Progress
	if progress == nil || progress.InProgressChapter <= 0 {
		return false, recoveryResult{}
	}
	ch := progress.InProgressChapter
	return true, recoveryResult{
		PromptText: snapshot.withGuidance(fmt.Sprintf(
			"第 %d 章正在进行中，已有部分草稿。请调用 writer 继续完成该章（可用 read_chapter 读取已有草稿）。总共需要写 %d 章。",
			ch, progress.TotalChapters)),
		Label: fmt.Sprintf("恢复：第 %d 章进行中", ch),
	}
}

func recoveryPendingRewriteRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	progress := snapshot.Progress
	if progress == nil || len(progress.PendingRewrites) == 0 {
		return false, recoveryResult{}
	}
	verb := "重写"
	if progress.Flow == domain.FlowPolishing {
		verb = "打磨"
	}
	return true, recoveryResult{
		PromptText: snapshot.withGuidance(fmt.Sprintf(
			"上次审阅后有 %d 章被标记为待%s（受影响章节：%v）。原因：%s。\n"+
				"请先调用 novel_context 读取相关章节原文，然后逐章调用 writer 执行%s。这是审阅阶段的确定性裁定，必须执行，不可跳过。\n"+
				"全部%s完成后继续写第 %d 章。总共需要写 %d 章。",
			len(progress.PendingRewrites), verb, progress.PendingRewrites, progress.RewriteReason,
			verb, verb, progress.NextChapter(), progress.TotalChapters)),
		Label: fmt.Sprintf("%s恢复：%d 章待处理 %v", verb, len(progress.PendingRewrites), progress.PendingRewrites),
	}
}

func recoveryReviewingRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	progress := snapshot.Progress
	if progress == nil || progress.Flow != domain.FlowReviewing {
		return false, recoveryResult{}
	}
	return true, recoveryResult{
		PromptText: snapshot.withGuidance(fmt.Sprintf(
			"上次审阅中断，请重新调用 editor 对已完成章节进行全局审阅。已完成 %d 章，共 %d 字。总共需要写 %d 章。",
			len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters)),
		Label: "审阅恢复：上次审阅中断",
	}
}

func recoverySteeringResetRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	progress := snapshot.Progress
	if progress == nil || progress.Flow != domain.FlowSteering {
		return false, recoveryResult{}
	}
	if snapshot.RunMeta != nil && snapshot.RunMeta.PendingSteer != "" {
		return false, recoveryResult{}
	}
	if !progress.IsResumable() {
		return false, recoveryResult{}
	}
	next := progress.NextChapter()
	return true, recoveryResult{
		PromptText: snapshot.withGuidance(fmt.Sprintf(
			"从第 %d 章继续写作。之前已完成 %d 章，共 %d 字。总共需要写 %d 章。",
			next, len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters)),
		Label: fmt.Sprintf("恢复模式：从第 %d 章继续（干预状态已重置）", next),
	}
}

func recoveryPendingSteerRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	progress := snapshot.Progress
	if progress == nil || !progress.IsResumable() || snapshot.RunMeta == nil || snapshot.RunMeta.PendingSteer == "" {
		return false, recoveryResult{}
	}
	next := progress.NextChapter()
	return true, recoveryResult{
		PromptText: snapshot.withGuidance(fmt.Sprintf(
			"从第 %d 章继续写作。之前已完成 %d 章，共 %d 字。总共需要写 %d 章。\n\n[用户干预-恢复] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。",
			next, len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters, snapshot.RunMeta.PendingSteer)),
		Label:                "Steer 恢复：上次干预未完成，重新注入",
		ConsumesPendingSteer: true,
	}
}

func recoveryLayeredPlanningRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	progress := snapshot.Progress
	if progress == nil || !progress.IsResumable() || !progress.Layered || snapshot.Store == nil {
		return false, recoveryResult{}
	}

	next := progress.NextChapter()
	if _, err := snapshot.Store.Outline.GetChapterOutline(next); err == nil {
		return false, recoveryResult{}
	}

	volumes := mustLoadLayered(snapshot.Store)
	if vol, arc := domain.NextSkeletonArc(volumes, progress.CurrentVolume, progress.CurrentArc); vol > 0 {
		return true, recoveryResult{
			PromptText: snapshot.withGuidance(fmt.Sprintf(
				"上次弧级评审已完成，但第 %d 卷第 %d 弧尚未展开章节。请调用 architect_long 为该弧展开详细章节规划（save_foundation type=expand_arc, volume=%d, arc=%d），然后继续写作。已完成 %d 章，共 %d 字。",
				vol, arc, vol, arc, len(progress.CompletedChapters), progress.TotalWordCount)),
			Label: fmt.Sprintf("恢复模式：展开第 %d 卷第 %d 弧", vol, arc),
		}
	}

	currentFinal := false
	for _, v := range volumes {
		if v.Index == progress.CurrentVolume {
			currentFinal = v.Final
			break
		}
	}
	if currentFinal {
		return false, recoveryResult{}
	}

	return true, recoveryResult{
		PromptText: snapshot.withGuidance(fmt.Sprintf(
			"上次卷级评审已完成，需要创建下一卷。请调用 architect_long 自主规划下一卷（save_foundation type=append_volume），参考终局方向和已写内容决定方向，同时更新指南针（save_foundation type=update_compass），然后继续写作。已完成 %d 章，共 %d 字。",
			len(progress.CompletedChapters), progress.TotalWordCount)),
		Label: "恢复模式：创建下一卷",
	}
}

func recoveryResumableRule(snapshot recoverySnapshot) (bool, recoveryResult) {
	progress := snapshot.Progress
	if progress == nil || !progress.IsResumable() {
		return false, recoveryResult{}
	}
	next := progress.NextChapter()
	return true, recoveryResult{
		PromptText: snapshot.withGuidance(fmt.Sprintf(
			"从第 %d 章继续写作。之前已完成 %d 章，共 %d 字。总共需要写 %d 章。",
			next, len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters)),
		Label: fmt.Sprintf("恢复模式：从第 %d 章继续（已完成 %d 章，共 %d 字）",
			next, len(progress.CompletedChapters), progress.TotalWordCount),
	}
}

func mustLoadLayered(s *storepkg.Store) []domain.VolumeOutline {
	v, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		slog.Warn("加载分层大纲失败", "module", "recovery", "err", err)
	}
	return v
}

func reconcileCommittedChapter(snapshot recoverySnapshot, pending *domain.PendingCommit) (recoveryResult, error) {
	if pending == nil || pending.Result == nil || snapshot.Store == nil {
		return recoveryResult{}, fmt.Errorf("pending commit 缺少可恢复结果")
	}

	actions := evaluateCommitPolicy(snapshot.Progress, snapshot.RunMeta, pending.Result)
	if err := applyRecoveryCommitActions(actions, snapshot.Store); err != nil {
		return recoveryResult{}, err
	}
	if err := snapshot.Store.Signals.ClearLastCommit(); err != nil {
		return recoveryResult{}, err
	}
	if err := snapshot.Store.Progress.ClearInProgress(); err != nil {
		return recoveryResult{}, err
	}
	if err := snapshot.Store.Signals.ClearPendingCommit(); err != nil {
		return recoveryResult{}, err
	}

	fallback := fallbackPostCommitPrompt(snapshot.Progress, pending.Result)
	return recoveryResult{
		PromptText: snapshot.withGuidance(recoveryPromptFromActions(actions, fallback)),
		Label:      fmt.Sprintf("恢复：补齐第 %d 章提交", pending.Chapter),
	}, nil
}

func recoveryPromptFromTask(task domain.TaskRecord) (prompt string, label string) {
	switch task.Kind {
	case domain.TaskFoundationPlan:
		return "上次在基础规划阶段中断。请调用 novel_context 检查当前基础设定状态，补全缺失的设定项（premise/outline/characters/world_rules），然后开始写作。",
			"任务恢复：基础规划"
	case domain.TaskChapterWrite:
		ch := task.Chapter
		if ch <= 0 {
			ch = 1
		}
		return fmt.Sprintf("上次在第 %d 章写作任务中断。请调用 writer 继续完成该章，必要时先用 read_chapter 读取已有草稿。", ch),
			fmt.Sprintf("任务恢复：第 %d 章写作", ch)
	case domain.TaskChapterReview:
		if task.Chapter > 0 {
			return fmt.Sprintf("上次在第 %d 章评审任务中断。请调用 editor 继续完成该章或当前批次的审阅。", task.Chapter),
				fmt.Sprintf("任务恢复：第 %d 章评审", task.Chapter)
		}
		return "上次审阅任务中断。请调用 editor 继续完成当前审阅。", "任务恢复：章节评审"
	case domain.TaskChapterRewrite:
		return fmt.Sprintf("上次在第 %d 章重写任务中断。请调用 writer 继续重写该章。", task.Chapter),
			fmt.Sprintf("任务恢复：第 %d 章重写", task.Chapter)
	case domain.TaskChapterPolish:
		return fmt.Sprintf("上次在第 %d 章打磨任务中断。请调用 writer 继续打磨该章。", task.Chapter),
			fmt.Sprintf("任务恢复：第 %d 章打磨", task.Chapter)
	case domain.TaskArcExpand:
		return fmt.Sprintf("上次在第 %d 卷第 %d 弧展开任务中断。请调用 architect_long 为该弧展开详细章节规划。", task.Volume, task.Arc),
			fmt.Sprintf("任务恢复：展开第 %d 卷第 %d 弧", task.Volume, task.Arc)
	case domain.TaskVolumeAppend:
		return "上次在下一卷规划任务中断。请调用 architect_long 规划下一卷并更新指南针。", "任务恢复：规划下一卷"
	case domain.TaskSteerApply:
		return "上次在用户干预处理任务中断。请优先评估干预影响范围，决定是否修改设定或重写章节。", "任务恢复：处理用户干预"
	case domain.TaskCoordinatorDecision:
		return "", ""
	default:
		return "", ""
	}
}

func applyRecoveryCommitActions(actions []policyAction, store *storepkg.Store) error {
	for _, action := range actions {
		switch action.Kind {
		case actionSetFlow:
			if err := store.Progress.SetFlow(action.Flow); err != nil {
				return err
			}
		case actionSetPendingRewrites:
			if err := store.Progress.SetPendingRewrites(action.Chapters, action.Reason); err != nil {
				return err
			}
		case actionCompleteRewrite:
			if err := store.Progress.CompleteRewrite(action.Chapter); err != nil {
				return err
			}
		case actionSaveCheckpoint:
			progress, _ := store.Progress.Load()
			if err := store.RunMeta.SaveCheckpoint(action.Label, progress); err != nil {
				return err
			}
		case actionSaveHandoff:
			if err := saveHandoffSnapshot(store, action.Label); err != nil {
				return err
			}
		case actionMarkComplete:
			if err := store.Progress.MarkComplete(); err != nil {
				return err
			}
		}
	}
	return nil
}

func recoveryPromptFromActions(actions []policyAction, fallback string) string {
	var parts []string
	for _, action := range actions {
		if action.Kind == actionFollowUp && strings.TrimSpace(action.Message) != "" {
			parts = append(parts, strings.TrimSpace(action.Message))
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, "\n\n")
}

func fallbackPostCommitPrompt(progress *domain.Progress, result *domain.CommitResult) string {
	if result != nil && progress != nil && progress.TotalChapters > 0 && result.NextChapter > progress.TotalChapters {
		return fmt.Sprintf("[系统] 全部 %d 章已写完。请总结全书并结束。不要再调用 writer。", progress.TotalChapters)
	}
	if result != nil && progress != nil {
		return fmt.Sprintf(
			"从第 %d 章继续写作。之前已完成 %d 章，共 %d 字。总共需要写 %d 章。",
			result.NextChapter, len(progress.CompletedChapters), progress.TotalWordCount, progress.TotalChapters,
		)
	}
	if result != nil {
		return fmt.Sprintf("第 %d 章提交已补齐。请继续处理后续流程。", result.Chapter)
	}
	return "检测到上次章节提交已进入收尾阶段，请继续处理后续流程。"
}

func pendingCommitManualPrompt(pending *domain.PendingCommit) string {
	if pending == nil {
		return ""
	}
	return fmt.Sprintf(
		"检测到第 %d 章的提交在阶段 %s 中断，终稿、摘要或状态文件可能只写入了一部分。请先调用 read_chapter 读取第 %d 章（source=final 和 source=draft）核对现状，再让 writer 重新整理本章摘要与状态，并重新调用 commit_chapter 完成提交。不要直接继续写下一章。",
		pending.Chapter, pending.Stage, pending.Chapter,
	)
}
