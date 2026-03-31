package tui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type commandPaletteItem struct {
	Name        string
	Usage       string
	Description string
	AutoExecute bool
}

func builtinCommandItems() []commandPaletteItem {
	var items []commandPaletteItem
	for _, spec := range commandSpecs() {
		items = append(items, commandPaletteItem{
			Name:        spec.Name,
			Usage:       spec.Usage,
			Description: spec.Description,
			AutoExecute: spec.AutoExecute,
		})
	}
	return items
}

func scoreCommandItem(item commandPaletteItem, query string) int {
	if query == "" {
		return 100
	}

	name := strings.ToLower(item.Name)
	desc := strings.ToLower(item.Description)
	usage := strings.ToLower(item.Usage)

	switch {
	case name == query:
		return 1200
	case strings.HasPrefix(name, query):
		return 900 - min(len(name)-len(query), 40)
	case strings.Contains(name, query):
		return 650
	case strings.Contains(desc, query):
		return 420
	case strings.Contains(usage, query):
		return 360
	default:
		return 0
	}
}

func commandCompletions(prefix string) []commandPaletteItem {
	query := strings.TrimSpace(strings.ToLower(prefix))
	items := append([]commandPaletteItem(nil), builtinCommandItems()...)
	slices.SortStableFunc(items, func(a, b commandPaletteItem) int {
		scoreA := scoreCommandItem(a, query)
		scoreB := scoreCommandItem(b, query)
		if scoreA != scoreB {
			return scoreB - scoreA
		}
		return strings.Compare(a.Name, b.Name)
	})

	var out []commandPaletteItem
	for _, item := range items {
		if scoreCommandItem(item, query) > 0 {
			out = append(out, item)
		}
	}
	return out
}

func (m *Model) clearCommandPalette() {
	m.compItems = nil
	m.compIdx = 0
	m.compActive = false
}

func (m *Model) updateCommandPalette() {
	text := strings.TrimSpace(m.textarea.Value())
	if !strings.HasPrefix(text, "/") {
		m.clearCommandPalette()
		return
	}
	if strings.ContainsAny(text, " \t") {
		m.clearCommandPalette()
		return
	}

	items := commandCompletions(strings.TrimPrefix(text, "/"))
	m.compItems = items
	m.compActive = len(items) > 0
	if !m.compActive {
		m.compIdx = 0
		return
	}
	if m.compIdx >= len(items) {
		m.compIdx = max(0, len(items)-1)
	}
}

func (m *Model) selectedCommandItem() (commandPaletteItem, bool) {
	if !m.compActive || m.compIdx < 0 || m.compIdx >= len(m.compItems) {
		return commandPaletteItem{}, false
	}
	return m.compItems[m.compIdx], true
}

func (m *Model) acceptCommandCompletion() (commandPaletteItem, bool) {
	item, ok := m.selectedCommandItem()
	if !ok {
		return commandPaletteItem{}, false
	}
	m.textarea.Reset()
	m.textarea.SetValue("/" + item.Name + " ")
	m.textarea.CursorEnd()
	m.compItems = nil
	m.compIdx = 0
	m.compActive = false
	return item, true
}

func renderCommandPalette(width int, items []commandPaletteItem, cursor int) string {
	if len(items) == 0 || width <= 0 {
		return ""
	}

	boxW := width - 2
	if boxW > 72 {
		boxW = 72
	}
	if boxW < 48 {
		boxW = 48
	}
	innerW := boxW - 2
	contentW := innerW - 4
	if contentW < 20 {
		contentW = 20
	}

	title := lipgloss.NewStyle().Foreground(colorMuted).Bold(true).Render("命令")
	lineStyle := lipgloss.NewStyle().Foreground(colorDim)
	titleLine := lineStyle.Render("┌─ ") + title + lineStyle.Render(" "+strings.Repeat("─", max(0, innerW-lipgloss.Width(title)-3))+"┐")

	start, end := commandPaletteWindow(len(items), cursor, 5)
	visible := items[start:end]
	remaining := len(items) - end

	nameStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(colorText)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	selectedNameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#1c1a14")).Background(colorAccent).Bold(true)
	selectedDescStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	var body []string
	selectedIdx := cursor - start
	for i, item := range visible {
		prefix := "  "
		nameRenderer := nameStyle
		descRenderer := descStyle
		if i == selectedIdx {
			prefix = "› "
			nameRenderer = selectedNameStyle
			descRenderer = selectedDescStyle
		}

		name := nameRenderer.Render(item.Name)
		desc := truncate(item.Description, max(12, contentW-18))
		descText := descRenderer.Render(desc)
		line := prefix + name
		gap := contentW - lipgloss.Width(line) - lipgloss.Width(descText)
		if gap < 1 {
			gap = 1
		}
		body = append(body, lineStyle.Render("│ ")+line+strings.Repeat(" ", gap)+descText+lineStyle.Render(" │"))
	}

	if selectedIdx < 0 || selectedIdx >= len(visible) {
		selectedIdx = 0
	}
	hint := mutedStyle.Render("↑↓ 选择 · Tab/Enter 接受 · Esc 关闭")
	usage := "Usage: " + visible[selectedIdx].Usage
	if remaining > 0 {
		usage = usage + " · 还有 " + strconv.Itoa(remaining) + " 个命令"
	}
	body = append(body, lineStyle.Render("│ ")+mutedStyle.Render(truncate(usage, contentW))+strings.Repeat(" ", max(0, contentW-lipgloss.Width(truncate(usage, contentW))))+lineStyle.Render(" │"))
	body = append(body, lineStyle.Render("│ ")+hint+strings.Repeat(" ", max(0, contentW-lipgloss.Width(hint)))+lineStyle.Render(" │"))

	bottomLine := lineStyle.Render("└" + strings.Repeat("─", innerW) + "┘")
	return strings.Join(append(append([]string{titleLine}, body...), bottomLine), "\n")
}

func commandPaletteWindow(total, cursor, limit int) (start, end int) {
	if total <= limit {
		return 0, total
	}
	start = max(cursor-limit/2, 0)
	end = min(start+limit, total)
	if end-start < limit {
		start = max(end-limit, 0)
	}
	return start, end
}
