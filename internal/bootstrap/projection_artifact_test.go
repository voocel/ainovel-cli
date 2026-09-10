package bootstrap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestProjectionPreservesAttachmentsAndRejectsMissingMediaBeforeImport(t *testing.T) {
	ctx := context.Background()
	api, _ := newRenderingApp(t, model.ApprovalAuto)
	run := startRenderingRun(t, api, 1, testTime())
	if _, err := api.Runs.Drive(ctx, run, renderingTasks(api), renderingCommand(testTime())); err != nil {
		t.Fatal(err)
	}
	projection, err := api.Projects.ExportProject(ctx, renderingProject, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Attachments) != 3 {
		t.Fatalf("attachments omitted: %+v", projection)
	}
	projection.Attachments[0].Role = "alternate-cover"
	proposal, err := api.Projects.ImportProject(ctx, "edit-attachment", "user-1", "编辑引用", projection, testTime().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Patches) != 1 || proposal.Patches[0].Document.Kind != model.DocumentAttachment {
		t.Fatalf("unexpected roundtrip changes: %+v", proposal.Patches)
	}
	if _, err := api.Decisions.Approve(ctx, proposal.ID, "user-1", testTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	other := newTestApp(openTestStore(t))
	if _, err := other.Projects.ImportNewProject(ctx, "import", "user-1", "跨库导入", projection, testTime()); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing media import was accepted: %v", err)
	}
	if _, err := other.Projects.Project(ctx, renderingProject, 0); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("failed preflight created a partial book: %v", err)
	}
}
