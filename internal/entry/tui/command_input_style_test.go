package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestCommandInputHighlightsOnlyRegisteredCommands(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	m := Model{textarea: textarea.New()}
	m.textarea.Focus()

	for _, input := range []string{"/config", "/model writer", "/plan"} { // /plan 是 /cocreate 别名
		m.textarea.SetValue(input)
		m.syncCommandInputHighlight()
		if m.commandToken == "" {
			t.Errorf("已注册命令 %q 应被识别", input)
		}
		plain := m.textarea.View()
		if colored := highlightCommandToken(plain, input, m.commandToken); colored == plain {
			t.Errorf("已注册命令 %q 的实际渲染没有变色", input)
		}
	}

	for _, input := range []string{"普通输入", "/con", "/unknown"} {
		m.textarea.SetValue(input)
		m.syncCommandInputHighlight()
		if m.commandToken != "" {
			t.Errorf("非完整命令 %q 不应高亮，token=%q", input, m.commandToken)
		}
	}
}

func TestCommandInputDoesNotHighlightArguments(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	m := Model{textarea: textarea.New()}
	m.textarea.Focus()
	m.textarea.SetValue("/reopen 继续创作")
	m.textarea.CursorEnd()
	m.syncCommandInputHighlight()

	plainView := m.textarea.View()
	view := highlightCommandToken(plainView, m.textarea.Value(), m.commandToken)
	if stripped := ansi.Strip(view); stripped != ansi.Strip(plainView) {
		t.Fatalf("高亮不应改变输入内容: %q", stripped)
	}
	if !strings.Contains(view, "/reopen"+resetForeground+" 继续创作") {
		t.Fatalf("命令后的参数没有恢复正文颜色: %q", view)
	}
}
