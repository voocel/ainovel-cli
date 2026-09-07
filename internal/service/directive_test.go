package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestAddAndRetireDirectiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	api := New(openServiceStore(t))
	now := serviceTime()
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "directed-book", ChangeID: "create", UserID: "user-1", Reason: "建立作品",
		Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	added, err := api.AddDirective(ctx, AddDirectiveCommand{
		ProjectID: "directed-book", ChangeID: "directive-1", UserID: "user-1", Reason: "提要求",
		DirectiveID: "d-1", Scope: "from_chapter:1", Text: "每章结尾留钩子",
		Constraints: &domain.DirectiveConstraints{TargetWords: 3000}, CreatedAt: now.Add(time.Minute),
	})
	if err != nil || added.Revision != 2 || added.Directive.Status != domain.DirectiveActive {
		t.Fatalf("add = %#v, %v", added, err)
	}

	// 同 ID 同内容重复提交幂等：不产生新 Revision。
	repeated, err := api.AddDirective(ctx, AddDirectiveCommand{
		ProjectID: "directed-book", ChangeID: "directive-1-again", UserID: "user-1", Reason: "重复",
		DirectiveID: "d-1", Scope: "from_chapter:1", Text: "每章结尾留钩子",
		Constraints: &domain.DirectiveConstraints{TargetWords: 3000}, CreatedAt: now.Add(2 * time.Minute),
	})
	if err != nil || repeated.Revision != 2 {
		t.Fatalf("repeated add = %#v, %v", repeated, err)
	}

	bench, err := api.WorkbenchSnapshot(ctx, "directed-book")
	if err != nil || len(bench.Directives) != 1 || bench.Directives[0].ID != "d-1" {
		t.Fatalf("workbench directives = %#v, %v", bench.Directives, err)
	}

	retired, err := api.RetireDirective(ctx, RetireDirectiveCommand{
		ProjectID: "directed-book", ChangeID: "retire-1", UserID: "user-1", Reason: "不再需要",
		DirectiveID: "d-1", CreatedAt: now.Add(3 * time.Minute),
	})
	if err != nil || retired.Revision != 3 || retired.Directive.Status != domain.DirectiveRetired {
		t.Fatalf("retire = %#v, %v", retired, err)
	}
	again, err := api.RetireDirective(ctx, RetireDirectiveCommand{
		ProjectID: "directed-book", ChangeID: "retire-2", UserID: "user-1", Reason: "重复退役",
		DirectiveID: "d-1", CreatedAt: now.Add(4 * time.Minute),
	})
	if err != nil || again.Revision != 3 {
		t.Fatalf("repeated retire = %#v, %v", again, err)
	}
	project, err := api.Project(ctx, "directed-book", 0)
	if err != nil || len(project.Directives) != 1 || project.Directives[0].Status != domain.DirectiveRetired {
		t.Fatalf("project directives = %#v, %v", project.Directives, err)
	}
	bench, err = api.WorkbenchSnapshot(ctx, "directed-book")
	if err != nil || len(bench.Directives) != 0 {
		t.Fatalf("workbench should hide retired directives: %#v, %v", bench.Directives, err)
	}

	// 投影往返：导出含退役记录；导入新增一条不会误删已有记录。
	projection, err := api.ExportProject(ctx, "directed-book", 0)
	if err != nil || len(projection.Directives) != 1 {
		t.Fatalf("export directives = %#v, %v", projection.Directives, err)
	}
	projection.Directives = append(projection.Directives, domain.Directive{
		ID: "d-2", Scope: "plan_node:chapter-plan-1", Text: "第一章主角要落败", Status: domain.DirectiveActive,
	})
	proposal, err := api.ImportProject(ctx, "import-directive", "user-1", "补要求", projection, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := api.Approve(ctx, proposal.ID, "user-1", now.Add(6*time.Minute)); err != nil {
		t.Fatalf("approve import: %v", err)
	}
	project, err = api.Project(ctx, "directed-book", 0)
	if err != nil || project.Revision != 4 || len(project.Directives) != 2 {
		t.Fatalf("after import directives = %#v (rev %d), %v", project.Directives, project.Revision, err)
	}

	if _, err := api.AddDirective(ctx, AddDirectiveCommand{
		ProjectID: "directed-book", ChangeID: "bad", UserID: "user-1", Reason: "x",
		Scope: "chapter:3", Text: "x", CreatedAt: now.Add(7 * time.Minute),
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid scope error = %v, want ErrInvalid", err)
	}
}
