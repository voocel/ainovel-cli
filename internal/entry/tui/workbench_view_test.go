package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

func studioModel(t *testing.T, width, height int) model {
	t.Helper()
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.page, m.width, m.height = pageWorkbench, width, height
	m.bench = newWorkbenchState("letters", 1)
	m.bench.loaded, m.bench.writing = true, true
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "letters", Intent: domainmodel.Intent{Premise: "亡者来信", TargetChapters: 8},
		Run: &domainmodel.CreationRun{ID: "run", State: domainmodel.RunRunning},
	}
	for i, title := range []string{"无人签收", "雨夜来客", "回信", "门后的声音"} {
		state := workbench.ChapterConfirmed
		if i == 3 {
			state = workbench.ChapterInProgress
		}
		m.bench.snap.Outline = append(m.bench.snap.Outline, workbench.OutlineNode{Node: domainmodel.PlanNode{ID: fmt.Sprint(i), Kind: domainmodel.PlanChapter, Title: title}, Number: i + 1, State: state})
	}
	m.bench.cursor = 3
	m.bench.activity = activity.Snapshot{Seq: 1, ProjectID: "letters", RunID: "run", OperationID: "writing", Thinking: true,
		ThinkingNote: "保留上一章的雨声作为过渡。门后的人可以认出他的脚步，但暂时不解释身份。",
		Prose:        []byte("雨停了，屋檐却还在滴水。\n\n陈渡站在门前，把那封没有邮戳的信翻了过来。收件人的名字已经被雨水洇开，只有最后一个“渡”字，还像一枚钉子，牢牢留在纸上。\n\n他没有敲门。\n\n门后却传来一个声音：“你今天来晚了。”"),
		Entries:      []activity.Entry{{Kind: activity.ToolStart, Tool: "authority_read", Done: true}, {Kind: activity.ToolStart, Tool: "workspace_put_chapter", Bytes: 2048}},
	}
	return m
}

func clickAction(t *testing.T, m model, key string) model {
	t.Helper()
	for _, hit := range m.workbenchFrame().hits {
		if hit.key == key {
			updated, _ := m.Update(tea.MouseMsg{X: hit.x, Y: hit.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
			return updated.(model)
		}
	}
	t.Fatalf("action %s is not visible", key)
	return m
}

func TestStudioClickAndKeyboardRespectInputFocus(t *testing.T) {
	m := studioModel(t, 120, 40)
	clicked := clickAction(t, m, "t")
	keyed, _ := pressRune(t, m, 't')
	if clicked.bench.thoughtOpen || keyed.bench.thoughtOpen {
		t.Fatal("thinking toggle did not collapse")
	}
	clicked = clickAction(t, clicked, "2")
	if clicked.bench.tab != benchTabActivity {
		t.Fatal("activity tab click did not switch")
	}
	input := clickAction(t, m, "i")
	if input.bench.prompt == nil {
		t.Fatal("requirement input not opened")
	}
	input, _ = pressRune(t, input, 't')
	input = clickAction(t, input, "t")
	if !input.bench.thoughtOpen || input.bench.prompt.input.Value() != "t" {
		t.Fatal("navigation stole input focus")
	}
	if input.bench.cursor != m.bench.cursor {
		t.Fatal("footer click changed selected chapter")
	}
}

func TestStudioFramesFitDarkAndLightTerminals(t *testing.T) {
	renderer := lipgloss.DefaultRenderer()
	profile, dark := renderer.ColorProfile(), renderer.HasDarkBackground()
	t.Cleanup(func() { renderer.SetColorProfile(profile); renderer.SetHasDarkBackground(dark) })
	renderer.SetColorProfile(termenv.TrueColor)
	var variants []string
	for _, isDark := range []bool{false, true} {
		renderer.SetHasDarkBackground(isDark)
		for _, size := range [][2]int{{40, 14}, {80, 24}, {120, 40}, {180, 48}} {
			m := studioModel(t, size[0], size[1])
			m.bench.writing = false
			m.bench.activity.ThinkingNote = strings.Repeat("很长的思考片段，含中文与🙂。", 80)
			m.bench.decision = &decisionState{reason: strings.Repeat("请检查人物动机", 30), hasProposal: true}
			for _, tab := range []benchTab{benchTabProse, benchTabActivity, benchTabDetail} {
				m.bench.tab = tab
				view := m.View()
				if lipgloss.Height(view) != size[1] {
					t.Fatalf("%v dark=%v height=%d", size, isDark, lipgloss.Height(view))
				}
				for _, line := range strings.Split(view, "\n") {
					if lipgloss.Width(line) > size[0] {
						t.Fatalf("%v overflow: %q", size, line)
					}
				}
				if !strings.Contains(ansi.Strip(view), "y 批准并继续") {
					t.Fatalf("decision clipped at %v", size)
				}
				if !strings.Contains(ansi.Strip(view), "? 更多") {
					t.Fatalf("footer clipped at %v", size)
				}
				for _, hit := range m.workbenchFrame().hits {
					if hit.y < 0 || hit.y >= size[1] || hit.x < 0 || hit.x+hit.width > size[0] {
						t.Fatalf("out-of-frame target at %v: %+v", size, hit)
					}
				}
			}
			m = m.openPrompt("directive", "对第四章的要求", strings.Repeat("输入中的中文", 30))
			if lipgloss.Width(m.View()) > size[0] || lipgloss.Height(m.View()) != size[1] {
				t.Fatalf("input frame overflow at %v", size)
			}
		}
		variants = append(variants, studioModel(t, 120, 40).View())
	}
	if variants[0] == variants[1] {
		t.Fatal("light and dark rendering used identical colors")
	}
}

func TestStudioLiveReadingFreezesUntilFollowResumes(t *testing.T) {
	m := studioModel(t, 100, 24)
	m.bench.activity.Prose = []byte(strings.Repeat("雨停了，他站在门前。\n", 60))
	m.bench.pane = benchPaneMain
	m, _ = press(t, m, tea.KeyUp)
	if !m.bench.liveHeld {
		t.Fatal("scrolling live prose did not stop following")
	}
	before := strings.Join(m.prosePanel(m.mainWidth()-4, m.workbenchFrame().proseHeight), "\n")
	m.bench.activity.Prose = append(m.bench.activity.Prose, []byte("新到达的正文，不应抢走焦点。")...)
	after := strings.Join(m.prosePanel(m.mainWidth()-4, m.workbenchFrame().proseHeight), "\n")
	if before != after {
		t.Fatal("stream changed frozen reading content")
	}
	m, _ = pressRune(t, m, 'f')
	if m.bench.liveHeld || !strings.Contains(m.View(), "新到达的正文") {
		t.Fatal("follow did not resume at latest prose")
	}
	m.bench.tab = benchTabActivity
	cursor, offset := m.bench.cursor, m.bench.previewOffset
	m, _ = press(t, m, tea.KeyUp)
	if m.bench.feedOffset != 1 || m.bench.cursor != cursor || m.bench.previewOffset != offset {
		t.Fatal("activity scroll altered chapter reading")
	}
}

func TestStudioDetailScrollStopsAtVisibleEnd(t *testing.T) {
	m := studioModel(t, 80, 14)
	m.bench.tab = benchTabDetail
	for i := 0; i < 20; i++ {
		m.bench.snap.Directives = append(m.bench.snap.Directives, domainmodel.Directive{Text: fmt.Sprintf("要求 %d", i)})
	}
	m = m.scrollBenchPane(benchPaneMain, 10000).(model)
	end := m.workbenchFrame().detailMaxOffset
	if end == 0 || m.bench.detailOffset != end {
		t.Fatalf("detail offset = %d, want visible end %d", m.bench.detailOffset, end)
	}
	m = m.scrollBenchPane(benchPaneMain, -1).(model)
	if m.bench.detailOffset != end-1 {
		t.Fatal("scrolling up from the end did not move immediately")
	}
}
