package tui

import "strings"

func (m model) entryWidth() int { return min(76, max(1, m.width-4)) }

// Entry forms share the home/workbench surface and keep feedback and navigation
// in a fixed footer, rather than recentering the screen as fields change.
func (m model) entryPage(title string, content []string, problem, hint string) string {
	w, h := max(1, m.width), max(1, m.height)
	inner := m.entryWidth()
	pad := strings.Repeat(" ", (w-inner)/2)
	lines := []string{benchTheme.Accent.Render("AINOVEL") + benchTheme.Muted.Render(" / "+title), benchRule(inner), ""}
	lines = append(lines, content...)
	lines = fitBlock(strings.Join(lines, "\n"), inner, h-3)
	lines = append(lines, benchRule(inner), benchTheme.Error.Render(truncate(problem, inner)), benchTheme.Muted.Render(truncate(hint, inner)))
	for i, line := range lines {
		lines[i] = pad + fitLine(line, inner)
	}
	return strings.Join(fitBlock(strings.Join(lines, "\n"), w, h), "\n")
}
