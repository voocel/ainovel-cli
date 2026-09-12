package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

func TestProjectPublicationExportCLI(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	api := bootstrap.New(s, bootstrap.Options{})
	now := time.Now().UTC()
	p := projectdoc.ProjectProjection{ProjectID: "book", Intent: model.Intent{Premise: "来信"}, Plan: []model.PlanNode{{ID: "v", Kind: model.PlanVolume, Title: "卷", Summary: "卷"}, {ID: "a", ParentID: "v", Kind: model.PlanArc, Title: "弧", Summary: "弧"}, {ID: "p", ParentID: "a", Kind: model.PlanChapter, Title: "信", Summary: "信"}}, Manuscript: []model.ManuscriptChapter{{ID: "c", PlanNodeID: "p", Number: 1, Title: "信", Author: model.AuthorUser, Blocks: []model.ManuscriptBlock{{ID: "b", Text: "中文正文。"}}}}}
	proposal, err := api.Projects.ImportNewProject(ctx, "import", "user", "test", p, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Decisions.Approve(ctx, proposal.ID, "user", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"txt", "epub"} {
		path := filepath.Join(t.TempDir(), "作品."+format)
		args := []string{"project", "export", "--project", "book", "--format", format, "--file", path, "--title", "亡者来信", "--author", "作者"}
		var stdout, stderr bytes.Buffer
		if err := Run(ctx, api, args, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		var result novel.ExportResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Chapters != 1 || result.Bytes == 0 {
			t.Fatalf("result: %s %v", stdout.String(), err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if format == "txt" && !bytes.Contains(content, []byte("中文正文。")) {
			t.Fatal("TXT content missing")
		}
		stdout.Reset()
		if err := Run(ctx, api, args, &stdout, &stderr); err == nil {
			t.Fatal("CLI overwrote existing file")
		}
	}
	var stdout, stderr bytes.Buffer
	if err := Run(ctx, api, []string{"project", "export", "--project", "book"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var projection projectdoc.ProjectProjection
	if err := json.Unmarshal(stdout.Bytes(), &projection); err != nil || len(projection.Manuscript) != 1 {
		t.Fatal("default JSON export changed")
	}
}
