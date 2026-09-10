package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestProjectionPreservesAttachmentsAndRejectsMissingMediaBeforeImport(t *testing.T) {
	ctx := context.Background()
	api, _ := newRenderingService(t, domain.ApprovalAuto)
	run := startRenderingRun(t, api, 1, serviceTime())
	if _, err := api.driveCreationRun(ctx, run, renderingCommand(serviceTime())); err != nil {
		t.Fatal(err)
	}
	projection, err := api.ExportProject(ctx, renderingProject, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Attachments) != 3 {
		t.Fatalf("attachments omitted: %+v", projection)
	}
	projection.Attachments[0].Role = "alternate-cover"
	proposal, err := api.ImportProject(ctx, "edit-attachment", "user-1", "编辑引用", projection, serviceTime().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Patches) != 1 || proposal.Patches[0].Document.Kind != domain.DocumentAttachment {
		t.Fatalf("unexpected roundtrip changes: %+v", proposal.Patches)
	}
	if _, err := api.Approve(ctx, proposal.ID, "user-1", serviceTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	other := New(openServiceStore(t))
	if _, err := other.ImportNewProject(ctx, "import", "user-1", "跨库导入", projection, serviceTime()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing media import was accepted: %v", err)
	}
	if _, err := other.Project(ctx, renderingProject, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed preflight created a partial book: %v", err)
	}
}
