package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

func longWorkbench(count int) model {
	m := model{ctx: context.Background(), width: 150, height: 40, page: pageWorkbench}
	m.bench = newWorkbenchState("long", 1)
	m.bench.loaded, m.bench.writing = true, true
	m.bench.snap.Intent.TargetChapters = count + 500
	m.bench.snap.Run = &domainmodel.CreationRun{ID: "run", State: domainmodel.RunRunning}
	for i := 1; i <= count; i++ {
		if i%50 == 1 {
			m.bench.snap.Outline = append(m.bench.snap.Outline, workbench.OutlineNode{Node: domainmodel.PlanNode{ID: fmt.Sprintf("v%d", i), Kind: domainmodel.PlanVolume, Title: fmt.Sprintf("第%d卷", i/50+1)}})
		}
		m.bench.snap.Outline = append(m.bench.snap.Outline, workbench.OutlineNode{Node: domainmodel.PlanNode{ID: fmt.Sprint(i), Kind: domainmodel.PlanChapter, Title: fmt.Sprintf("雨夜来信 %d", i)}, Number: i, State: workbench.ChapterConfirmed})
		m.bench.snap.Manuscript = append(m.bench.snap.Manuscript, domainmodel.ManuscriptChapter{ID: fmt.Sprint(i), Number: i, Title: "雨夜来信", Blocks: []domainmodel.ManuscriptBlock{{Text: strings.Repeat("雨停了，屋檐却还在滴水。", 250)}}})
	}
	m.bench.activity = activity.Snapshot{Seq: 1, RunID: "run", Prose: []byte(strings.Repeat("雨停了，屋檐却还在滴水。", 700)), ThinkingNote: strings.Repeat("保留雨声作为过渡。", 450)}
	m.bench.activity.Output = []activity.OutputBlock{
		{ID: 1, Version: 1, Kind: activity.Thinking, Text: []byte(m.bench.activity.ThinkingNote)},
		{ID: 2, Version: 1, Kind: activity.Prose, Text: append([]byte(nil), m.bench.activity.Prose...)},
	}
	return m
}

func TestLongOutlineNavigation(t *testing.T) {
	m := longWorkbench(500)
	rows := m.outlineRows()
	if len(rows) != 511 || !rows[510].placeholder || rows[510].endChapter != 1000 {
		t.Fatalf("unplanned range was not collapsed: %d rows", len(rows))
	}
	m.bench.collapsed["v451"] = true
	if !m.findChapter("499", false) || m.selectedChapterNumber() != 499 || m.bench.collapsed["v451"] {
		t.Fatal("jump did not reveal chapter inside collapsed volume")
	}
	if !m.findChapter("雨夜来信", true) || m.selectedChapterNumber() != 500 {
		t.Fatal("next search match failed")
	}
	if m.findChapter("1001", false) {
		t.Fatal("nonexistent chapter matched")
	}
	m.bench.pane = benchPaneOutline
	m = m.navigateOutline(tea.KeyHome)
	if m.bench.cursor != 0 {
		t.Fatal("Home did not reach beginning")
	}
	m = m.navigateOutline(tea.KeyPgDown)
	if m.bench.cursor != m.benchLayout().outlineRows {
		t.Fatalf("page down moved to %d, want one viewport", m.bench.cursor)
	}
	m = m.navigateOutline(tea.KeyEnd)
	if m.selectedChapterNumber() != 500 {
		t.Fatal("End selected placeholder instead of last chapter")
	}
	for _, width := range []int{150, 180, 240} {
		m.width = width
		view := m.View()
		if lipgloss.Height(view) != 40 || lipgloss.Width(view) > width {
			t.Fatalf("long layout overflow at width %d", width)
		}
	}
}

func TestRefreshCoalescesWhileQueryIsInFlight(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.page = pageWorkbench
	m.bench = newWorkbenchState("long", 1)
	first := m.refreshBenchCmd()
	if first == nil {
		t.Fatal("first refresh missing")
	}
	for i := 0; i < 10; i++ {
		if m.refreshBenchCmd() != nil {
			t.Fatal("overlapping refresh scheduled")
		}
	}
	updated, cmd := m.Update(benchRefreshedMsg{gen: 1, snap: workbench.WorkbenchSnapshot{ProjectID: "long"}})
	m = updated.(model)
	if cmd == nil || !m.bench.refresh.busy || m.bench.refresh.pending {
		t.Fatal("coalesced refresh did not schedule exactly one follow-up")
	}
	updated, cmd = m.Update(benchRefreshedMsg{gen: 1, snap: workbench.WorkbenchSnapshot{ProjectID: "long"}})
	m = updated.(model)
	if cmd != nil || m.bench.refresh.busy {
		t.Fatal("idle refresh loop did not settle")
	}
}

func TestActivityAnchorSurvivesFullBuffer(t *testing.T) {
	deps, api := newTestDeps(t, true)
	hub := activity.NewHub()
	api.Workbench.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	m.page = pageWorkbench
	m.bench = newWorkbenchState("long", 1)
	publish := func(i int) {
		hub.Publish(activity.Event{ProjectID: "long", RunID: "run", OperationID: "op", Kind: activity.ToolStart, CallID: fmt.Sprint(i)})
	}
	update := func() { next, _ := m.Update(activityMsg{gen: 1, open: true}); m = next.(model) }
	for i := 0; i < 64; i++ {
		publish(i)
	}
	update()
	m.bench.feedOffset = 4
	anchor := m.bench.activity.Entries[59].ID
	for i := 64; i < 74; i++ {
		publish(i)
	}
	update()
	if m.bench.feedOffset != 14 || m.bench.activity.Entries[len(m.bench.activity.Entries)-m.bench.feedOffset-1].ID != anchor {
		t.Fatal("history moved when capped buffer rotated")
	}
	for i := 74; i < 150; i++ {
		publish(i)
	}
	update()
	if !strings.Contains(m.bench.notice, "保留范围") {
		t.Fatal("expired anchor was silently replaced")
	}
}

func BenchmarkLongWorkbench(b *testing.B) {
	for _, count := range []int{50, 500, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			m := longWorkbench(count)
			m.findChapter(fmt.Sprint(count), false)
			m.bench.pinned = false
			_ = m.View()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
	}
}

func BenchmarkLongWorkbenchStreaming(b *testing.B) {
	m := longWorkbench(500)
	m.findChapter("500", false)
	m.bench.pinned = false
	m.switchContent(contentOutput)
	prefix := string(m.bench.activity.Prose)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.bench.activity.Output[1].Text = []byte(prefix + fmt.Sprint(i))
		m.bench.activity.Output[1].Version = uint64(i + 2)
		_ = m.View()
	}
}

func TestLeavingWorkbenchReleasesBookCaches(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.page = pageWorkbench
	m.bench = longWorkbench(500).bench
	oldCache := m.bench.rowsCache
	_ = m.View()
	closed := false
	m.bench.activityOff = func() { closed = true }
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if !closed || m.page != pageHome || len(m.bench.snap.Manuscript) != 0 || m.bench.rowsCache == oldCache || m.bench.refresh.reader != nil {
		t.Fatal("leaving retained the workbench cache or subscription")
	}
	if m.home.lastOpened != "long" {
		t.Fatal("leaving lost library selection")
	}
}
