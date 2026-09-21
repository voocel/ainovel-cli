package export

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"text/template"
	"time"
	"unicode/utf8"
)

// EPUB 是容器编码的纯数据入参，所有字符串均为未转义原文。
type EPUB struct {
	Identifier, Title, Author, Language string
	Modified                            time.Time
	Chapters                            []EPUBChapter
}

// EPUBChapter 的 Title 已含章节编号；Paragraphs 每项对应一个 <p>。
type EPUBChapter struct {
	Title      string
	Paragraphs []string
}

// WriteEPUB 按 EPUB 3（https://www.w3.org/TR/epub-33/）写出容器：首个 ZIP 成员必须是
// 未压缩、无扩展字段与 BOM 的 mimetype。
func WriteEPUB(w io.Writer, book EPUB) error {
	archive := zip.NewWriter(w)
	mimetype := []byte("application/epub+zip")
	first, err := archive.CreateRaw(&zip.FileHeader{Name: "mimetype", Method: zip.Store, ReaderVersion: 20, CreatorVersion: 20, CRC32: crc32.ChecksumIEEE(mimetype), CompressedSize64: uint64(len(mimetype)), UncompressedSize64: uint64(len(mimetype))})
	if err != nil {
		return err
	}
	if _, err := first.Write(mimetype); err != nil {
		return err
	}
	add := func(name string, doc *template.Template, data any) error {
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		return doc.Execute(entry, data)
	}
	if err := add("META-INF/container.xml", epubContainer, nil); err != nil {
		return err
	}
	if err := add("EPUB/package.opf", epubPackage, book); err != nil {
		return err
	}
	if err := add("EPUB/nav.xhtml", epubNavigation, book); err != nil {
		return err
	}
	for i, chapter := range book.Chapters {
		data := struct {
			EPUBChapter
			Language string
		}{chapter, book.Language}
		if err := add(fmt.Sprintf("EPUB/chapter-%d.xhtml", i), epubChapter, data); err != nil {
			return err
		}
	}
	return archive.Close()
}

// escapeXML 拒绝 XML 1.0 不允许的字符而不是悄悄丢弃，避免阅读器静默截断。
func escapeXML(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("EPUB 文本不是有效 UTF-8")
	}
	for _, r := range text {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0xfffe || r == 0xffff {
			return "", fmt.Errorf("EPUB 文本含不支持的控制字符 U+%04X", r)
		}
	}
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(text)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func epubTemplate(source string) *template.Template {
	return template.Must(template.New("").Funcs(template.FuncMap{"xml": escapeXML}).Parse(source))
}

var (
	epubContainer = epubTemplate(`<?xml version="1.0" encoding="UTF-8"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="EPUB/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)

	epubPackage = epubTemplate(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="book-id" xml:lang="{{xml .Language}}">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="book-id">{{xml .Identifier}}</dc:identifier>
<dc:title>{{xml .Title}}</dc:title><dc:language>{{xml .Language}}</dc:language>
{{if .Author}}<dc:creator>{{xml .Author}}</dc:creator>{{end}}
<meta property="dcterms:modified">{{.Modified.Format "2006-01-02T15:04:05Z"}}</meta>
</metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
{{range $i,$chapter:=.Chapters}}<item id="c{{$i}}" href="chapter-{{$i}}.xhtml" media-type="application/xhtml+xml"/>{{end}}
</manifest><spine>{{range $i,$chapter:=.Chapters}}<itemref idref="c{{$i}}"/>{{end}}</spine></package>`)

	epubNavigation = epubTemplate(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="{{xml .Language}}" lang="{{xml .Language}}">
<head><title>{{xml .Title}} · 目录</title></head><body><nav epub:type="toc" id="toc"><h1>目录</h1><ol>
{{range $i,$chapter:=.Chapters}}<li><a href="chapter-{{$i}}.xhtml">{{xml $chapter.Title}}</a></li>{{end}}
</ol></nav></body></html>`)

	epubChapter = epubTemplate(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="{{xml .Language}}" lang="{{xml .Language}}"><head><title>{{xml .Title}}</title>
<style>body{line-height:1.8;margin:5%;}h1{font-size:1.4em;}p{white-space:pre-wrap;text-indent:2em;margin:0.5em 0;}</style>
</head><body><h1>{{xml .Title}}</h1>{{range .Paragraphs}}<p>{{xml .}}</p>{{end}}</body></html>`)
)
