package imp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
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
Chính văn一。

## 第二回 风浪
Chính văn二。`

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

func TestSplitText_Volume(t *testing.T) {
	src := `第一卷 风起
卷一Chính văn。

卷二 云涌
卷二Chính văn。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "风起" || got[1].Title != "云涌" {
		t.Errorf("volume titles wrong: %+v", got)
	}
}

func TestSplitText_SpecialUnits(t *testing.T) {
	src := `楔子
古老的传说。

第一章 启程
踏上旅途。

尾声：归乡
多年以后。

番外
番外Chính văn。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 4 {
		t.Fatalf("want 4, got %d", len(got))
	}
	wantTitles := []string{"楔子", "启程", "归乡", "番外"}
	for i, w := range wantTitles {
		if got[i].Title != w {
			t.Errorf("unit %d title: got %q want %q", i+1, got[i].Title, w)
		}
	}
}

func TestSplitText_EnglishPrologueEpilogue(t *testing.T) {
	src := `Prologue
Before it all began.

Chapter 1 The Start
Here we go.

Epilogue: After
Years later.`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	if got[2].Title != "After" {
		t.Errorf("epilogue title: %q", got[2].Title)
	}
}

func TestSplitText_NoTitle_FallsBack(t *testing.T) {
	src := `第一章
没有Rỗng格的Tiêu đề，Chính văn紧跟。

第二章
第二段Chính văn。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "第1章" || got[1].Title != "第2章" {
		t.Errorf("fallback titles wrong: %+v", got)
	}
}

func TestSplitText_NoMatches(t *testing.T) {
	src := `这是一段没有任何ChươngTiêu đề的文本。
Tất cả按一段处理。`
	got := splitText(src, defaultChapterRegex)
	if len(got) != 0 {
		t.Errorf("want empty, got %d", len(got))
	}
}

func TestSplitText_EmptyChapterSkipped(t *testing.T) {
	src := `第一章 Tiêu đề
Chính văn。

第二章 Rỗng章

第三章 末章
末章Chính văn。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2 (skip empty), got %d", len(got))
	}
	if got[0].Title != "Tiêu đề" || got[1].Title != "末章" {
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

func TestSplitText_FullWidthSpace(t *testing.T) {
	src := "第一章　风起\n北风卷地。\n\n第2章　\n云动。\n"
	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "风起" {
		t.Errorf("ch1 title: %q", got[0].Title)
	}
	if got[1].Title != "第2章" { // 仅尾随全角Rỗng格 → 回退占位Tiêu đề
		t.Errorf("ch2 title: %q", got[1].Title)
	}
}

func TestSplitText_BodyPrefix(t *testing.T) {
	src := "Chính văn 第一章 风起\n北风。\n\nChính văn　第二章　云涌\n乌云。\n"
	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "风起" || got[1].Title != "云涌" {
		t.Errorf("titles: %+v", got)
	}
}

func TestSplitFile_ReadsAndSplits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "novel.txt")
	src := "第一章 起\nChính văn A\n\n第二章 终\nChính văn B\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SplitFile(path)
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
	_, err := SplitFile(path)
	if err == nil {
		t.Fatal("want error for empty file")
	}
}

func TestSplitFile_GBKEncoded(t *testing.T) {
	src := "第一章 起\nChính văn A\n\n第二章 终\nChính văn B\n"
	data, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "gbk.txt")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SplitFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "起" || got[1].Title != "终" {
		t.Errorf("titles: %+v", got)
	}
}

func TestSplitFile_UTF8BOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bom.txt")
	src := "\uFEFF第一章 起\nChính văn A\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SplitFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "起" {
		t.Fatalf("BOM file: %+v", got)
	}
}

func TestSplitText_SectionAndAct(t *testing.T) {
	src := `第一节 开端
Chính văn一。

第二幕 高潮
Chính văn二。`
	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "开端" || got[1].Title != "高潮" {
		t.Errorf("titles: %+v", got)
	}
}

func TestSplitText_UppercaseNumbers(t *testing.T) {
	src := `第壹章 起
Chính văn一。

第贰拾章 终
Chính văn二。`
	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "起" || got[1].Title != "终" {
		t.Errorf("titles: %+v", got)
	}
}

func TestSplitText_BracketWrapped(t *testing.T) {
	src := `【第一章 风起】
Chính văn一。

〖第二章 云涌〗
Chính văn二。

【楔子】
楔子Chính văn。`
	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	if got[0].Title != "风起" || got[1].Title != "云涌" {
		t.Errorf("titles: %+v", got)
	}
	if got[2].Title != "楔子" {
		t.Errorf("bracket spkw title: %q", got[2].Title)
	}
}
