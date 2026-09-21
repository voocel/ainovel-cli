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

func TestOutputKeepsAllChaptersAndOnlyThreeTabs(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.snap.Manuscript = []domainmodel.ManuscriptChapter{{ID: "chapter-one", Number: 1}, {ID: "chapter-two", Number: 2}}
	m.bench.activity.Output = []activity.OutputBlock{
		{ID: 1, Scope: activity.Scope{ChapterNumber: 1}, Text: []byte("第一章")},
		{ID: 2, Scope: activity.Scope{ChapterNumber: 2}, Text: []byte("第二章")},
		{ID: 3, Scope: activity.Scope{ChapterIDs: []string{"chapter-one", "chapter-two"}}, Text: []byte("跨章审阅")},
		{ID: 4, Text: []byte("没有章节归属")},
	}
	m.selectOutline(0)
	m.switchContent(contentOutput)
	blocks, _ := m.outputBlocks()
	if len(blocks) != 4 {
		t.Fatalf("chapter selection filtered output: %+v", blocks)
	}
	m.bench.outputFrozen, m.bench.outputHeld = true, blocks
	m.selectOutline(1)
	m.switchContent(contentOutput)
	blocks, _ = m.outputBlocks()
	if !m.bench.outputFrozen || len(blocks) != 4 {
		t.Fatal("chapter selection changed frozen output")
	}
	m, _ = submit(t, m, "/follow")
	if m.bench.outputFrozen || m.bench.pinned {
		t.Fatal("follow did not restore live output")
	}
	l := m.benchLayout()
	m = clickBench(m, l.mainX+1+len(contentLabels)*contentTabWidth+2, l.contentY)
	m, _ = press(t, m, tea.KeyF4)
	blocks, _ = m.outputBlocks()
	if m.bench.content != contentOutput || len(blocks) != 4 {
		t.Fatal("removed controls still affect output")
	}
	tabs := m.contentTabs(100)
	for _, label := range []string{"F1 实时输出", "F2 正文", "F3 审阅"} {
		if !strings.Contains(tabs, label) {
			t.Fatalf("missing tab %q", label)
		}
	}
	for _, label := range []string{"F4", "当前章节", "本轮全部"} {
		if strings.Contains(tabs, label) {
			t.Fatalf("removed control remains: %q", label)
		}
	}
}

func TestManuscriptUsesAvailableColumnWidth(t *testing.T) {
	m := studioModel(t, 240, 50)
	m.bench.snap.Manuscript = []domainmodel.ManuscriptChapter{{Number: 1, Blocks: []domainmodel.ManuscriptBlock{{Text: strings.Repeat("正文", 100)}}}}
	m.selectOutline(0)
	lines := m.prosePanel(140, 10)
	if lipgloss.Width(lines[3]) <= 96 {
		t.Fatal("manuscript still uses the old narrow measure")
	}
}
