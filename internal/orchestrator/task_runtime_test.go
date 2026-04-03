package orchestrator

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestNovelTaskRuntimePrefersRunningTaskForOwner(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}

	if _, err := rt.Start(domain.TaskChapterRewrite, "writer", "重写第 3 章", "", taskLocation{Chapter: 3}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := rt.Queue(domain.TaskChapterRewrite, "writer", "重写第 5 章", "", taskLocation{Chapter: 5}); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if err := rt.UpdateProgress("writer", func(task *domain.TaskRecord) {
		task.Progress.Summary = "正在处理第 3 章"
	}); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}

	tasks := rt.Snapshot()
	byChapter := make(map[int]domain.TaskRecord, len(tasks))
	for _, task := range tasks {
		byChapter[task.Chapter] = task
	}
	if got := byChapter[3].Progress.Summary; got != "正在处理第 3 章" {
		t.Fatalf("expected running task to receive progress, got %q", got)
	}
	if got := byChapter[5].Progress.Summary; got != "" {
		t.Fatalf("expected queued task to stay untouched, got %q", got)
	}

	if err := rt.CompleteActive("writer"); err != nil {
		t.Fatalf("CompleteActive: %v", err)
	}
	tasks = rt.Snapshot()
	byChapter = make(map[int]domain.TaskRecord, len(tasks))
	for _, task := range tasks {
		byChapter[task.Chapter] = task
	}
	if got := byChapter[3].Status; got != domain.TaskSucceeded {
		t.Fatalf("expected chapter 3 succeeded, got %s", got)
	}
	if got := byChapter[5].Status; got != domain.TaskQueued {
		t.Fatalf("expected chapter 5 remain queued, got %s", got)
	}
}

func TestExecutePolicyActionsClearHandledSteerKeepsRewriteQueue(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 5); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	if err := store.Progress.MarkChapterComplete(1, 1200, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowSteering); err != nil {
		t.Fatalf("SetFlow steering: %v", err)
	}
	if err := store.RunMeta.Init("default", "test-provider", "test-model"); err != nil {
		t.Fatalf("Init run meta: %v", err)
	}
	if err := store.RunMeta.SetPendingSteer("加入反转"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}
	s := &session{
		store:     store,
		taskRT:    rt,
		scheduler: newTaskScheduler(rt),
	}

	s.executePolicyActions([]policyAction{
		setPendingRewrites([]int{1}, "需要修复动机"),
		setFlow(domain.FlowRewriting),
		clearHandledSteerAction(),
	}, nil)

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if progress.Flow != domain.FlowRewriting {
		t.Fatalf("expected flow rewriting, got %s", progress.Flow)
	}
	if len(progress.PendingRewrites) != 1 || progress.PendingRewrites[0] != 1 {
		t.Fatalf("expected pending rewrite [1], got %v", progress.PendingRewrites)
	}

	runMeta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("Load run meta: %v", err)
	}
	if runMeta.PendingSteer != "" {
		t.Fatalf("expected pending steer cleared, got %q", runMeta.PendingSteer)
	}

	tasks := rt.Snapshot()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Kind != domain.TaskChapterRewrite {
		t.Fatalf("expected rewrite task, got %s", tasks[0].Kind)
	}
	if tasks[0].Status != domain.TaskQueued {
		t.Fatalf("expected queued rewrite task, got %s", tasks[0].Status)
	}
}

func TestExecutePolicyActionsClearHandledSteerDoesNotFinishRuntimeCoordinatorTask(t *testing.T) {
	dir := t.TempDir()
	store := storepkg.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 5); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	if err := store.Progress.MarkChapterComplete(1, 1200, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowSteering); err != nil {
		t.Fatalf("SetFlow steering: %v", err)
	}
	if err := store.RunMeta.Init("default", "test-provider", "test-model"); err != nil {
		t.Fatalf("Init run meta: %v", err)
	}
	if err := store.RunMeta.SetPendingSteer("加入反转"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}

	rt, err := newNovelTaskRuntime(store)
	if err != nil {
		t.Fatalf("newNovelTaskRuntime: %v", err)
	}
	if _, err := rt.Start(domain.TaskCoordinatorDecision, coordinatorRuntimeOwner, "协调中", "", taskLocation{}); err != nil {
		t.Fatalf("Start runtime coordinator task: %v", err)
	}
	if _, err := rt.Start(domain.TaskSteerApply, "coordinator", "处理用户干预", "加入反转", taskLocation{}); err != nil {
		t.Fatalf("Start steer task: %v", err)
	}

	s := &session{
		store:     store,
		taskRT:    rt,
		scheduler: newTaskScheduler(rt),
	}
	s.executePolicyActions([]policyAction{clearHandledSteerAction()}, nil)

	tasks := rt.Snapshot()
	statusByOwner := map[string]domain.TaskStatus{}
	kindByOwner := map[string]domain.TaskKind{}
	for _, task := range tasks {
		if task.Status == domain.TaskSucceeded || task.Status == domain.TaskRunning {
			statusByOwner[task.Owner] = task.Status
			kindByOwner[task.Owner] = task.Kind
		}
	}
	if got := statusByOwner["coordinator"]; got != domain.TaskSucceeded {
		t.Fatalf("expected steer task succeeded, got %s", got)
	}
	if got := kindByOwner["coordinator"]; got != domain.TaskSteerApply {
		t.Fatalf("expected coordinator task kind steer_apply, got %s", got)
	}
	if got := statusByOwner[coordinatorRuntimeOwner]; got != domain.TaskRunning {
		t.Fatalf("expected runtime coordinator task still running, got %s", got)
	}
	if got := kindByOwner[coordinatorRuntimeOwner]; got != domain.TaskCoordinatorDecision {
		t.Fatalf("expected runtime task kind coordinator_decision, got %s", got)
	}
}
