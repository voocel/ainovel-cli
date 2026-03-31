package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type helpState struct {
	viewport viewport.Model
}

func newHelpState(width, height int) *helpState {
	boxW, boxH := reportModalSize(width, height)
	contentW := boxW - 6
	text := renderHelpText(contentW)

	vp := viewport.New(contentW, boxH-4)
	vp.SetContent(text)
	return &helpState{viewport: vp}
}

func renderHelpText(width int) string {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	nameStyle := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	usageStyle := lipgloss.NewStyle().Foreground(colorMuted)
	descStyle := lipgloss.NewStyle().Foreground(colorText)
	hintStyle := lipgloss.NewStyle().Foreground(colorDim)

	var b strings.Builder
	b.WriteString(titleStyle.Render("命令帮助"))
	b.WriteString("\n\n")

	for i, spec := range commandSpecs() {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(nameStyle.Render("/" + spec.Name))
		b.WriteString("\n")
		b.WriteString(usageStyle.Render("Usage: " + spec.Usage))
		b.WriteString("\n")
		b.WriteString(descStyle.Render(wrapText(spec.Description, width)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("快捷键"))
	b.WriteString("\n\n")
	for _, line := range []string{
		"输入 / 搜索命令",
		"↑↓ 选择命令候选",
		"Tab/Enter 接受补全",
		"Esc 关闭当前命令面板",
	} {
		b.WriteString(hintStyle.Render(line))
		b.WriteString("\n")
	}
	return b.String()
}

func renderHelpModal(width, height int, state *helpState) string {
	if state == nil {
		return ""
	}

	boxW, boxH := reportModalSize(width, height)
	lineStyle := lipgloss.NewStyle().Foreground(colorDim)
	titleText := lipgloss.NewStyle().Foreground(colorMuted).Bold(true).Render("命令帮助")
	hint := lineStyle.Render("  ↑↓ 滚动 · Esc 关闭")

	innerW := boxW - 2
	contentW := innerW - 4

	if state.viewport.Width != contentW {
		state.viewport.Width = contentW
		state.viewport.Height = boxH - 4
	}

	vpContent := state.viewport.View()
	titleLine := lineStyle.Render("┌─ ") + titleText + lineStyle.Render(" "+strings.Repeat("─", max(0, innerW-lipgloss.Width(titleText)-3))+"┐")
	bottomLine := lineStyle.Render("└") + hint + lineStyle.Render(strings.Repeat("─", max(0, innerW-lipgloss.Width(hint)-1))+"┘")

	bodyLines := strings.Split(vpContent, "\n")
	var body []string
	for _, line := range bodyLines {
		padding := contentW - lipgloss.Width(line)
		if padding < 0 {
			padding = 0
		}
		body = append(body, lineStyle.Render("│ ")+line+strings.Repeat(" ", padding)+lineStyle.Render(" │"))
	}

	emptyLine := lineStyle.Render("│ ") + strings.Repeat(" ", contentW) + lineStyle.Render(" │")
	for len(body) < boxH-2 {
		body = append(body, emptyLine)
	}

	all := append([]string{titleLine}, body...)
	all = append(all, bottomLine)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, strings.Join(all, "\n"))
}

func (m Model) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.help == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.help = nil
		if m.mode != modeDone {
			return m, m.textarea.Focus()
		}
		return m, nil
	case tea.KeyUp:
		m.help.viewport.ScrollUp(1)
		return m, nil
	case tea.KeyDown:
		m.help.viewport.ScrollDown(1)
		return m, nil
	case tea.KeyPgUp:
		m.help.viewport.HalfPageUp()
		return m, nil
	case tea.KeyPgDown:
		m.help.viewport.HalfPageDown()
		return m, nil
	default:
		return m, nil
	}
}
