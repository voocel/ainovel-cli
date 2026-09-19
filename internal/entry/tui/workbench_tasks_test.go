package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

func TestTaskPanelPrioritizesActiveAndLinksOwnOutput(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.activity.Tasks = []activity.Task{
		{OperationID: "active", Label: "章节写作", StartedAt: time.Now(), Phase: activity.Thinking},
		{OperationID: "done", Label: "审阅", Done: true, StartedAt: time.Now(), EndedAt: time.Now()},
	}
	m.bench.activity.Output = []activity.OutputBlock{
		{ID: 1, Version: 1, OperationID: "active", Kind: activity.Thinking, Text: []byte("写作思考")},
		{ID: 2, Version: 1, OperationID: "done", Kind: activity.Text, Text: []byte("审阅结果")},
	}
	if m.visibleTasks()[0].OperationID != "active" {
		t.Fatal("completed task displaced active one")
	}
	l := m.benchLayout()
	if lipgloss.Height(m.View()) != 40 || lipgloss.Width(m.View()) != 150 {
		t.Fatal("task panel changed frame geometry")
	}
	m = clickBench(m, l.inspectorX+2, l.bodyY+11)
	if !m.bench.outputFrozen || m.bench.content != contentOutput || len(m.bench.outputHeld) != 1 || m.bench.outputHeld[0].OperationID != "active" {
		t.Fatal("task click did not isolate its output")
	}
	m, _ = submit(t, m, "/follow")
	if m.bench.outputFrozen {
		t.Fatal("follow did not restore all output")
	}
	m.bench.activity.Tasks[0].Err = "具体失败原因"
	m.inspectTask(m.bench.activity.Tasks[0])
	if !m.bench.reading || !strings.Contains(m.bench.bodyText, "具体失败原因") {
		t.Fatal("task error is inaccessible")
	}
}
