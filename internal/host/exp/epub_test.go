package exp

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRenderEPUB_StructuralInvariants(t *testing.T) {
	data, err := renderEPUB(
		"光斑",
		[]int{1, 2},
		chapterTitleIndex{1: "雨夜归人", 2: "破晓"},
		nil,
		map[int]string{
			1: "# 第 1 章 雨夜归人\n\n他望着窗外。\n\n第二段。",
			2: "她推开门。",
		},
	)
	if err != nil {
		t.Fatalf("renderEPUB: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty data")
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}

	if len(zr.File) == 0 {
		t.Fatal("zip has no files")
	}
	first := zr.File[0]
	if first.Name != "mimetype" {
		t.Errorf("first entry should be mimetype, got %q", first.Name)
	}
	if first.Method != zip.Store {
		t.Errorf("mimetype must be uncompressed (Method=Store), got %d", first.Method)
	}

	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		buf, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		files[f.Name] = string(buf)
	}

	if files["mimetype"] != "application/epub+zip" {
		t.Errorf("mimetype content = %q", files["mimetype"])
	}

	for _, want := range []string{
		"META-INF/container.xml",
		"OEBPS/content.opf",
		"OEBPS/nav.xhtml",
		"OEBPS/style.css",
		"OEBPS/cover.xhtml",
		"OEBPS/chapter001.xhtml",
		"OEBPS/chapter002.xhtml",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing required file %q", want)
		}
	}

	// container.xml 指向 OEBPS/content.opf
	if !strings.Contains(files["META-INF/container.xml"], `full-path="OEBPS/content.opf"`) {
		t.Errorf("container.xml does not point to content.opf")
	}

	// content.opf 必含 metadata + manifest + spine 三大块；spine 顺序 = 章节顺序
	opf := files["OEBPS/content.opf"]
	for _, want := range []string{
		"<metadata", "</metadata>",
		"<manifest>", "</manifest>",
		"<spine>", "</spine>",
		"urn:uuid:",
		"<dc:title>光斑</dc:title>",
		`href="chapter001.xhtml"`,
		`href="chapter002.xhtml"`,
		`idref="ch001"`,
		`idref="ch002"`,
	} {
		if !strings.Contains(opf, want) {
			t.Errorf("OPF missing %q", want)
		}
	}
	if idx1, idx2 := strings.Index(opf, `idref="ch001"`), strings.Index(opf, `idref="ch002"`); idx1 < 0 || idx1 > idx2 {
		t.Errorf("spine order wrong: ch001=%d ch002=%d", idx1, idx2)
	}

	// 章节 XHTML 含标题 + 段落 + 转义；首行 markdown 标题已剥
	ch1 := files["OEBPS/chapter001.xhtml"]
	if !strings.Contains(ch1, "第 1 章 雨夜归人") {
		t.Errorf("chapter1 missing display title")
	}
	if !strings.Contains(ch1, "<p>他望着窗外。</p>") {
		t.Errorf("chapter1 missing paragraph 1: %s", ch1)
	}
	if !strings.Contains(ch1, "<p>第二段。</p>") {
		t.Errorf("chapter1 missing paragraph 2: %s", ch1)
	}
	if strings.Contains(ch1, "# 第 1 章") {
		t.Errorf("chapter1 should have stripped markdown header: %s", ch1)
	}

	// nav.xhtml 列出所有章节
	nav := files["OEBPS/nav.xhtml"]
	if !strings.Contains(nav, `epub:type="toc"`) {
		t.Errorf("nav missing epub:type=toc")
	}
	if !strings.Contains(nav, `href="chapter001.xhtml"`) || !strings.Contains(nav, `href="chapter002.xhtml"`) {
		t.Errorf("nav missing chapter links")
	}
}

func TestRenderEPUB_HTMLEscape(t *testing.T) {
	data, err := renderEPUB(
		"A & B", // & 必须转义
		[]int{1},
		chapterTitleIndex{1: "C \"D\""},
		nil,
		map[int]string{1: "正文 < & > 内容。"},
	)
	if err != nil {
		t.Fatalf("renderEPUB: %v", err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		buf, _ := io.ReadAll(rc)
		_ = rc.Close()
		files[f.Name] = string(buf)
	}

	if !strings.Contains(files["OEBPS/cover.xhtml"], "A &amp; B") {
		t.Errorf("cover should escape &: %s", files["OEBPS/cover.xhtml"])
	}
	if !strings.Contains(files["OEBPS/chapter001.xhtml"], "正文 &lt; &amp; &gt; 内容。") {
		t.Errorf("chapter body should escape entities")
	}
	if !strings.Contains(files["OEBPS/content.opf"], "<dc:title>A &amp; B</dc:title>") {
		t.Errorf("opf should escape & in title")
	}
}

// TestRenderEPUB_LayeredVolume 验证分层大纲只在卷首插卷分隔，弧分隔永不出现。
func TestRenderEPUB_LayeredVolume(t *testing.T) {
	locs := map[int]chapterLocation{
		1: {VolumeIdx: 1, VolumeTitle: "起源", IsFirstOfVolume: true},
		2: {VolumeIdx: 1, VolumeTitle: "起源"},
	}
	data, err := renderEPUB(
		"X",
		[]int{1, 2},
		chapterTitleIndex{1: "A", 2: "B"},
		locs,
		map[int]string{1: "正文一。", 2: "正文二。"},
	)
	if err != nil {
		t.Fatalf("renderEPUB: %v", err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		buf, _ := io.ReadAll(rc)
		_ = rc.Close()
		files[f.Name] = string(buf)
	}

	ch1 := files["OEBPS/chapter001.xhtml"]
	if !strings.Contains(ch1, `class="volume-divider"`) || !strings.Contains(ch1, "第 1 卷 起源") {
		t.Errorf("ch1 should have volume divider: %s", ch1)
	}
	if strings.Contains(ch1, `class="arc-divider"`) {
		t.Errorf("arc divider should never appear: %s", ch1)
	}

	ch2 := files["OEBPS/chapter002.xhtml"]
	if strings.Contains(ch2, `class="volume-divider"`) {
		t.Errorf("ch2 should NOT have volume divider (same volume)")
	}
}

func TestRenderEPUB_NoCoverWhenNoTitle(t *testing.T) {
	data, err := renderEPUB(
		"", []int{1},
		chapterTitleIndex{1: "唯一一章"},
		nil,
		map[int]string{1: "正文。"},
	)
	if err != nil {
		t.Fatalf("renderEPUB: %v", err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	for _, f := range zr.File {
		if f.Name == "OEBPS/cover.xhtml" {
			t.Errorf("cover.xhtml should not exist when title is empty")
		}
	}
	// content.opf 不应引用 cover
	for _, f := range zr.File {
		if f.Name != "OEBPS/content.opf" {
			continue
		}
		rc, _ := f.Open()
		buf, _ := io.ReadAll(rc)
		_ = rc.Close()
		if strings.Contains(string(buf), "cover.xhtml") || strings.Contains(string(buf), `idref="cover"`) {
			t.Errorf("OPF should not reference cover when there is none: %s", buf)
		}
	}
}

func TestSplitParagraphs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a\n\nb", []string{"a", "b"}},
		{"a\n\n\n\nb", []string{"a", "b"}}, // 多空行折叠为一个分隔
		{"a\nb", []string{"a b"}},          // 段内单换行变空格
		{"  ", nil},                        // 全空白返回 nil
		{"a\r\n\r\nb", []string{"a", "b"}}, // CRLF 兼容
	}
	for _, c := range cases {
		got := splitParagraphs(c.in)
		if !equalStrings(got, c.want) {
			t.Errorf("splitParagraphs(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestBookIdentifier_StableAcrossChapterRanges(t *testing.T) {
	// 同名作品、不同导出范围必须返回同一 ID — 阅读器才能识别为"更新版本"
	idFull := bookIdentifier("光斑")
	idAgain := bookIdentifier("光斑")
	if idFull != idAgain {
		t.Errorf("identifier not stable across calls: %s vs %s", idFull, idAgain)
	}
	if id := bookIdentifier("月相"); id == idFull {
		t.Errorf("different titles must yield different identifiers")
	}
	if !strings.HasPrefix(idFull, "urn:uuid:") {
		t.Errorf("identifier should be urn:uuid: prefixed, got %s", idFull)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
