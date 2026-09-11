package bootstrap_test

import (
	"context"
	"testing"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestLibraryUsesCurrentRunGoalAndRecentActivity(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	api := newTestApp(store)
	for _, id := range []string{"a", "z", "idle"} {
		draft := testProjectDraft()
		draft.Intent.TargetChapters = 500
		if _, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{ProjectID: id, ChangeID: "create-" + id, UserID: "user", Reason: "test", Draft: draft, CreatedAt: testTime()}); err != nil {
			t.Fatal(err)
		}
	}
	a := ensureTestRun(t, ctx, store, "a", testTime())
	ensureTestRun(t, ctx, store, "z", testTime().Add(time.Minute))
	entries, err := api.Workbench.Library(ctx)
	if err != nil || len(entries) != 3 || entries[0].ID != "z" || entries[0].Target != 3 || entries[0].State != model.RunRunning || entries[2].ID != "idle" || entries[2].Target != 500 {
		t.Fatalf("library: %+v %v", entries, err)
	}
	if _, err := api.Runs.PauseCreationRun(ctx, a, testTime().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	entries, err = api.Workbench.Library(ctx)
	if err != nil || entries[0].ID != "a" || entries[0].State != model.RunPaused {
		t.Fatalf("updated library: %+v %v", entries, err)
	}
}
