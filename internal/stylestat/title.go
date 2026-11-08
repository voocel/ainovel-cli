package stylestat

import (
	"strings"
	"unicode/utf8"
)

// TitleScanBytes 限制统计用标题的扫描与保留长度，不限制章节正文。
const TitleScanBytes = 4096

// TitleFromChapter 优先取终稿首个非空行，保留 Markdown 与章号供格式统计；
// 没有可用首行时才回落到摘要标题。独立拷贝避免标题缓存保留整章缓冲。
func TitleFromChapter(text, summaryTitle string) string {
	text = clipTitlePrefix(strings.TrimPrefix(text, "\ufeff"))
	for text != "" {
		line, rest, _ := strings.Cut(text, "\n")
		if title := strings.TrimSpace(line); title != "" {
			return strings.Clone(title)
		}
		text = rest
	}
	return strings.Clone(strings.TrimSpace(clipTitlePrefix(summaryTitle)))
}

func clipTitlePrefix(s string) string {
	if len(s) <= TitleScanBytes {
		return s
	}
	s = s[:TitleScanBytes]
	// UTF-8 截断最多留下三个不完整尾字节。
	for i := 0; i < utf8.UTFMax-1 && len(s) > 0 && !utf8.ValidString(s); i++ {
		s = s[:len(s)-1]
	}
	return s
}
