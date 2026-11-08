package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/host"
)

func restrictedCommandModel(t *testing.T, name string) Model {
	t.Helper()
	m := Model{mode: modeRunning, textarea: textarea.New(), eventIndex: map[string]int{}, snapshot: host.UISnapshot{IsRunning: true}}
	m.textarea.SetValue("/" + name)
	m.updateCommandPalette()
	if len(m.compItems) == 0 || m.compItems[0].Name != name {
		t.Fatalf("expected /%s: %+v", name, m.compItems)
	}
	return m
}

func TestCommandPaletteBlocksIdleCommandsAndExplainsPause(t *testing.T) {
	for _, spec := range commandRegistryInstance().Visible() {
		if !spec.NeedsIdle {
			continue
		}
		t.Run(spec.Name, func(t *testing.T) {
			m := restrictedCommandModel(t, spec.Name)
			items := m.commandPaletteItems()
			out := ansi.Strip(renderCommandPalette(100, items, m.compIdx))
			if !strings.Contains(out, "暂不可用") || !strings.Contains(out, "Esc 关闭面板") || !strings.Contains(out, "再按 Esc 暂停") || strings.Contains(out, "Tab/Enter 接受") {
				t.Fatalf("面板须说明如何暂停，不能提示可直接执行: %s", out)
			}
			for _, key := range []tea.KeyType{tea.KeyEnter, tea.KeyTab} {
				next, cmd, handled := m.handleCommandPaletteKey(tea.KeyMsg{Type: key})
				got := next.(Model)
				if !handled || cmd != nil || !got.compActive || got.textarea.Value() != "/"+spec.Name || len(got.events) != 0 {
					t.Fatalf("不可用候选不能补全或执行: key=%v input=%q active=%v events=%+v", key, got.textarea.Value(), got.compActive, got.events)
				}
			}
			// 候选生成后状态变化，执行侧仍须按当前状态拒绝。
			m.clearCommandPalette()
			next, cmd := m.handleSlashCommand(slashCommand{name: spec.Name})
			got := next.(Model)
			if cmd != nil || len(got.events) != 1 || !strings.Contains(got.events[0].Summary, "请按 Esc 暂停创作") {
				t.Fatalf("直接输入也须给出暂停操作指引: %+v", got.events)
			}
		})
	}
}

func TestCommandPaletteTracksPauseWithoutRetyping(t *testing.T) {
	m := restrictedCommandModel(t, "simulate")
	// 使用真实消息处理更新状态，不重新生成搜索词或候选。
	next, _, _ := m.handleRuntimeMsg(abortResultMsg{stopped: true})
	m = next.(Model)
	if hint := m.commandPaletteItems()[0].Unavailable; !strings.Contains(hint, "正在暂停") || strings.Contains(hint, "按 Esc 暂停") {
		t.Fatalf("暂停中应等待，不能再提示发起暂停: %q", hint)
	}
	next, _, _ = m.handleRuntimeMsg(doneMsg{})
	m = next.(Model)
	items := m.commandPaletteItems()
	if items[0].Unavailable != "" {
		t.Fatalf("暂停完成后不能残留限制: %+v", items[0])
	}
	out := ansi.Strip(renderCommandPalette(100, items, m.compIdx))
	if strings.Contains(out, "暂不可用") || !strings.Contains(out, "Tab/Enter 接受") {
		t.Fatalf("暂停后须恢复正常操作提示: %s", out)
	}
	next, cmd, handled := m.handleCommandPaletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if !handled || cmd != nil || got.compActive || got.textarea.Value() != "/simulate " {
		t.Fatalf("暂停完成后应恢复补全: %+v", got.compItems)
	}
}

func TestCommandPaletteKeepsRuntimeCommandsAvailable(t *testing.T) {
	m := restrictedCommandModel(t, "help")
	if hint := m.commandPaletteItems()[0].Unavailable; hint != "" {
		t.Fatalf("不要求空闲的命令应保持可用: %q", hint)
	}
	next, _, handled := m.handleCommandPaletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled || next.(Model).help == nil {
		t.Fatal("运行中仍应能打开帮助")
	}
}

func TestCommandPaletteDoesNotRelockAfterLatePauseResult(t *testing.T) {
	m := restrictedCommandModel(t, "simulate")
	next, _, _ := m.handleRuntimeMsg(doneMsg{})
	m = next.(Model)
	next, _, _ = m.handleRuntimeMsg(abortResultMsg{stopped: true})
	m = next.(Model)
	if m.abortPending || m.commandPaletteItems()[0].Unavailable != "" {
		t.Fatal("已完成暂停后，晚到的回执不能重新锁住命令")
	}
}

func TestIdleCommandsWaitForHostPausingAndStartup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		starting bool
		state    string
		want     string
	}{
		{"pausing", false, "pausing", "正在暂停"},
		{"starting", true, "starting", "正在启动"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{starting: tc.starting, snapshot: host.UISnapshot{RuntimeState: tc.state}, eventIndex: map[string]int{}}
			next, cmd := m.handleSlashCommand(slashCommand{name: "simulate"})
			got := next.(Model)
			if cmd != nil || got.simulator != nil || len(got.events) != 1 || !strings.Contains(got.events[0].Summary, tc.want) {
				t.Fatalf("须等待真实空闲状态后再执行: %+v", got.events)
			}
		})
	}
}
