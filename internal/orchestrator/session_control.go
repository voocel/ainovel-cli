package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/orchestrator/action"
	"github.com/voocel/ainovel-cli/internal/orchestrator/policy"
	"github.com/voocel/ainovel-cli/internal/orchestrator/recovery"
)

// ---------------------------------------------------------------------------
// Task tracking
// ---------------------------------------------------------------------------

func (s *session) trackTaskStart(ev agentcore.Event) {
	if s.taskRT == nil || ev.Tool != "subagent" {
		return
	}
	inv, ok := parseSubagentInvocation(ev.Args)
	if !ok {
		return
	}
	owner := canonicalAgentName(inv.Agent)
	progress, _ := s.store.Progress.Load()
	kind := inferTaskKind(inv.Agent, progress, inv.Task)
	loc := inferTaskLocation(kind, progress)
	task, err := s.taskRT.Start(kind, owner, taskTitle(kind, loc), inv.Task, loc)
	if err != nil {
		slog.Error("启动任务失败", "module", "task", "agent", inv.Agent, "err", err)
		return
	}
	if s.agents != nil {
		s.agents.Start(owner, task.ID, kind, task.Title)
	}
}

func (s *session) trackTaskEnd(ev agentcore.Event) {
	if s.taskRT == nil || ev.Tool != "subagent" {
		return
	}
	inv, ok := parseSubagentInvocation(ev.Args)
	if !ok {
		return
	}
	owner := canonicalAgentName(inv.Agent)
	if ev.IsError {
		s.recorder.logTaskEvent(owner, "task_failed", "", truncateLog(extractToolErrorText(ev.Result), 120), nil)
		_ = s.taskRT.FailActive(owner, extractToolErrorText(ev.Result))
		if s.agents != nil {
			s.agents.Fail(owner, "任务失败")
		}
		return
	}
	s.recorder.logTaskEvent(owner, "task_done", "", "任务完成", nil)
	_ = s.taskRT.CompleteActive(owner)
	if s.agents != nil {
		s.agents.Idle(owner, "任务完成")
	}
}

func (s *session) trackAgentProgress(progress toolProgress) {
	owner := canonicalAgentName(progress.Agent)
	if s.agents != nil {
		s.agents.Update(owner, progress.Tool, progressSummaryLabel(progress), 0)
	}
	if s.taskRT != nil {
		_ = s.taskRT.UpdateProgress(owner, func(task *domain.TaskRecord) {
			task.Progress.Tool = progress.Tool
			task.Progress.ToolSummary = progress.Message
			task.Progress.Summary = progressSummaryLabel(progress)
			if progress.Error {
				task.Progress.Stage = "error"
			} else {
				task.Progress.Stage = "tool"
			}
		})
	}
}

func (s *session) trackAgentTurn(agent string, turn int) {
	if agent == "" {
		return
	}
	owner := canonicalAgentName(agent)
	if s.agents != nil {
		s.agents.Update(owner, "", "", turn)
	}
	if s.taskRT != nil {
		_ = s.taskRT.UpdateProgress(owner, func(task *domain.TaskRecord) {
			task.Progress.Turn = turn
		})
	}
}

func (s *session) trackAgentContext(progress contextProgress) {
	if progress.Agent == "" || s.agents == nil {
		return
	}
	owner := canonicalAgentName(progress.Agent)
	s.agents.UpdateContext(owner, AgentContextSnapshot{
		Tokens:          progress.Tokens,
		ContextWindow:   progress.ContextWindow,
		Percent:         progress.Percent,
		Scope:           progress.Scope,
		Strategy:        progress.Strategy,
		ActiveMessages:  progress.ActiveMessages,
		SummaryMessages: progress.SummaryMessages,
		CompactedCount:  progress.CompactedCount,
		KeptCount:       progress.KeptCount,
	})
}

func taskTitle(kind domain.TaskKind, loc taskLocation) string {
	switch kind {
	case domain.TaskFoundationPlan:
		return "规划故事基础设定"
	case domain.TaskChapterWrite:
		return fmt.Sprintf("创作第 %d 章", loc.Chapter)
	case domain.TaskChapterReview:
		if loc.Chapter > 0 {
			return fmt.Sprintf("评审第 %d 章", loc.Chapter)
		}
		return "执行章节评审"
	case domain.TaskChapterRewrite:
		return fmt.Sprintf("重写第 %d 章", loc.Chapter)
	case domain.TaskChapterPolish:
		return fmt.Sprintf("打磨第 %d 章", loc.Chapter)
	case domain.TaskArcExpand:
		return fmt.Sprintf("展开第 %d 卷第 %d 弧", loc.Volume, loc.Arc)
	case domain.TaskVolumeAppend:
		return "规划下一卷"
	case domain.TaskSteerApply:
		return "处理用户干预"
	default:
		return "协调小说任务"
	}
}

func progressSummaryLabel(progress toolProgress) string {
	if progress.Message != "" {
		return progress.Message
	}
	if progress.Tool != "" {
		return progress.Tool
	}
	return "处理中"
}

// ---------------------------------------------------------------------------
// Recovery & steer
// ---------------------------------------------------------------------------

func (s *session) recovery() recovery.Result {
	progress, _ := s.store.Progress.Load()
	runMeta, _ := s.store.RunMeta.Load()
	return applyHandoffToRecovery(s.store, recovery.Evaluate(progress, runMeta, s.store, saveHandoffSnapshot))
}

func (s *session) persistSteer(text string) {
	slog.Info("用户干预", "module", "steer", "text", text)
	if s.taskRT != nil {
		_, _ = s.taskRT.Queue(domain.TaskSteerApply, "coordinator", "处理用户干预", text, taskLocation{})
	}
	if err := s.store.RunMeta.AppendSteerEntry(domain.SteerEntry{
		Input:     text,
		Timestamp: time.Now().Format(time.RFC3339),
	}); err != nil {
		slog.Error("追加干预记录失败", "module", "steer", "err", err)
	}
	if err := s.store.RunMeta.SetPendingSteer(text); err != nil {
		slog.Error("设置待处理干预失败", "module", "steer", "err", err)
	}
	if err := s.store.Progress.SetFlow(domain.FlowSteering); err != nil {
		slog.Error("设置流程状态失败", "module", "steer", "err", err)
	}
}

func (s *session) submitSteer(text string) {
	s.persistSteer(text)
	s.dispatchSteer(text)
}

func (s *session) finalizeSteerIfIdle() {
	runMeta, _ := s.store.RunMeta.Load()
	progress, _ := s.store.Progress.Load()
	if runMeta == nil || runMeta.PendingSteer == "" || progress == nil {
		return
	}
	if progress.Flow != domain.FlowSteering {
		return
	}
	s.clearHandledSteer()
}

func (s *session) dispatchSteer(text string) {
	if s.taskRT != nil {
		if _, err := s.taskRT.Start(domain.TaskSteerApply, "coordinator", "处理用户干预", text, taskLocation{}); err != nil {
			slog.Error("启动干预任务失败", "module", "task", "err", err)
		}
	}
	if s.agents != nil {
		s.agents.Start("coordinator", "", domain.TaskSteerApply, "正在评估用户干预")
	}
	s.coordinator.Steer(agentcore.UserMsg(s.buildSteerMessage(text)))
}

func (s *session) buildSteerMessage(text string) string {
	runMeta, err := s.store.RunMeta.Load()
	if err != nil {
		slog.Warn("读取运行元信息失败", "module", "steer", "err", err)
	}
	guidance := recovery.PlanningTierGuidance(runMeta)
	message := fmt.Sprintf("[用户干预] %s\n请评估影响范围，决定是否需要修改设定或重写已有章节。", text)
	if guidance != "" {
		message += "\n" + guidance
	}
	return message
}

func (s *session) clearHandledSteer() {
	if err := s.store.ClearHandledSteer(); err != nil {
		slog.Error("清除干预状态失败", "module", "host", "err", err)
	}
}

func (s *session) saveCheckpoint(label string) {
	progress, _ := s.store.Progress.Load()
	if err := s.store.RunMeta.SaveCheckpoint(label, progress); err != nil {
		slog.Error("保存检查点失败", "module", "host", "label", label, "err", err)
	}
}

// ---------------------------------------------------------------------------
// Policy action execution
// ---------------------------------------------------------------------------

func (s *session) executePolicyActions(actions []action.Action, emit emitFn) {
	for _, act := range actions {
		s.recorder.logControlAction(act)
		switch act.Kind {
		case action.KindEmitNotice:
			if emit != nil {
				category := act.Category
				if category == "" {
					category = "SYSTEM"
				}
				emit(UIEvent{
					Time:     time.Now(),
					Category: category,
					Summary:  act.Summary,
					Level:    act.Level,
				})
			}
		case action.KindFollowUp:
			if s.enqueueCtrl != nil {
				if err := s.enqueueCtrl(domain.ControlIntent{
					Kind:     domain.ControlIntentFollowUp,
					Priority: domain.RuntimePriorityControl,
					Summary:  truncateLog(act.Message, 80),
					Message:  act.Message,
				}); err != nil {
					slog.Error("follow_up 入队失败", "module", "control", "err", err)
					s.coordinator.FollowUp(agentcore.UserMsg(act.Message))
				}
			} else {
				s.coordinator.FollowUp(agentcore.UserMsg(act.Message))
			}
		case action.KindSetFlow:
			if err := s.store.Progress.SetFlow(act.Flow); err != nil {
				slog.Error("设置流程状态失败", "module", "host", "flow", act.Flow, "err", err)
			}
			progress, _ := s.store.Progress.Load()
			if err := s.scheduler.SyncFlow(act.Flow, progress); err != nil {
				slog.Error("同步任务队列失败", "module", "task", "flow", act.Flow, "err", err)
			}
		case action.KindSetPendingRewrites:
			if err := s.store.Progress.SetPendingRewrites(act.Chapters, act.Reason); err != nil {
				slog.Error("设置待处理章节失败", "module", "host", "chapters", act.Chapters, "err", err)
			}
		case action.KindCompleteRewrite:
			if err := s.store.Progress.CompleteRewrite(act.Chapter); err != nil {
				slog.Error("完成重写标记失败", "module", "host", "chapter", act.Chapter, "err", err)
				continue
			}
			if s.taskRT != nil {
				_ = s.taskRT.ClearQueued(domain.TaskChapterRewrite, act.Chapter)
				_ = s.taskRT.ClearQueued(domain.TaskChapterPolish, act.Chapter)
			}
			updated, _ := s.store.Progress.Load()
			if updated != nil && len(updated.PendingRewrites) == 0 {
				if err := s.scheduler.SyncFlow(domain.FlowWriting, updated); err != nil {
					slog.Error("同步写作任务失败", "module", "task", "err", err)
				}
				s.saveCheckpoint("rewrite-done")
				if emit != nil {
					emit(UIEvent{
						Time:     time.Now(),
						Category: "SYSTEM",
						Summary:  "所有重写/打磨已完成",
						Level:    "success",
					})
				}
			}
		case action.KindClearHandledSteer:
			s.clearHandledSteer()
			if s.taskRT != nil {
				_ = s.taskRT.CompleteActive("coordinator")
				_ = s.taskRT.ClearQueued(domain.TaskSteerApply, 0)
			}
			if s.agents != nil {
				s.agents.Idle("coordinator", "干预处理完成")
			}
			progress, _ := s.store.Progress.Load()
			targetFlow := domain.FlowWriting
			if progress != nil && progress.Flow != "" {
				targetFlow = progress.Flow
			}
			if err := s.scheduler.SyncFlow(targetFlow, progress); err != nil {
				slog.Error("恢复任务队列失败", "module", "task", "flow", targetFlow, "err", err)
			}
		case action.KindSaveCheckpoint:
			s.saveCheckpoint(act.Label)
		case action.KindSaveHandoff:
			if err := saveHandoffSnapshot(s.store, act.Label); err != nil {
				slog.Error("保存交接包失败", "module", "host", "label", act.Label, "err", err)
			}
		case action.KindMarkComplete:
			if err := s.store.Progress.MarkComplete(); err != nil {
				slog.Error("标记完成失败", "module", "host", "err", err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Commit & review signal handling
// ---------------------------------------------------------------------------

// handleSubAgentDone 在每次 SubAgent 调用完成后读取文件系统信号，注入确定性任务。
// 返回 true 表示检测到 commit 信号（Writer 正常完成）。
func (s *session) handleSubAgentDone(emit emitFn) bool {
	result, err := s.store.Signals.LoadAndClearLastCommit()
	if err != nil || result == nil {
		return false
	}
	if s.taskRT != nil {
		_ = s.taskRT.AttachOutputRef("writer", fmt.Sprintf("chapters/%02d.md", result.Chapter))
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

	progress, _ := s.store.Progress.Load()
	runMeta, _ := s.store.RunMeta.Load()
	actions := policy.EvaluateCommit(progress, runMeta, result)
	s.executePolicyActions(actions, emit)
	updated, _ := s.store.Progress.Load()
	if err := s.scheduler.AfterCommit(updated, result, actions); err != nil {
		slog.Error("提交后同步任务失败", "module", "task", "chapter", result.Chapter, "err", err)
	}
	return true
}

// handleEditorDone 在 Editor SubAgent 完成后读取审阅信号。
func (s *session) handleEditorDone(emit emitFn) {
	review, err := s.store.Signals.LoadAndClearLastReview()
	if err != nil {
		slog.Error("加载审阅信号失败", "module", "host", "err", err)
		return
	}
	if review == nil {
		return
	}
	if s.taskRT != nil {
		_ = s.taskRT.AttachOutputRef("editor", reviewOutputRef(*review))
	}

	criticalN := review.CriticalCount()
	slog.Info("审阅信号", "module", "host",
		"verdict", review.Verdict, "issues", len(review.Issues),
		"critical", criticalN, "errors", review.ErrorCount())

	if review.Verdict == "accept" && criticalN > 0 {
		slog.Warn("critical 问题但 verdict=accept，强制升级为 rewrite", "module", "host", "critical", criticalN)
		review.Verdict = "rewrite"
	}
	s.recorder.recordEvidence("editor", "review_outcome", fmt.Sprintf("review.ch%02d", review.Chapter), buildReviewOutcomeEvidence(*review))
	runMeta, _ := s.store.RunMeta.Load()
	actions := policy.EvaluateReview(runMeta, review)
	s.executePolicyActions(actions, emit)
}

func (s *session) autoContinueIntent() (domain.ControlIntent, bool) {
	if s == nil || s.taskRT == nil {
		return domain.ControlIntent{}, false
	}
	task, ok := nextQueuedTaskForAutoContinue(s.taskRT.Snapshot())
	if !ok {
		return domain.ControlIntent{}, false
	}
	message := autoContinueMessage(task)
	return domain.ControlIntent{
		Kind:      domain.ControlIntentRunTask,
		Priority:  domain.RuntimePriorityControl,
		Summary:   autoContinueSummary(task),
		Message:   message,
		TaskKind:  task.Kind,
		TaskTitle: task.Title,
		TaskInput: task.Input,
		Payload: map[string]string{
			"owner":   task.Owner,
			"chapter": fmt.Sprintf("%d", task.Chapter),
			"volume":  fmt.Sprintf("%d", task.Volume),
			"arc":     fmt.Sprintf("%d", task.Arc),
		},
	}, true
}

func nextQueuedTaskForAutoContinue(tasks []domain.TaskRecord) (domain.TaskRecord, bool) {
	for _, task := range tasks {
		if task.Status != domain.TaskQueued {
			continue
		}
		if task.Owner == coordinatorRuntimeOwner {
			continue
		}
		return task, true
	}
	for _, task := range tasks {
		if task.Status == domain.TaskQueued {
			return task, true
		}
	}
	return domain.TaskRecord{}, false
}

func autoContinueSummary(task domain.TaskRecord) string {
	return "自动续跑：" + task.Title
}

func autoContinueMessage(task domain.TaskRecord) string {
	switch task.Kind {
	case domain.TaskFoundationPlan:
		return "[系统] 检测到待处理任务：规划故事基础设定。请立即调用合适的 architect 子智能体完成基础设定规划，然后继续推进后续任务。"
	case domain.TaskChapterWrite:
		return fmt.Sprintf("[系统] 检测到待处理任务：创作第 %d 章。请立即调用 writer 创作第 %d 章，并在提交后继续自动推进后续任务。", task.Chapter, task.Chapter)
	case domain.TaskChapterReview:
		if task.Chapter > 0 {
			return fmt.Sprintf("[系统] 检测到待处理任务：评审第 %d 章。请立即调用 editor 执行评审，并根据结果继续推进。", task.Chapter)
		}
		return "[系统] 检测到待处理任务：执行章节评审。请立即调用 editor 执行评审，并根据结果继续推进。"
	case domain.TaskChapterRewrite:
		return fmt.Sprintf("[系统] 检测到待处理任务：重写第 %d 章。请立即调用 writer 重写该章，完成后继续处理后续任务。", task.Chapter)
	case domain.TaskChapterPolish:
		return fmt.Sprintf("[系统] 检测到待处理任务：打磨第 %d 章。请立即调用 writer 打磨该章，完成后继续处理后续任务。", task.Chapter)
	case domain.TaskArcExpand:
		return fmt.Sprintf("[系统] 检测到待处理任务：展开第 %d 卷第 %d 弧。请立即调用 architect_long 展开该弧的详细章节规划，然后继续写作。", task.Volume, task.Arc)
	case domain.TaskVolumeAppend:
		return "[系统] 检测到待处理任务：规划下一卷。请立即调用 architect_long 规划下一卷并更新指南针，然后继续写作。"
	case domain.TaskSteerApply:
		return "[系统] 检测到待处理任务：处理用户干预。请优先评估影响范围，处理干预，再继续后续任务。"
	case domain.TaskCoordinatorDecision:
		return "[系统] 当前仍有待处理工作。请继续协调并推进下一步，不要停止。"
	default:
		return fmt.Sprintf("[系统] 检测到待处理任务：%s。请立即处理并继续推进后续任务。", task.Title)
	}
}


func reviewOutputRef(review domain.ReviewEntry) string {
	switch review.Scope {
	case "global":
		return fmt.Sprintf("reviews/%02d-global.json", review.Chapter)
	default:
		return fmt.Sprintf("reviews/%02d.json", review.Chapter)
	}
}

func foundationOutputRef(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var payload struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return ""
	}
	switch payload.Type {
	case "premise":
		return "premise.md"
	case "outline":
		return "outline.md"
	case "layered_outline", "expand_arc", "append_volume":
		return "layered_outline.md"
	default:
		return ""
	}
}
