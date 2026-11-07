package domain

import "testing"

// 正文开头的章节标题行由系统统一生成：各种写法的标题行都剥掉，其他首行属正文。
func TestChapterBody(t *testing.T) {
	cases := []struct {
		name, in, title, want string
	}{
		{"plain body untouched", "他望着窗外。", "雨夜归人", "他望着窗外。"},
		{"strip h1 chinese title", "# 第 1 章  雨夜归人\n\n他望着窗外。", "雨夜归人", "他望着窗外。"},
		{"strip h2 with chapter token", "## 第二章\n\n他望着窗外。", "", "他望着窗外。"},
		{"strip plain chapter heading", "第200章 三丈线上\n\n回流的刻点。", "三丈线上", "回流的刻点。"},
		{"strip heading with book marks", "第 186 章《回流之前》\n\n回流的刻点。", "回流之前", "回流的刻点。"},
		{"strip heading with another title", "第 5 章 旧标题\n正文。", "新标题", "正文。"},
		{"strip bare title", "第一炉丹\n\n　　炉火起来了。", "第一炉丹", "　　炉火起来了。"},
		{"strip h1 matching chapter title", "# 边村浮生\n\n天还没亮。", "边村浮生", "天还没亮。"},
		{"keep body even if no header", "正文第一句。\n第二句。", "", "正文第一句。\n第二句。"},
		{"keep prose starting with an ordinal", "第二天清晨，他翻开第三章的账。\n正文。", "账", "第二天清晨，他翻开第三章的账。\n正文。"},
		{"keep non-chapter heading", "# 序章\n他望着窗外。", "边村浮生", "# 序章\n他望着窗外。"},
		{"keep h1 not matching title", "# 别的小标题\n正文。", "边村浮生", "# 别的小标题\n正文。"},
		{"single line header only", "# 第 1 章", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ChapterBody(c.in, c.title); got != c.want {
				t.Fatalf("ChapterBody(%q, %q) = %q, want %q", c.in, c.title, got, c.want)
			}
		})
	}
}

// 终稿统一写成「# 第 N 章 标题」加正文，重复规整结果不变。
func TestChapterTextIsCanonical(t *testing.T) {
	got := ChapterText(186, "回流之前", "第 186 章《回流之前》\n\n回流的刻点。")
	if want := "# 第 186 章 回流之前\n\n回流的刻点。"; got != want {
		t.Fatalf("ChapterText = %q, want %q", got, want)
	}
	if again := ChapterText(186, "回流之前", got); again != got {
		t.Fatalf("ChapterText is not idempotent: %q", again)
	}
}
