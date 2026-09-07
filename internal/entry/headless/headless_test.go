package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/service"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestProjectCreateAndShowCommands(t *testing.T) {
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	api := service.New(authorityStore)
	draftPath := filepath.Join(t.TempDir(), "draft.json")
	draft, _ := json.Marshal(service.ProjectDraft{
		Intent: domain.Intent{Premise: "凡人修仙"},
		Plan: []domain.PlanNode{
			{ID: "volume-1", Kind: domain.PlanVolume, Title: "第一卷", Summary: "入道"},
			{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "入门"},
			{ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Title: "第一章", Summary: "抵达"},
		},
	})
	if err := os.WriteFile(draftPath, draft, 0o644); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	var output bytes.Buffer
	var errorsOutput bytes.Buffer
	if err := Run(ctx, api, []string{
		"project", "create", "--project", "book-1", "--change", "create-book-1",
		"--user", "user-1", "--reason", "创建作品", "--draft", draftPath,
	}, &output, &errorsOutput); err != nil {
		t.Fatalf("project create: %v; stderr=%s", err, errorsOutput.String())
	}
	var created service.ProjectSnapshot
	if err := json.Unmarshal(output.Bytes(), &created); err != nil {
		t.Fatalf("decode create output: %v", err)
	}
	if created.ID != "book-1" || created.Revision != 1 {
		t.Fatalf("created project = %#v", created)
	}

	output.Reset()
	if err := Run(ctx, api, []string{"project", "show", "--project", "book-1"}, &output, &errorsOutput); err != nil {
		t.Fatalf("project show: %v", err)
	}
	var shown service.ProjectSnapshot
	if err := json.Unmarshal(output.Bytes(), &shown); err != nil {
		t.Fatalf("decode show output: %v", err)
	}
	if shown.Revision != created.Revision || shown.Intent.Premise != "凡人修仙" {
		t.Fatalf("shown project = %#v", shown)
	}
}

func TestProjectDirectiveCommands(t *testing.T) {
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	api := service.New(authorityStore)
	if _, err := api.CreateProject(ctx, service.CreateProjectCommand{
		ProjectID: "book-1", ChangeID: "create-book-1", UserID: "user-1", Reason: "创建作品",
		Draft: service.ProjectDraft{Intent: domain.Intent{Premise: "凡人修仙"}}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	var output, errorsOutput bytes.Buffer
	if err := Run(ctx, api, []string{
		"project", "directive", "add", "--project", "book-1", "--reason", "提要求",
		"--scope", "from_chapter:1", "--text", "每章结尾留钩子",
	}, &output, &errorsOutput); err == nil {
		t.Fatal("directive add without --user must fail")
	}
	output.Reset()
	if err := Run(ctx, api, []string{
		"project", "directive", "add", "--project", "book-1", "--user", "user-1", "--reason", "提要求",
		"--scope", "from_chapter:1", "--text", "每章结尾留钩子", "--target-words", "3000",
	}, &output, &errorsOutput); err != nil {
		t.Fatalf("directive add: %v; stderr=%s", err, errorsOutput.String())
	}
	var added service.DirectiveResult
	if err := json.Unmarshal(output.Bytes(), &added); err != nil {
		t.Fatalf("decode add output: %v", err)
	}
	if added.Revision != 2 || added.Directive.Scope != "from_chapter:1" ||
		added.Directive.Constraints == nil || added.Directive.Constraints.TargetWords != 3000 {
		t.Fatalf("added = %#v", added)
	}
	output.Reset()
	if err := Run(ctx, api, []string{"project", "show", "--project", "book-1"}, &output, &errorsOutput); err != nil {
		t.Fatalf("project show: %v", err)
	}
	var shown service.ProjectSnapshot
	if err := json.Unmarshal(output.Bytes(), &shown); err != nil {
		t.Fatalf("decode show output: %v", err)
	}
	if len(shown.Directives) != 1 || shown.Directives[0].Text != "每章结尾留钩子" {
		t.Fatalf("shown directives = %#v", shown.Directives)
	}
	output.Reset()
	if err := Run(ctx, api, []string{
		"project", "directive", "retire", "--project", "book-1", "--user", "user-1",
		"--reason", "不再需要", "--id", added.Directive.ID,
	}, &output, &errorsOutput); err != nil {
		t.Fatalf("directive retire: %v", err)
	}
	output.Reset()
	if err := Run(ctx, api, []string{"project", "directive", "list", "--project", "book-1"}, &output, &errorsOutput); err != nil {
		t.Fatalf("directive list: %v", err)
	}
	var listed []domain.Directive
	if err := json.Unmarshal(output.Bytes(), &listed); err != nil {
		t.Fatalf("decode list output: %v", err)
	}
	if len(listed) != 1 || listed[0].Status != domain.DirectiveRetired {
		t.Fatalf("listed = %#v", listed)
	}
}
