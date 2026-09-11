package tui

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Prose follows Unicode line-break opportunities (including Chinese punctuation),
// while long unbroken tokens fall back to whole grapheme clusters.
func readingLines(text string, width int) []string {
	width = max(1, width)
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		start, lineWidth, breakAt, breakWidth := 0, 0, 0, 0
		graphemes := uniseg.NewGraphemes(paragraph)
		for graphemes.Next() {
			from, to := graphemes.Positions()
			cellWidth := graphemes.Width()
			if lineWidth+cellWidth > width && breakAt > start {
				lines = append(lines, paragraph[start:breakAt])
				start, lineWidth = breakAt, lineWidth-breakWidth
			}
			if lineWidth+cellWidth > width && from > start {
				lines = append(lines, paragraph[start:from])
				start, lineWidth = from, 0
			}
			lineWidth += cellWidth
			if graphemes.LineBreak() != uniseg.LineDontBreak {
				breakAt, breakWidth = to, lineWidth
			}
		}
		lines = append(lines, paragraph[start:])
	}
	return lines
}
