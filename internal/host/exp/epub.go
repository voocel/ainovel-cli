package exp

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"fmt"
	"html"
	"strings"
	"time"
)

// renderEPUB 把章节集合打包成 EPUB 3 字节流。
//
// 包结构（OEBPS 是 OPS package 容器）：
//
//	mimetype                    （必须 zip 第一项 + Method=Store 不压缩）
//	META-INF/container.xml      （指向 OEBPS/content.opf）
//	OEBPS/content.opf           （metadata + manifest + spine）
//	OEBPS/nav.xhtml             （EPUB 3 navigation）
//	OEBPS/style.css             （极简排版）
//	OEBPS/cover.xhtml           （书名，可选）
//	OEBPS/chapterNNN.xhtml      （每章一文件）
func renderEPUB(
	novelName string,
	chapters []int,
	titleIdx chapterTitleIndex,
	locations map[int]chapterLocation,
	bodies map[int]string,
) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// 1. mimetype 必须是 zip 第一项 + Store（不压缩）+ 内容精确无 BOM
	mt, err := zw.CreateHeader(&zip.FileHeader{
		Name:   "mimetype",
		Method: zip.Store,
	})
	if err != nil {
		return nil, fmt.Errorf("create mimetype: %w", err)
	}
	if _, err := mt.Write([]byte("application/epub+zip")); err != nil {
		return nil, err
	}

	if err := zipDeflate(zw, "META-INF/container.xml", containerXML); err != nil {
		return nil, err
	}
	if err := zipDeflate(zw, "OEBPS/style.css", styleCSS); err != nil {
		return nil, err
	}

	hasCover := strings.TrimSpace(novelName) != ""
	if hasCover {
		if err := zipDeflate(zw, "OEBPS/cover.xhtml", renderCoverXHTML(novelName)); err != nil {
			return nil, err
		}
	}

	for _, ch := range chapters {
		loc, hasLoc := locations[ch]
		title := strings.TrimSpace(titleIdx[ch])
		body := stripChapterTitleHeader(strings.TrimSpace(bodies[ch]), title)
		xhtml := renderChapterXHTML(ch, title, loc, hasLoc, body)
		if err := zipDeflate(zw, "OEBPS/"+chapterFileName(ch), xhtml); err != nil {
			return nil, err
		}
	}

	if err := zipDeflate(zw, "OEBPS/nav.xhtml", renderNavXHTML(hasCover, chapters, titleIdx)); err != nil {
		return nil, err
	}

	if err := zipDeflate(zw, "OEBPS/content.opf", renderOPF(novelName, hasCover, chapters)); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalize zip: %w", err)
	}
	return buf.Bytes(), nil
}

// zipDeflate 写入一个普通（压缩）条目。
func zipDeflate(zw *zip.Writer, name, content string) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	_, err = w.Write([]byte(content))
	return err
}

func chapterFileName(ch int) string {
	return fmt.Sprintf("chapter%03d.xhtml", ch)
}

// chapterID 是 manifest item 的 id；与文件名一一对应。
func chapterID(ch int) string {
	return fmt.Sprintf("ch%03d", ch)
}

// 固定模板 ────────────────────────────────────────────────

const containerXML = `<?xml version="1.0" encoding="utf-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

const styleCSS = `body { font-family: serif; line-height: 1.7; margin: 1em; }
h1.book-title { font-size: 2em; text-align: center; margin: 4em 0 1em; }
.volume-divider { font-size: 1.6em; text-align: center; margin: 4em 0 1em; font-weight: bold; }
h1.chapter-title { font-size: 1.4em; text-align: center; margin: 2em 0 1.5em; }
p { text-indent: 2em; margin: 0.5em 0; }
`

// 章节 XHTML ────────────────────────────────────────────────

func renderChapterXHTML(ch int, title string, loc chapterLocation, hasLoc bool, body string) string {
	var b strings.Builder
	displayTitle := fmt.Sprintf("第 %d 章", ch)
	if title != "" {
		displayTitle = fmt.Sprintf("第 %d 章 %s", ch, title)
	}

	fmt.Fprintf(&b, `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="zh-CN">
<head>
  <title>%s</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
`, html.EscapeString(displayTitle))

	if hasLoc && loc.IsFirstOfVolume {
		fmt.Fprintf(&b, "  <div class=\"volume-divider\">第 %d 卷 %s</div>\n",
			loc.VolumeIdx, html.EscapeString(strings.TrimSpace(loc.VolumeTitle)))
	}

	fmt.Fprintf(&b, "  <h1 class=\"chapter-title\">%s</h1>\n", html.EscapeString(displayTitle))
	for _, para := range splitParagraphs(body) {
		fmt.Fprintf(&b, "  <p>%s</p>\n", html.EscapeString(para))
	}
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// splitParagraphs 按空行切段；连续多空行视为一个分段。返回的段落都已 TrimSpace 且非空。
// 段内换行（单个 \n）保留为段内空格——XHTML 的 <p> 不保留换行，浏览器自动 wrap。
func splitParagraphs(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	parts := strings.Split(body, "\n\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// 段内换行变空格，避免 XHTML 渲染时丢内容
		p = strings.ReplaceAll(p, "\n", " ")
		out = append(out, p)
	}
	return out
}

// 封面 ────────────────────────────────────────────────

func renderCoverXHTML(novelName string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="zh-CN">
<head>
  <title>封面</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
`)
	if name := strings.TrimSpace(novelName); name != "" {
		fmt.Fprintf(&b, "  <h1 class=\"book-title\">%s</h1>\n", html.EscapeString(name))
	}
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// nav.xhtml（EPUB 3 navigation）────────────────────────────────────────────────

func renderNavXHTML(hasCover bool, chapters []int, titleIdx chapterTitleIndex) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="zh-CN">
<head>
  <title>目录</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
  <nav epub:type="toc">
    <h1>目录</h1>
    <ol>
`)
	if hasCover {
		b.WriteString("      <li><a href=\"cover.xhtml\">封面</a></li>\n")
	}

	// 平铺章节列表。卷/弧分组在阅读器里反而不如单层目录清爽（阅读器自己会折叠），
	// 而且 EPUB 3 nav 嵌套 ol 在某些阅读器上渲染怪。保持简单。
	for _, ch := range chapters {
		title := strings.TrimSpace(titleIdx[ch])
		display := fmt.Sprintf("第 %d 章", ch)
		if title != "" {
			display = fmt.Sprintf("第 %d 章 %s", ch, title)
		}
		fmt.Fprintf(&b, "      <li><a href=\"%s\">%s</a></li>\n",
			chapterFileName(ch), html.EscapeString(display))
	}

	b.WriteString(`    </ol>
  </nav>
</body>
</html>
`)
	return b.String()
}

// content.opf ────────────────────────────────────────────────

func renderOPF(novelName string, hasCover bool, chapters []int) string {
	bookID := bookIdentifier(novelName)
	modified := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	title := strings.TrimSpace(novelName)
	if title == "" {
		title = "Untitled"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="zh-CN">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">%s</dc:identifier>
    <dc:title>%s</dc:title>
    <dc:language>zh-CN</dc:language>
    <dc:creator>ainovel-cli</dc:creator>
    <meta property="dcterms:modified">%s</meta>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="css" href="style.css" media-type="text/css"/>
`, html.EscapeString(bookID), html.EscapeString(title), modified)

	if hasCover {
		b.WriteString(`    <item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/>` + "\n")
	}
	for _, ch := range chapters {
		fmt.Fprintf(&b, `    <item id="%s" href="%s" media-type="application/xhtml+xml"/>`+"\n",
			chapterID(ch), chapterFileName(ch))
	}

	b.WriteString("  </manifest>\n  <spine>\n")
	if hasCover {
		b.WriteString(`    <itemref idref="cover"/>` + "\n")
	}
	b.WriteString(`    <itemref idref="nav"/>` + "\n")
	for _, ch := range chapters {
		fmt.Fprintf(&b, `    <itemref idref="%s"/>`+"\n", chapterID(ch))
	}
	b.WriteString("  </spine>\n</package>\n")
	return b.String()
}

// bookIdentifier 由小说名派生稳定 UUID 字符串。
//
// **只用 novelName，不掺章节列表**：作品身份应跟"是哪本书"绑定，不跟"导出范围"
// 或"导出时刻已写到第几章"绑定。重导出同一本书 ID 不变，阅读器据此识别为同一作品
// 的更新版本（更新与否由 dcterms:modified 时间戳承担）。空 novelName 共享 ID 是
// 已知边角 case：用户给两本书都不起名时责任自负。
func bookIdentifier(novelName string) string {
	h := sha1.New()
	h.Write([]byte(novelName))
	sum := h.Sum(nil)
	// 格式化为 UUID 风格（8-4-4-4-12），不要求严格 RFC 4122 — EPUB 只要求字符串唯一稳定。
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x",
		sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
