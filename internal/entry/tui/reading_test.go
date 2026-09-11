package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestReadingWrapPreservesTextAndPunctuation(t *testing.T) {
	for _, text := range []string{"保留上一章的雨声作为过渡。门后的人说：“你来晚了。”", "中文👨‍👩‍👧‍👦与é混排，继续故事。", strings.Repeat("abcdefgh", 20)} {
		for _, width := range []int{8, 14, 40} {
			lines := readingLines(text, width)
			if strings.Join(lines, "") != text {
				t.Fatal("wrapping changed source text")
			}
			for _, line := range lines {
				if lipgloss.Width(line) > width {
					t.Fatalf("line exceeds %d cells: %q", width, line)
				}
				if strings.HasPrefix(line, "，") || strings.HasPrefix(line, "。") || strings.HasPrefix(line, "：") || strings.HasPrefix(line, "”") {
					t.Fatalf("closing punctuation at line start: %q", line)
				}
			}
		}
	}
	if got := strings.Join(readingLines("第一段\n\n第二段", 40), "\n"); got != "第一段\n\n第二段" {
		t.Fatal("paragraph spacing changed")
	}
}
