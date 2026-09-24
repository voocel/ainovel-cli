package bootstrap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/diag"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestDiagnosticsReadOnlyLocalDetailsAndShare(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	api := newTestApp(s)
	now := time.Now().UTC()
	const secret = "PRIVATE_SENTINEL"
	project, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{ProjectID: secret, ChangeID: "create", UserID: "user", Reason: "test", Draft: testProjectDraft(), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	runID := ensureTestRun(t, ctx, s, project.ID, now)
	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	op := model.Operation{ID: secret + "-task", RunID: runID, Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: project.ID}, Kind: model.OperationWriteChapter, State: model.OperationQueued,
		Snapshot: model.ExecutionSnapshot{Executor: "llm.agent@1", BaseRevision: project.Revision, InputDigest: model.Digest(input), ConfigDigest: secret, ApprovalPolicy: model.ApprovalManual}, Input: input, CreatedAt: now, UpdatedAt: now}
	if _, err := s.CreateOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimOperationForExecutor(ctx, op.ID, "worker", "llm.agent@1", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, event := range []struct{ kind, payload string }{
		{"agent.message_committed", `{"role":"tool","metadata":{"is_error":true},"content":[{"type":"text","text":"PRIVATE_SENTINEL"}]}`},
		{"agent.run_ended", `{"usage":{"input":12,"output":3,"cache_read":0,"cache_write":0,"total_tokens":15},"error":"PRIVATE_SENTINEL"}`},
	} {
		if _, err := s.AppendOperationEvent(ctx, model.OperationEvent{OperationID: op.ID, Attempt: claimed.Attempt, StepID: event.kind, IdempotencyKey: event.kind, Kind: event.kind, Payload: []byte(event.payload), CreatedAt: now.Add(time.Duration(i) * time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.TransitionOperation(ctx, op.ID, model.OperationRunning, model.OperationSucceeded, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	request := diag.Request{ProjectID: project.ID}
	summary, err := api.Diag.Inspect(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Metrics.Operations != 1 || len(summary.Findings) != 0 {
		t.Fatalf("summary false failure: %+v", summary)
	}
	for _, event := range summary.Events {
		if event.Text != "" {
			t.Fatal("summary loaded message bodies")
		}
	}
	request.OperationID = op.ID
	detail, err := api.Diag.Inspect(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	foundTool, foundUsage := false, false
	for _, finding := range detail.Findings {
		foundTool = foundTool || finding.Code == "execution.tool_error"
	}
	for _, event := range detail.Events {
		foundUsage = foundUsage || (event.Usage != nil && event.Usage.Input == 12)
	}
	if !foundTool || !foundUsage || detail.Metrics.States[model.OperationSucceeded] != 1 {
		t.Fatalf("self-corrected details lost: %+v", detail)
	}
	path := filepath.Join(t.TempDir(), "diagnostics.json")
	if err := api.Diag.ExportShare(ctx, request, path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), secret) {
		t.Fatalf("share leaked: %s %v", raw, err)
	}
	var shared diag.ShareReport
	if err := json.Unmarshal(raw, &shared); err != nil {
		t.Fatal(err)
	}
	sharedTool := false
	for _, f := range shared.Findings {
		sharedTool = sharedTool || f.Code == "execution.tool_error"
	}
	if !sharedTool {
		t.Fatal("selected-task share lost the observed tool error")
	}
	after, err := s.GetOperation(ctx, op.ID)
	if err != nil || after.State != model.OperationSucceeded || after.Attempt != claimed.Attempt {
		t.Fatalf("diagnostic mutated operation: %+v %v", after, err)
	}
	final, err := api.Projects.Project(ctx, project.ID, 0)
	if err != nil || final.Revision != project.Revision {
		t.Fatal("diagnostic changed project")
	}
	// A last event page still omits earlier events, despite having no next cursor.
	last, err := api.Diag.Inspect(ctx, diag.Request{ProjectID: project.ID, OperationID: op.ID, EventAfter: 3})
	if err != nil {
		t.Fatal(err)
	}
	if last.NextEventSequence != 0 || last.Metrics.Events <= len(last.Events) {
		t.Fatal("fixture is not a partial final page")
	}
	assertDiagCoverage(t, last, "events", "truncated")
	assertDiagCoverage(t, last, "statistics", "complete")

	// The failing task is beyond both the first UI page and the share budget.
	// Export must select it by its failure, independently of the UI cursor.
	for i := 0; i < 205; i++ {
		queued := op
		queued.ID = fmt.Sprintf("task-%03d", i)
		if _, err := s.CreateOperation(ctx, queued); err != nil {
			t.Fatal(err)
		}
	}
	failedID := "task-204"
	if _, err := s.ClaimOperationForExecutor(ctx, failedID, "worker", op.Snapshot.Executor, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionOperation(ctx, failedID, model.OperationRunning, model.OperationFailed, secret, now); err != nil {
		t.Fatal(err)
	}
	last, err = api.Diag.Inspect(ctx, diag.Request{ProjectID: project.ID, After: "task-199"})
	if err != nil {
		t.Fatal(err)
	}
	if last.NextOperationID != "" || len(last.Operations) != 5 {
		t.Fatal("fixture is not a final task page")
	}
	assertDiagCoverage(t, last, "operations", "truncated")
	for i, cursor := range []string{"", "task-049"} {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("share-%d.json", i))
		if err := api.Diag.ExportShare(ctx, diag.Request{ProjectID: project.ID, After: cursor}, path); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var report diag.ShareReport
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Operations) != 200 || report.Operations[0].State != "failed" || report.Operations[0].Executor == "" || report.OmittedTaskDetails != 6 {
			t.Fatalf("failed task context missing: %+v", report)
		}
		if strings.Contains(string(raw), secret) || strings.Contains(string(raw), failedID) {
			t.Fatal("share leaked private context")
		}
		found := false
		for _, f := range report.Findings {
			if f.Code == "execution.failed" {
				found = len(f.Evidence) == 1 && f.Evidence[0].Operation == report.Operations[0].ID
			}
		}
		if !found {
			t.Fatal("failure evidence lost task association")
		}
	}
}

// 单次自纠不该在运行级报成问题（任务成功了还报警就是误报），同一次尝试里反复撞同一堵墙
// 才是信号。但两种情况的总数都必须进 Metrics——运行级看不见报错，正是用户撞上的那个缺口。
func TestDiagnosticsRaiseToolErrorsOnlyWhenRepeated(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	api := newTestApp(s)
	now := time.Now().UTC()
	project, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
		ProjectID: "book", ChangeID: "create", UserID: "user", Reason: "test",
		Draft: testProjectDraft(), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	runID := ensureTestRun(t, ctx, s, project.ID, now)
	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	seed := func(id string, toolErrors int) {
		t.Helper()
		op := model.Operation{ID: id, RunID: runID, Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: project.ID},
			Kind: model.OperationWriteChapter, State: model.OperationQueued, Input: input, CreatedAt: now, UpdatedAt: now,
			Snapshot: model.ExecutionSnapshot{Executor: "llm.agent@1", BaseRevision: project.Revision,
				InputDigest: model.Digest(input), ConfigDigest: "profile", ApprovalPolicy: model.ApprovalManual}}
		if _, err := s.CreateOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		claimed, err := s.ClaimOperationForExecutor(ctx, id, "worker", "llm.agent@1", time.Hour, now)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < toolErrors; i++ {
			key := fmt.Sprintf("tool-%d", i)
			if _, err := s.AppendOperationEvent(ctx, model.OperationEvent{OperationID: id, Attempt: claimed.Attempt,
				StepID: key, IdempotencyKey: key, Kind: "agent.message_committed", CreatedAt: now,
				Payload: []byte(`{"role":"tool","metadata":{"is_error":true},"content":"workspace version conflict"}`)}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.TransitionOperation(ctx, id, model.OperationRunning, model.OperationSucceeded, "", now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	seed("self-corrected", 1)
	quiet, err := api.Diag.Inspect(ctx, diag.Request{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if quiet.Metrics.ToolErrors != 1 {
		t.Fatalf("工具报错计数 = %d, want 1", quiet.Metrics.ToolErrors)
	}
	for _, f := range quiet.Findings {
		if f.Code == "execution.tool_error" {
			t.Fatal("单次自纠被报成了运行级问题")
		}
	}

	seed("stuck", 5)
	noisy, err := api.Diag.Inspect(ctx, diag.Request{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if noisy.Metrics.ToolErrors != 6 {
		t.Fatalf("工具报错计数 = %d, want 6", noisy.Metrics.ToolErrors)
	}
	found := false
	for _, f := range noisy.Findings {
		if f.Code != "execution.tool_error" {
			continue
		}
		found = true
		if f.Count != 6 {
			t.Errorf("发现计数 = %d, want 6（总数而非分组数）", f.Count)
		}
		pointsAtStuck := false
		for _, e := range f.Evidence {
			pointsAtStuck = pointsAtStuck || e.OperationID == "stuck"
		}
		if !pointsAtStuck {
			t.Errorf("证据没有指向反复报错的任务：%+v", f.Evidence)
		}
	}
	if !found {
		t.Fatal("同一尝试里反复报错没有立发现")
	}
}

func assertDiagCoverage(t *testing.T, report diag.Report, source, status string) {
	t.Helper()
	for _, c := range report.Coverage {
		if c.Source == source && c.Status == status {
			return
		}
	}
	t.Fatalf("missing %s=%s: %+v", source, status, report.Coverage)
}
