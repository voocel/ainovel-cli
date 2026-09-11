package tui

import (
	"math"
	"strconv"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestBenchThemeContrast(t *testing.T) {
	// Representative terminal surfaces are verification inputs, not application
	// backgrounds. Slightly tinted terminals must retain readable secondary text.
	for _, theme := range []struct {
		name       string
		dark       bool
		background string
	}{
		{"light", false, "#FFFFFF"}, {"light warm", false, "#F5F2EA"},
		{"dark", true, "#181818"}, {"dark warm", true, "#24211D"},
	} {
		t.Run(theme.name, func(t *testing.T) {
			resolve := func(c lipgloss.AdaptiveColor) string {
				if theme.dark {
					return c.Dark
				}
				return c.Light
			}
			for role, color := range map[string]lipgloss.AdaptiveColor{
				"text": benchColors.Text, "muted": benchColors.Muted,
				"accent": benchColors.Accent, "warning": benchColors.Warning, "error": benchColors.Error, "success": benchColors.Success,
			} {
				if ratio := themeContrast(t, resolve(color), theme.background); ratio < 4.5 {
					t.Errorf("%s contrast %.2f, want at least 4.5", role, ratio)
				}
			}
			if ratio := themeContrast(t, resolve(benchColors.SelectedText), resolve(benchColors.SelectedBackground)); ratio < 4.5 {
				t.Errorf("selection contrast %.2f, want at least 4.5", ratio)
			}
		})
	}
}

func TestBenchThemePreservesTerminalSurface(t *testing.T) {
	styles := newBenchStyles()
	for name, style := range map[string]lipgloss.Style{
		"text": styles.Text, "muted": styles.Muted, "border": styles.Border,
		"accent": styles.Accent, "warning": styles.Warning, "error": styles.Error, "title": styles.Title,
	} {
		if _, ok := style.GetBackground().(lipgloss.NoColor); !ok {
			t.Errorf("%s overrides terminal background", name)
		}
		if _, ok := style.GetForeground().(lipgloss.AdaptiveColor); !ok {
			t.Errorf("%s foreground does not adapt to terminal theme", name)
		}
	}
	if _, ok := styles.Selected.GetBackground().(lipgloss.AdaptiveColor); !ok {
		t.Fatal("selection background must adapt with its foreground")
	}
}

func themeContrast(t *testing.T, foreground, background string) float64 {
	t.Helper()
	luminance := func(hex string) float64 {
		value, err := strconv.ParseUint(hex[1:], 16, 24)
		if err != nil {
			t.Fatal(err)
		}
		linear := func(channel uint64) float64 {
			v := float64(channel) / 255
			if v <= 0.04045 {
				return v / 12.92
			}
			return math.Pow((v+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(value>>16) + 0.7152*linear((value>>8)&255) + 0.0722*linear(value&255)
	}
	a, b := luminance(foreground), luminance(background)
	return (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
}
