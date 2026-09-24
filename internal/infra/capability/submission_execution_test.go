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
)

type repeatingSubmissionModel struct {
	recoveryRuntimeModel
	calls int
}

func (m *repeatingSubmissionModel) GenerateStream(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.calls++
	if m.calls > 3 {
		return nil, errors.New("guard did not stop model calls")
	}
	message := runtimeToolCallMessage(fmt.Sprintf("submit-%d", m.calls), prompt.ToolProposalSubmit,
		json.RawMessage(`{"reason":"提交","workspace_key":"draft","workspace_version":99,"patches":[]}`), m.now)
	stream := make(chan agentcore.StreamEvent, 3)
	call := message.ToolCalls()[0]
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallStart, Message: message}
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallEnd, Message: message, CompletedToolCall: &call}
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: message}
	close(stream)
	return stream, nil
}

func TestRuntimePersistsSubmissionFailureAndRetainsDraft(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, op := runningWriterWithDraft(t, ctx)
	llm := &repeatingSubmissionModel{recoveryRuntimeModel: recoveryRuntimeModel{now: time.Now()}}
	r := boundRuntime(s, llm)
	_, err := r.Execute(ctx, op)
	if !errors.Is(err, model.ErrSubmissionBlocked) || model.FailureCodeFor(err) != model.FailureSubmissionBlocked || !strings.Contains(err.Error(), "连续 3 次") || !strings.Contains(err.Error(), "version mismatch") || llm.calls != 3 {
		t.Fatalf("failure was hidden or loop continued: calls=%d err=%v", llm.calls, err)
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
		if message.Role == agentcore.RoleTool && message.Metadata["tool_name"] == prompt.ToolProposalSubmit {
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
			llm := &recoveryRuntimeModel{now: time.Now(), proposalArgs: json.RawMessage(`{"reason":"完成","workspace_key":"draft","workspace_version":1,"patches":[{"document":{"kind":"canon","id":"arrival"},"operation":"put","content":{"id":"arrival","kind":"event","subject_id":"hero","predicate":"event.arrival","new_value":"抵达山门","source_chapter_id":"chapter-1"}}]}`)}
			r := boundRuntime(s, llm)
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
			if len(llm.requests) != 2 {
				t.Fatalf("extra model calls: %d", len(llm.requests))
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
					if message.Role == agentcore.RoleTool && message.Metadata["tool_name"] == prompt.ToolProposalSubmit {
						resultSaved = true
					}
				}
				if event.Kind == "agent.run_ended" {
					var end struct {
						Summary agentcore.RunSummary
						Error   string
					}
					if err := json.Unmarshal(event.Payload, &end); err != nil {
						t.Fatal(err)
					}
					want := agentcore.EndReasonStop
					if failCommit {
						want = agentcore.EndReasonError
					}
					if end.Summary.EndReason != want || (end.Error != "") != failCommit {
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
