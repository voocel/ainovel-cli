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
)

func homeFixture(t *testing.T, width, height, count int) model {
	t.Helper()
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.width, m.height = width, height
	m.home.loaded = true
	m.home.chapters = 500
	for i := 0; i < count; i++ {
		m.home.library = append(m.home.library, libraryEntry{id: fmt.Sprintf("book-%03d", i), premise: fmt.Sprintf("雨夜来信 %03d", i), written: 128, target: 500, state: "等你决定"})
	}
	return m
}

func clickHome(t *testing.T, m model, focus int) (model, tea.Cmd) {
	t.Helper()
	for _, hit := range m.homeFrame().hits {
		if hit.focus == focus {
			next, cmd := m.Update(tea.MouseMsg{X: hit.x, Y: hit.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
			return next.(model), cmd
		}
	}
	t.Fatalf("control %d not visible", focus)
	return m, nil
}

func TestHomeFramesFitThemesAndLargeLibraries(t *testing.T) {
	renderer := lipgloss.DefaultRenderer()
	profile, dark := renderer.ColorProfile(), renderer.HasDarkBackground()
	t.Cleanup(func() { renderer.SetColorProfile(profile); renderer.SetHasDarkBackground(dark) })
	renderer.SetColorProfile(termenv.TrueColor)
	for _, isDark := range []bool{false, true} {
		renderer.SetHasDarkBackground(isDark)
		for _, size := range [][2]int{{30, 16}, {40, 18}, {80, 24}, {120, 40}, {180, 48}} {
			m := homeFixture(t, size[0], size[1], 100)
			m.home.focus, m.home.cursor = focusLibrary, 99
			m.home.premise.SetValue(strings.Repeat("写一个关于旧信与雨夜的故事", 30))
			for _, mode := range []string{"list", "search", "chapters"} {
				m.home.searching = mode == "search"
				m.home.editingChapters = mode == "chapters"
				frame := m.homeFrame()
				if lipgloss.Width(frame.text) > size[0] || lipgloss.Height(frame.text) != size[1] {
					t.Fatalf("frame overflow %v %s", size, mode)
				}
				if !strings.Contains(ansi.Strip(frame.text), "Esc") {
					t.Fatalf("footer missing at %v %s", size, mode)
				}
				for _, hit := range frame.hits {
					if hit.y < 0 || hit.y >= size[1]-3 || hit.x < 0 || hit.x+hit.width > size[0] {
						t.Fatalf("invalid hit %v %+v", size, hit)
					}
				}
			}
		}
	}
	if lipgloss.Width(progressBar(128, 500, 6)) != 6 {
		t.Fatal("long book expanded progress bar")
	}
}

func TestHomeSearchAndMousePreserveInputAndSelectCorrectBook(t *testing.T) {
	m := homeFixture(t, 100, 30, 100)
	m, _ = clickHome(t, m, focusChapters)
	m.home.chapterInput.SetValue("1000")
	m, _ = press(t, m, tea.KeyEnter)
	if m.home.chapters != 1000 || m.page != pageHome {
		t.Fatal("chapter editing started creation")
	}
	m, _ = clickHome(t, m, focusSearch)
	m = typeText(t, m, "099")
	if len(m.libraryIndices()) != 1 || m.home.cursor != 99 {
		t.Fatal("search did not select filtered result")
	}
	m, _ = clickHome(t, m, focusStart)
	if !m.home.searching || m.page != pageHome {
		t.Fatal("click stole focus from search input")
	}
	m, _ = press(t, m, tea.KeyEnter)
	m, cmd := clickHome(t, m, focusLibrary)
	if m.page != pageWorkbench || m.bench.projectID != "book-099" || cmd == nil {
		t.Fatal("filtered click opened wrong book")
	}
}

func TestHomePagingAndLateLoadKeepUserIntent(t *testing.T) {
	m := homeFixture(t, 80, 24, 100)
	m.home.focus = focusLibrary
	m, _ = press(t, m, tea.KeyPgDown)
	if m.home.cursor < 2 {
		t.Fatal("page navigation did not advance")
	}
	m, _ = press(t, m, tea.KeyEnd)
	if m.home.cursor != 99 {
		t.Fatal("End did not reach last book")
	}
	m.home.loaded = false
	m.home.lastOpened = "book-050"
	m.home.focus = focusPremise
	m.home.premise.SetValue("正在输入新故事")
	next, _ := m.Update(libraryLoadedMsg{entries: m.home.library})
	m = next.(model)
	if m.home.focus != focusPremise || m.home.premise.Value() != "正在输入新故事" {
		t.Fatal("late load stole story input")
	}
}

func TestEntryFormsKeepActiveInputAndFeedbackVisible(t *testing.T) {
	for _, size := range [][2]int{{40, 18}, {80, 24}, {160, 48}} {
		m := homeFixture(t, size[0], size[1], 0)
		m.page = pageWizard
		m.wizard = newWizardState(m.config, "连接失败", true)
		m.wizard.inputs[2].SetValue("secret-key-should-not-appear")
		for step := range wizardFields {
			m.wizard.step = step
			view := m.View()
			if lipgloss.Width(view) > size[0] || lipgloss.Height(view) != size[1] || strings.Contains(view, "secret-key-should-not-appear") || !strings.Contains(view, "连接失败") {
				t.Fatalf("invalid form at %v step %d", size, step)
			}
		}
	}
}

func TestHomeLongStoryInputKeepsCursorEndVisible(t *testing.T) {
	m := homeFixture(t, 80, 24, 0)
	m.home.premise.SetValue("BEGINONLY" + strings.Repeat("雨夜来信", 60) + "TAIL")
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "TAIL") || strings.Contains(view, "BEGINONLY") {
		t.Fatal("input did not scroll to the cursor")
	}
}
