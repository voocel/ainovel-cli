package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

func TestWizardPresetMouseAndKeyboard(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	m := newModel(context.Background(), deps)
	next, _ := m.Update(tea.MouseMsg{X: 10, Y: 8, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = next.(model)
	if m.wizard.choosing || m.wizard.inputs[0].Value() != "deepseek" || m.wizard.step != 1 {
		t.Fatal("provider click did not open model field")
	}
	m = typeText(t, m, "my-model")
	m, _ = press(t, m, tea.KeyTab)
	m = typeText(t, m, "private-key")
	m, _ = press(t, m, tea.KeyShiftTab)
	if m.wizard.step != 1 || m.wizard.inputs[2].Value() != "private-key" {
		t.Fatal("navigation lost field values")
	}
	for _, size := range [][2]int{{150, 40}, {180, 50}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) != size[1] || !strings.Contains(view, "保存并开始") || strings.Contains(view, "private-key") {
			t.Fatalf("bad layout at %v: %s", size, view)
		}
	}
}

func TestWizardCancellationDiscardsLateSuccess(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	var cancelled bool
	deps.Verify = func(ctx context.Context, _ appconfig.Config) error { cancelled = ctx.Err() != nil; return nil }
	m := newModel(context.Background(), deps)
	m.wizard = newWizardState(appconfig.Config{Provider: "ollama", Model: "local-model"}, "", false)
	next, cmd := m.submitWizard()
	m = next.(model)
	m, _ = press(t, m, tea.KeyEsc)
	next, _ = m.Update(cmd())
	m = next.(model)
	if !cancelled || m.page != pageWizard || m.wizard.verifying {
		t.Fatal("cancelled result was accepted")
	}
	if _, err := os.Stat(appconfig.ConfigPath(deps.ConfigDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled verification saved config")
	}
	next, cmd = m.submitWizard()
	m = next.(model)
	next, _ = m.Update(cmd())
	if next.(model).page != pageWizard || next.(model).wizard.notice == "" {
		t.Fatal("retry with blank local-model key failed")
	}
}

func TestWizardValidatesAddressAndRedactsProviderErrors(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	deps.Verify = func(context.Context, appconfig.Config) error { return errors.New("rejected key secret-value") }
	m := newModel(context.Background(), deps)
	m.wizard = newWizardState(appconfig.Config{Provider: "openai", Model: "custom", APIKey: "secret-value", BaseURL: "bad-address"}, "", false)
	next, cmd := m.submitWizard()
	m = next.(model)
	if cmd == nil || m.wizard.verifying || m.wizard.step != 3 {
		t.Fatal("invalid URL was submitted")
	}
	m.wizard.inputs[3].SetValue("https://example.com/v1")
	next, cmd = m.submitWizard()
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if strings.Contains(m.View(), "secret-value") || !strings.Contains(m.wizard.err, "[已隐藏]") {
		t.Fatal("provider error leaked key")
	}
}

func TestWizardResponsiveControlsAndCollapsedAddress(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	m := newModel(context.Background(), deps)
	for _, size := range [][2]int{{150, 40}, {180, 50}} {
		m.width, m.height = size[0], size[1]
		m.wizard = newWizardState(appconfig.Config{Provider: "custom", Model: "my-model", BaseURL: "https://example.com/v1"}, "", false)
		m.wizard.custom = true
		view := m.View()
		if lipgloss.Width(view) > m.width || lipgloss.Height(view) != m.height || !strings.Contains(view, "保存并开始") {
			t.Fatalf("controls clipped at %v", size)
		}
		for _, hit := range m.wizardLayout().hits {
			if hit.action == 4 {
				continue
			}
			if hit.y >= m.height-3 {
				t.Fatalf("offscreen control at %v", size)
			}
			updated, _ := m.Update(tea.MouseMsg{X: hit.x, Y: hit.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
			next := updated.(model)
			if hit.action < 4 && next.wizard.step != hit.action {
				t.Fatalf("mouse focus mismatch at %v", size)
			}
		}
	}
	m.wizard = newWizardState(appconfig.Config{Provider: "openai", Model: "my-model"}, "", false)
	next, _ := m.focusWizard(2)
	m = next.(model)
	m, _ = press(t, m, tea.KeyTab)
	if m.wizard.step != 5 {
		t.Fatal("missing address toggle")
	}
	m, _ = press(t, m, tea.KeyTab)
	if m.wizard.step != 8 {
		t.Fatal("collapsed address traps focus")
	}
	m, _ = press(t, m, tea.KeyShiftTab)
	m, _ = press(t, m, tea.KeyEnter)
	m, _ = press(t, m, tea.KeyTab)
	if !m.wizard.advanced || m.wizard.step != 3 {
		t.Fatal("expanded address inaccessible")
	}
}

func TestWizardCustomConnectionPreservesSavedConnections(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	initial := appconfig.Config{Provider: "existing", Model: "old", Providers: map[string]appconfig.ProviderConfig{"existing": {Type: "openai", APIKey: "keep-me", Models: []string{"old"}}}}
	m := newModel(context.Background(), deps)
	m.wizard = newWizardState(initial, "", true)
	m.wizard.provider = 5
	next, _ := m.chooseWizardProvider()
	m = next.(model)
	if m.wizard.step != 7 || !m.wizard.advanced {
		t.Fatal("proxy setup must ask for name and expose address")
	}
	m.wizard.name.SetValue("我的代理")
	m.wizard.inputs[1].SetValue("custom-model")
	m.wizard.inputs[2].SetValue("new-key")
	m.wizard.inputs[3].SetValue("https://example.com/v1")
	m.wizard.endpoint = "responses"
	next, cmd := m.submitWizard()
	m = next.(model)
	msg := cmd().(wizardVerifiedMsg)
	pc, err := msg.config.ActiveProvider()
	if err != nil || msg.config.Provider != "我的代理" || pc.Type != "openai" || pc.API != "responses" || msg.config.Providers["existing"].APIKey != "keep-me" {
		t.Fatal("connection identity or credentials lost")
	}
	m, _ = press(t, m, tea.KeyEsc)
	m.wizard.name.SetValue("existing")
	next, _ = m.submitWizard()
	m = next.(model)
	if m.wizard.verifying || !strings.Contains(m.wizard.err, "已存在") {
		t.Fatal("existing connection silently overwritten")
	}
	// Choosing a saved connection restores its own credentials and protocol.
	for i, c := range m.wizardChoices() {
		if c.saved && c.name == "existing" {
			m.wizard.provider = i
		}
	}
	next, _ = m.chooseWizardProvider()
	m = next.(model)
	if m.wizard.inputs[2].Value() != "keep-me" || m.wizard.name.Value() != "existing" {
		t.Fatal("saved selection lost credentials")
	}
}

func TestWizardWideColumnsStayAligned(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	m := newModel(context.Background(), deps)
	for _, width := range []int{150, 160, 200, 240} {
		m.width, m.height = width, 40
		for _, choosing := range []bool{true, false} {
			m.wizard = newWizardState(appconfig.Config{Provider: "openai", Model: "custom-model"}, "", false)
			m.wizard.choosing = choosing
			lines := strings.Split(m.View(), "\n")
			for row := 3; row < 12; row++ {
				prefix, _, ok := strings.Cut(lines[row], "│")
				if !ok || lipgloss.Width(prefix) != max(76, width/2) {
					t.Fatalf("width=%d choosing=%v row=%d: divider at %d, want %d", width, choosing, row, lipgloss.Width(prefix), max(76, width/2))
				}
			}
		}
	}
}

func TestWizardProtocolIsSelectionOnly(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	m := newModel(context.Background(), deps)
	m.wizard.provider = 6
	next, _ := m.chooseWizardProvider()
	m = next.(model)
	next, _ = m.focusWizard(0)
	m = next.(model)
	m = typeText(t, m, "arbitrary-protocol")
	if m.wizard.inputs[0].Value() != "openai" {
		t.Fatal("protocol accepted free text")
	}
	m, _ = press(t, m, tea.KeyRight)
	if m.wizard.inputs[0].Value() != "anthropic" {
		t.Fatal("right did not select anthropic")
	}
	m.width = 160
	for _, hit := range m.wizardLayout().hits {
		if hit.action == 22 {
			next, _ = m.Update(tea.MouseMsg{X: hit.x, Y: hit.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
			m = next.(model)
			break
		}
	}
	if m.wizard.inputs[0].Value() != "gemini" {
		t.Fatal("mouse did not select gemini")
	}
	for _, control := range m.wizardControls() {
		if control == 8 {
			t.Fatal("non-OpenAI endpoint visible")
		}
	}
}

func TestWizardSaveDoesNotRequireConnectionTest(t *testing.T) {
	for _, testFirst := range []bool{false, true} {
		deps, _ := newTestDeps(t, false)
		calls := 0
		deps.Verify = func(context.Context, appconfig.Config) error { calls++; return errors.New("offline") }
		m := newModel(context.Background(), deps)
		m.wizard = newWizardState(appconfig.Config{Provider: "openai", Model: "custom", APIKey: "key"}, "", false)
		if testFirst {
			next, cmd := m.submitWizard()
			m = next.(model)
			next, _ = m.Update(cmd())
			m = next.(model)
			if m.wizard.err == "" {
				t.Fatal("failed test hidden")
			}
		}
		before := calls
		next, _ := m.activateWizard(4)
		m = next.(model)
		if m.page != pageHome || calls != before {
			t.Fatal("save required a successful network test")
		}
		saved, err := appconfig.LoadConfig(deps.ConfigDir)
		if err != nil || saved.Provider != "openai" {
			t.Fatal("configuration not saved")
		}
	}
}
