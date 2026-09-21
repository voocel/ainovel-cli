package tui

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

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
