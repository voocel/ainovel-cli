package tui

import (
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

func (m model) visibleTasks() []activity.Task {
	feed, _ := m.activityFeed()
	var tasks []activity.Task
	// Active tasks first, then unsuccessful and completed executions.
	for _, group := range []int{0, 1, 2} {
		for i := len(feed.Tasks) - 1; i >= 0; i-- {
			t := feed.Tasks[i]
			category := 0
			if t.Done {
				category = 2
				if t.Err != "" {
					category = 1
				}
			}
			if category == group {
				tasks = append(tasks, t)
			}
			if len(tasks) == 3 {
				return tasks
			}
		}
	}
	return tasks
}

func (m model) taskRows() int {
	if count := len(m.visibleTasks()); count > 0 {
		return 2 + 2*count
	}
	return 0
}

func (m model) taskPanel(width int) []string {
	tasks := m.visibleTasks()
	if len(tasks) == 0 {
		return nil
	}
	feed, _ := m.activityFeed()
	lines := []string{sectionTitle(fmt.Sprintf("任务状态 · 最近 %d/%d", len(tasks), len(feed.Tasks)), width, false)}
	for _, task := range tasks {
		state, marker := "准备中", "●"
		switch task.Phase {
		case activity.TurnStart:
			state = "等待模型"
		case activity.Thinking:
			state = "思考中"
		case activity.Text:
			state = "输出说明"
		case activity.Prose:
			state = "生成正文"
		case activity.ToolStart, activity.ToolDelta:
			state = toolActivityLabel(task.Tool)
		case activity.Retry:
			state = "模型重试中"
		}
		end := time.Now()
		if task.Done {
			end, state, marker = task.EndedAt, "执行完成", "✓"
			if task.Err != "" {
				state, marker = "未完成 · 点击查看原因", "!"
			}
		}
		label := task.Label
		if label == "" {
			label = "创作任务"
		}
		if task.Attempt > 1 {
			label += fmt.Sprintf(" · 第 %d 次", task.Attempt)
		}
		lines = append(lines, benchTheme.Accent.Render(fitLine(marker+" "+label, width)))
		elapsed := max(0, int(end.Sub(task.StartedAt).Seconds()))
		lines = append(lines, fitLine(fmt.Sprintf("  %s · %ds", state, elapsed), width))
	}
	return append(lines, "")
}

func (m *model) inspectTask(task activity.Task) {
	feed, _ := m.activityFeed()
	var blocks []activity.OutputBlock
	for _, block := range feed.Output {
		if block.OperationID == task.OperationID {
			blocks = append(blocks, block)
		}
	}
	m.bench.notice = fmt.Sprintf("%s · 输入 %s / 输出 %s / 缓存 %s token", task.Label, formatTokens(task.Usage.Input), formatTokens(task.Usage.Output), formatTokens(task.Usage.CacheRead))
	if task.Err != "" {
		*m = m.openBody(task.Label + " · 未完成\n\n" + task.Err + "\n\n" + m.bench.notice).(model)
		return
	}
	if len(blocks) == 0 {
		m.bench.notice += " · 暂无保留的文本输出"
		return
	}
	m.switchContent(contentOutput)
	m.bench.outputHeld, m.bench.outputDropped = blocks, feed.OutputDropped
	m.bench.outputFrozen, m.bench.outputOffset = true, 0
}
