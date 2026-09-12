package novel

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestEPUBContainsReadableLinkedChapters(t *testing.T) {
	book := exportBook{ID: "book", Title: "雨 & <信>", Author: "作者", Revision: 2, Modified: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}
	for i := 1; i <= 500; i++ {
		book.Chapters = append(book.Chapters, model.ManuscriptChapter{Number: i, Title: fmt.Sprintf("来信 %d", i), Blocks: []model.ManuscriptBlock{{Text: "他说：<不要走> & 留下。\n\n第二段🙂"}}})
	}
	var output bytes.Buffer
	if err := writeEPUB(context.Background(), &output, book); err != nil {
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
	for i := 0; i < 500; i++ {
		href := fmt.Sprintf("chapter-%d.xhtml", i)
		if documents["EPUB/"+href] == nil || !bytes.Contains(documents["EPUB/nav.xhtml"], []byte(`href="`+href+`"`)) || !bytes.Contains(documents["EPUB/package.opf"], []byte(fmt.Sprintf(`idref="c%d"`, i))) {
			t.Fatalf("broken chapter link %d", i)
		}
	}
	if !bytes.Contains(documents["EPUB/chapter-0.xhtml"], []byte("&lt;不要走&gt; &amp; 留下。")) {
		t.Fatal("text not XML escaped")
	}
}

func TestExportPublicationIsAtomicAndDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "作品.txt")
	write := func(w io.Writer) error { _, err := io.WriteString(w, "已确认正文"); return err }
	if err := publishExport(context.Background(), path, false, write); err != nil {
		t.Fatal(err)
	}
	if err := publishExport(context.Background(), path, false, write); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing output: %v", err)
	}
	failed := func(w io.Writer) error { io.WriteString(w, "半成品"); return errors.New("write failed") }
	if err := publishExport(context.Background(), path, true, failed); err == nil {
		t.Fatal("write failure ignored")
	}
	content, _ := os.ReadFile(path)
	if string(content) != "已确认正文" {
		t.Fatal("failed export damaged old file")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := publishExport(ctx, path, true, write); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatal("temporary files leaked")
	}
	if err := publishExport(context.Background(), path, true, func(w io.Writer) error { _, err := io.WriteString(w, "新正文"); return err }); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(path)
	if string(content) != "新正文" {
		t.Fatal("explicit overwrite did not replace file")
	}
}

func TestExportRejectsInvalidXMLAndPreservesChapterHeadings(t *testing.T) {
	for _, title := range []string{"第一章 雨夜", "第1章 雨夜"} {
		if chapterHeading(model.ManuscriptChapter{Number: 1, Title: title}) != title {
			t.Fatal("duplicated chapter heading")
		}
	}
	_, err := escapePublicationXML("正文\x00")
	if err == nil {
		t.Fatal("invalid XML text was silently changed")
	}
	var text bytes.Buffer
	book := exportBook{Title: "来信", Chapters: []model.ManuscriptChapter{{Number: 5, Title: "回信", Blocks: []model.ManuscriptBlock{{Text: "第一段\n继续。"}, {Text: "第二段。"}}}}}
	if err := writeTXT(context.Background(), &text, book); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "第5章 回信\n\n第一段\n继续。\n\n第二段。") {
		t.Fatalf("TXT paragraph structure: %s", text.String())
	}
}
