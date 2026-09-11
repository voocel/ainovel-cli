package bootstrap_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

func TestWorkbenchReaderRefreshesAuthorityAndIndependentRunState(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	api := newTestApp(store)
	reader := api.Workbench.OpenReader("reader-book")
	if _, err := reader.Snapshot(ctx); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing project: %v", err)
	}
	if _, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{ProjectID: "reader-book", ChangeID: "create-reader", UserID: "user-1", Reason: "test", Draft: testProjectDraft(), CreatedAt: testTime()}); err != nil {
		t.Fatal(err)
	}
	runID := ensureTestRun(t, ctx, store, "reader-book", testTime())
	first, err := reader.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reader.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("unchanged snapshot differs: %v", err)
	}
	if _, err := api.Runs.PauseCreationRun(ctx, runID, testTime().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	paused, err := reader.Snapshot(ctx)
	if err != nil || paused.Revision != first.Revision || paused.Run.State != model.RunPaused {
		t.Fatalf("run state was cached with authority: %v %#v", err, paused.Run)
	}
	if _, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{ProjectID: "reader-book", ChangeID: "reader-directive", UserID: "user-1", Reason: "test", DirectiveID: "rule", Scope: "from_chapter:1", Text: "每章结尾留钩子", CreatedAt: testTime().Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	changed, err := reader.Snapshot(ctx)
	if err != nil || changed.Revision <= first.Revision || len(changed.Directives) != 1 {
		t.Fatalf("new revision not loaded: %v", err)
	}
	if len(first.Directives) != 0 {
		t.Fatal("old snapshot was mutated")
	}
	fresh, err := api.Workbench.WorkbenchSnapshot(ctx, "reader-book")
	if err != nil || !reflect.DeepEqual(changed, fresh) {
		t.Fatalf("reader differs from uncached query: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := reader.Snapshot(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached data hid query failure: %v", err)
	}
}

func BenchmarkWorkbenchReader500Chapters(b *testing.B) {
	ctx := context.Background()
	authority, err := store.Open(ctx, filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { authority.Close() })
	api := newTestApp(authority)
	projection := projectdoc.ProjectProjection{ProjectID: "long-reader", Intent: model.Intent{Premise: "雨夜来信", TargetChapters: 500}}
	projection.Plan = []model.PlanNode{{ID: "volume", Kind: model.PlanVolume, Title: "来信", Summary: "来信"}, {ID: "arc", ParentID: "volume", Kind: model.PlanArc, Title: "雨夜", Summary: "雨夜"}}
	for i := 1; i <= 500; i++ {
		planID, id := fmt.Sprintf("plan-%d", i), fmt.Sprintf("chapter-%d", i)
		projection.Plan = append(projection.Plan, model.PlanNode{ID: planID, ParentID: "arc", Kind: model.PlanChapter, Title: id, Summary: "雨夜来信", Order: i})
		projection.Manuscript = append(projection.Manuscript, model.ManuscriptChapter{ID: id, PlanNodeID: planID, Number: i, Title: id, Author: model.AuthorUser, Blocks: []model.ManuscriptBlock{{ID: fmt.Sprintf("block-%d", i), Text: strings.Repeat("雨停了，屋檐却还在滴水。", 250)}}})
	}
	proposal, err := api.Projects.ImportNewProject(ctx, "import-long", "user-1", "benchmark", projection, testTime())
	if err != nil {
		b.Fatal(err)
	}
	if _, err := api.Decisions.Approve(ctx, proposal.ID, "user-1", testTime().Add(time.Minute)); err != nil {
		b.Fatal(err)
	}
	reader := api.Workbench.OpenReader(projection.ProjectID)
	if _, err := reader.Snapshot(ctx); err != nil {
		b.Fatal(err)
	}
	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := api.Workbench.WorkbenchSnapshot(ctx, projection.ProjectID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("same_revision", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := reader.Snapshot(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
}
