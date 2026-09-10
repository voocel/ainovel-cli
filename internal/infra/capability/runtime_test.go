package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/store"
	"github.com/voocel/ainovel-cli/internal/infra/workspace"
)

func TestRuntimeToolsRespectAuthoritySnapshotAndWorkspaceBoundary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	target := domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-1"}
	seed := approvedRuntimeProposal("seed", target, 0, now, domainmodel.Patch{
		Document:  domainmodel.DocumentRef{Kind: domainmodel.DocumentIntent, ID: "root"},
		Operation: domainmodel.PatchPut, Content: json.RawMessage(`{"premise":"凡人修仙"}`),
	})
	pending := seed
	pending.ApprovalState, pending.DecidedBy, pending.DecidedAt = domainmodel.ApprovalPending, nil, nil
	if _, err := authorityStore.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save seed: %v", err)
	}
	if _, err := authorityStore.CommitProposal(ctx, seed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	task := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	operation := domainmodel.Operation{
		ID: "write-1", Kind: domainmodel.OperationWriteChapter, Target: target,
		State: domainmodel.OperationQueued, RunID: createRuntimeTestRun(t, ctx, authorityStore, target.ID, now),
		Snapshot: runtimeSnapshot(task, 1, domainmodel.ApprovalManual, "profile"),
		Input:    task, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	runtime := NewRuntime(nil, "model", authorityStore)
	runtime.now = func() time.Time { return now.Add(2 * time.Second) }

	read, err := runtime.toolExecutor(operation, "writer.compose@1", "authority_read", nil, nil)
	if err != nil {
		t.Fatalf("build read tool: %v", err)
	}
	if _, err := read(ctx, json.RawMessage(`{"kind":"intent","id":"root","revision":2}`)); !errors.Is(err, domainmodel.ErrInvalid) {
		t.Fatalf("future revision read error = %v, want domainmodel.ErrInvalid", err)
	}
	if _, err := read(ctx, json.RawMessage(`{"kind":"intent","id":"root","revision":1}`)); err != nil {
		t.Fatalf("read frozen revision: %v", err)
	}

	putChapter, err := runtime.toolExecutor(operation, "writer.compose@1", "workspace_put_chapter", nil, nil)
	if err != nil {
		t.Fatalf("build put chapter tool: %v", err)
	}
	chapter := domainmodel.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门", Author: domainmodel.AuthorAI,
		Blocks: []domainmodel.ManuscriptBlock{{ID: "p-1", Text: "他抵达山门。"}},
	}
	chapterArgs, _ := json.Marshal(map[string]any{
		"key": "chapter/chapter-1", "expected_version": 0,
		"chapter": chapter,
	})
	if _, err := putChapter(ctx, chapterArgs); err != nil {
		t.Fatalf("write operation workspace: %v", err)
	}

	var submitted domainmodel.Proposal
	submitTool, err := runtime.toolExecutor(operation, "writer.compose@1", "proposal_submit", func(proposal domainmodel.Proposal) (domainmodel.Proposal, error) {
		submitted = proposal
		return proposal, nil
	}, nil)
	if err != nil {
		t.Fatalf("build submit tool: %v", err)
	}
	chapterContent, _ := json.Marshal(chapter)
	canonContent, _ := json.Marshal(domainmodel.CanonFact{
		ID: "chapter-1-outcome", Kind: domainmodel.CanonEvent, SubjectID: "chapter-1",
		Predicate: "event.chapter_outcome", Value: json.RawMessage(`"抵达山门"`), SourceChapterID: chapter.ID,
	})
	proposalArgs, _ := json.Marshal(map[string]any{
		"reason":        "提交章节候选",
		"workspace_key": "chapter/chapter-1",
		"patches": []domainmodel.Patch{
			{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: "chapter-1"}, Operation: domainmodel.PatchPut, Content: chapterContent},
			{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "chapter-1-outcome"}, Operation: domainmodel.PatchPut, Content: canonContent},
		},
	})
	if _, err := submitTool(ctx, proposalArgs); err != nil {
		t.Fatalf("submit proposal candidate: %v", err)
	}
	if submitted.OperationID != operation.ID || submitted.BaseRevision != 1 {
		t.Fatalf("submitted proposal = %#v", submitted)
	}
	if _, err := authorityStore.GetProposal(ctx, submitted.ID); !errors.Is(err, domainmodel.ErrNotFound) {
		t.Fatalf("capability wrote authority proposal directly: %v", err)
	}
}

func TestBuiltinCapabilityToolsHaveRuntimeImplementations(t *testing.T) {
	definitions, err := prompt.BuiltinCapabilities()
	if err != nil {
		t.Fatalf("built-in capabilities: %v", err)
	}
	runtime := &Runtime{}
	for _, definition := range definitions {
		operation := domainmodel.Operation{Kind: definition.OperationKinds[0]}
		compiled := prompt.Compiled{WorkerProfile: definition.Worker.ID + "@" + definition.Worker.Version, Tools: definition.Worker.Tools}
		if _, err := runtime.toolsFor(operation, compiled, func(proposal domainmodel.Proposal) (domainmodel.Proposal, error) {
			return proposal, nil
		}, func(verdict domainmodel.ReviewVerdict) (domainmodel.ReviewVerdict, error) {
			return verdict, nil
		}); err != nil {
			t.Fatalf("worker %s tools: %v", definition.Worker.ID, err)
		}
	}
}

func TestRuntimeRestoresCommittedConversationAndWorkspace(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	worker, err := prompt.BuiltinWorkerProfile("writer.compose")
	if err != nil {
		t.Fatalf("load writer capability: %v", err)
	}
	task := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	operation := domainmodel.Operation{
		ID: "recover-draft", Kind: domainmodel.OperationWriteChapter,
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-recovery"},
		State:  domainmodel.OperationQueued,
		RunID:  createRuntimeTestRun(t, ctx, authorityStore, "book-recovery", now),
		Snapshot: runtimeSnapshot(task, 0, domainmodel.ApprovalManual,
			seedRuntimeProfile(t, ctx, authorityStore, "book-recovery", worker, task, 0)),
		Input: task, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	first, err := authorityStore.ClaimNextOperation(ctx, "worker-before-crash", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim first attempt: %v", err)
	}
	chapter := domainmodel.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门", Author: domainmodel.AuthorAI,
		Blocks: []domainmodel.ManuscriptBlock{{ID: "block-1", Text: "已经写入工作区的半成品。"}},
	}
	artifact, err := workspace.New(authorityStore).PutChapter(
		ctx, first.ID, "chapter/chapter-1", chapter, 0, first.Attempt, now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatalf("write recoverable draft: %v", err)
	}
	previous := agentcore.UserMsg("这是崩溃前已经提交到会话日志的原始任务")
	previous.Timestamp = now.Add(3 * time.Second)
	payload, _ := json.Marshal(previous)
	if _, err := authorityStore.AppendOperationEvent(ctx, domainmodel.OperationEvent{
		OperationID: first.ID, StepID: "agent.message", Attempt: first.Attempt,
		IdempotencyKey: "agent-message:1:1", Kind: "agent.message_committed",
		Payload: payload, CreatedAt: previous.Timestamp,
	}); err != nil {
		t.Fatalf("persist previous message: %v", err)
	}
	if _, err := authorityStore.TransitionOperation(
		ctx, first.ID, domainmodel.OperationRunning, domainmodel.OperationFailed, "simulated process failure", now.Add(4*time.Second),
	); err != nil {
		t.Fatalf("fail first attempt: %v", err)
	}
	if _, err := authorityStore.TransitionOperation(
		ctx, first.ID, domainmodel.OperationFailed, domainmodel.OperationQueued, "resume", now.Add(5*time.Second),
	); err != nil {
		t.Fatalf("queue second attempt: %v", err)
	}
	second, err := authorityStore.ClaimNextOperation(ctx, "worker-after-crash", time.Minute, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("claim second attempt: %v", err)
	}
	chapterContent, _ := json.Marshal(chapter)
	canonContent, _ := json.Marshal(domainmodel.CanonFact{
		ID: "chapter-1-outcome", Kind: domainmodel.CanonEvent, SubjectID: "chapter-1",
		Predicate: "event.chapter_outcome", Value: json.RawMessage(`"完成已有工作稿"`), SourceChapterID: chapter.ID,
	})
	proposalArgs, _ := json.Marshal(map[string]any{
		"reason": "继续并提交已有工作稿", "workspace_key": artifact.Key,
		"patches": []domainmodel.Patch{
			{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: chapter.ID}, Operation: domainmodel.PatchPut, Content: chapterContent},
			{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "chapter-1-outcome"}, Operation: domainmodel.PatchPut, Content: canonContent},
		},
	})
	model := &recoveryRuntimeModel{proposalArgs: proposalArgs, now: now.Add(7 * time.Second)}
	runtime := NewRuntime(model, "model", authorityStore)
	runtime.now = func() time.Time { return now.Add(8 * time.Second) }
	outcome, err := runtime.Execute(ctx, second)
	if err != nil {
		t.Fatalf("resume runtime: %v", err)
	}
	if outcome.Proposal == nil || outcome.Proposal.Patches[0].Document.ID != chapter.ID {
		t.Fatalf("outcome = %#v", outcome)
	}
	firstRequest := model.Request(0)
	foundPrevious, foundRecovery := false, false
	for _, message := range firstRequest {
		foundPrevious = foundPrevious || message.TextContent() == previous.TextContent()
		foundRecovery = foundRecovery || strings.Contains(message.TextContent(), "先用 workspace_list")
	}
	if !foundPrevious || !foundRecovery {
		t.Fatalf("restored request missing history or recovery instruction: %#v", firstRequest)
	}
}

func TestRuntimeReviewSubmitsVerdictWithoutProposal(t *testing.T) {
	// D30：审阅 Worker 以 verdict_submit 收尾——不产 Proposal、不造空 Patch；
	// 裁定 Revision 由宿主绑定启动快照，越界或漏审的裁定在工具层被拒绝。
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	worker, err := prompt.BuiltinWorkerProfile("editor.review")
	if err != nil {
		t.Fatalf("load review capability: %v", err)
	}
	task := json.RawMessage(`{"chapter_ids":["chapter-1","chapter-2"],"verify_intent":true,"directives":[{"id":"hook","scope":"project","text":"每章结尾留钩子","status":"active"}],"basis":{"documents":[{"ref":{"kind":"manuscript","id":"chapter-1"},"revision":2}]}}`)
	operation := domainmodel.Operation{
		ID: "review-1", Kind: domainmodel.OperationReviewRange,
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-review"},
		State:  domainmodel.OperationQueued,
		RunID:  createRuntimeTestRun(t, ctx, authorityStore, "book-review", now),
		Snapshot: runtimeSnapshot(task, 3, domainmodel.ApprovalAuto,
			seedRuntimeProfile(t, ctx, authorityStore, "book-review", worker, task, 3)),
		Input: task, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	// 递进式纠错：漏审范围被拒 → 终审 pass 缺意图核验声明被拒 → 与工件不一致被拒
	// → 漏掉要求核验被拒 → 完整裁定通过。
	findings := []map[string]any{{"chapter_id": "chapter-2", "severity": "note", "note": "第二章节奏偏慢"}}
	review, _ := json.Marshal(map[string]any{
		"key": "review/findings", "expected_version": 0, "findings": findings,
	})
	incomplete, _ := json.Marshal(map[string]any{
		"status": "pass", "chapter_ids": []string{"chapter-1"},
		"review_key": "review/findings", "findings": findings,
	})
	noIntent, _ := json.Marshal(map[string]any{
		"status": "pass", "chapter_ids": []string{"chapter-1", "chapter-2"},
		"review_key": "review/findings", "findings": findings,
	})
	mismatched, _ := json.Marshal(map[string]any{
		"status": "pass", "chapter_ids": []string{"chapter-1", "chapter-2"},
		"review_key": "review/findings",
		"intent":     map[string]any{"required_present": true, "forbidden_absent": true, "ending_consistent": true},
		"findings":   []map[string]any{{"chapter_id": "chapter-2", "severity": "note", "note": "与工件不一致"}},
	})
	noDirectives, _ := json.Marshal(map[string]any{
		"status": "pass", "chapter_ids": []string{"chapter-1", "chapter-2"},
		"review_key": "review/findings",
		"intent":     map[string]any{"required_present": true, "forbidden_absent": true, "ending_consistent": true},
		"findings":   findings,
	})
	complete, _ := json.Marshal(map[string]any{
		"status": "pass", "chapter_ids": []string{"chapter-1", "chapter-2"},
		"review_key": "review/findings",
		"intent":     map[string]any{"required_present": true, "forbidden_absent": true, "ending_consistent": true},
		"directives": []map[string]any{{"directive_id": "hook", "satisfied": true}},
		"findings":   findings,
	})
	model := &verdictRuntimeModel{
		review: review, steps: []json.RawMessage{incomplete, noIntent, mismatched, noDirectives, complete},
		now: now.Add(2 * time.Second),
	}
	runtime := NewRuntime(model, "model", authorityStore)
	runtime.now = func() time.Time { return now.Add(3 * time.Second) }
	hub := activity.NewHub()
	runtime.SetActivitySink(hub)
	outcome, err := runtime.Execute(ctx, operation)
	if err != nil {
		t.Fatalf("execute review: %v", err)
	}
	if outcome.Proposal != nil || len(outcome.Verdict) == 0 {
		t.Fatalf("outcome = %#v, want verdict-only", outcome)
	}
	var verdict domainmodel.ReviewVerdict
	if err := json.Unmarshal(outcome.Verdict, &verdict); err != nil {
		t.Fatalf("decode verdict: %v", err)
	}
	if verdict.Status != domainmodel.ReviewPass || verdict.Revision != 3 ||
		len(verdict.ChapterIDs) != 2 || len(verdict.Findings) != 1 ||
		verdict.ReviewKey != "review/findings" || !verdict.IntentSatisfied() ||
		len(verdict.Directives) != 1 || !verdict.DirectivesSatisfied() {
		t.Fatalf("verdict = %#v", verdict)
	}
	artifact, err := authorityStore.GetWorkspaceArtifact(ctx, operation.ID, verdict.ReviewKey)
	if err != nil || artifact.MediaType != domainmodel.ReviewArtifactMediaType {
		t.Fatalf("review artifact = %#v, %v", artifact, err)
	}
	// 活动通道（页面设计 §4）：执行过程以带归属的事件发布——四次被拒的提交
	// 呈现为出错条目（模型自纠可见），最终提交成功收尾。
	feed, ok := hub.Snapshot("book-review")
	if !ok || feed.RunID != operation.RunID || feed.OperationID != operation.ID {
		t.Fatalf("activity feed identity = %#v ok=%v", feed, ok)
	}
	var rejected, accepted int
	for _, entry := range feed.Entries {
		if entry.Tool != "verdict_submit" || !entry.Done {
			continue
		}
		if entry.Err != "" {
			rejected++
		} else {
			accepted++
		}
	}
	if rejected != 4 || accepted != 1 {
		t.Fatalf("verdict_submit activity: rejected=%d accepted=%d entries=%#v", rejected, accepted, feed.Entries)
	}
}

type verdictRuntimeModel struct {
	mu       sync.Mutex
	requests int
	review   json.RawMessage
	steps    []json.RawMessage
	now      time.Time
}

func (m *verdictRuntimeModel) Generate(
	context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return nil, errors.New("verdict runtime must use streaming")
}

func (m *verdictRuntimeModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	m.mu.Lock()
	index := m.requests
	m.requests++
	m.mu.Unlock()
	var message agentcore.Message
	if index == 0 {
		message = runtimeToolCallMessage("put-review", "workspace_put_review", m.review, m.now)
	} else if index <= len(m.steps) {
		message = runtimeToolCallMessage(fmt.Sprintf("verdict-%d", index), "verdict_submit",
			m.steps[index-1], m.now.Add(time.Duration(index)*time.Second))
	} else {
		message = agentcore.Message{
			Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("done")},
			StopReason: agentcore.StopReasonStop, Timestamp: m.now.Add(time.Duration(index) * time.Second),
		}
	}
	stream := make(chan agentcore.StreamEvent, 4)
	for _, call := range message.ToolCalls() {
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallStart, Message: message}
		completed := call
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallEnd, Message: message, CompletedToolCall: &completed}
	}
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: message}
	close(stream)
	return stream, nil
}

func (m *verdictRuntimeModel) SupportsTools() bool { return true }

func createRuntimeTestRun(
	t *testing.T,
	ctx context.Context,
	authorityStore *store.Store,
	projectID string,
	now time.Time,
) string {
	t.Helper()
	run := domainmodel.CreationRun{
		ID: "run:" + projectID, ProjectID: projectID,
		Goal: domainmodel.NovelGoal{Premise: "测试创作", TargetChapters: 3}.Goal(),
		Strategy: domainmodel.CreationRunStrategy{
			PlanWindowChapters: 3, ReviewCadence: domainmodel.ReviewPerPlanWindow, AutoRepairBudget: 3,
		},
		Preset: domainmodel.CreationRunPreset{
			Source: "test", Digest: "test-preset", Approval: domainmodel.ApprovalAuto,
		},
		State: domainmodel.RunRunning, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateCreationRun(ctx, run); err != nil {
		t.Fatalf("create runtime test run: %v", err)
	}
	return run.ID
}

type recoveryRuntimeModel struct {
	mu           sync.Mutex
	requests     [][]agentcore.Message
	proposalArgs json.RawMessage
	now          time.Time
}

func (m *recoveryRuntimeModel) Generate(
	context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return nil, errors.New("recovery runtime must use streaming")
}

func (m *recoveryRuntimeModel) GenerateStream(
	_ context.Context,
	messages []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, append([]agentcore.Message(nil), messages...))
	m.mu.Unlock()
	var message agentcore.Message
	switch index {
	case 0:
		message = runtimeToolCallMessage("list-workspace", "workspace_list", json.RawMessage(`{}`), m.now)
	case 1:
		message = runtimeToolCallMessage("submit-existing", "proposal_submit", m.proposalArgs, m.now.Add(time.Second))
	default:
		message = agentcore.Message{
			Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("done")},
			StopReason: agentcore.StopReasonStop, Timestamp: m.now.Add(2 * time.Second),
		}
	}
	stream := make(chan agentcore.StreamEvent, 4)
	for _, call := range message.ToolCalls() {
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallStart, Message: message}
		completed := call
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallEnd, Message: message, CompletedToolCall: &completed}
	}
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: message}
	close(stream)
	return stream, nil
}

func (m *recoveryRuntimeModel) SupportsTools() bool { return true }

func (m *recoveryRuntimeModel) Request(index int) []agentcore.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentcore.Message(nil), m.requests[index]...)
}

// TestPublishActivityTranslatesStreamingToolDeltas 守卫真实事件时序（agentcore
// 在整条消息完成后才执行工具）：toolcall delta 阶段就要有带工具名的进行中条目
// 与字节进度，exec start 不重复建条、exec end 收尾。
func TestPublishActivityTranslatesStreamingToolDeltas(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	hub := activity.NewHub()
	runtime := &Runtime{now: func() time.Time { return now }, activity: hub}
	operation := domainmodel.Operation{
		ID: "op-1", RunID: "run-1",
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-live"},
	}
	prose := newProseTracker()
	partial := runtimeToolCallMessage("call-1", "workspace_put_chapter", json.RawMessage(`{}`), now)
	runtime.publishActivity(operation, agentcore.Event{
		Type: agentcore.EventMessageUpdate, DeltaKind: agentcore.DeltaToolCall, ToolID: "call-1",
		Delta: `{"chapter":{"id":"c1","blocks":[{"id":"p1","text":"正文`, Message: partial,
	}, prose)
	snapshot, ok := hub.Snapshot("book-live")
	if !ok || len(snapshot.Entries) != 1 || snapshot.Entries[0].Tool != "workspace_put_chapter" ||
		snapshot.Entries[0].Done || snapshot.Entries[0].Bytes == 0 {
		t.Fatalf("streaming snapshot = %#v ok=%v", snapshot, ok)
	}
	// 逐字预览：参数流里的正文被解出并归属本次调用（页面设计 §3）。
	if string(snapshot.Prose) != "正文" || snapshot.ProseCallID != "call-1" {
		t.Fatalf("prose = %q callID = %q", snapshot.Prose, snapshot.ProseCallID)
	}
	runtime.publishActivity(operation, agentcore.Event{
		Type: agentcore.EventMessageUpdate, DeltaKind: agentcore.DeltaToolCall, ToolID: "call-1",
		Delta: `，一句接一句"}]}}`, Message: partial,
	}, prose)
	runtime.publishActivity(operation, agentcore.Event{
		Type: agentcore.EventToolExecStart, Tool: "workspace_put_chapter", ToolID: "call-1",
	}, prose)
	runtime.publishActivity(operation, agentcore.Event{
		Type: agentcore.EventToolExecEnd, Tool: "workspace_put_chapter", ToolID: "call-1",
	}, prose)
	snapshot, _ = hub.Snapshot("book-live")
	if len(snapshot.Entries) != 1 || !snapshot.Entries[0].Done {
		t.Fatalf("after exec = %#v", snapshot.Entries)
	}
	if string(snapshot.Prose) != `正文，一句接一句` {
		t.Fatalf("prose after close = %q", snapshot.Prose)
	}
}

// TestPublishActivityAttributesDeltaByToolID 守卫交错归属（agentcore ≥1.8.3
// 在 toolcall delta 上携带 ToolID）：并行调用并存时按 ID 精确定位所属调用，
// 不再取"最后一个"——正文 delta 归属正文调用，哪怕消息里后面又开了别的调用。
func TestPublishActivityAttributesDeltaByToolID(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	hub := activity.NewHub()
	runtime := &Runtime{now: func() time.Time { return now }, activity: hub}
	operation := domainmodel.Operation{
		ID: "op-2", RunID: "run-2",
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-mix"},
	}
	prose := newProseTracker()
	partial := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: "call-a", Name: "workspace_put_chapter"}),
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: "call-b", Name: "authority_read"}),
		},
		Timestamp: now,
	}
	runtime.publishActivity(operation, agentcore.Event{
		Type: agentcore.EventMessageUpdate, DeltaKind: agentcore.DeltaToolCall,
		ToolID: "call-a", Delta: `{"chapter":{"blocks":[{"text":"甲稿正文`, Message: partial,
	}, prose)
	snapshot, ok := hub.Snapshot("book-mix")
	if !ok || string(snapshot.Prose) != "甲稿正文" || snapshot.ProseCallID != "call-a" {
		t.Fatalf("prose = %q callID = %q ok=%v", snapshot.Prose, snapshot.ProseCallID, ok)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Tool != "workspace_put_chapter" ||
		snapshot.Entries[0].CallID != "call-a" {
		t.Fatalf("entries = %#v", snapshot.Entries)
	}
}

// TestPublishActivityThinkingTextAndErrorUnquoting 守卫 M3 两处翻译语义：
// 思考增量作为推理摘要文本入快照；出错 Result 解掉 json.Marshal 的外层引号。
func TestPublishActivityThinkingTextAndErrorUnquoting(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	hub := activity.NewHub()
	runtime := &Runtime{now: func() time.Time { return now }, activity: hub}
	operation := domainmodel.Operation{
		ID: "op-9", RunID: "run-9",
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-9"},
	}
	prose := newProseTracker()
	runtime.publishActivity(operation, agentcore.Event{
		Type: agentcore.EventMessageUpdate, DeltaKind: agentcore.DeltaThinking, Delta: "回顾伏笔",
	}, prose)
	runtime.publishActivity(operation, agentcore.Event{
		Type: agentcore.EventToolExecEnd, Tool: "workspace_put_chapter", ToolID: "c1",
		IsError: true, Result: json.RawMessage(`"章节段落 \"p-9\" 不存在"`),
	}, prose)
	snapshot, ok := hub.Snapshot("book-9")
	if !ok || snapshot.ThinkingNote != "回顾伏笔" {
		t.Fatalf("thinking note = %q ok=%v", snapshot.ThinkingNote, ok)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Err != `章节段落 "p-9" 不存在` {
		t.Fatalf("entries = %#v", snapshot.Entries)
	}
}

func runtimeToolCallMessage(id, name string, args json.RawMessage, at time.Time) agentcore.Message {
	return agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
			ID: id, Name: name, Args: args,
		})},
		StopReason: agentcore.StopReasonToolUse, Timestamp: at,
	}
}

func approvedRuntimeProposal(
	id string,
	target domainmodel.AuthorityTarget,
	base domainmodel.Revision,
	at time.Time,
	patches ...domainmodel.Patch,
) domainmodel.Proposal {
	return domainmodel.Proposal{
		ID: id, Target: target, BaseRevision: base,
		Author: domainmodel.Author{Kind: domainmodel.AuthorUser, ID: "user-1"},
		Reason: "seed", Patches: patches, ApprovalState: domainmodel.ApprovalApproved,
		DecidedBy: &domainmodel.Author{Kind: domainmodel.AuthorUser, ID: "user-1"}, DecidedAt: &at, CreatedAt: at,
	}
}

func TestRuntimeSemanticComplianceUsesIndependentStructuredCall(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	target := domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-semantic"}
	seed := approvedRuntimeProposal("seed-semantic", target, 0, now,
		domainmodel.Patch{
			Document:  domainmodel.DocumentRef{Kind: domainmodel.DocumentIntent, ID: "root"},
			Operation: domainmodel.PatchPut, Content: json.RawMessage(`{"premise":"守住底线"}`),
		},
		domainmodel.Patch{
			Document:  domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "hero-bottom-line"},
			Operation: domainmodel.PatchPut,
			Content:   json.RawMessage(`{"id":"hero-bottom-line","kind":"world_rule","subject_id":"hero","predicate":"rule.bottom_line","new_value":"不伤无辜"}`),
		},
	)
	pending := seed
	pending.ApprovalState, pending.DecidedBy, pending.DecidedAt = domainmodel.ApprovalPending, nil, nil
	if _, err := authorityStore.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save seed: %v", err)
	}
	if _, err := authorityStore.CommitProposal(ctx, seed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	task := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","chapter_number":1}`)
	operation := domainmodel.Operation{
		ID: "semantic-operation", Kind: domainmodel.OperationWriteChapter, Target: target,
		State: domainmodel.OperationQueued, RunID: createRuntimeTestRun(t, ctx, authorityStore, target.ID, now),
		Snapshot: runtimeSnapshot(task, 1, domainmodel.ApprovalAuto, "profile"),
		Input:    task, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	model := &semanticResponseModel{}
	runtime := NewRuntime(model, "model", authorityStore)
	runtime.now = func() time.Time { return now.Add(2 * time.Second) }
	report, err := runtime.AnalyzeSemanticCompliance(ctx, operation, domainmodel.Proposal{
		Patches: []domainmodel.Patch{{
			Document:  domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: "chapter-1"},
			Operation: domainmodel.PatchPut, Content: json.RawMessage(`{"candidate":"正文"}`),
		}},
	}, []domainmodel.OwnershipRule{{
		Target: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "hero-bottom-line"}, Control: domainmodel.ControlLocked,
	}})
	if err != nil {
		t.Fatalf("analyze semantic compliance: %v", err)
	}
	if report.Status != domainmodel.SemanticCompliancePass || !model.usedJSONSchema {
		t.Fatalf("report = %#v, json schema = %v", report, model.usedJSONSchema)
	}
	events, err := authorityStore.ListOperationEvents(ctx, operation.ID)
	if err != nil {
		t.Fatalf("list operation events: %v", err)
	}
	if len(events) != 2 || events[1].Kind != "semantic.compliance_checked" {
		t.Fatalf("events = %#v", events)
	}
}

type semanticResponseModel struct {
	usedJSONSchema bool
	response       string
}

func (m *semanticResponseModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	config := agentcore.ResolveCallConfig(opts)
	m.usedJSONSchema = config.ResponseFormat != nil && config.ResponseFormat.Type == agentcore.ResponseFormatJSONSchema
	response := m.response
	if response == "" {
		response = `{"status":"pass","findings":[]}`
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(response)},
		Usage:   &agentcore.Usage{Input: 10, Output: 3}, Timestamp: time.Now(),
	}}, nil
}

func (*semanticResponseModel) GenerateStream(
	context.Context,
	[]agentcore.Message,
	[]agentcore.ToolSpec,
	...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("streaming is not used by semantic compliance")
}

func (*semanticResponseModel) SupportsTools() bool { return false }

func TestRuntimeAnalyzesSemanticImpactWithStrictContract(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	target := domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "semantic-book"}
	seed := approvedRuntimeProposal("seed-semantic", target, 0, now, domainmodel.Patch{
		Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentIntent, ID: "root"}, Operation: domainmodel.PatchPut,
		Content: json.RawMessage(`{"premise":"旧事实已经进入正文"}`),
	})
	pending := seed
	pending.ApprovalState, pending.DecidedBy, pending.DecidedAt = domainmodel.ApprovalPending, nil, nil
	if _, err := authorityStore.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save seed: %v", err)
	}
	if _, err := authorityStore.CommitProposal(ctx, seed); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	model := &semanticResponseModel{response: `{
		"status":"conflict",
		"findings":[{"document":{"kind":"intent","id":"root"},"explanation":"新前提与旧前提冲突"}],
		"options":[
			{"strategy":"rewrite_affected","chapter_ids":["chapter-1"],"explanation":"重写受影响章节"},
			{"strategy":"reinterpret_future","explanation":"在后文重新解释"},
			{"strategy":"abandon","explanation":"放弃变更"}
		]}`}
	runtime := NewRuntime(model, "model", authorityStore)
	proposal := domainmodel.Proposal{
		ID: "semantic-change", Target: target, BaseRevision: 1,
		Author: domainmodel.Author{Kind: domainmodel.AuthorUser, ID: "user-1"}, Reason: "修改前提",
		Patches: []domainmodel.Patch{{
			Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentIntent, ID: "root"}, Operation: domainmodel.PatchPut,
			Content: json.RawMessage(`{"premise":"新的前提"}`),
		}}, ApprovalState: domainmodel.ApprovalPending, CreatedAt: now.Add(time.Minute),
	}
	reportJSON, err := runtime.Analyze(ctx, proposal, change.StructuralImpact{
		Direct: []domainmodel.DocumentRef{{Kind: domainmodel.DocumentIntent, ID: "root"}},
	})
	if err != nil {
		t.Fatalf("analyze semantic impact: %v", err)
	}
	var report change.SemanticImpactReport
	if err := json.Unmarshal(reportJSON, &report); err != nil || report.Status != change.SemanticImpactConflict || !model.usedJSONSchema {
		t.Fatalf("report = %#v, schema = %v, error = %v", report, model.usedJSONSchema, err)
	}
}

func TestAffectedRewriteSubmissionCoversEveryWorkspaceChapter(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	operation := domainmodel.Operation{
		ID: "rewrite-affected", Kind: domainmodel.OperationRewriteAffected,
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-1"}, State: domainmodel.OperationQueued,
		RunID: createRuntimeTestRun(t, ctx, authorityStore, "book-1", now),
		Snapshot: runtimeSnapshot(
			json.RawMessage(`{"chapter_ids":["chapter-1","chapter-2"],"base_revision":3,"resolution_proposal_id":"change-1","reason":"同步旧事实"}`),
			3, domainmodel.ApprovalManual, "profile",
		),
		Input:     json.RawMessage(`{"chapter_ids":["chapter-1","chapter-2"],"base_revision":3,"resolution_proposal_id":"change-1","reason":"同步旧事实"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimOperationForExecutor(ctx, operation.ID, "worker-1", prompt.ExecutorIdentity("model"), time.Minute, now)
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	chapters := []domainmodel.ManuscriptChapter{
		{ID: "chapter-1", PlanNodeID: "plan-1", Number: 1, Title: "第一章", Author: domainmodel.AuthorAI, Blocks: []domainmodel.ManuscriptBlock{{ID: "block-1", Text: "新正文一"}}},
		{ID: "chapter-2", PlanNodeID: "plan-2", Number: 2, Title: "第二章", Author: domainmodel.AuthorAI, Blocks: []domainmodel.ManuscriptBlock{{ID: "block-2", Text: "新正文二"}}},
	}
	var patches []domainmodel.Patch
	keys := make([]string, 0, len(chapters))
	for index, chapter := range chapters {
		content, err := json.Marshal(chapter)
		if err != nil {
			t.Fatal(err)
		}
		key := "chapter/" + chapter.ID
		keys = append(keys, key)
		if _, err := authorityStore.PutWorkspaceArtifact(ctx, domainmodel.WorkspaceArtifact{
			OperationID: operation.ID, Key: key, MediaType: workspace.ChapterMediaType,
			Content: content, UpdatedAt: now,
		}, 0, operation.Attempt); err != nil {
			t.Fatalf("put workspace chapter: %v", err)
		}
		patches = append(patches,
			domainmodel.Patch{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: chapter.ID}, Operation: domainmodel.PatchPut, Content: content},
			domainmodel.Patch{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: fmt.Sprintf("fact-%d", index+1)}, Operation: domainmodel.PatchPut,
				Content: json.RawMessage(fmt.Sprintf(`{"id":"fact-%d","kind":"event","subject_id":"hero","predicate":"event.rewrite_%d","new_value":true,"source_chapter_id":"%s"}`, index+1, index+1, chapter.ID))},
		)
	}
	runtime := NewRuntime(nil, "model", authorityStore)
	if err := runtime.validateSubmissionArtifact(ctx, operation, keys, "", patches); err != nil {
		t.Fatalf("validate complete batch: %v", err)
	}
	if err := runtime.validateSubmissionArtifact(ctx, operation, keys[:1], "", patches); err == nil {
		t.Fatal("incomplete affected rewrite submission was accepted")
	}
}

func TestWriterSubmissionEnforcesDirectiveWordCounts(t *testing.T) {
	// S13：量化要求由宿主确定性校验——越界提交连同实际值与区间被拒回给模型自纠。
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	operation := domainmodel.Operation{
		ID: "write-counted", Kind: domainmodel.OperationWriteChapter,
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-1"}, State: domainmodel.OperationQueued,
		RunID: createRuntimeTestRun(t, ctx, authorityStore, "book-1", now),
		Snapshot: runtimeSnapshot(
			json.RawMessage(`{"chapter_plan_id":"plan-1","chapter_number":1,"directives":[{"id":"length","scope":"project","text":"每章十字左右","constraints":{"target_words":10},"status":"active"}]}`),
			2, domainmodel.ApprovalAuto, "profile",
		),
		Input:     json.RawMessage(`{"chapter_plan_id":"plan-1","chapter_number":1,"directives":[{"id":"length","scope":"project","text":"每章十字左右","constraints":{"target_words":10},"status":"active"}]}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimOperationForExecutor(ctx, operation.ID, "worker-1", prompt.ExecutorIdentity("model"), time.Minute, now)
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	runtime := NewRuntime(nil, "model", authorityStore)
	submit := func(text string, version int64) error {
		chapter := domainmodel.ManuscriptChapter{
			ID: "chapter-1", PlanNodeID: "plan-1", Number: 1, Title: "标题不计入字数", Author: domainmodel.AuthorAI,
			Blocks: []domainmodel.ManuscriptBlock{{ID: "block-1", Text: text}},
		}
		content, _ := json.Marshal(chapter)
		if _, err := authorityStore.PutWorkspaceArtifact(ctx, domainmodel.WorkspaceArtifact{
			OperationID: operation.ID, Key: "chapter/chapter-1", MediaType: workspace.ChapterMediaType,
			Content: content, UpdatedAt: now,
		}, version, operation.Attempt); err != nil {
			t.Fatalf("put workspace chapter: %v", err)
		}
		patches := []domainmodel.Patch{
			{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: chapter.ID}, Operation: domainmodel.PatchPut, Content: content},
			{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "fact-1"}, Operation: domainmodel.PatchPut,
				Content: json.RawMessage(`{"id":"fact-1","kind":"event","subject_id":"hero","predicate":"event.done","new_value":true,"source_chapter_id":"chapter-1"}`)},
		}
		return runtime.validateSubmissionArtifact(ctx, operation, []string{"chapter/chapter-1"}, "", patches)
	}
	if err := submit("太短", 0); !errors.Is(err, domainmodel.ErrInvalid) || !strings.Contains(err.Error(), "9-11") {
		t.Fatalf("short chapter err = %v", err)
	}
	if err := submit(strings.Repeat("长", 12), 1); !errors.Is(err, domainmodel.ErrInvalid) {
		t.Fatalf("long chapter err = %v", err)
	}
	if err := submit(strings.Repeat("好", 10), 2); err != nil {
		t.Fatalf("in-range chapter err = %v", err)
	}
}

func TestRuntimePlanSubmissionEnforcesRequestedChapters(t *testing.T) {
	// §6.3 数量不变量前移到工具边界：滚动规划多产章节在 proposal_submit 当场
	// 被拒，模型在同一会话内纠正后重新提交，而不是收尾时把整个 Operation 打死。
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	worker, err := prompt.BuiltinWorkerProfile("architect.design")
	if err != nil {
		t.Fatalf("load architect capability: %v", err)
	}
	task := json.RawMessage(`{"intent":"规划开篇","target_chapters":3,"requested_chapters":1}`)
	operation := domainmodel.Operation{
		ID: "plan-1", Kind: domainmodel.OperationDevelopPlan,
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-plan"},
		State:  domainmodel.OperationQueued,
		RunID:  createRuntimeTestRun(t, ctx, authorityStore, "book-plan", now),
		Snapshot: runtimeSnapshot(task, 1, domainmodel.ApprovalAuto,
			seedRuntimeProfile(t, ctx, authorityStore, "book-plan", worker, task, 1)),
		Input: task, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	patchFor := func(node domainmodel.PlanNode) domainmodel.Patch {
		content, err := json.Marshal(node)
		if err != nil {
			t.Fatalf("marshal plan node: %v", err)
		}
		return domainmodel.Patch{
			Document:  domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: node.ID},
			Operation: domainmodel.PatchPut, Content: content,
		}
	}
	volume := domainmodel.PlanNode{ID: "volume-1", Kind: domainmodel.PlanVolume, Order: 1, Title: "第一卷", Summary: "开端"}
	arc := domainmodel.PlanNode{ID: "arc-1", Kind: domainmodel.PlanArc, ParentID: "volume-1", Order: 1, Title: "第一幕", Summary: "启程"}
	chapterOne := domainmodel.PlanNode{ID: "chapter-plan-1", Kind: domainmodel.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "出发"}
	chapterTwo := domainmodel.PlanNode{ID: "chapter-plan-2", Kind: domainmodel.PlanChapter, ParentID: "arc-1", Order: 2, Title: "第二章", Summary: "多余"}
	overshoot, _ := json.Marshal(map[string]any{
		"reason":  "多规划一章",
		"patches": []domainmodel.Patch{patchFor(volume), patchFor(arc), patchFor(chapterOne), patchFor(chapterTwo)},
	})
	exact, _ := json.Marshal(map[string]any{
		"reason":  "按请求规划一章",
		"patches": []domainmodel.Patch{patchFor(volume), patchFor(arc), patchFor(chapterOne)},
	})
	model := &planRuntimeModel{steps: []json.RawMessage{overshoot, exact}, now: now.Add(2 * time.Second)}
	runtime := NewRuntime(model, "model", authorityStore)
	runtime.now = func() time.Time { return now.Add(3 * time.Second) }
	outcome, err := runtime.Execute(ctx, operation)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if outcome.Proposal == nil || len(outcome.Proposal.Patches) != 3 {
		t.Fatalf("outcome = %#v, want corrected 3-patch proposal", outcome)
	}
	if model.requests < 3 {
		t.Fatalf("model requests = %d, want overshoot rejected then corrected resubmission", model.requests)
	}
}

type planRuntimeModel struct {
	mu       sync.Mutex
	requests int
	steps    []json.RawMessage
	now      time.Time
}

func (m *planRuntimeModel) Generate(
	context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return nil, errors.New("plan runtime must use streaming")
}

func (m *planRuntimeModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	m.mu.Lock()
	index := m.requests
	m.requests++
	m.mu.Unlock()
	var message agentcore.Message
	if index < len(m.steps) {
		message = runtimeToolCallMessage(fmt.Sprintf("plan-submit-%d", index+1), "proposal_submit",
			m.steps[index], m.now.Add(time.Duration(index)*time.Second))
	} else {
		message = agentcore.Message{
			Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("done")},
			StopReason: agentcore.StopReasonStop, Timestamp: m.now.Add(time.Duration(index) * time.Second),
		}
	}
	stream := make(chan agentcore.StreamEvent, 4)
	for _, call := range message.ToolCalls() {
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallStart, Message: message}
		completed := call
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallEnd, Message: message, CompletedToolCall: &completed}
	}
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: message}
	close(stream)
	return stream, nil
}

func (m *planRuntimeModel) SupportsTools() bool { return true }

// runtimeSnapshot 按 D45 形状冻结快照；config 为已落盘的 Execution Profile 摘要，
// 只走工具校验不执行模型的用例可以用任意非空值。
func runtimeSnapshot(input json.RawMessage, base domainmodel.Revision, policy domainmodel.ApprovalPolicy, config string) domainmodel.ExecutionSnapshot {
	return domainmodel.ExecutionSnapshot{
		Executor: prompt.ExecutorIdentity("model"), BaseRevision: base, InputDigest: domainmodel.Digest(input),
		ConfigDigest: config, ApprovalPolicy: policy,
	}
}

// seedRuntimeProfile 编译并落盘一份 Execution Profile，返回其摘要供快照引用。
func seedRuntimeProfile(
	t *testing.T,
	ctx context.Context,
	authorityStore *store.Store,
	projectID string,
	worker prompt.WorkerProfile,
	task json.RawMessage,
	base domainmodel.Revision,
) string {
	t.Helper()
	compiled, err := prompt.NewRegistry(authorityStore).Reload(ctx, prompt.CompileRequest{
		ProjectID: projectID, CoreProtocolVersion: "core-v1", Worker: worker,
		Intent: domainmodel.Intent{Premise: "测试作品"}, StoryContext: json.RawMessage(`{"revision":` + fmt.Sprint(base) + `}`),
		Task: task, BaseRevision: base, ProjectOverlayRevision: base, ModelConfigDigest: "model",
	}, time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("seed execution profile: %v", err)
	}
	return compiled.ProfileDigest
}

// TestWriterSubmissionRequiresRedeclaringChapterFacts：工具边界当场执行 D41 的重申报与
// 来源归属，偏差回给模型在同一会话内纠正。
func TestWriterSubmissionRequiresRedeclaringChapterFacts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	target := domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-1"}
	document := func(ref domainmodel.DocumentRef, value any) domainmodel.Patch {
		content, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return domainmodel.Patch{Document: ref, Operation: domainmodel.PatchPut, Content: content}
	}
	seed, err := change.New(authorityStore).Prepare(ctx, domainmodel.Proposal{
		ID: "seed", Target: target, Author: domainmodel.Author{Kind: domainmodel.AuthorUser, ID: "user-1"}, Reason: "seed",
		Patches: []domainmodel.Patch{
			document(domainmodel.DocumentRef{Kind: domainmodel.DocumentIntent, ID: "root"}, domainmodel.Intent{Premise: "凡人修仙"}),
			document(domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: "volume-1"}, domainmodel.PlanNode{ID: "volume-1", Kind: domainmodel.PlanVolume, Title: "卷一", Summary: "入道"}),
			document(domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: "arc-1"}, domainmodel.PlanNode{ID: "arc-1", Kind: domainmodel.PlanArc, ParentID: "volume-1", Title: "弧一", Summary: "山门"}),
			document(domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: "plan-1"}, domainmodel.PlanNode{ID: "plan-1", Kind: domainmodel.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "抵达"}),
			document(domainmodel.DocumentRef{Kind: domainmodel.DocumentEntity, ID: "hero"}, domainmodel.Entity{ID: "hero", Kind: domainmodel.EntityCharacter, Name: "主角"}),
			document(domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: "chapter-1"}, domainmodel.ManuscriptChapter{
				ID: "chapter-1", PlanNodeID: "plan-1", Number: 1, Title: "第一章", Author: domainmodel.AuthorAI,
				Blocks: []domainmodel.ManuscriptBlock{{ID: "block-1", Text: "旧正文"}},
			}),
			document(domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "fact-1"}, domainmodel.CanonFact{
				ID: "fact-1", Kind: domainmodel.CanonEvent, SubjectID: "hero", Predicate: "event.done", Value: json.RawMessage(`true`), SourceChapterID: "chapter-1",
			}),
		},
		ApprovalState: domainmodel.ApprovalPending, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("prepare seed: %v", err)
	}
	if seed, err = change.Decide(seed, domainmodel.ApprovalApproved, domainmodel.Author{Kind: domainmodel.AuthorUser, ID: "user-1"}, now); err != nil {
		t.Fatalf("approve seed: %v", err)
	}
	if _, err := change.New(authorityStore).Commit(ctx, seed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	input := json.RawMessage(`{"chapter_id":"chapter-1","chapter_plan_id":"plan-1","chapter_number":1,"findings":["结尾仓促"]}`)
	operation := domainmodel.Operation{
		ID: "rewrite-1", Kind: domainmodel.OperationRewriteChapter, Target: target, State: domainmodel.OperationQueued,
		RunID: createRuntimeTestRun(t, ctx, authorityStore, "book-1", now), Snapshot: runtimeSnapshot(input, 1, domainmodel.ApprovalAuto, "profile"),
		Input: input, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if operation, err = authorityStore.ClaimOperationForExecutor(ctx, operation.ID, "worker-1", prompt.ExecutorIdentity("model"), time.Minute, now); err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	chapter := domainmodel.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "plan-1", Number: 1, Title: "第一章", Author: domainmodel.AuthorAI,
		Blocks: []domainmodel.ManuscriptBlock{{ID: "block-1", Text: "新正文"}},
	}
	content, _ := json.Marshal(chapter)
	if _, err := authorityStore.PutWorkspaceArtifact(ctx, domainmodel.WorkspaceArtifact{
		OperationID: operation.ID, Key: "chapter/chapter-1", MediaType: workspace.ChapterMediaType, Content: content, UpdatedAt: now,
	}, 0, operation.Attempt); err != nil {
		t.Fatalf("put workspace chapter: %v", err)
	}
	runtime := NewRuntime(nil, "model", authorityStore)
	manuscript := domainmodel.Patch{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: "chapter-1"}, Operation: domainmodel.PatchPut, Content: content}
	canon := func(raw string) domainmodel.Patch {
		var fact domainmodel.CanonFact
		if err := json.Unmarshal([]byte(raw), &fact); err != nil {
			t.Fatal(err)
		}
		return domainmodel.Patch{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: fact.ID}, Operation: domainmodel.PatchPut, Content: json.RawMessage(raw)}
	}
	submit := func(patches ...domainmodel.Patch) error {
		return runtime.validateSubmissionArtifact(ctx, operation, []string{"chapter/chapter-1"}, "", append([]domainmodel.Patch{manuscript}, patches...))
	}
	fresh := `{"id":"fact-2","kind":"event","subject_id":"hero","predicate":"event.left","new_value":true,"source_chapter_id":"chapter-1"}`
	if err := submit(canon(fresh)); !errors.Is(err, domainmodel.ErrInvalid) || !strings.Contains(err.Error(), `redeclare canon "fact-1"`) {
		t.Fatalf("missing redeclaration err = %v", err)
	}
	foreign := `{"id":"fact-3","kind":"event","subject_id":"hero","predicate":"event.elsewhere","new_value":true,"source_chapter_id":"chapter-9"}`
	if err := submit(canon(fresh), canon(foreign)); !errors.Is(err, domainmodel.ErrInvalid) || !strings.Contains(err.Error(), "sourced from a submitted chapter") {
		t.Fatalf("foreign source err = %v", err)
	}
	confirmed := `{"id":"fact-1","kind":"event","subject_id":"hero","predicate":"event.done","old_value":true,"new_value":true,"source_chapter_id":"chapter-1"}`
	if err := submit(canon(confirmed), canon(fresh)); err != nil {
		t.Fatalf("redeclared submission err = %v", err)
	}
	deleted := domainmodel.Patch{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "fact-1"}, Operation: domainmodel.PatchDelete}
	if err := submit(deleted, canon(fresh)); err != nil {
		t.Fatalf("deletion counts as redeclaration: %v", err)
	}
}

// TestReviseCanonSubmissionCoversRequestedFacts：事实核验任务只提交 canon 补丁、
// 来源固定为任务章节、待核验事实全部处理且至少留一条事实。
func TestReviseCanonSubmissionCoversRequestedFacts(t *testing.T) {
	input := json.RawMessage(`{"chapter_id":"chapter-1","fact_ids":["fact-1"],"reason":"正文被改过"}`)
	operation := domainmodel.Operation{
		ID: "canon-1", Kind: domainmodel.OperationReviseCanon,
		Target:   domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-1"},
		Snapshot: runtimeSnapshot(input, 3, domainmodel.ApprovalAuto, "profile"), Input: input,
	}
	canon := func(id, source string) domainmodel.Patch {
		return domainmodel.Patch{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: id}, Operation: domainmodel.PatchPut,
			Content: json.RawMessage(fmt.Sprintf(`{"id":%q,"kind":"event","subject_id":"hero","predicate":"event.done","old_value":true,"new_value":true,"source_chapter_id":%q}`, id, source))}
	}
	deleted := domainmodel.Patch{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "fact-1"}, Operation: domainmodel.PatchDelete}
	manuscript := domainmodel.Patch{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: "chapter-1"}, Operation: domainmodel.PatchPut, Content: json.RawMessage(`{}`)}
	cases := []struct {
		name    string
		patches []domainmodel.Patch
		wantErr string
	}{
		{"manuscript patches are rejected", []domainmodel.Patch{manuscript, canon("fact-1", "chapter-1")}, "only change canon facts"},
		{"pending fact must be handled", []domainmodel.Patch{canon("fact-2", "chapter-1")}, "pending verification"},
		{"chapter keeps at least one fact", []domainmodel.Patch{deleted}, "at least one canon fact"},
		{"source must be the task chapter", []domainmodel.Patch{canon("fact-1", "chapter-2")}, "sourced from chapter"},
		{"confirmation passes", []domainmodel.Patch{canon("fact-1", "chapter-1")}, ""},
		{"replacement passes", []domainmodel.Patch{deleted, canon("fact-2", "chapter-1")}, ""},
	}
	for _, tc := range cases {
		err := validateCanonRevision(operation, tc.patches)
		if tc.wantErr == "" && err != nil {
			t.Fatalf("%s: unexpected err %v", tc.name, err)
		}
		if tc.wantErr != "" && (!errors.Is(err, domainmodel.ErrInvalid) || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}
