package tui

import (
	"strings"

	"github.com/rivo/uniseg"
)

// readingLines 是正文与中文长文的唯一折行：按 Unicode 断行机会（含中文标点）换行，
// 超长的不可断片段退回按字素簇硬切。带样式的界面块只经 fitBlock 裁切，不走这里。
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
