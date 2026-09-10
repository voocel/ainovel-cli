package bootstrap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	novelapp "github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	tasks "github.com/voocel/ainovel-cli/internal/app/task"
	runs "github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/operation"
)

// 媒体检查是第二种证据形状：没有小说章节发现，沿用同一套基线、恢复和收尾。
type mediaInspection struct {
	Artifact model.ArtifactRef   `json:"artifact"`
	Size     int64               `json:"size"`
	Passed   bool                `json:"passed"`
	Basis    model.EvidenceBasis `json:"basis"`
}

type inspectingExecutor struct {
	*externalGenerationExecutor
	checks int
}

func (e *inspectingExecutor) Execute(ctx context.Context, op model.Operation) (model.OperationOutcome, error) {
	if op.Kind != model.OperationInspectAsset {
		return e.externalGenerationExecutor.Execute(ctx, op)
	}
	e.checks++
	input, err := model.TaskInputAs[model.InspectAssetInput](op)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	content, err := e.store.ReadArtifact(input.Artifact.Digest)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	payload, err := json.Marshal(mediaInspection{Artifact: input.Artifact, Size: int64(len(content)), Passed: len(content) > 0, Basis: input.Basis})
	return model.OperationOutcome{Verdict: payload}, err
}

func inspectionContract(e *inspectingExecutor) operation.VerdictContract {
	return operation.VerdictContract{Kind: model.OperationInspectAsset, Validate: func(ctx context.Context, op model.Operation, raw json.RawMessage) (model.EvidenceBasis, error) {
		input, err := model.TaskInputAs[model.InspectAssetInput](op)
		if err != nil {
			return model.EvidenceBasis{}, err
		}
		var result mediaInspection
		if err := model.DecodeStrict(raw, &result); err != nil {
			return model.EvidenceBasis{}, err
		}
		content, err := e.store.ReadArtifact(input.Artifact.Digest)
		if err != nil {
			return model.EvidenceBasis{}, err
		}
		if result.Artifact != input.Artifact || !result.Basis.Equal(input.Basis) || result.Size != int64(len(content)) || result.Passed != (len(content) > 0) {
			return model.EvidenceBasis{}, fmt.Errorf("inspection does not match frozen input and content: %w", model.ErrInvalid)
		}
		return result.Basis, nil
	}}
}

type inspectedRenderingDeriver struct{ renderingDeriver }

func (d inspectedRenderingDeriver) Next(project projectdoc.Snapshot, run model.CreationRun, evidence renderingEvidence) (runs.Step, error) {
	next, err := d.renderingDeriver.Next(project, run, evidence)
	if err != nil || next.Done == "" {
		return next, err
	}
	for _, attachment := range project.Attachments {
		checked := false
		for _, check := range evidence.checks {
			if check.Kind != model.OperationInspectAsset {
				continue
			}
			var result mediaInspection
			if err := json.Unmarshal(check.Content, &result); err != nil {
				return runs.Step{}, err
			}
			if result.Artifact == attachment.Artifact {
				if !result.Passed {
					return runs.Step{Fail: "媒体检查未通过"}, nil
				}
				checked = true
				break
			}
		}
		if checked {
			continue
		}
		basis, err := mediaBasis(project, model.DocumentRef{Kind: model.DocumentAttachment, ID: attachment.ID})
		if err != nil {
			return runs.Step{}, err
		}
		basis.Artifacts = []model.ArtifactRef{attachment.Artifact}
		return runs.Step{Work: &runs.WorkItem{ID: run.ID + ":inspect:" + attachment.Artifact.ID, Kind: model.OperationInspectAsset,
			Input: model.InspectAssetInput{Artifact: attachment.Artifact, Basis: basis}, Reasons: runs.WorkReasons{Waiting: "等待核验", Failure: "媒体核验失败", Stuck: "核验未产出有效证据"}}}, nil
	}
	return next, nil
}

func TestMixedNovelGenerationAndInspectionUseOneApplication(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	llm := &scriptedQuickExecutor{now: testTime(), authorityStore: s}
	external := &inspectingExecutor{externalGenerationExecutor: &externalGenerationExecutor{store: s, service: &fakeGenerationService{recoverable: true}, now: testTime()}}
	api := newMediaApp(s, tasks.ExecutorSet{LLM: llm, External: external}, inspectedRenderingDeriver{}, inspectionContract(external))
	command := novelapp.QuickWriteCommand{ProjectID: renderingProject, UserID: "user-1", Premise: "邮差送信", Chapters: 1, WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: testTime()}
	if result, err := api.Novels.QuickWrite(ctx, command); err != nil || result.RunState != model.RunCompleted {
		t.Fatalf("novel: %+v %v", result, err)
	}
	run := startRenderingRun(t, api, 2, testTime().Add(time.Hour))
	result, err := api.Runs.Drive(ctx, run, renderingTasks(api), renderingCommand(testTime().Add(time.Hour)))
	if err != nil || result.Run.State != model.RunCompleted {
		t.Fatalf("rendering: %+v %v", result, err)
	}
	if llm.calls != 3 || external.calls != 1 || external.checks != 1 {
		t.Fatalf("wrong routing: llm=%d external=%d checks=%d", llm.calls, external.calls, external.checks)
	}
	if verdicts, err := api.Reviews.ListVerdicts(ctx, renderingSnapshot(t, api)); err != nil || len(verdicts) != 1 {
		t.Fatalf("media evidence confused with novel verdict: %v %v", verdicts, err)
	}
	checks, err := api.Evidence.Checks(ctx, renderingProject, result.Revision, model.OperationInspectAsset)
	if err != nil || len(checks) != 1 {
		t.Fatalf("media evidence missing: %v %v", checks, err)
	}
	// 源内容变化后旧生成与旧检查同时失效，只重生、重查对应产物。
	projection, err := api.Projects.ExportProject(ctx, renderingProject, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range projection.Plan {
		if projection.Plan[i].Kind == model.PlanChapter {
			projection.Plan[i].Summary = "新的剧情"
		}
	}
	proposal, err := api.Projects.ImportProject(ctx, "change-source", "user-1", "改源", projection, testTime().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.Decisions.Approve(ctx, proposal.ID, "user-1", testTime().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	project, err := api.Projects.Project(ctx, renderingProject, 0)
	if err != nil {
		t.Fatal(err)
	}
	checks, err = api.Evidence.Checks(ctx, project.ID, project.Revision, model.OperationInspectAsset)
	if err != nil || len(checks) != 0 {
		t.Fatalf("stale check survived: %v %v", checks, err)
	}
	run = startRenderingRun(t, api, 3, testTime().Add(3*time.Hour))
	result, err = api.Runs.Drive(ctx, run, renderingTasks(api), renderingCommand(testTime().Add(3*time.Hour)))
	if err != nil || result.Run.State != model.RunCompleted || external.calls != 2 || external.checks != 2 {
		t.Fatalf("regeneration: %+v calls=%d checks=%d err=%v", result, external.calls, external.checks, err)
	}
	// 同一 testApp 再回到小说审阅，无需换执行器或重建服务。
	command.CreatedAt = testTime().Add(4 * time.Hour)
	if result, err := api.Novels.QuickWrite(ctx, command); err != nil || result.RunState != model.RunCompleted {
		t.Fatalf("novel after media: %+v %v", result, err)
	}
}

func TestMediaEvidenceRecoversWithoutReexecutingInspector(t *testing.T) {
	ctx := context.Background()
	api, generator := newRenderingApp(t, model.ApprovalAuto)
	external := &inspectingExecutor{externalGenerationExecutor: generator}
	api = newMediaApp(api.store, tasks.ExecutorSet{External: external}, renderingDeriver{}, inspectionContract(external))
	run := startRenderingRun(t, api, 1, testTime())
	if _, err := api.Runs.Drive(ctx, run, renderingTasks(api), renderingCommand(testTime())); err != nil {
		t.Fatal(err)
	}
	project, err := api.Projects.Project(ctx, renderingProject, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Reassemble after the completed generation run, as a fresh process would.
	api = newMediaApp(api.store, tasks.ExecutorSet{External: external}, inspectedRenderingDeriver{}, inspectionContract(external))
	at := testTime().Add(time.Hour)
	run = startRenderingRun(t, api, 2, at)
	artifacts, err := api.Evidence.Artifacts(ctx, project.ID, project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	next, err := (inspectedRenderingDeriver{}).Next(project, run, renderingEvidence{artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := tasks.WorkCommand(renderingBase(), *next.Work, at)
	if err != nil {
		t.Fatal(err)
	}
	cmd.RunID = run.ID
	op, err := api.Tasks.StartOperation(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	op, err = api.store.ClaimOperationForExecutor(ctx, op.ID, "crashed", external.Identity(), time.Minute, at)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := external.Execute(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.operations.ValidateEvidence(ctx, op, outcome.Verdict); err != nil {
		t.Fatal(err)
	}
	if _, err = api.store.SaveExecutionDerivedDocument(ctx, model.DerivedDocument{ProjectID: project.ID, Revision: op.Snapshot.BaseRevision, Kind: model.DerivedVerdictKind, Key: op.ID, Content: outcome.Verdict, CreatedAt: at}, op.ID, op.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err = api.Tasks.RecoverOperations(ctx, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	result, err := api.Runs.Drive(ctx, run, renderingTasks(api), renderingCommand(at.Add(3*time.Minute)))
	if err != nil || result.Run.State != model.RunCompleted || external.checks != 3 {
		t.Fatalf("inspection recovery: %+v checks=%d err=%v", result, external.checks, err)
	}
}
