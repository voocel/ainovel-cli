package stylestat

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTitleFromChapter(t *testing.T) {
	for _, tc := range []struct {
		name, text, summary, want string
	}{
		{"终稿优先", "\n  # 第 1 章 风起  \n正文", "第九章 旧标题", "# 第 1 章 风起"},
		{"不补造章号", "# 风起\n正文", "第一章 风起", "# 风起"},
		{"摘要兜底", " \n\t", "  第一章 风起  ", "第一章 风起"},
		{"空输入", "", "", ""},
		{"BOM与换行", "\ufeff\r\n# 第 1 章 风起\r\n正文", "旧标题", "# 第 1 章 风起"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TitleFromChapter(tc.text, tc.summary); got != tc.want {
				t.Fatalf("统计用标题 = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTitleFromChapterClipsOnUTF8Boundary(t *testing.T) {
	for _, text := range []bool{false, true} {
		long := "# 第 1 章 " + strings.Repeat("甲", TitleScanBytes)
		var got string
		if text {
			got = TitleFromChapter(long+"\n正文", "摘要标题")
		} else {
			got = TitleFromChapter("", long)
		}
		if len(got) == 0 || len(got) > TitleScanBytes || !utf8.ValidString(got) || !strings.HasPrefix(got, "# 第 1 章 ") {
			t.Fatalf("长标题须保留有效 UTF-8 前缀: bytes=%d valid=%v", len(got), utf8.ValidString(got))
		}
	}
}
