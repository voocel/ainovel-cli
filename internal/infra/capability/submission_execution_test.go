package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/store"
	"github.com/voocel/ainovel-cli/internal/infra/workspace"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

func TestRuntimePersistsSubmissionFailureAndRetainsDraft(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, op := runningWriterWithDraft(t, ctx)
	// 第四次调用越出脚本：守卫没拦住就会以别的错误收场。
	var replies []litellmtest.Reply
	for i := 1; i <= 3; i++ {
		replies = append(replies, callReply(fmt.Sprintf("submit-%d", i), prompt.ToolProposalSubmit,
			json.RawMessage(`{"reason":"提交","workspace_key":"draft","workspace_version":99,"facts":[{"subject":"主角","predicate":"event.arrival","value":"抵达"}]}`)))
	}
	llm := litellmtest.New(replies...)
	r := boundRuntime(s, testChat(llm))
	_, err := r.Execute(ctx, op)
	if !errors.Is(err, model.ErrSubmissionBlocked) || model.FailureCodeFor(err) != model.FailureSubmissionBlocked || !strings.Contains(err.Error(), "连续 3 次") || !strings.Contains(err.Error(), "version mismatch") || len(llm.Requests()) != 3 {
		t.Fatalf("failure was hidden or loop continued: calls=%d err=%v", len(llm.Requests()), err)
	}
	if ctx.Err() != nil {
		t.Fatal("submission guard cancelled its caller")
	}
	artifact, err := s.GetWorkspaceArtifact(ctx, op.ID, "draft")
	if err != nil || artifact.Version != 1 {
		t.Fatalf("draft lost: %+v %v", artifact, err)
	}
	events, err := s.ListOperationEvents(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "agent.run_ended" && strings.Contains(string(event.Payload), "提交受阻") {
			return
		}
	}
	t.Fatal("failure summary was not persisted")
}

// 模型没提交就想停：提醒它继续，而不是以"没有 Proposal"收场；每次回应的用量都计入收尾汇总。
func TestRuntimeRemindsToSubmitBeforeStopping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, op := runningWriterWithDraft(t, ctx)
	stop := litellmtest.Text("草稿写好了。")
	stop.Usage = litellm.Usage{InputTokens: 10, OutputTokens: 3}
	submit := callReply("submit-existing", prompt.ToolProposalSubmit,
		json.RawMessage(`{"reason":"完成","workspace_key":"draft","workspace_version":1,"facts":[{"subject":"主角","predicate":"event.arrival","value":"抵达山门"}]}`))
	submit.Usage = litellm.Usage{InputTokens: 20, OutputTokens: 5}
	llm := litellmtest.New(stop, submit)
	outcome, err := boundRuntime(s, testChat(llm)).Execute(ctx, op)
	if err != nil || outcome.Proposal == nil {
		t.Fatalf("outcome = %+v, %v", outcome, err)
	}
	requests := llm.Requests()
	if len(requests) != 2 || !strings.Contains(requestText(requests[1].Messages[len(requests[1].Messages)-1]), "任务尚未形成 Proposal") {
		t.Fatalf("the model was not reminded to submit: %d requests", len(requests))
	}
	events, err := s.ListOperationEvents(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind != "agent.run_ended" {
			continue
		}
		var end struct{ Usage agentcore.Usage }
		if err := json.Unmarshal(event.Payload, &end); err != nil || end.Usage.InputTokens != 30 || end.Usage.OutputTokens != 8 {
			t.Fatalf("run usage = %s, %v", event.Payload, err)
		}
		return
	}
	t.Fatal("missing run summary")
}

func runningWriterWithDraft(t *testing.T, ctx context.Context) (*store.Store, model.Operation) {
	t.Helper()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: "book"}
	seedRuntimeProject(t, ctx, s, target, now)
	worker, err := prompt.BuiltinWorkerProfile("writer.compose")
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	op := model.Operation{ID: "write", Kind: model.OperationWriteChapter, Target: target, State: model.OperationQueued,
		RunID: createRuntimeTestRun(t, ctx, s, target.ID, now), Input: input, CreatedAt: now, UpdatedAt: now,
		Snapshot: runtimeSnapshot(input, 1, model.ApprovalAuto, seedRuntimeProfile(t, ctx, s, target.ID, worker, input, 1))}
	if _, err := s.CreateOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	op, err = s.ClaimNextOperation(ctx, "worker", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	chapter := model.ManuscriptChapter{ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门", Author: model.AuthorAI, Blocks: []model.ManuscriptBlock{{ID: "p1", Text: "保留这个草稿。"}}}
	if _, err := workspace.New(s).PutChapter(ctx, op.ID, "draft", chapter, nil, op.Attempt, now); err != nil {
		t.Fatal(err)
	}
	return s, op
}

type failedResultStore struct {
	runtimeStore
	cause error
}

func (s failedResultStore) AppendOperationEvent(ctx context.Context, event model.OperationEvent) (model.OperationEvent, error) {
	if event.Kind == "agent.message_committed" {
		var message agentcore.Message
		if err := json.Unmarshal(event.Payload, &message); err != nil {
			return model.OperationEvent{}, err
		}
		if isSubmitResult(message) {
			return model.OperationEvent{}, s.cause
		}
	}
	return s.runtimeStore.AppendOperationEvent(ctx, event)
}

func TestRuntimeSubmissionStopsOnlyAfterResultIsPersisted(t *testing.T) {
	for _, failCommit := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail_commit=%t", failCommit), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s, op := runningWriterWithDraft(t, ctx)
			llm := recoveryScript(json.RawMessage(`{"reason":"完成","workspace_key":"draft","workspace_version":1,"facts":[{"subject":"主角","predicate":"event.arrival","value":"抵达山门"}]}`))
			r := boundRuntime(s, testChat(llm))
			cause := errors.New("result persistence failed")
			if failCommit {
				r.store = failedResultStore{runtimeStore: s, cause: cause}
			}
			outcome, err := r.Execute(ctx, op)
			if failCommit {
				if !errors.Is(err, cause) || outcome.Proposal != nil {
					t.Fatalf("persistence failure hidden: %+v %v", outcome, err)
				}
			} else if err != nil || outcome.Proposal == nil {
				t.Fatalf("submission failed: %+v %v", outcome, err)
			}
			if len(llm.Requests()) != 2 {
				t.Fatalf("extra model calls: %d", len(llm.Requests()))
			}
			events, err := s.ListOperationEvents(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			var resultSaved, ended bool
			for _, event := range events {
				if event.Kind == "agent.message_committed" {
					var message agentcore.Message
					if err := json.Unmarshal(event.Payload, &message); err != nil {
						t.Fatal(err)
					}
					resultSaved = resultSaved || isSubmitResult(message)
				}
				if event.Kind == "agent.run_ended" {
					var end struct {
						Reason agentcore.EndReason `json:"reason"`
						Error  string              `json:"error"`
					}
					if err := json.Unmarshal(event.Payload, &end); err != nil {
						t.Fatal(err)
					}
					want := agentcore.EndDone
					if failCommit {
						want = agentcore.EndError
					}
					if end.Reason != want || (end.Error != "") != failCommit {
						t.Fatalf("wrong ending: %+v", end)
					}
					ended = true
				}
			}
			if !ended || resultSaved == failCommit {
				t.Fatalf("persistence order: result=%t end=%t", resultSaved, ended)
			}
		})
	}
}

// isSubmitResult 报告 message 是否是 recoveryScript 那次 proposal_submit 的结果。
func isSubmitResult(message agentcore.Message) bool {
	result, ok := message.ToolResult()
	return ok && result.ToolUseID == "submit-existing"
}
