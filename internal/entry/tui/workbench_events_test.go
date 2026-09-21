package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

func TestFailureEventRowsAndDiagnosticHitboxes(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.activity.Entries = []activity.Entry{
		{ID: 1, OperationID: "earlier", Tool: "authority_read", Done: true},
		{ID: 2, OperationID: "failed-task", Tool: "proposal_submit", Done: true, Err: strings.Repeat("正文与工作稿不一致，", 30)},
	}
	l := m.benchLayout()
	rows := m.activityRows(l.mainWidth-2, l.activityRows-2)
	failures := 0
	for i, row := range rows {
		if lipgloss.Width(row.text) > l.mainWidth-2 {
			t.Fatal("event overflowed its column")
		}
		if row.operationID != "failed-task" {
			continue
		}
		failures++
		opened, cmd := m.handleBenchMouse(tea.MouseMsg{X: l.mainX + 2, Y: l.activityY + 1 + i, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		next := opened.(model)
		if cmd == nil || next.bench.diag == nil || next.bench.diag.request.OperationID != "failed-task" {
			t.Fatal("error row opened the wrong diagnostic scope")
		}
		next.bench.diag.cancel()
	}
	if failures != 2 {
		t.Fatalf("failure should occupy two rows, got %d", failures)
	}
	for offset := 0; offset < 2; offset++ {
		m.bench.feedOffset = offset
		for limit := 1; limit < 8; limit++ {
			if got := len(m.activityRows(60, limit)); got > limit {
				t.Fatalf("offset=%d limit=%d got=%d", offset, limit, got)
			}
		}
	}
}
