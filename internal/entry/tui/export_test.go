package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestWorkbenchExportsAcceptedContent(t *testing.T) {
	deps, api := newTestDeps(t, true)
	ctx := context.Background()
	now := time.Now().UTC()
	p := projectdoc.ProjectProjection{ProjectID: "book", Intent: domainmodel.Intent{Premise: "来信"}, Plan: []domainmodel.PlanNode{{ID: "v", Kind: domainmodel.PlanVolume, Title: "卷", Summary: "卷"}, {ID: "a", ParentID: "v", Kind: domainmodel.PlanArc, Title: "弧", Summary: "弧"}, {ID: "p", ParentID: "a", Kind: domainmodel.PlanChapter, Title: "信", Summary: "信"}}, Manuscript: []domainmodel.ManuscriptChapter{{ID: "c", PlanNodeID: "p", Number: 1, Title: "信", Author: domainmodel.AuthorUser, Blocks: []domainmodel.ManuscriptBlock{{ID: "b", Text: "已确认正文。"}}}}}
	proposal, err := api.Projects.ImportNewProject(ctx, "import", "user", "test", p, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Decisions.Approve(ctx, proposal.ID, "user", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	m := newModel(ctx, deps)
	m.page = pageWorkbench
	m.bench = newWorkbenchState("book", 1)
	m.width, m.height = 150, 40
	m.bench.snap, err = api.Workbench.WorkbenchSnapshot(ctx, "book")
	if err != nil {
		t.Fatal(err)
	}
	m.bench.loaded = true
	m, cmd := submit(t, m, "/e")
	if cmd != nil || m.bench.exporting || !strings.Contains(m.bench.err, "用法") {
		t.Fatalf("missing path must explain usage: err=%q", m.bench.err)
	}
	m, _ = press(t, m, tea.KeyEsc)
	m, cmd = submit(t, m, "/e "+filepath.Join(t.TempDir(), "作品.txt"))
	if cmd == nil || !m.bench.exporting {
		t.Fatal("export not started")
	}
	message := cmd().(exportDoneMsg)
	updated, _ := m.Update(message)
	m = updated.(model)
	if m.bench.exporting || m.bench.err != "" || !strings.Contains(m.bench.notice, "已导出 1 章") {
		t.Fatalf("result state: %s %s", m.bench.err, m.bench.notice)
	}
	content, err := os.ReadFile(message.result.Path)
	if err != nil || !strings.Contains(string(content), "已确认正文。") {
		t.Fatalf("output: %s %v", content, err)
	}
}
