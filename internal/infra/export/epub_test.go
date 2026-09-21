package export

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestWriteEPUBProducesReadableLinkedChapters(t *testing.T) {
	book := EPUB{Identifier: "ainovel:book:r2", Title: "雨 & <信>", Author: "作者", Language: "zh-CN", Modified: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}
	for i := 1; i <= 500; i++ {
		book.Chapters = append(book.Chapters, EPUBChapter{Title: fmt.Sprintf("第%d章 来信", i), Paragraphs: []string{"他说：<不要走> & 留下。", "第二段🙂"}})
	}
	var output bytes.Buffer
	if err := WriteEPUB(&output, book); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if archive.File[0].Name != "mimetype" || archive.File[0].Method != zip.Store || len(archive.File[0].Extra) != 0 {
		t.Fatal("invalid EPUB mimetype member")
	}
	documents := map[string][]byte{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		documents[file.Name] = content
		if file.Name == "mimetype" {
			if string(content) != "application/epub+zip" {
				t.Fatal("wrong mimetype")
			}
			continue
		}
		decoder := xml.NewDecoder(bytes.NewReader(content))
		for {
			_, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", file.Name, err)
			}
		}
	}
	if len(documents) != 504 {
		t.Fatalf("missing EPUB resources: %d", len(documents))
	}
	if !bytes.Contains(documents["META-INF/container.xml"], []byte(`full-path="EPUB/package.opf"`)) {
		t.Fatal("container does not point at the package document")
	}
	if !bytes.Contains(documents["EPUB/package.opf"], []byte("<dc:identifier id=\"book-id\">ainovel:book:r2</dc:identifier>")) || !bytes.Contains(documents["EPUB/package.opf"], []byte("<dc:creator>作者</dc:creator>")) {
		t.Fatalf("package metadata: %s", documents["EPUB/package.opf"])
	}
	for i := 0; i < 500; i++ {
		href := fmt.Sprintf("chapter-%d.xhtml", i)
		if documents["EPUB/"+href] == nil || !bytes.Contains(documents["EPUB/nav.xhtml"], []byte(`href="`+href+`"`)) || !bytes.Contains(documents["EPUB/package.opf"], []byte(fmt.Sprintf(`idref="c%d"`, i))) {
			t.Fatalf("broken chapter link %d", i)
		}
	}
	if !bytes.Contains(documents["EPUB/chapter-0.xhtml"], []byte("<h1>第1章 来信</h1><p>他说：&lt;不要走&gt; &amp; 留下。</p><p>第二段🙂</p>")) {
		t.Fatalf("chapter body: %s", documents["EPUB/chapter-0.xhtml"])
	}
}

func TestWriteEPUBRejectsInvalidXMLText(t *testing.T) {
	for _, text := range []string{"正文\x00", "坏字节\xff", "￾"} {
		book := EPUB{Title: "来信", Language: "zh-CN", Chapters: []EPUBChapter{{Title: "第1章", Paragraphs: []string{text}}}}
		if err := WriteEPUB(io.Discard, book); err == nil {
			t.Fatalf("invalid XML text %q was silently changed", text)
		}
	}
}
