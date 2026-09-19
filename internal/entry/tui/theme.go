package tui

import "github.com/charmbracelet/lipgloss"

// Keep the terminal's own surface; only selected controls paint a background.
// Separate foreground pairs preserve legibility on both light and dark themes.
var benchColors = struct {
	Text, Muted, Border, Accent      lipgloss.AdaptiveColor
	SelectedText, SelectedBackground lipgloss.AdaptiveColor
	Placeholder, InputBackground     lipgloss.AdaptiveColor
	Warning, Error, Success          lipgloss.AdaptiveColor
}{
	Text:               lipgloss.AdaptiveColor{Light: "#252F30", Dark: "#DDE3DD"},
	Muted:              lipgloss.AdaptiveColor{Light: "#56635D", Dark: "#A8B5AC"},
	Border:             lipgloss.AdaptiveColor{Light: "#86948A", Dark: "#53645A"},
	Accent:             lipgloss.AdaptiveColor{Light: "#356B58", Dark: "#A5C6AA"},
	SelectedText:       lipgloss.AdaptiveColor{Light: "#244A39", Dark: "#DCE9DC"},
	SelectedBackground: lipgloss.AdaptiveColor{Light: "#DDE7D9", Dark: "#2B3C30"},
	Placeholder:        lipgloss.AdaptiveColor{Light: "#8B9294", Dark: "#8E9699"},
	InputBackground:    lipgloss.AdaptiveColor{Light: "#ECEEEF", Dark: "#303438"},
	Warning:            lipgloss.AdaptiveColor{Light: "#92541B", Dark: "#F0B477"},
	Error:              lipgloss.AdaptiveColor{Light: "#AD352F", Dark: "#FF928B"},
	Success:            lipgloss.AdaptiveColor{Light: "#32633B", Dark: "#8BCB94"},
}

type benchStyles struct {
	Text, Muted, Border, Accent lipgloss.Style
	Selected, Warning, Error    lipgloss.Style
	Title                       lipgloss.Style
}

var benchTheme = newBenchStyles()

func newBenchStyles() benchStyles {
	return benchStyles{
		Text:     lipgloss.NewStyle().Foreground(benchColors.Text),
		Muted:    lipgloss.NewStyle().Foreground(benchColors.Muted),
		Border:   lipgloss.NewStyle().Foreground(benchColors.Border),
		Accent:   lipgloss.NewStyle().Foreground(benchColors.Accent),
		Selected: lipgloss.NewStyle().Foreground(benchColors.SelectedText).Background(benchColors.SelectedBackground).Bold(true),
		Warning:  lipgloss.NewStyle().Foreground(benchColors.Warning).Bold(true),
		Error:    lipgloss.NewStyle().Foreground(benchColors.Error).Bold(true),
		Title:    lipgloss.NewStyle().Foreground(benchColors.Text).Bold(true),
	}
}
