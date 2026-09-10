package bootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	tasks "github.com/voocel/ainovel-cli/internal/app/task"
	runs "github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// fakeGenerationService 是内存里的外部生成服务：按 RequestID 幂等提交；recoverable
// 控制能否按 ID 查询状态，模拟服务端不支持恢复或查询超时。
type fakeGenerationService struct {
	submits     int
	jobs        map[string]string
	recoverable bool
	onSubmit    func(requestID string)
}

func (f *fakeGenerationService) Submit(requestID, source string) string {
	f.submits++
	if f.jobs == nil {
		f.jobs = map[string]string{}
	}
	f.jobs[requestID] = "rendered:" + source
	if f.onSubmit != nil {
		f.onSubmit(requestID)
	}
	return f.jobs[requestID]
}

// Status 返回请求结果；known=false 表示服务不认识该请求，err 表示服务无法回答。
func (f *fakeGenerationService) Status(requestID string) (content string, known bool, err error) {
	if !f.recoverable {
		return "", false, errors.New("status endpoint unavailable")
	}
	content, known = f.jobs[requestID]
	return content, known, nil
}

// externalGenerationExecutor 是外部任务契约（D46）的受控实现：提交前先落请求记录，
// 恢复按记录里的 RequestID 查询而不是重提；产出工件并提交附件提案。
type externalGenerationExecutor struct {
	store   *store.Store
	service *fakeGenerationService
	now     time.Time
	calls   int
}

func (externalGenerationExecutor) Identity() string { return "external.test@1" }

func (externalGenerationExecutor) ConfigDigest() string { return "render-v1" }

func (e *externalGenerationExecutor) Execute(ctx context.Context, operation model.Operation) (model.OperationOutcome, error) {
	e.calls++
	input, err := model.TaskInputAs[model.GenerateAssetInput](operation)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	content, err := e.resolve(ctx, operation, input)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	writer, err := e.store.NewArtifactWriter(operation.ID, operation.Attempt)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	writer.Write([]byte(content))
	digest, size, err := writer.Publish()
	if err != nil {
		return model.OperationOutcome{}, err
	}
	artifact := model.Artifact{
		ID: model.ArtifactID(operation.ID, input.Role), ProjectID: operation.Target.ID, Digest: digest,
		MediaType: "text/plain", Size: size, Basis: input.Basis,
		OperationID: operation.ID, Attempt: operation.Attempt, CreatedAt: e.now,
	}
	attachment, err := json.Marshal(model.Attachment{
		ID: input.Role + ":" + input.Target.Key(), Target: input.Target, Role: input.Role, Artifact: artifact.Ref(),
	})
	if err != nil {
		return model.OperationOutcome{}, err
	}
	return model.OperationOutcome{
		Artifacts: []model.Artifact{artifact},
		Proposal: &model.Proposal{
			ID: operation.ID + "-proposal", OperationID: operation.ID, Target: operation.Target,
			BaseRevision: operation.Snapshot.BaseRevision,
			Author:       model.Author{Kind: model.AuthorExtension, ID: e.Identity()},
			Reason:       "external asset generated",
			Patches: []model.Patch{{
				Document:  model.DocumentRef{Kind: model.DocumentAttachment, ID: input.Role + ":" + input.Target.Key()},
				Operation: model.PatchPut, Content: attachment,
			}},
			ApprovalState: model.ApprovalPending, CreatedAt: e.now.Add(time.Duration(e.calls) * time.Second),
		},
	}, nil
}

// resolve 按 D46 流程取结果：没有记录就先落记录再提交；自身记录按 ID 恢复，服务不认识
// 则同 ID 重提，服务无法回答即结果未知；继承自前任的记录恢复一次，仍拿不到就换新 ID 重提。
func (e *externalGenerationExecutor) resolve(ctx context.Context, operation model.Operation, input model.GenerateAssetInput) (string, error) {
	record, version, err := e.readRequest(ctx, operation.ID)
	if errors.Is(err, model.ErrNotFound) {
		return e.submit(ctx, operation, input, 0)
	}
	if err != nil {
		return "", err
	}
	if record.Identity != model.ExternalIdentityFor(operation) {
		if record.OperationID == operation.ID {
			return "", fmt.Errorf("request identity differs from its operation: %w", model.ErrInvalid)
		}
		return e.submit(ctx, operation, input, version)
	}
	content, known, statusErr := e.service.Status(record.RequestID)
	switch {
	case statusErr == nil && known:
		return content, nil
	case record.OperationID != operation.ID:
		return e.submit(ctx, operation, input, version)
	case statusErr != nil:
		return "", fmt.Errorf("request %s: %w", record.RequestID, model.ErrResultUnknown)
	default:
		return e.service.Submit(record.RequestID, input.Target.ID), nil
	}
}

func (e *externalGenerationExecutor) readRequest(ctx context.Context, operationID string) (model.ExternalRequestRecord, int64, error) {
	artifact, err := e.store.GetWorkspaceArtifact(ctx, operationID, model.ExternalRequestKey)
	if err != nil {
		return model.ExternalRequestRecord{}, 0, err
	}
	var record model.ExternalRequestRecord
	if err := model.DecodeStrict(artifact.Content, &record); err != nil {
		return model.ExternalRequestRecord{}, 0, err
	}
	return record, artifact.Version, record.Validate()
}

// submit 先落请求记录（受 attempt 围栏保护）再提交：崩溃后恢复只会查询，不会重提。
func (e *externalGenerationExecutor) submit(ctx context.Context, operation model.Operation, input model.GenerateAssetInput, expectedVersion int64) (string, error) {
	requestID := fmt.Sprintf("%s#%d", operation.ID, operation.Attempt)
	record, err := json.Marshal(model.ExternalRequestRecord{
		OperationID: operation.ID, Attempt: operation.Attempt, RequestID: requestID, SubmittedAt: e.now, Identity: model.ExternalIdentityFor(operation),
	})
	if err != nil {
		return "", err
	}
	if _, err := e.store.PutWorkspaceArtifact(ctx, model.WorkspaceArtifact{
		OperationID: operation.ID, Key: model.ExternalRequestKey, MediaType: model.ExternalRequestMediaType,
		Content: record, UpdatedAt: e.now,
	}, expectedVersion, operation.Attempt); err != nil {
		return "", err
	}
	return e.service.Submit(requestID, input.Target.ID), nil
}

const externalTestProject = "asset-book"

func newExternalTestService(t *testing.T) (*testApp, *externalGenerationExecutor) {
	t.Helper()
	ctx := context.Background()
	authorityStore := openTestStore(t)
	executor := &externalGenerationExecutor{store: authorityStore, service: &fakeGenerationService{recoverable: true}, now: testTime()}
	api := newTestAppWithExecutors(authorityStore, tasks.ExecutorSet{External: executor})
	api.now = testTime
	if _, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
		ProjectID: externalTestProject, ChangeID: "create", UserID: "user-1", Reason: "建书",
		Draft: testProjectDraft(), CreatedAt: testTime(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	strategy := model.CreationRunStrategy{PlanWindowChapters: 1, ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: 1}
	preset, err := model.NewCreationRunPreset("test", model.ApprovalAuto, strategy)
	if err != nil {
		t.Fatalf("preset: %v", err)
	}
	if _, err := api.Runs.StartCreationRun(ctx, runs.StartCreationRunCommand{
		RunID: "run:" + externalTestProject + ":1", ProjectID: externalTestProject,
		Goal: model.NovelGoal{Premise: "故事", TargetChapters: 1}.Goal(), Strategy: strategy, Preset: preset, CreatedAt: testTime(),
	}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	return api, executor
}

func assetTestInput() model.GenerateAssetInput {
	target := model.DocumentRef{Kind: model.DocumentPlan, ID: "chapter-plan-1"}
	return model.GenerateAssetInput{
		Target: target, Role: "cover",
		Basis: model.EvidenceBasis{Documents: []model.DocumentBasis{{Ref: target, Revision: 1}}},
	}
}

func startAssetOperation(t *testing.T, api *testApp, id string) model.Operation {
	t.Helper()
	input, err := json.Marshal(assetTestInput())
	if err != nil {
		t.Fatalf("encode input: %v", err)
	}
	operation, err := api.Tasks.StartOperation(context.Background(), tasks.StartOperationCommand{
		OperationID: id, ProjectID: externalTestProject, Kind: model.OperationGenerateAsset, Input: input,
		ConfigDigest: "render-v1", RunID: "run:" + externalTestProject + ":1", CreatedAt: testTime(),
	})
	if err != nil {
		t.Fatalf("start asset operation: %v", err)
	}
	return operation
}

// submitThenCrash 模拟"提交后进程崩溃"：领取后落记录并提交，然后让租约过期。
func submitThenCrash(t *testing.T, api *testApp, executor *externalGenerationExecutor, id string) {
	t.Helper()
	ctx := context.Background()
	claimed, err := api.store.ClaimOperationForExecutor(ctx, id, "crashed-worker", executor.Identity(), time.Minute, testTime())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := executor.submit(ctx, claimed, assetTestInput(), 0); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if recovered, err := api.Tasks.RecoverOperations(ctx, testTime().Add(2*time.Minute)); err != nil || len(recovered) != 1 {
		t.Fatalf("recover expired = %v, %v", recovered, err)
	}
}

func TestExternalExecutorPersistsRequestBeforeSubmit(t *testing.T) {
	ctx := context.Background()
	api, executor := newExternalTestService(t)
	operation := startAssetOperation(t, api, "asset-1")
	var recordedAtSubmit string
	executor.service.onSubmit = func(requestID string) {
		record, _, err := executor.readRequest(ctx, operation.ID)
		if err != nil {
			t.Fatalf("request record must exist before submit: %v", err)
		}
		recordedAtSubmit = record.RequestID
	}
	result, err := api.Tasks.RunOperation(ctx, operation.ID, "worker", time.Minute, testTime().Add(time.Minute))
	if err != nil || result.Operation.State != model.OperationSucceeded || len(result.Artifacts) != 1 {
		t.Fatalf("run = %#v, %v", result.Operation, err)
	}
	if executor.service.submits != 1 || recordedAtSubmit != "asset-1#1" {
		t.Fatalf("submits = %d, recorded request = %q", executor.service.submits, recordedAtSubmit)
	}
	project, err := api.Projects.Project(ctx, externalTestProject, 0)
	if err != nil || len(project.Attachments) != 1 || project.Attachments[0].Artifact != result.Artifacts[0].Ref() {
		t.Fatalf("attachments = %#v, %v", project.Attachments, err)
	}
	if content, err := api.store.ReadArtifact(result.Artifacts[0].Digest); err != nil || string(content) != "rendered:chapter-plan-1" {
		t.Fatalf("artifact content = %q, %v", content, err)
	}
}

func TestExternalExecutorRecoversByRequestIDAfterLeaseExpiry(t *testing.T) {
	ctx := context.Background()
	api, executor := newExternalTestService(t)
	operation := startAssetOperation(t, api, "asset-1")
	submitThenCrash(t, api, executor, operation.ID)
	result, err := api.Tasks.RunOperation(ctx, operation.ID, "worker-2", time.Minute, testTime().Add(3*time.Minute))
	if err != nil || result.Operation.State != model.OperationSucceeded || result.Operation.Attempt != 2 {
		t.Fatalf("run = %#v, %v", result.Operation, err)
	}
	if executor.service.submits != 1 {
		t.Fatalf("recovery must not resubmit, submits = %d", executor.service.submits)
	}
}

func TestExternalUnknownResultFailsWithCodeAndUserResumeReconciles(t *testing.T) {
	ctx := context.Background()
	api, executor := newExternalTestService(t)
	operation := startAssetOperation(t, api, "asset-1")
	submitThenCrash(t, api, executor, operation.ID)
	executor.service.recoverable = false
	result, err := api.Tasks.RunOperation(ctx, operation.ID, "worker-2", time.Minute, testTime().Add(3*time.Minute))
	if !errors.Is(err, model.ErrResultUnknown) || result.Operation.State != model.OperationFailed ||
		result.Operation.FailureCode != model.FailureResultUnknown {
		t.Fatalf("run = %#v, %v", result.Operation, err)
	}
	// 对账 = Resume：服务恢复可查询后，同一请求按 ID 拿回结果，仍然没有重提。
	executor.service.recoverable = true
	if _, err := api.Tasks.ResumeOperation(ctx, operation.ID, testTime().Add(4*time.Minute)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	result, err = api.Tasks.RunOperation(ctx, operation.ID, "worker-3", time.Minute, testTime().Add(5*time.Minute))
	if err != nil || result.Operation.State != model.OperationSucceeded || result.Operation.Attempt != 3 || result.Operation.FailureCode != "" {
		t.Fatalf("reconciled run = %#v, %v", result.Operation, err)
	}
	if executor.service.submits != 1 {
		t.Fatalf("reconciliation must not resubmit, submits = %d", executor.service.submits)
	}
}

func TestExternalRestartResubmitsInheritedUnknownRequest(t *testing.T) {
	ctx := context.Background()
	api, executor := newExternalTestService(t)
	operation := startAssetOperation(t, api, "asset-1")
	submitThenCrash(t, api, executor, operation.ID)
	executor.service.recoverable = false
	if result, err := api.Tasks.RunOperation(ctx, operation.ID, "worker-2", time.Minute, testTime().Add(3*time.Minute)); !errors.Is(err, model.ErrResultUnknown) || result.Operation.FailureCode != model.FailureResultUnknown {
		t.Fatalf("run = %#v, %v", result.Operation, err)
	}
	// 重提 = Restart：后继继承请求记录，恢复一次仍未知就换新 ID 重提（用户已明确决定）。
	successor, err := api.Tasks.RestartOperation(ctx, tasks.RestartOperationCommand{
		FromOperationID: operation.ID, OperationID: operation.ID + ":r2", CreatedAt: testTime().Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	result, err := api.Tasks.RunOperation(ctx, successor.ID, "worker-3", time.Minute, testTime().Add(5*time.Minute))
	if err != nil || result.Operation.State != model.OperationSucceeded {
		t.Fatalf("successor run = %#v, %v", result.Operation, err)
	}
	record, _, err := executor.readRequest(ctx, successor.ID)
	if err != nil || record.OperationID != successor.ID || record.RequestID != successor.ID+"#1" || executor.service.submits != 2 {
		t.Fatalf("record = %#v, %v, submits = %d", record, err, executor.service.submits)
	}
}

// Restart may change the target, source basis or configuration. The inherited remote
// request is reusable only when it still describes the same generation inputs.
func TestExternalRestartBindsResultToFrozenInputs(t *testing.T) {
	for _, scenario := range []string{"same", "target", "config", "source_revision"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			api, executor := newExternalTestService(t)
			previous := startAssetOperation(t, api, "old-asset")
			submitThenCrash(t, api, executor, previous.ID)
			if _, err := api.Tasks.CancelOperation(ctx, previous.ID, testTime().Add(3*time.Minute)); err != nil {
				t.Fatal(err)
			}
			command := tasks.RestartOperationCommand{FromOperationID: previous.ID, OperationID: "new-asset", CreatedAt: testTime().Add(4 * time.Minute)}
			wantContent := "rendered:chapter-plan-1"
			switch scenario {
			case "target":
				input := assetTestInput()
				input.Target.ID = "ending-beat"
				input.Basis.Documents[0].Ref = input.Target
				command.Input, _ = json.Marshal(input)
				wantContent = "rendered:ending-beat"
			case "config":
				command.ConfigDigest = "render-v2"
			case "source_revision":
				project, err := api.Projects.Project(ctx, externalTestProject, 0)
				if err != nil {
					t.Fatal(err)
				}
				var changed model.PlanNode
				for _, node := range project.Plan {
					if node.ID == "chapter-plan-1" {
						changed = node
						changed.Summary = "新的剧情"
					}
				}
				content, _ := json.Marshal(changed)
				at := testTime().Add(3 * time.Minute)
				if _, err := api.changes.CommitUser(ctx, model.Proposal{
					ID: "source-change", Target: previous.Target, BaseRevision: project.Revision,
					Author: model.Author{Kind: model.AuthorUser, ID: "user-1"}, Reason: "修改来源",
					Patches:       []model.Patch{{Document: model.DocumentRef{Kind: model.DocumentPlan, ID: changed.ID}, Operation: model.PatchPut, Content: content}},
					ApprovalState: model.ApprovalPending, CreatedAt: at,
				}, at); err != nil {
					t.Fatal(err)
				}
				input := assetTestInput()
				input.Basis.Documents[0].Revision = project.Revision + 1
				command.Input, _ = json.Marshal(input)
			}
			successor, err := api.Tasks.RestartOperation(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			result, err := api.Tasks.RunOperation(ctx, successor.ID, "worker", time.Minute, testTime().Add(5*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			got, err := api.store.ReadArtifact(result.Artifacts[0].Digest)
			if err != nil {
				t.Fatal(err)
			}
			wantSubmits := 2
			if scenario == "same" {
				wantSubmits = 1
			}
			if string(got) != wantContent || executor.service.submits != wantSubmits || result.Operation.State != model.OperationSucceeded {
				t.Fatalf("content=%q submits=%d state=%s", got, executor.service.submits, result.Operation.State)
			}
		})
	}
}
