package orchestrator

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestTaskSchedulerAfterCommitQueuesNextWriteTask(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 3); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	if err := store.Progress.MarkChapterComplete(1, 1200, "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}
	scheduler := newTaskScheduler(rt)

	if err := scheduler.AfterCommit(progress, &domain.CommitResult{
		Chapter:        1,
		NextChapter:    2,
		ReviewRequired: false,
	}, nil); err != nil {
		t.Fatalf("AfterCommit: %v", err)
	}

	tasks := rt.Snapshot()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 queued task, got %d", len(tasks))
	}
	if tasks[0].Kind != domain.TaskChapterWrite {
		t.Fatalf("expected chapter_write task, got %s", tasks[0].Kind)
	}
	if tasks[0].Chapter != 2 {
		t.Fatalf("expected next chapter 2, got %d", tasks[0].Chapter)
	}
}

func TestTaskSchedulerSyncFlowReviewingQueuesReviewTask(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 5); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	if err := store.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowReviewing); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}
	scheduler := newTaskScheduler(rt)

	if err := scheduler.SyncFlow(domain.FlowReviewing, progress); err != nil {
		t.Fatalf("SyncFlow: %v", err)
	}

	tasks := rt.Snapshot()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 queued task, got %d", len(tasks))
	}
	if tasks[0].Kind != domain.TaskChapterReview {
		t.Fatalf("expected chapter_review task, got %s", tasks[0].Kind)
	}
	if tasks[0].Owner != "editor" {
		t.Fatalf("expected editor owner, got %s", tasks[0].Owner)
	}
}
