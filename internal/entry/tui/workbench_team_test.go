package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

func TestTeamShowsRealRolesAndKeepsHitsWithDecision(t *testing.T) {
	for _, size := range [][2]int{{150, 40}, {180, 50}, {240, 70}} {
		m := studioModel(t, size[0], size[1])
		m.bench.activity.Tasks = []activity.Task{
			{OperationID: "writer", Kind: "write_chapter", Label: "写作章节", Phase: activity.Prose, Scope: activity.Scope{ChapterNumber: 4}, StartedAt: time.Now()},
			{OperationID: "editor", Kind: "review_range", Label: "审阅章节", Phase: activity.Thinking, Scope: activity.Scope{ChapterIDs: []string{"ch-1", "ch-2"}}, StartedAt: time.Now()},
		}
		m.bench.activity.Output = []activity.OutputBlock{{OperationID: "writer", Kind: activity.Prose, Text: []byte("雨落在窗外")}}
		for _, decision := range []bool{false, true} {
			if decision {
				m.bench.decision = &decisionState{hasProposal: true, reason: "请确认人物动机", proposal: domainmodel.Proposal{ID: "candidate"}}
			}
			l := m.benchLayout()
			frame := m.teamFrame(l.inspectorWidth-2, l.bodyHeight)
			view := ansi.Strip(strings.Join(frame.lines, "\n"))
			for _, want := range []string{"编辑 · 2 章", "作者 · 第 4 章", "思考中", "正在写正文", "正文预览 5 字"} {
				if !strings.Contains(view, want) {
					t.Fatalf("%v decision=%v missing %q:\n%s", size, decision, want, view)
				}
			}
			if decision && !strings.HasPrefix(view, "需要你决定") {
				t.Fatal("decision must precede team activity")
			}
			for _, hit := range frame.hits {
				clicked := clickBench(m, l.inspectorX+2, l.bodyY+hit.start)
				if hit.review && clicked.bench.content != contentReview {
					t.Fatal("review hit shifted")
				}
				if hit.task.OperationID == "writer" && (!clicked.bench.outputFrozen || len(clicked.bench.outputHeld) != 1) {
					t.Fatal("task hit shifted when decision appeared")
				}
			}
			if lipgloss.Height(m.View()) != size[1] || lipgloss.Width(m.View()) != size[0] {
				t.Fatal("team exceeded frame")
			}
		}
	}
}

func TestLongFindingsDoNotOverwhelmInspector(t *testing.T) {
	m := studioModel(t, 180, 70)
	m.bench.snap.Manuscript = []domainmodel.ManuscriptChapter{{ID: "ch-4", Number: 4}}
	for i := 0; i < 3; i++ {
		m.bench.snap.Findings = append(m.bench.snap.Findings, workbench.WorkbenchFinding{
			ReviewFinding: domainmodel.ReviewFinding{ChapterID: "ch-4", Note: strings.Repeat("角色前后表现需要保持一致。", 30) + "详细证据末尾"},
		})
	}
	for i := range m.bench.snap.Outline {
		if m.bench.snap.Outline[i].Number == 4 {
			m.bench.snap.Outline[i].Node.Summary = "主角在雨夜找到关键线索。"
		}
	}
	lines := m.detailSummary(34, 50)
	view := ansi.Strip(strings.Join(lines, "\n"))
	if len(lines) > 16 || !strings.Contains(view, "需要留意 · 3 项") || !strings.Contains(view, "主角在雨夜找到关键线索") || strings.Contains(view, "详细证据末尾") {
		t.Fatalf("long findings overwhelmed the chapter context:\n%s", view)
	}
	if !strings.Contains(m.detailReport(100), "详细证据末尾") {
		t.Fatal("excerpt lost the full finding")
	}
}

func TestInspectorActionHitboxes(t *testing.T) {
	m := studioModel(t, 180, 70)
	m.bench.snap.Manuscript = []domainmodel.ManuscriptChapter{{ID: "ch-4", Number: 4}}
	m.bench.snap.Findings = []workbench.WorkbenchFinding{{ReviewFinding: domainmodel.ReviewFinding{ChapterID: "ch-4", Note: "需要核对人物动机"}}}
	for i := range m.bench.snap.Outline {
		if m.bench.snap.Outline[i].Number == 4 {
			m.bench.snap.Outline[i].Node.Summary = "本章寻找失踪信件"
		}
	}
	l := m.benchLayout()
	frame := m.teamFrame(l.inspectorWidth-2, l.bodyHeight)
	count := 0
	for _, hit := range frame.hits {
		if hit.detail == "" {
			continue
		}
		count++
		x, y := l.inspectorX+1+hit.x, l.bodyY+hit.start
		opened := clickBench(m, x, y)
		if !opened.bench.reading || opened.bench.bodyText != hit.detail {
			t.Fatal("action did not open its own content")
		}
		for _, point := range [][2]int{{x - 1, y}, {x + hit.width, y}, {x, y - 1}, {l.inspectorX + 1, y - 2}} {
			if clickBench(m, point[0], point[1]).bench.reading {
				t.Fatalf("nearby title, preview or whitespace is clickable: %v", point)
			}
		}
		if strings.Contains(hit.detail, "完整问题") && strings.Contains(hit.detail, "失踪信件") {
			t.Fatal("problem action included the chapter target")
		}
	}
	if count != 2 {
		t.Fatalf("want independent problem and goal actions, got %d", count)
	}
	full := m.detailFrame(34, 50)
	for _, hit := range full.hits {
		if hit.end == len(full.lines) {
			continue // No clipping occurs when the final action exactly fits.
		}
		clipped := m.detailFrame(34, hit.start+1)
		for _, visible := range clipped.hits {
			if visible.start >= len(clipped.lines)-1 {
				t.Fatal("omission hint retained an invisible action")
			}
		}
	}
}

func TestTeamKeepsChapterScannableAndOpensFullContext(t *testing.T) {
	m := studioModel(t, 180, 70)
	for i := range m.bench.snap.Outline {
		if m.bench.snap.Outline[i].Number == 4 {
			m.bench.snap.Outline[i].Node.Summary = strings.Repeat("雨夜追踪线索，", 20) + "目标末尾"
		}
	}
	l := m.benchLayout()
	view := ansi.Strip(strings.Join(m.teamFrame(l.inspectorWidth-2, l.bodyHeight).lines, "\n"))
	if strings.Contains(view, "目标末尾") || !strings.Contains(view, "…") || strings.Contains(view, "暂无待决定事项") || strings.Contains(view, "审阅通过") {
		t.Fatalf("room wasted or result invented:\n%s", view)
	}
	frame := m.teamFrame(l.inspectorWidth-2, l.bodyHeight)
	for _, hit := range frame.hits {
		if hit.detail != "" {
			m = clickBench(m, l.inspectorX+1+hit.x, l.bodyY+hit.start)
		}
	}
	if !m.bench.reading || !strings.Contains(m.bench.bodyText, "目标末尾") {
		t.Fatal("full context must remain accessible")
	}
	if taskRole(activity.Task{Label: "作者正在写作"}) != "创作助手" {
		t.Fatal("role inferred from arbitrary label")
	}
}
