package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type taskRetryState struct {
	TaskID    string
	UpdatedAt time.Time
}

func (eng *Engine) decideNextAfterIdle() bool {
	state := eng.finishLifecycleAfterIdle()
	if state != runtimeIdle {
		return false
	}
	if eng.drainPendingControls() {
		return true
	}
	if eng.hasRetryableTask() {
		return eng.retryActiveTask()
	}
	if eng.dispatchNextTask() {
		return true
	}
	return false
}

func (eng *Engine) hasRetryableTask() bool {
	if eng.taskRT == nil {
		return false
	}
	_, ok := nextRetryableTask(eng.taskRT.Snapshot())
	return ok
}

func (eng *Engine) drainPendingControls() bool {
	if eng.store == nil || eng.store.Runtime == nil {
		return false
	}
	intent, err := eng.store.Runtime.PeekControl()
	if err != nil {
		eng.emit(UIEvent{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  "检查控制队列失败: " + err.Error(),
			Level:    "error",
		})
		return false
	}
	if intent == nil {
		return false
	}
	if err := eng.drainControlQueue(); err != nil {
		eng.emit(UIEvent{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  "控制队列处理失败: " + err.Error(),
			Level:    "error",
		})
		return false
	}
	eng.mu.Lock()
	active := isRuntimeActive(eng.lifecycle)
	eng.mu.Unlock()
	return active
}

func (eng *Engine) retryActiveTask() bool {
	if eng.taskRT == nil {
		return false
	}
	task, ok := nextRetryableTask(eng.taskRT.Snapshot())
	if !ok {
		return false
	}
	if !eng.markTaskRetry(task) {
		eng.emit(UIEvent{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  "自动续跑已停止：任务重复重试但没有新进展，请检查当前任务状态",
			Level:    "error",
		})
		return false
	}
	prompt := eng.retryPromptForTask(task)
	if strings.TrimSpace(prompt) == "" {
		eng.emit(UIEvent{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  "自动续跑失败：存在未完成任务但缺少可继续执行的提示",
			Level:    "error",
		})
		return false
	}
	eng.emit(UIEvent{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  "自动续跑：" + task.Title,
		Level:    "warn",
	})
	if err := eng.startCoordinatorFollowUp(domain.TaskCoordinatorDecision, "继续协调小说任务", task.Title, taskLocation{}, prompt); err != nil {
		eng.emit(UIEvent{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  "自动续跑失败: " + err.Error(),
			Level:    "error",
		})
		return false
	}
	return true
}

func (eng *Engine) dispatchNextTask() bool {
	if eng.taskRT == nil {
		return false
	}
	task, ok := nextDispatchableTask(eng.taskRT.Snapshot())
	if !ok {
		return false
	}
	if _, err := eng.taskRT.Start(task.Kind, task.Owner, task.Title, task.Input, taskLocationFromTask(task)); err != nil {
		eng.emit(UIEvent{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  "任务派发失败: " + err.Error(),
			Level:    "error",
		})
		return false
	}
	eng.emit(UIEvent{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  "自动续跑：" + task.Title,
		Level:    "info",
	})
	if err := eng.startCoordinatorFollowUp(domain.TaskCoordinatorDecision, "继续协调小说任务", task.Title, taskLocation{}, taskDispatchPromptFromTask(task)); err != nil {
		eng.emit(UIEvent{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  "自动续跑失败: " + err.Error(),
			Level:    "error",
		})
		return false
	}
	return true
}

func (eng *Engine) markTaskRetry(task domain.TaskRecord) bool {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if eng.lastRetry.TaskID == task.ID && eng.lastRetry.UpdatedAt.Equal(task.UpdatedAt) {
		return false
	}
	eng.lastRetry = taskRetryState{
		TaskID:    task.ID,
		UpdatedAt: task.UpdatedAt,
	}
	return true
}

func nextRetryableTask(tasks []domain.TaskRecord) (domain.TaskRecord, bool) {
	for i := len(tasks) - 1; i >= 0; i-- {
		task := tasks[i]
		if task.Status != domain.TaskRunning {
			continue
		}
		if task.Owner == coordinatorRuntimeOwner {
			continue
		}
		if task.Kind == domain.TaskCoordinatorDecision {
			continue
		}
		return task, true
	}
	return domain.TaskRecord{}, false
}

func nextDispatchableTask(tasks []domain.TaskRecord) (domain.TaskRecord, bool) {
	for _, task := range tasks {
		if task.Status != domain.TaskQueued {
			continue
		}
		if task.Owner == coordinatorRuntimeOwner {
			continue
		}
		if task.Kind == domain.TaskCoordinatorDecision {
			continue
		}
		return task, true
	}
	return domain.TaskRecord{}, false
}

func taskLocationFromTask(task domain.TaskRecord) taskLocation {
	return taskLocation{
		Chapter: task.Chapter,
		Volume:  task.Volume,
		Arc:     task.Arc,
	}
}

func taskDispatchPromptFromTask(task domain.TaskRecord) string {
	return taskDispatchPrompt(domain.ControlIntent{
		TaskKind:  task.Kind,
		TaskTitle: task.Title,
		TaskInput: task.Input,
		Payload: map[string]string{
			"owner":   task.Owner,
			"chapter": fmt.Sprintf("%d", task.Chapter),
			"volume":  fmt.Sprintf("%d", task.Volume),
			"arc":     fmt.Sprintf("%d", task.Arc),
		},
	})
}

func (eng *Engine) retryPromptForTask(task domain.TaskRecord) string {
	switch task.Kind {
	case domain.TaskSteerApply:
		if eng.session != nil {
			return eng.session.buildSteerMessage(task.Input)
		}
		return strings.TrimSpace(task.Input)
	default:
		if eng.session != nil {
			message, _ := eng.session.incompleteTaskFollowUp(task)
			if strings.TrimSpace(message) != "" {
				return message
			}
		}
		return taskDispatchPromptFromTask(task)
	}
}
