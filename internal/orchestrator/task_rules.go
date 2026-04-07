package orchestrator

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func (s *session) handleArchitectDone() bool {
	task, ok := s.activeTask("architect")
	if !ok {
		return false
	}
	switch task.Kind {
	case domain.TaskFoundationPlan:
		missing, err := s.foundationMissing()
		if err != nil || len(missing) > 0 {
			return false
		}
	case domain.TaskArcExpand:
		if !s.arcExpanded(task.Volume, task.Arc) {
			return false
		}
	case domain.TaskVolumeAppend:
		if !s.volumeAppendedAfter(task.Volume) {
			return false
		}
	default:
		return false
	}
	s.completeOwnerTask("architect", "任务完成")
	s.resumeWritingAfterArchitectTask(task.Kind)
	return true
}

func (s *session) resumeWritingAfterArchitectTask(kind domain.TaskKind) {
	if s == nil || s.store == nil || s.scheduler == nil {
		return
	}
	progress, err := s.store.Progress.Load()
	if err != nil || progress == nil {
		return
	}

	switch kind {
	case domain.TaskFoundationPlan, domain.TaskArcExpand, domain.TaskVolumeAppend:
		if progress.Phase != domain.PhaseWriting {
			if err := s.store.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
				slog.Error("规划完成后切换写作阶段失败", "module", "task", "kind", kind, "err", err)
				return
			}
		}
	default:
		return
	}

	updated, err := s.store.Progress.Load()
	if err != nil || updated == nil {
		return
	}
	if err := s.store.Progress.SetFlow(domain.FlowWriting); err != nil {
		slog.Error("规划完成后切换写作流程失败", "module", "task", "kind", kind, "err", err)
		return
	}
	updated, _ = s.store.Progress.Load()
	if err := s.scheduler.SyncFlow(domain.FlowWriting, updated); err != nil {
		slog.Error("规划完成后同步写作任务失败", "module", "task", "kind", kind, "err", err)
	}
}

func (s *session) handleIncompleteSubAgent(inv subagentInvocation, emit emitFn) {
	task, ok := s.activeTask(canonicalAgentName(inv.Agent))
	if !ok {
		return
	}

	message, summary := s.incompleteTaskFollowUp(task)
	if strings.TrimSpace(message) == "" {
		return
	}

	s.recorder.logTaskEvent(task.Owner, "task_incomplete", "", truncateLog(summary, 120), nil)
	if emit != nil {
		emit(UIEvent{
			Time:     time.Now(),
			Category: "SYSTEM",
			Summary:  summary,
			Level:    "warn",
		})
	}

	if err := s.dispatchFollowUp(message); err != nil {
		slog.Error("未完成任务 follow_up 发送失败", "module", "task", "owner", task.Owner, "err", err)
	}
}

func (s *session) dispatchFollowUp(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	if s.continueRun != nil {
		return s.continueRun(message)
	}
	if s.coordinator != nil {
		s.coordinator.FollowUp(agentcore.UserMsg(message))
	}
	return nil
}

func (s *session) activeTask(owner string) (domain.TaskRecord, bool) {
	if s == nil || s.taskRT == nil {
		return domain.TaskRecord{}, false
	}
	return s.taskRT.ActiveTask(owner)
}

func (s *session) completeOwnerTask(owner, summary string) {
	if s.taskRT != nil {
		_ = s.taskRT.CompleteActive(owner)
	}
	if s.agents != nil {
		s.agents.Idle(owner, summary)
	}
	s.recorder.logTaskEvent(owner, "task_done", "", summary, nil)
}

func (s *session) foundationMissing() ([]string, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("store is nil")
	}
	var missing []string
	if premise, err := s.store.Outline.LoadPremise(); err != nil {
		return nil, err
	} else if premise == "" {
		missing = append(missing, "premise")
	}
	if outline, err := s.store.Outline.LoadOutline(); err != nil {
		return nil, err
	} else if len(outline) == 0 {
		missing = append(missing, "outline")
	}
	if layered, err := s.store.Outline.LoadLayeredOutline(); err != nil {
		return nil, err
	} else if len(layered) > 0 {
		compass, err := s.store.Outline.LoadCompass()
		if err != nil {
			return nil, err
		}
		if compass == nil {
			missing = append(missing, "compass")
		}
	}
	if chars, err := s.store.Characters.Load(); err != nil {
		return nil, err
	} else if len(chars) == 0 {
		missing = append(missing, "characters")
	}
	if rules, err := s.store.World.LoadWorldRules(); err != nil {
		return nil, err
	} else if len(rules) == 0 {
		missing = append(missing, "world_rules")
	}
	return missing, nil
}

func (s *session) arcExpanded(volume, arc int) bool {
	if s == nil || s.store == nil || volume <= 0 || arc <= 0 {
		return false
	}
	volumes, err := s.store.Outline.LoadLayeredOutline()
	if err != nil {
		return false
	}
	for _, v := range volumes {
		if v.Index != volume {
			continue
		}
		for _, a := range v.Arcs {
			if a.Index == arc {
				return a.IsExpanded()
			}
		}
	}
	return false
}

func (s *session) volumeAppendedAfter(volume int) bool {
	if s == nil || s.store == nil {
		return false
	}
	volumes, err := s.store.Outline.LoadLayeredOutline()
	if err != nil {
		return false
	}
	for _, v := range volumes {
		if v.Index > volume && len(v.Arcs) > 0 && v.Arcs[0].IsExpanded() {
			return true
		}
	}
	return false
}

func (s *session) incompleteTaskFollowUp(task domain.TaskRecord) (message, summary string) {
	switch task.Kind {
	case domain.TaskFoundationPlan:
		missing, err := s.foundationMissing()
		if err != nil {
			return "", ""
		}
		summary = "基础规划未完成，继续补齐缺失设定"
		message = fmt.Sprintf(
			"[系统-任务未完成]\n上一轮基础规划没有完成可持久化结果。缺失项：%s。\n请继续调用 novel_context 检查当前状态，并使用 save_foundation 补齐这些缺失项；不要只描述计划，完成落库后再结束。",
			strings.Join(missing, ", "),
		)
	case domain.TaskArcExpand:
		summary = fmt.Sprintf("第 %d 卷第 %d 弧尚未展开完成，继续规划", task.Volume, task.Arc)
		message = fmt.Sprintf(
			"[系统-任务未完成]\n上一轮弧展开任务没有完成落库。请继续调用 architect_long，并使用 save_foundation type=expand_arc, volume=%d, arc=%d 完成该弧的详细章节展开后再结束。",
			task.Volume, task.Arc,
		)
	case domain.TaskVolumeAppend:
		summary = "下一卷规划未完成，继续规划"
		message = "[系统-任务未完成]\n上一轮下一卷规划没有完成落库。请继续调用 architect_long，并使用 save_foundation type=append_volume 追加新卷、save_foundation type=update_compass 更新指南针，完成后再结束。"
	case domain.TaskChapterWrite, domain.TaskChapterRewrite, domain.TaskChapterPolish:
		summary = fmt.Sprintf("第 %d 章任务未提交，继续写作", task.Chapter)
		message = fmt.Sprintf(
			"[系统-任务未完成]\n上一轮 writer 没有完成章节提交。请继续完成第 %d 章任务，并在结束前调用 commit_chapter；不要只解释计划。",
			task.Chapter,
		)
	case domain.TaskChapterReview:
		summary = fmt.Sprintf("第 %d 章审阅未保存，继续审阅", task.Chapter)
		message = fmt.Sprintf(
			"[系统-任务未完成]\n上一轮 editor 没有保存审阅结果。请继续完成第 %d 章审阅，并在结束前调用 save_review；不要只输出说明文字。",
			task.Chapter,
		)
	}
	return message, summary
}
