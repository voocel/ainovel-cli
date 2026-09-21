package novel

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestExportPreservesChapterHeadingsAndParagraphs(t *testing.T) {
	for _, title := range []string{"第一章 雨夜", "第1章 雨夜"} {
		if chapterHeading(model.ManuscriptChapter{Number: 1, Title: title}) != title {
			t.Fatal("duplicated chapter heading")
		}
	}
	book := exportBook{ID: "book", Title: "来信", Author: "作者", Revision: 2, Chapters: []model.ManuscriptChapter{{Number: 5, Title: "回信", Blocks: []model.ManuscriptBlock{{Text: "第一段\n继续。\r\n\r\n第二段。"}, {Text: "第三段。"}}}}}
	var text bytes.Buffer
	if err := writeTXT(context.Background(), &text, book); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "第5章 回信\n\n第一段\n继续。\r\n\r\n第二段。\n\n第三段。") {
		t.Fatalf("TXT paragraph structure: %s", text.String())
	}
	epub := epubBook(book)
	if epub.Identifier != "ainovel:book:r2" || epub.Title != "来信" || epub.Author != "作者" || epub.Language != "zh-CN" {
		t.Fatalf("EPUB metadata: %+v", epub)
	}
	if len(epub.Chapters) != 1 || epub.Chapters[0].Title != "第5章 回信" || strings.Join(epub.Chapters[0].Paragraphs, "|") != "第一段\n继续。|第二段。|第三段。" {
		t.Fatalf("EPUB chapter mapping: %+v", epub.Chapters)
	}
}
