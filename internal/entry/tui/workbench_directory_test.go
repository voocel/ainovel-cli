package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestLargeDirectoryDefaultsAndFlatRanges(t *testing.T) {
	for _, flat := range []bool{false, true} {
		m := longWorkbench(500)
		if flat {
			var chapters []workbench.OutlineNode
			for _, entry := range m.bench.snap.Outline {
				if entry.Node.Kind == domainmodel.PlanChapter {
					chapters = append(chapters, entry)
				}
			}
			m.bench.snap.Outline = chapters
		}
		m.initializeDirectory()
		if len(m.outlineRows()) != 61 || m.selectedChapterNumber() != 500 {
			t.Fatalf("flat=%v: want one open group, got %d rows at chapter %d", flat, len(m.outlineRows()), m.selectedChapterNumber())
		}
		if !m.findChapter("127", false) || m.selectedChapterNumber() != 127 {
			t.Fatal("jump failed to reveal collapsed chapter")
		}
		m.bench.input.SetValue("未提交的要求")
		m.bench.outputFrozen, m.bench.outputOffset = true, 7
		m.returnToCurrentChapter()
		if m.selectedChapterNumber() != 500 || m.bench.input.Value() != "未提交的要求" || !m.bench.outputFrozen || m.bench.outputOffset != 7 {
			t.Fatal("return-to-current lost input or frozen output")
		}
		if flat && len(m.bench.snap.Outline) != 500 {
			t.Fatal("presentation grouping changed the project outline")
		}
		if !strings.Contains(m.outlineRowLine(m.outlineRows()[0], false, 31), "50/50 章") {
			t.Fatal("volume progress is missing")
		}
		for _, width := range []int{150, 180, 240} {
			m.width = width
			if lipgloss.Width(m.View()) != width || lipgloss.Height(m.View()) != 40 {
				t.Fatal("large directory overflowed")
			}
		}
	}
}

func TestDirectoryRefreshKeepsReadingAndToolbarPreservesDraft(t *testing.T) {
	m := longWorkbench(500)
	m.initializeDirectory()
	m.findChapter("127", false)
	for i := range m.bench.snap.Outline {
		if m.bench.snap.Outline[i].Number == 499 {
			m.bench.snap.Outline[i].State = workbench.ChapterInProgress
		}
	}
	next, _ := m.Update(benchRefreshedMsg{gen: m.bench.gen, snap: m.bench.snap})
	m = next.(model)
	if m.selectedChapterNumber() != 127 {
		t.Fatal("refresh stole the reading position")
	}
	l := m.benchLayout()
	m = clickBench(m, l.leftWidth-3, l.outlineTitleY+1)
	if m.selectedChapterNumber() != 499 {
		t.Fatal("toolbar did not return to running chapter")
	}
	m.bench.input.SetValue("保留这段要求")
	m = clickBench(m, 2, l.outlineTitleY+1)
	if m.bench.input.Value() != "保留这段要求" {
		t.Fatal("search toolbar overwrote draft")
	}
	m.bench.input.SetValue("")
	m = clickBench(m, 2, l.outlineTitleY+1)
	if m.bench.input.Value() != "/" {
		t.Fatal("search toolbar did not prepare jump input")
	}
}
