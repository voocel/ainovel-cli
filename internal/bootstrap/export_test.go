package bootstrap_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestNovelExportUsesAcceptedRevisionAndNumericOrder(t *testing.T) {
	ctx := context.Background()
	api := newTestApp(openTestStore(t))
	projection := projectdoc.ProjectProjection{ProjectID: "export-book", Intent: model.Intent{Premise: "来信"}, Plan: []model.PlanNode{{ID: "v", Kind: model.PlanVolume, Title: "卷", Summary: "卷"}, {ID: "a", ParentID: "v", Kind: model.PlanArc, Title: "弧", Summary: "弧"}}}
	for _, n := range []int{10, 2, 1} {
		id := fmt.Sprint(n)
		projection.Plan = append(projection.Plan, model.PlanNode{ID: "p" + id, ParentID: "a", Kind: model.PlanChapter, Order: n, Title: "章" + id, Summary: "章"})
		projection.Manuscript = append(projection.Manuscript, model.ManuscriptChapter{ID: "c" + id, PlanNodeID: "p" + id, Number: n, Title: "章" + id, Author: model.AuthorUser, Blocks: []model.ManuscriptBlock{{ID: "b" + id, Text: "已确认正文" + id}}})
	}
	proposal, err := api.Projects.ImportNewProject(ctx, "import", "user", "test", projection, testTime())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "book.txt")
	if _, err := api.Novels.Export(ctx, novel.ExportCommand{ProjectID: projection.ProjectID, Path: path}); err == nil {
		t.Fatal("unapproved import was exported")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("empty manuscript left an output")
	}
	if _, err := api.Decisions.Approve(ctx, proposal.ID, "user", testTime().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	result, err := api.Novels.Export(ctx, novel.ExportCommand{ProjectID: projection.ProjectID, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	text := string(content)
	if result.Chapters != 3 || !(strings.Index(text, "第1章 ") < strings.Index(text, "第2章 ") && strings.Index(text, "第2章 ") < strings.Index(text, "第10章 ")) {
		t.Fatalf("order/result: %+v %s", result, text)
	}
	projection, err = api.Projects.ExportProject(ctx, projection.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	projection.Manuscript[0].Blocks[0].Text = "尚未批准的新稿"
	if _, err := api.Projects.ImportProject(ctx, "pending", "user", "test", projection, testTime().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "range.txt")
	filtered, err := api.Novels.Export(ctx, novel.ExportCommand{ProjectID: projection.ProjectID, Path: path, Revision: result.Revision, From: 2, To: 2})
	if err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(path)
	if filtered.Chapters != 1 || strings.Contains(string(content), "尚未批准") || !strings.Contains(string(content), "第2章") {
		t.Fatal("range or accepted-revision isolation failed")
	}
}
