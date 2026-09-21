package task

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/profile"
	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// A recording compiler isolates the binding contract from provider/prompt details.
type recordingProfiles struct {
	compiled map[string]prompt.Compiled
	last     profile.CompileCommand
}

func (p *recordingProfiles) Compile(_ context.Context, command profile.CompileCommand) (prompt.Compiled, error) {
	p.last = command
	c := prompt.Compiled{ProjectID: command.ProjectID, WorkerProfile: command.WorkerProfileID + "@1", CoreProtocolVersion: command.CoreProtocolVersion}
	for _, ref := range command.Packs {
		c.Sources = append(c.Sources, prompt.Source{Layer: "pack_defaults", ID: ref.ID + "@1", Revision: ref.Revision})
	}
	for _, ref := range command.CreatorProfiles {
		c.Sources = append(c.Sources, prompt.Source{Layer: "creator_profile", ID: ref.ID + "/" + ref.Scope, Revision: ref.Revision})
	}
	c.ProfileDigest, _ = model.DigestJSON(c)
	p.compiled[c.ProfileDigest] = c
	return c, nil
}
func (p *recordingProfiles) Load(_ context.Context, digest string) (prompt.Compiled, error) {
	return p.compiled[digest], nil
}

func restartManager(t *testing.T) (*Manager, *recordingProfiles, StartOperationCommand) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	_, err = change.New(s).CommitUser(ctx, model.Proposal{ID: "create", Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book"}, Author: model.Author{Kind: model.AuthorUser, ID: "user"}, Reason: "create", ApprovalState: model.ApprovalPending, CreatedAt: now, Patches: []model.Patch{{Document: model.DocumentRef{Kind: model.DocumentIntent, ID: "root"}, Operation: model.PatchPut, Content: json.RawMessage(`{"premise":"test","target_chapters":1}`)}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateCreationRun(ctx, model.CreationRun{ID: "run", ProjectID: "book", Goal: model.NovelGoal{Premise: "test", TargetChapters: 1}.Goal(), Strategy: model.CreationRunStrategy{PlanWindowChapters: 1, ReviewCadence: model.ReviewPerPlanWindow}, Preset: model.CreationRunPreset{Source: "test", Digest: "test", Approval: model.ApprovalAuto}, State: model.RunRunning, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	p := &recordingProfiles{compiled: make(map[string]prompt.Compiled)}
	m := New(s, nil, ExecutorSet{}, p)
	return m, p, StartOperationCommand{ProjectID: "book", RunID: "run", OperationID: "source", Kind: model.OperationWriteChapter, Input: json.RawMessage(`{"chapter_plan_id":"chapter","chapter_number":1}`), WorkerProfileID: "writer", CoreProtocolVersion: "core-v1", ApprovalPolicy: model.ApprovalAuto, CreatedAt: now}
}

func TestCreationRestartKeepsExplicitExecutionSettings(t *testing.T) {
	m, p, base := restartManager(t)
	ctx := context.Background()
	first, err := m.StartOperation(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.CancelOperation(ctx, first.ID, base.CreatedAt); err != nil {
		t.Fatal(err)
	}
	base.WorkerProfileID = "new-writer"
	input, err := model.DecodeTaskInput(base.Kind, base.Input)
	if err != nil {
		t.Fatal(err)
	}
	successor, err := m.ForCreation(base).Restart(ctx, first.ID, base.RunID, creation.WorkItem{ID: "successor", Kind: base.Kind, Input: input}, base.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if p.last.WorkerProfileID != base.WorkerProfileID {
		t.Fatalf("dropped explicit execution settings: %#v", p.last)
	}
	if successor.Snapshot.Executor != prompt.ExecutorIdentity {
		t.Fatalf("wrong executor: %#v", successor.Snapshot)
	}
}

func TestRestartSameIDRejectsDifferentResources(t *testing.T) {
	for _, which := range []string{"pack", "profile"} {
		t.Run(which, func(t *testing.T) {
			m, profiles, base := restartManager(t)
			ctx := context.Background()
			first, err := m.StartOperation(ctx, base)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.CancelOperation(ctx, first.ID, base.CreatedAt); err != nil {
				t.Fatal(err)
			}
			command := RestartOperationCommand{FromOperationID: first.ID, OperationID: "successor", CreatedAt: base.CreatedAt.Add(time.Second), Packs: []resource.PackRef{{ID: "pack-a", Revision: 1}}, CreatorProfiles: []resource.CreatorProfileRef{{ID: "profile-a", Scope: "global", Revision: 1}}}
			if _, err = m.RestartOperation(ctx, command); err != nil {
				t.Fatal(err)
			}
			_, err = change.New(m.store).CommitUser(ctx, model.Proposal{
				ID: "later-edit", Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book"}, BaseRevision: 1,
				Author: model.Author{Kind: model.AuthorUser, ID: "user"}, Reason: "edit after restart",
				ApprovalState: model.ApprovalPending, CreatedAt: base.CreatedAt.Add(2 * time.Second),
				Patches: []model.Patch{{Document: model.DocumentRef{Kind: model.DocumentIntent, ID: "root"}, Operation: model.PatchPut, Content: json.RawMessage(`{"premise":"edited","target_chapters":1}`)}},
			}, base.CreatedAt.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.RestartOperation(ctx, command); err != nil {
				t.Fatalf("identical retry after source changed: %v", err)
			}
			if profiles.last.Revision != 1 {
				t.Fatalf("retry adopted current revision: %d", profiles.last.Revision)
			}
			if which == "pack" {
				command.Packs[0].ID = "pack-b"
			} else {
				command.CreatorProfiles[0].ID = "profile-b"
			}
			if _, err = m.RestartOperation(ctx, command); !errors.Is(err, model.ErrIdempotencyConflict) {
				t.Fatalf("different resources accepted: %v", err)
			}
		})
	}
}

type identityExecutor string

func (e identityExecutor) Identity() string { return string(e) }
func (e identityExecutor) Execute(context.Context, model.Operation) (model.OperationOutcome, error) {
	panic("not used")
}

func TestRestartSameIDRejectsDifferentExternalExecutor(t *testing.T) {
	m, _, base := restartManager(t)
	m.executors.External = identityExecutor("external@1")
	base.Kind = model.OperationGenerateAsset
	base.Input = json.RawMessage(`{"role":"cover","target":{"kind":"intent","id":"root"},"basis":{"documents":[{"ref":{"kind":"intent","id":"root"},"revision":1}]}}`)
	base.ConfigDigest = "external-config"
	ctx := context.Background()
	first, err := m.StartOperation(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelOperation(ctx, first.ID, base.CreatedAt); err != nil {
		t.Fatal(err)
	}
	command := RestartOperationCommand{FromOperationID: first.ID, OperationID: "successor", CreatedAt: base.CreatedAt.Add(time.Second)}
	if _, err := m.RestartOperation(ctx, command); err != nil {
		t.Fatal(err)
	}
	m.executors.External = identityExecutor("external@2")
	if _, err := m.RestartOperation(ctx, command); !errors.Is(err, model.ErrIdempotencyConflict) {
		t.Fatalf("executor identity change accepted: %v", err)
	}
}
