package imp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitText_Chinese(t *testing.T) {
	src := `第一章 初见
张三走进客栈，要了一壶酒。

李四从角落抬起头。

第二章 离别
天亮时张三起身告辞。

第三章：决战
雪夜，二人相对。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3 chapters, got %d", len(got))
	}
	want := []struct{ title, headOf string }{
		{"初见", "张三走进客栈"},
		{"离别", "天亮时张三起身告辞"},
		{"决战", "雪夜"},
	}
	for i, w := range want {
		if got[i].Title != w.title {
			t.Errorf("ch%d title: got %q want %q", i+1, got[i].Title, w.title)
		}
		if !strings.HasPrefix(got[i].Content, w.headOf) {
			t.Errorf("ch%d content head: got %q want prefix %q", i+1, got[i].Content, w.headOf)
		}
	}
}

func TestSplitText_ChineseWithMarkdownPrefix(t *testing.T) {
	src := `# 第1章 起航
正文一。

## 第二回 风浪
正文二。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2 chapters, got %d", len(got))
	}
	if got[0].Title != "起航" || got[1].Title != "风浪" {
		t.Errorf("titles wrong: %+v", got)
	}
}

func TestSplitText_English(t *testing.T) {
	src := `Chapter 1: The Beginning
Hero awoke at dawn.

Chapter II. Crossing
The river ran cold.

CHAPTER 3 Final
A blade fell.`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3 chapters, got %d", len(got))
	}
	if got[0].Title != "The Beginning" {
		t.Errorf("ch1 title: %q", got[0].Title)
	}
	if got[1].Title != "Crossing" {
		t.Errorf("ch2 title: %q", got[1].Title)
	}
	if got[2].Title != "Final" {
		t.Errorf("ch3 title: %q", got[2].Title)
	}
}

func TestSplitText_NoTitle_FallsBack(t *testing.T) {
	src := `第一章
没有空格的标题，正文紧跟。

第二章
第二段正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "第1章" || got[1].Title != "第2章" {
		t.Errorf("fallback titles wrong: %+v", got)
	}
}

func TestSplitText_NoMatches(t *testing.T) {
	src := `这是一段没有任何章节标题的文本。
全部按一段处理。`
	got := splitText(src, defaultChapterRegex)
	if len(got) != 0 {
		t.Errorf("want empty, got %d", len(got))
	}
}

func TestSplitText_EmptyChapterSkipped(t *testing.T) {
	src := `第一章 标题
正文。

第二章 空章

第三章 末章
末章正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2 (skip empty), got %d", len(got))
	}
	if got[0].Title != "标题" || got[1].Title != "末章" {
		t.Errorf("titles after skip: %+v", got)
	}
}

func TestSplitText_TrailingLicenseStripped(t *testing.T) {
	src := `Chapter 1 Start
First chapter body.

Project Gutenberg eBook
LICENSE TEXT HERE
END OF EBOOK`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if strings.Contains(got[0].Content, "Project Gutenberg") {
		t.Errorf("trailer not stripped: %q", got[0].Content)
	}
	if !strings.HasPrefix(got[0].Content, "First chapter body.") {
		t.Errorf("body head wrong: %q", got[0].Content)
	}
}

func TestSplitFile_ReadsAndSplits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "novel.txt")
	src := "第一章 起\n正文 A\n\n第二章 终\n正文 B\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SplitFile(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}

func TestSplitFile_EmptyError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte("   \n  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := SplitFile(path, "")
	if err == nil {
		t.Fatal("want error for empty file")
	}
}

func TestSplitFile_CustomRegex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.txt")
	src := "===Section Alpha===\nA body.\n\n===Section Beta===\nB body.\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SplitFile(path, `^===Section\s+(.+)===$`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "Alpha" || got[1].Title != "Beta" {
		t.Errorf("custom titles: %+v", got)
	}
}
