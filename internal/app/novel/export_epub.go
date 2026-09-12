package novel

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// EPUB 3: https://www.w3.org/TR/epub-33/ . The first ZIP member must
// be the uncompressed mimetype with no extra fields or byte-order mark.
func writeEPUB(ctx context.Context, w io.Writer, book exportBook) error {
	archive := zip.NewWriter(w)
	add := func(name, content string, method uint16) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			return err
		}
		_, err = io.WriteString(entry, content)
		return err
	}
	mimetype := []byte("application/epub+zip")
	first, err := archive.CreateRaw(&zip.FileHeader{Name: "mimetype", Method: zip.Store, ReaderVersion: 20, CreatorVersion: 20, CRC32: crc32.ChecksumIEEE(mimetype), CompressedSize64: uint64(len(mimetype)), UncompressedSize64: uint64(len(mimetype))})
	if err != nil {
		return err
	}
	if _, err := first.Write(mimetype); err != nil {
		return err
	}
	if err := add("META-INF/container.xml", `<?xml version="1.0" encoding="UTF-8"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="EPUB/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`, zip.Deflate); err != nil {
		return err
	}
	for _, doc := range []struct{ name, source string }{{"EPUB/package.opf", epubPackage}, {"EPUB/nav.xhtml", epubNavigation}} {
		content, err := renderPublication(doc.source, book)
		if err != nil {
			return err
		}
		if err := add(doc.name, content, zip.Deflate); err != nil {
			return err
		}
	}
	for i, chapter := range book.Chapters {
		content, err := renderPublication(epubChapter, struct {
			Title  string
			Blocks []string
		}{Title: chapterHeading(chapter), Blocks: chapterParagraphs(chapter.Blocks)})
		if err != nil {
			return err
		}
		if err := add(fmt.Sprintf("EPUB/chapter-%d.xhtml", i), content, zip.Deflate); err != nil {
			return err
		}
	}
	return archive.Close()
}

func renderPublication(source string, data any) (string, error) {
	t, err := template.New("publication").Funcs(template.FuncMap{"xml": escapePublicationXML, "heading": chapterHeading}).Parse(source)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func escapePublicationXML(text string) (string, error) {
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

const epubPackage = `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="book-id" xml:lang="zh-CN">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="book-id">ainovel:{{xml .ID}}:r{{.Revision}}</dc:identifier>
<dc:title>{{xml .Title}}</dc:title><dc:language>zh-CN</dc:language>
{{if .Author}}<dc:creator>{{xml .Author}}</dc:creator>{{end}}
<meta property="dcterms:modified">{{.Modified.Format "2006-01-02T15:04:05Z"}}</meta>
</metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
{{range $i,$chapter:=.Chapters}}<item id="c{{$i}}" href="chapter-{{$i}}.xhtml" media-type="application/xhtml+xml"/>{{end}}
</manifest><spine>{{range $i,$chapter:=.Chapters}}<itemref idref="c{{$i}}"/>{{end}}</spine></package>`

const epubNavigation = `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="zh-CN" lang="zh-CN">
<head><title>{{xml .Title}} · 目录</title></head><body><nav epub:type="toc" id="toc"><h1>目录</h1><ol>
{{range $i,$chapter:=.Chapters}}<li><a href="chapter-{{$i}}.xhtml">{{xml (heading $chapter)}}</a></li>{{end}}
</ol></nav></body></html>`

const epubChapter = `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="zh-CN" lang="zh-CN"><head><title>{{xml .Title}}</title>
<style>body{line-height:1.8;margin:5%;}h1{font-size:1.4em;}p{white-space:pre-wrap;text-indent:2em;margin:0.5em 0;}</style>
</head><body><h1>{{xml .Title}}</h1>{{range .Blocks}}<p>{{xml .}}</p>{{end}}</body></html>`

func chapterParagraphs(blocks []model.ManuscriptBlock) []string {
	var paragraphs []string
	for _, block := range blocks {
		paragraphs = append(paragraphs, strings.Split(strings.ReplaceAll(block.Text, "\r\n", "\n"), "\n\n")...)
	}
	return paragraphs
}
