package novel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type ExportCommand struct {
	ProjectID                   string
	Revision                    model.Revision
	Path, Format, Title, Author string
	From, To                    int
	Overwrite                   bool
}

type ExportResult struct {
	Path     string         `json:"path"`
	Format   string         `json:"format"`
	Revision model.Revision `json:"revision"`
	Chapters int            `json:"chapters"`
	Bytes    int64          `json:"bytes"`
}

type exportBook struct {
	ID, Title, Author string
	Revision          model.Revision
	Chapters          []model.ManuscriptChapter
	Modified          time.Time
}

// Export renders one immutable revision of accepted manuscript content. Pending
// proposals, workspaces and live previews never become part of the publication.
func (s *Application) Export(ctx context.Context, command ExportCommand) (ExportResult, error) {
	format := strings.ToLower(command.Format)
	if format == "" {
		format = strings.TrimPrefix(strings.ToLower(filepath.Ext(command.Path)), ".")
	}
	if format != "txt" && format != "epub" {
		return ExportResult{}, fmt.Errorf("导出格式必须是 txt 或 epub")
	}
	if strings.TrimSpace(command.Path) == "" {
		return ExportResult{}, fmt.Errorf("请指定导出文件路径")
	}
	if ext := strings.ToLower(filepath.Ext(command.Path)); ext != "" && ext != "."+format {
		return ExportResult{}, fmt.Errorf("文件扩展名必须与 %s 格式一致", format)
	}
	if command.Revision < 0 || command.From < 0 || command.To < 0 || (command.To > 0 && command.From > command.To) {
		return ExportResult{}, fmt.Errorf("导出版本或章节范围无效")
	}
	project, err := s.projects.Project(ctx, command.ProjectID, command.Revision)
	if err != nil {
		return ExportResult{}, err
	}
	book := exportBook{ID: project.ID, Title: strings.TrimSpace(command.Title), Author: strings.TrimSpace(command.Author), Revision: project.Revision, Modified: time.Now().UTC()}
	if book.Title == "" {
		book.Title = strings.TrimSpace(project.Intent.Premise)
	}
	if book.Title == "" {
		book.Title = project.ID
	}
	for _, chapter := range project.Manuscript {
		if chapter.Number >= command.From && (command.To == 0 || chapter.Number <= command.To) {
			book.Chapters = append(book.Chapters, chapter)
		}
	}
	if len(book.Chapters) == 0 {
		return ExportResult{}, fmt.Errorf("所选范围内没有已确认正文，候选稿需先批准")
	}
	slices.SortFunc(book.Chapters, func(a, b model.ManuscriptChapter) int {
		if a.Number < b.Number {
			return -1
		}
		if a.Number > b.Number {
			return 1
		}
		return 0
	})
	path, err := filepath.Abs(command.Path)
	if err != nil {
		return ExportResult{}, err
	}
	err = publishExport(ctx, path, command.Overwrite, func(w io.Writer) error {
		if format == "epub" {
			return writeEPUB(ctx, w, book)
		}
		return writeTXT(ctx, w, book)
	})
	if err != nil {
		return ExportResult{}, fmt.Errorf("导出失败：%w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{Path: path, Format: format, Revision: book.Revision, Chapters: len(book.Chapters), Bytes: info.Size()}, nil
}

var numberedChapterTitle = regexp.MustCompile(`^第[0-9零一二三四五六七八九十百千两〇]+章(?:[\s：:、.．]|$)`)

func chapterHeading(chapter model.ManuscriptChapter) string {
	prefix := fmt.Sprintf("第%d章", chapter.Number)
	title := strings.TrimSpace(chapter.Title)
	if numberedChapterTitle.MatchString(title) {
		return title
	}
	return prefix + " " + title
}

func writeTXT(ctx context.Context, w io.Writer, book exportBook) error {
	out := bufio.NewWriter(w)
	if _, err := fmt.Fprintln(out, book.Title); err != nil {
		return err
	}
	if book.Author != "" {
		if _, err := fmt.Fprintln(out, "作者："+book.Author); err != nil {
			return err
		}
	}
	for _, chapter := range book.Chapters {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "\n\n%s\n\n", chapterHeading(chapter)); err != nil {
			return err
		}
		for _, block := range chapter.Blocks {
			if _, err := fmt.Fprintln(out, block.Text+"\n"); err != nil {
				return err
			}
		}
	}
	return out.Flush()
}

// Publish only a fully written file. Link creates the destination exclusively,
// so another exporter cannot be overwritten between an existence check and IO.
func publishExport(ctx context.Context, path string, overwrite bool, write func(io.Writer) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".ainovel-export-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := write(file); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if overwrite {
		return os.Rename(file.Name(), path)
	}
	if err := os.Link(file.Name(), path); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("文件已存在，请换一个文件名，或使用 --overwrite：%w", err)
		}
		return err
	}
	return nil
}
