package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

func TestConsoleTabsAndSplitShareGeometry(t *testing.T) {
	for _, size := range [][2]int{{150, 40}, {180, 50}, {240, 60}} {
		for _, split := range []int{20, 25, 50} {
			m := studioModel(t, size[0], size[1])
			m.bench.splitPercent = split
			l := m.benchLayout()
			if l.activityY != l.bodyY || l.contentY != l.bodyY+l.activityRows+1 {
				t.Fatal("events must precede content")
			}
			divider := strings.Split(ansi.Strip(m.View()), "\n")[l.contentY-1]
			if !strings.Contains(divider, "├"+strings.Repeat("─", l.mainWidth)+"┤") {
				t.Fatal("event and content panels must have a continuous divider")
			}
			for i := range contentLabels {
				m = clickBench(m, l.mainX+2+i*contentTabWidth, l.contentY)
				if m.bench.content != benchContent(i) {
					t.Fatal("tab hitbox diverged")
				}
				view := m.View()
				if lipgloss.Height(view) != size[1] || lipgloss.Width(view) != size[0] {
					t.Fatalf("frame changed at %v split=%d", size, split)
				}
			}
		}
	}
}

func TestConsoleInputIsIndependentOfPendingReview(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.writing = false
	m.bench.decision = &decisionState{hasProposal: true, proposal: domainmodel.Proposal{ID: "candidate"}}
	m, cmd := submit(t, m, "让节奏更紧凑")
	if cmd == nil || m.bench.decision == nil {
		t.Fatal("ordinary requirements must not reject pending work")
	}
	m, cmd = submit(t, m, "y")
	if cmd != nil || m.bench.decision == nil || !strings.Contains(m.bench.notice, "F3") {
		t.Fatal("approval requires the review context")
	}
	m, _ = press(t, m, tea.KeyF3)
	m = typeText(t, m, "重写开头")
	m, _ = press(t, m, tea.KeyF2)
	if !m.composingReview() || !strings.Contains(ansi.Strip(m.benchContext()), "等你决定") {
		t.Fatal("navigation retargeted review feedback")
	}
	m, cmd = press(t, m, tea.KeyEnter)
	if cmd == nil || m.bench.decision != nil {
		t.Fatal("frozen review feedback was not dispatched")
	}
}

func TestOutputHistorySurvivesTaskChangeAndViewSwitch(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.activity.Output = []activity.OutputBlock{{ID: 1, Version: 1, Kind: activity.Thinking, OperationID: "old", Text: []byte(strings.Repeat("第一项任务的思考。\n", 80))}}
	m = m.scrollContent(-5)
	if !m.bench.outputFrozen {
		t.Fatal("stream did not freeze")
	}
	l := m.benchLayout()
	before := strings.Join(m.contentPanel(l.inner, l.proseRows), "\n")
	m.bench.activity.Output = []activity.OutputBlock{{ID: 2, Version: 2, Kind: activity.Text, OperationID: "new", Text: []byte("第二项任务")}}
	m.switchContent(contentManuscript)
	m.switchContent(contentOutput)
	if after := strings.Join(m.contentPanel(l.inner, l.proseRows), "\n"); after != before {
		t.Fatal("new task or tab switch moved frozen content")
	}
	m, _ = submit(t, m, "/follow")
	if m.bench.outputFrozen || !strings.Contains(m.View(), "第二项任务") {
		t.Fatal("follow did not release frozen history")
	}
}

func TestEmptyOutlineDoesNotDisableOutputPaging(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.snap.Outline = nil
	m.bench.activity.Output = []activity.OutputBlock{{ID: 1, Version: 1, Kind: activity.Thinking, Text: []byte(strings.Repeat("正在构思。\n", 80))}}
	m.bench.pane = benchPaneMain
	m, _ = press(t, m, tea.KeyPgUp)
	if !m.bench.outputFrozen {
		t.Fatal("pre-outline output cannot be paged")
	}
}
