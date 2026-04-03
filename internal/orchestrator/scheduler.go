package orchestrator

import (
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// taskScheduler 负责把流程状态和提交结果转换成领域任务队列。
// 它只做任务编排，不处理 Agent 执行细节。
type taskScheduler struct {
	taskRT *novelTaskRuntime
}

func newTaskScheduler(taskRT *novelTaskRuntime) *taskScheduler {
	return &taskScheduler{taskRT: taskRT}
}

func (s *taskScheduler) SeedStartup(prompt string) error {
	if s == nil || s.taskRT == nil {
		return nil
	}
	_, err := s.taskRT.Queue(domain.TaskFoundationPlan, "architect", "规划故事基础设定", prompt, taskLocation{})
	return err
}

func (s *taskScheduler) SeedRecovery(progress *domain.Progress, runMeta *domain.RunMeta) error {
	if s == nil || s.taskRT == nil {
		return nil
	}
	tasks := s.taskRT.Snapshot()
	for _, task := range tasks {
		if !task.IsTerminal() {
			return nil
		}
	}

	switch {
	case runMeta != nil && strings.TrimSpace(runMeta.PendingSteer) != "":
		_, err := s.taskRT.Queue(domain.TaskSteerApply, "coordinator", "处理用户干预", runMeta.PendingSteer, taskLocation{})
		return err
	case progress != nil && len(progress.PendingRewrites) > 0:
		return s.queueRewriteTasks(progress, progress.PendingRewrites)
	case progress != nil && progress.Flow == domain.FlowReviewing:
		loc := inferTaskLocation(domain.TaskChapterReview, progress)
		_, err := s.taskRT.Queue(domain.TaskChapterReview, "editor", taskTitle(domain.TaskChapterReview, loc), "", loc)
		return err
	case progress != nil && (progress.Phase == domain.PhasePremise || progress.Phase == domain.PhaseOutline):
		_, err := s.taskRT.Queue(domain.TaskFoundationPlan, "architect", "规划故事基础设定", "", taskLocation{})
		return err
	case progress != nil && progress.IsResumable():
		return s.syncFlow(domain.FlowWriting, progress)
	default:
		_, err := s.taskRT.Queue(domain.TaskCoordinatorDecision, "coordinator", "继续协调小说任务", "", taskLocation{})
		return err
	}
}

func (s *taskScheduler) SyncFlow(flow domain.FlowState, progress *domain.Progress) error {
	if s == nil || s.taskRT == nil {
		return nil
	}
	return s.syncFlow(flow, progress)
}

func (s *taskScheduler) syncFlow(flow domain.FlowState, progress *domain.Progress) error {
	switch flow {
	case domain.FlowReviewing:
		s.clearQueuedWritingTasks()
		loc := inferTaskLocation(domain.TaskChapterReview, progress)
		_, err := s.taskRT.Queue(domain.TaskChapterReview, "editor", taskTitle(domain.TaskChapterReview, loc), "等待编辑评审", loc)
		return err
	case domain.FlowRewriting, domain.FlowPolishing:
		s.clearQueuedWritingTasks()
		if progress == nil {
			return nil
		}
		return s.queueRewriteTasks(progress, progress.PendingRewrites)
	case domain.FlowSteering:
		s.clearQueuedWritingTasks()
		_, err := s.taskRT.Queue(domain.TaskSteerApply, "coordinator", "处理用户干预", "等待协调处理", taskLocation{})
		return err
	case domain.FlowWriting:
		s.clearQueuedNonWritingTasks()
		if progress == nil || progress.Phase == domain.PhaseComplete {
			return nil
		}
		loc := inferTaskLocation(domain.TaskChapterWrite, progress)
		if loc.Chapter <= 0 {
			return nil
		}
		_, err := s.taskRT.Queue(domain.TaskChapterWrite, "writer", taskTitle(domain.TaskChapterWrite, loc), "等待写作", loc)
		return err
	default:
		return nil
	}
}

func (s *taskScheduler) AfterCommit(progress *domain.Progress, result *domain.CommitResult, actions []policyAction) error {
	if s == nil || s.taskRT == nil || progress == nil || result == nil {
		return nil
	}
	if progress.Phase == domain.PhaseComplete {
		return nil
	}
	if hasPolicyAction(actions, actionSetFlow) || hasPolicyAction(actions, actionMarkComplete) {
		return nil
	}
	if progress.Flow != "" && progress.Flow != domain.FlowWriting {
		return nil
	}
	if result.ReviewRequired || result.ArcEnd {
		return nil
	}
	if progress.TotalChapters > 0 && result.NextChapter > progress.TotalChapters {
		return nil
	}
	return s.syncFlow(domain.FlowWriting, progress)
}

func (s *taskScheduler) clearQueuedWritingTasks() {
	if s == nil || s.taskRT == nil {
		return
	}
	_ = s.taskRT.ClearQueued(domain.TaskChapterWrite, 0)
}

func (s *taskScheduler) clearQueuedNonWritingTasks() {
	if s == nil || s.taskRT == nil {
		return
	}
	_ = s.taskRT.ClearQueued(domain.TaskChapterReview, 0)
	_ = s.taskRT.ClearQueued(domain.TaskChapterRewrite, 0)
	_ = s.taskRT.ClearQueued(domain.TaskChapterPolish, 0)
	_ = s.taskRT.ClearQueued(domain.TaskSteerApply, 0)
}

func (s *taskScheduler) queueRewriteTasks(progress *domain.Progress, chapters []int) error {
	if s == nil || s.taskRT == nil || len(chapters) == 0 {
		return nil
	}
	kind := domain.TaskChapterRewrite
	if progress != nil && progress.Flow == domain.FlowPolishing {
		kind = domain.TaskChapterPolish
	}
	for _, chapter := range chapters {
		loc := taskLocation{Chapter: chapter}
		if _, err := s.taskRT.Queue(kind, "writer", taskTitle(kind, loc), "等待返工处理", loc); err != nil {
			return err
		}
	}
	return nil
}

func hasPolicyAction(actions []policyAction, kind policyActionKind) bool {
	for _, action := range actions {
		if action.Kind == kind {
			return true
		}
	}
	return false
}
