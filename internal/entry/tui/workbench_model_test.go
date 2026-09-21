package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// twoConnectionConfig 有两个连接：deepseek 两个模型、proxy 一个模型，默认思考强度 high。
func twoConnectionConfig() appconfig.Config {
	cfg := appconfig.Config{Thinking: "high"}.WithProvider("deepseek", "deepseek-v4-flash", appconfig.ProviderConfig{APIKey: "test", Models: []string{"deepseek-v4-flash", "deepseek-reasoner"}})
	return cfg.WithProvider("proxy", "gpt-5", appconfig.ProviderConfig{Type: "openai", APIKey: "test", BaseURL: "https://proxy.test/v1"}).
		WithProvider("deepseek", "deepseek-v4-flash", appconfig.ProviderConfig{APIKey: "test", Models: []string{"deepseek-v4-flash", "deepseek-reasoner"}})
}

func modelPanelFixture(t *testing.T) model {
	t.Helper()
	m := studioModel(t, 150, 40)
	if err := m.api.Models.Apply(twoConnectionConfig()); err != nil {
		t.Fatal(err)
	}
	return m
}

func savedConfig(t *testing.T, m model) appconfig.Config {
	t.Helper()
	saved, err := appconfig.LoadConfig(m.deps.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestModelPanelSwitchesConnectionModelAndThinking(t *testing.T) {
	m := modelPanelFixture(t)
	m, _ = submit(t, m, "/model")
	if m.bench.modelPanel == nil {
		t.Fatal("/model must open the panel")
	}
	view := frame(m)
	requireContains(t, view, "模型 · ←→ 切换 · Tab 换行 · Enter 应用 · Esc 取消", "‹ 默认 ›", "deepseek", "deepseek-v4-flash", "思考强度", "高", "正在写的这一步用原来的模型")
	// 连接换到 proxy 后模型列表跟着换；思考强度从 high 起步向右一档是 xhigh。
	m, _ = press(t, m, tea.KeyTab)
	m, _ = press(t, m, tea.KeyRight)
	requireContains(t, frame(m), "‹ proxy ›", "gpt-5")
	m = pressTimes(t, m, tea.KeyTab, 2)
	m, _ = press(t, m, tea.KeyRight)
	requireContains(t, frame(m), "‹ 极高 ›")
	m, _ = press(t, m, tea.KeyEnter)
	if m.bench.modelPanel != nil {
		t.Fatal("Enter must close the panel on success")
	}
	if m.bench.notice != "已切换：默认 → proxy/gpt-5 · 思考 极高 · 当前任务写完后生效" {
		t.Fatalf("notice = %q", m.bench.notice)
	}
	if saved := savedConfig(t, m); saved.Provider != "proxy" || saved.Model != "gpt-5" || saved.Thinking != "xhigh" {
		t.Fatalf("saved = %#v", saved)
	}
	requireContains(t, frame(m), "gpt-5 · 思考 极高", m.bench.notice)
	// 空闲时的提示改为下个任务生效；思考强度没动过就不回写（存储意图不因面板重列被抹）。
	m.bench.writing = false
	m, _ = submit(t, m, "/model")
	m = pressTimes(t, m, tea.KeyTab, 2)
	m, _ = press(t, m, tea.KeyLeft) // proxy 只有 gpt-5：循环回自身
	m, _ = press(t, m, tea.KeyShiftTab)
	m, _ = press(t, m, tea.KeyLeft) // 连接回到 deepseek
	m, _ = press(t, m, tea.KeyEnter)
	if !strings.HasSuffix(m.bench.notice, "· 下个任务生效") || !strings.HasPrefix(m.bench.notice, "已切换：默认 → deepseek/deepseek-v4-flash · 思考 极高") {
		t.Fatalf("notice = %q", m.bench.notice)
	}
	if saved := savedConfig(t, m); saved.Provider != "deepseek" || saved.Thinking != "xhigh" {
		t.Fatalf("untouched thinking must survive the switch: %#v", saved)
	}
}

func TestModelPanelRoleOverrideAndFollowDefault(t *testing.T) {
	m := modelPanelFixture(t)
	m, _ = submit(t, m, "/model writer")
	requireContains(t, frame(m), "‹ 作者 writer ›", "跟随默认")
	// 角色行的连接首项是「跟随默认」；向右一格才是真实连接。
	m, _ = press(t, m, tea.KeyDown)
	m, _ = press(t, m, tea.KeyRight)
	m, _ = press(t, m, tea.KeyDown)
	m, _ = press(t, m, tea.KeyRight)
	requireContains(t, frame(m), "‹ deepseek-reasoner ›")
	m, _ = press(t, m, tea.KeyEnter)
	if m.bench.notice != "已切换：作者 writer → deepseek/deepseek-reasoner · 思考 高 · 当前任务写完后生效" {
		t.Fatalf("notice = %q", m.bench.notice)
	}
	if saved := savedConfig(t, m); saved.Roles["writer"] != (appconfig.RoleConfig{Provider: "deepseek", Model: "deepseek-reasoner"}) || saved.Model != "deepseek-v4-flash" {
		t.Fatalf("saved = %#v", saved)
	}
	requireContains(t, frame(m), "deepseek-v4-flash · 思考 高 · 角色 1")
	// 再打开时预选已覆盖的连接；换回「跟随默认」清除覆盖。
	m, _ = submit(t, m, "/model writer")
	requireContains(t, frame(m), "deepseek-reasoner")
	m, _ = press(t, m, tea.KeyTab)
	m, _ = press(t, m, tea.KeyLeft)
	requireContains(t, frame(m), "‹ 跟随默认 ›")
	m, _ = press(t, m, tea.KeyEnter)
	if !strings.HasPrefix(m.bench.notice, "已切换：作者 writer → 跟随默认（deepseek/deepseek-v4-flash）") {
		t.Fatalf("notice = %q", m.bench.notice)
	}
	if saved := savedConfig(t, m); len(saved.Roles) != 0 {
		t.Fatalf("follow default must drop the override: %#v", saved.Roles)
	}
	if strings.Contains(frame(m), "角色 1") {
		t.Fatal("footer must stop counting the cleared role")
	}
}

func TestModelPanelEscapeErrorsAndMouse(t *testing.T) {
	m := modelPanelFixture(t)
	m, _ = submit(t, m, "/model narrator")
	if m.bench.modelPanel != nil || !strings.Contains(m.bench.err, "用法：/model [architect|writer|editor]") {
		t.Fatalf("unknown role: panel=%v err=%q", m.bench.modelPanel != nil, m.bench.err)
	}
	m, _ = submit(t, m, "/model")
	m, _ = press(t, m, tea.KeyEsc)
	if m.bench.modelPanel != nil || m.bench.notice != "" || savedConfig(t, m).Configured() {
		t.Fatal("Esc must close the panel without applying or saving")
	}
	m, _ = submit(t, m, "/model")
	l := m.benchLayout()
	start := max(l.bodyY+benchTabRows, l.bodyY+benchTabRows+l.contentRows-modelPanelRows)
	updated, _ := m.Update(tea.MouseMsg{X: l.mainX + 2, Y: start + 1 + int(panelThinking), Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = updated.(model)
	if m.bench.modelPanel == nil || m.bench.modelPanel.focus != panelThinking {
		t.Fatalf("clicking a panel row must focus it: %+v", m.bench.modelPanel)
	}
	// 面板打开时方向键不再移动目录光标。
	cursor := m.bench.cursor
	m, _ = press(t, m, tea.KeyUp)
	if m.bench.cursor != cursor || m.bench.modelPanel.focus != panelModel {
		t.Fatal("arrow keys must stay inside the panel")
	}
	// 连接没有已保存模型时提示去模型设置。
	empty := appconfig.Config{}.WithProvider("bare", "", appconfig.ProviderConfig{Type: "openai", APIKey: "test", BaseURL: "https://bare.test/v1"})
	empty.Provider, empty.Model = "deepseek", "deepseek-v4-flash"
	empty = empty.WithProvider("deepseek", "deepseek-v4-flash", appconfig.ProviderConfig{APIKey: "test"})
	if err := m.api.Models.Apply(empty); err != nil {
		t.Fatal(err)
	}
	m.bench.modelPanel = nil
	m, _ = submit(t, m, "/model")
	m, _ = press(t, m, tea.KeyTab)
	m, _ = press(t, m, tea.KeyLeft)
	requireContains(t, frame(m), "‹ bare ›", "（无已保存模型）")
	m, _ = press(t, m, tea.KeyEnter)
	if m.bench.modelPanel == nil {
		t.Fatal("a connection without models must keep the panel open")
	}
	requireContains(t, frame(m), "! 这个连接还没有保存模型")
}
