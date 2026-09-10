package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/operation"
)

// 媒体检查是第二种证据形状：没有小说章节发现，沿用同一套基线、恢复和收尾。
type mediaInspection struct {
	Artifact domain.ArtifactRef   `json:"artifact"`
	Size     int64                `json:"size"`
	Passed   bool                 `json:"passed"`
	Basis    domain.EvidenceBasis `json:"basis"`
}

type inspectingExecutor struct {
	*externalGenerationExecutor
	checks int
}

func (e *inspectingExecutor) Execute(ctx context.Context, op domain.Operation) (domain.OperationOutcome, error) {
	if op.Kind != domain.OperationInspectAsset {
		return e.externalGenerationExecutor.Execute(ctx, op)
	}
	e.checks++
	input, err := domain.TaskInputAs[domain.InspectAssetInput](op)
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	content, err := e.store.ReadArtifact(input.Artifact.Digest)
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	payload, err := json.Marshal(mediaInspection{Artifact: input.Artifact, Size: int64(len(content)), Passed: len(content) > 0, Basis: input.Basis})
	return domain.OperationOutcome{Verdict: payload}, err
}

func inspectionContract(e *inspectingExecutor) operation.VerdictContract {
	return operation.VerdictContract{Kind: domain.OperationInspectAsset, Validate: func(ctx context.Context, op domain.Operation, raw json.RawMessage) (domain.EvidenceBasis, error) {
		input, err := domain.TaskInputAs[domain.InspectAssetInput](op)
		if err != nil {
			return domain.EvidenceBasis{}, err
		}
		var result mediaInspection
		if err := domain.DecodeStrict(raw, &result); err != nil {
			return domain.EvidenceBasis{}, err
		}
		content, err := e.store.ReadArtifact(input.Artifact.Digest)
		if err != nil {
			return domain.EvidenceBasis{}, err
		}
		if result.Artifact != input.Artifact || !result.Basis.Equal(input.Basis) || result.Size != int64(len(content)) || result.Passed != (len(content) > 0) {
			return domain.EvidenceBasis{}, fmt.Errorf("inspection does not match frozen input and content: %w", domain.ErrInvalid)
		}
		return result.Basis, nil
	}}
}

type inspectedRenderingDeriver struct{ renderingDeriver }

func (d inspectedRenderingDeriver) Next(project ProjectSnapshot, run domain.CreationRun, evidence runEvidence) (step, error) {
	next, err := d.renderingDeriver.Next(project, run, evidence)
	if err != nil || next.Done == "" {
		return next, err
	}
	for _, attachment := range project.Attachments {
		checked := false
		for _, check := range evidence.checks {
			if check.Kind != domain.OperationInspectAsset {
				continue
			}
			var result mediaInspection
			if err := json.Unmarshal(check.Content, &result); err != nil {
				return step{}, err
			}
			if result.Artifact == attachment.Artifact {
				if !result.Passed {
					return step{Fail: "媒体检查未通过"}, nil
				}
				checked = true
				break
			}
		}
		if checked {
			continue
		}
		basis, err := basisFor(project, []domain.DocumentRef{{Kind: domain.DocumentAttachment, ID: attachment.ID}}, nil)
		if err != nil {
			return step{}, err
		}
		basis.Artifacts = []domain.ArtifactRef{attachment.Artifact}
		return step{Work: &workItem{ID: run.ID + ":inspect:" + attachment.Artifact.ID, Kind: domain.OperationInspectAsset,
			Input: domain.InspectAssetInput{Artifact: attachment.Artifact, Basis: basis}, Reasons: workReasons{Waiting: "等待核验", Failure: "媒体核验失败", Stuck: "核验未产出有效证据"}}}, nil
	}
	return next, nil
}

func TestMixedNovelGenerationAndInspectionUseOneService(t *testing.T) {
	ctx := context.Background()
	s := openServiceStore(t)
	llm := &scriptedQuickExecutor{now: serviceTime(), authorityStore: s}
	external := &inspectingExecutor{externalGenerationExecutor: &externalGenerationExecutor{store: s, service: &fakeGenerationService{recoverable: true}, now: serviceTime()}}
	api := NewWithExecutors(s, ExecutorSet{LLM: llm, External: external}, inspectionContract(external))
	api.now = serviceTime
	api.derivers[goalRendering] = inspectedRenderingDeriver{}
	command := QuickWriteCommand{ProjectID: renderingProject, UserID: "user-1", Premise: "邮差送信", Chapters: 1, WorkerID: "worker", LeaseDuration: time.Minute, CreatedAt: serviceTime()}
	if result, err := api.QuickWrite(ctx, command); err != nil || result.RunState != domain.RunCompleted {
		t.Fatalf("novel: %+v %v", result, err)
	}
	run := startRenderingRun(t, api, 2, serviceTime().Add(time.Hour))
	result, err := api.driveCreationRun(ctx, run, renderingCommand(serviceTime().Add(time.Hour)))
	if err != nil || result.run.State != domain.RunCompleted {
		t.Fatalf("rendering: %+v %v", result, err)
	}
	if llm.calls != 3 || external.calls != 1 || external.checks != 1 {
		t.Fatalf("wrong routing: llm=%d external=%d checks=%d", llm.calls, external.calls, external.checks)
	}
	if verdicts, err := api.listVerdicts(ctx, result.project); err != nil || len(verdicts) != 1 {
		t.Fatalf("media evidence confused with novel verdict: %v %v", verdicts, err)
	}
	checks, err := api.validChecks(ctx, result.project)
	if err != nil || len(checks) != 1 {
		t.Fatalf("media evidence missing: %v %v", checks, err)
	}
	// 源内容变化后旧生成与旧检查同时失效，只重生、重查对应产物。
	projection, err := api.ExportProject(ctx, renderingProject, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range projection.Plan {
		if projection.Plan[i].Kind == domain.PlanChapter {
			projection.Plan[i].Summary = "新的剧情"
		}
	}
	proposal, err := api.ImportProject(ctx, "change-source", "user-1", "改源", projection, serviceTime().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.Approve(ctx, proposal.ID, "user-1", serviceTime().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	project, err := api.Project(ctx, renderingProject, 0)
	if err != nil {
		t.Fatal(err)
	}
	checks, err = api.validChecks(ctx, project)
	if err != nil || len(checks) != 0 {
		t.Fatalf("stale check survived: %v %v", checks, err)
	}
	run = startRenderingRun(t, api, 3, serviceTime().Add(3*time.Hour))
	result, err = api.driveCreationRun(ctx, run, renderingCommand(serviceTime().Add(3*time.Hour)))
	if err != nil || result.run.State != domain.RunCompleted || external.calls != 2 || external.checks != 2 {
		t.Fatalf("regeneration: %+v calls=%d checks=%d err=%v", result, external.calls, external.checks, err)
	}
	// 同一 Service 再回到小说审阅，无需换执行器或重建服务。
	command.CreatedAt = serviceTime().Add(4 * time.Hour)
	if result, err := api.QuickWrite(ctx, command); err != nil || result.RunState != domain.RunCompleted {
		t.Fatalf("novel after media: %+v %v", result, err)
	}
}

func TestMediaEvidenceRecoversWithoutReexecutingInspector(t *testing.T) {
	ctx := context.Background()
	api, generator := newRenderingService(t, domain.ApprovalAuto)
	external := &inspectingExecutor{externalGenerationExecutor: generator}
	api.executors.External = external
	api.operations = operation.NewEngine(api.store, inspectionContract(external))
	run := startRenderingRun(t, api, 1, serviceTime())
	if _, err := api.driveCreationRun(ctx, run, renderingCommand(serviceTime())); err != nil {
		t.Fatal(err)
	}
	project, err := api.Project(ctx, renderingProject, 0)
	if err != nil {
		t.Fatal(err)
	}
	api.derivers[goalRendering] = inspectedRenderingDeriver{}
	at := serviceTime().Add(time.Hour)
	run = startRenderingRun(t, api, 2, at)
	artifacts, err := api.validArtifacts(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	next, err := (inspectedRenderingDeriver{}).Next(project, run, runEvidence{artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := workCommand(api.runBaseCommand(run, renderingCommand(at)), *next.Work, at)
	if err != nil {
		t.Fatal(err)
	}
	cmd.RunID = run.ID
	op, err := api.StartOperation(ctx, cmd)
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
	if _, err = api.store.SaveExecutionDerivedDocument(ctx, domain.DerivedDocument{ProjectID: project.ID, Revision: op.Snapshot.BaseRevision, Kind: domain.DerivedVerdictKind, Key: op.ID, Content: outcome.Verdict, CreatedAt: at}, op.ID, op.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err = api.RecoverOperations(ctx, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	result, err := api.driveCreationRun(ctx, run, renderingCommand(at.Add(3*time.Minute)))
	if err != nil || result.run.State != domain.RunCompleted || external.checks != 3 {
		t.Fatalf("inspection recovery: %+v checks=%d err=%v", result, external.checks, err)
	}
}
