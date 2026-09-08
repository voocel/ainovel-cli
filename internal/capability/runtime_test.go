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
	"github.com/voocel/ainovel-cli/internal/activity"
	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/workspace"
)

func TestRuntimeToolsRespectAuthoritySnapshotAndWorkspaceBoundary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	seed := approvedRuntimeProposal("seed", target, 0, now, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
		Operation: domain.PatchPut, Content: json.RawMessage(`{"premise":"凡人修仙"}`),
	})
	pending := seed
	pending.ApprovalState, pending.DecidedBy, pending.DecidedAt = domain.ApprovalPending, nil, nil
	if _, err := authorityStore.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save seed: %v", err)
	}
	if _, err := authorityStore.CommitProposal(ctx, seed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	operation := domain.Operation{
		ID: "write-1", Kind: domain.OperationWriteChapter, Target: target,
		State: domain.OperationQueued, RunID: createRuntimeTestRun(t, ctx, authorityStore, target.ID, now),
		Snapshot: domain.ExecutionSnapshot{
			ExecutionProfileDigest: "profile", BaseRevision: 1,
			CoreProtocolVersion: "core-v1", WorkerProfileVersion: "writer.compose@1",
			ToolSchemaDigest: "tools", PromptDigest: "prompt", ModelConfigDigest: "model",
			ApprovalPolicy: domain.ApprovalManual, ApprovalPolicyDigest: "manual",
		},
		Input: json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`), CreatedAt: now, UpdatedAt: now,
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

	read, err := runtime.toolExecutor(operation, "authority_read", nil, nil)
	if err != nil {
		t.Fatalf("build read tool: %v", err)
	}
	if _, err := read(ctx, json.RawMessage(`{"kind":"intent","id":"root","revision":2}`)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("future revision read error = %v, want domain.ErrInvalid", err)
	}
	if _, err := read(ctx, json.RawMessage(`{"kind":"intent","id":"root","revision":1}`)); err != nil {
		t.Fatalf("read frozen revision: %v", err)
	}

	putChapter, err := runtime.toolExecutor(operation, "workspace_put_chapter", nil, nil)
	if err != nil {
		t.Fatalf("build put chapter tool: %v", err)
	}
	chapter := domain.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门",
		Blocks: []domain.ManuscriptBlock{{ID: "p-1", Text: "他抵达山门。"}},
	}
	chapterArgs, _ := json.Marshal(map[string]any{
		"key": "chapter/chapter-1", "expected_version": 0,
		"chapter": chapter,
	})
	if _, err := putChapter(ctx, chapterArgs); err != nil {
		t.Fatalf("write operation workspace: %v", err)
	}

	var submitted domain.Proposal
	submitTool, err := runtime.toolExecutor(operation, "proposal_submit", func(proposal domain.Proposal) (domain.Proposal, error) {
		submitted = proposal
		return proposal, nil
	}, nil)
	if err != nil {
		t.Fatalf("build submit tool: %v", err)
	}
	chapterContent, _ := json.Marshal(chapter)
	canonContent, _ := json.Marshal(domain.CanonFact{
		ID: "chapter-1-outcome", Kind: domain.CanonEvent, SubjectID: "chapter-1",
		Predicate: "event.chapter_outcome", Value: json.RawMessage(`"抵达山门"`), SourceChapterID: chapter.ID,
	})
	proposalArgs, _ := json.Marshal(map[string]any{
		"reason":        "提交章节候选",
		"workspace_key": "chapter/chapter-1",
		"patches": []domain.Patch{
			{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-1"}, Operation: domain.PatchPut, Content: chapterContent},
			{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "chapter-1-outcome"}, Operation: domain.PatchPut, Content: canonContent},
		},
	})
	if _, err := submitTool(ctx, proposalArgs); err != nil {
		t.Fatalf("submit proposal candidate: %v", err)
	}
	if submitted.OperationID != operation.ID || submitted.BaseRevision != 1 {
		t.Fatalf("submitted proposal = %#v", submitted)
	}
	if _, err := authorityStore.GetProposal(ctx, submitted.ID); !errors.Is(err, store.ErrNotFound) {
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
		operation := domain.Operation{Kind: definition.OperationKinds[0]}
		if _, err := runtime.toolsFor(operation, definition.Worker.Tools, func(proposal domain.Proposal) (domain.Proposal, error) {
			return proposal, nil
		}, func(verdict domain.ReviewVerdict) (domain.ReviewVerdict, error) {
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
	snapshot := domain.ExecutionSnapshot{
		ExecutionProfileDigest: "recovery-profile", BaseRevision: 0,
		CoreProtocolVersion: "core-v1", WorkerProfileVersion: "writer.compose@1",
		ToolSchemaDigest: "tools", PromptDigest: "prompt", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, ApprovalPolicyDigest: "manual",
	}
	operation := domain.Operation{
		ID: "recover-draft", Kind: domain.OperationWriteChapter,
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-recovery"},
		State:  domain.OperationQueued,
		RunID:  createRuntimeTestRun(t, ctx, authorityStore, "book-recovery", now), Snapshot: snapshot,
		Input: json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`), CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	first, err := authorityStore.ClaimNextOperation(ctx, "worker-before-crash", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim first attempt: %v", err)
	}
	chapter := domain.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门",
		Blocks: []domain.ManuscriptBlock{{ID: "block-1", Text: "已经写入工作区的半成品。"}},
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
	if _, err := authorityStore.AppendOperationEvent(ctx, domain.OperationEvent{
		OperationID: first.ID, StepID: "agent.message", Attempt: first.Attempt,
		IdempotencyKey: "agent-message:1:1", Kind: "agent.message_committed",
		Payload: payload, CreatedAt: previous.Timestamp,
	}); err != nil {
		t.Fatalf("persist previous message: %v", err)
	}
	if _, err := authorityStore.TransitionOperation(
		ctx, first.ID, domain.OperationRunning, domain.OperationFailed, "simulated process failure", now.Add(4*time.Second),
	); err != nil {
		t.Fatalf("fail first attempt: %v", err)
	}
	if _, err := authorityStore.TransitionOperation(
		ctx, first.ID, domain.OperationFailed, domain.OperationQueued, "resume", now.Add(5*time.Second),
	); err != nil {
		t.Fatalf("queue second attempt: %v", err)
	}
	second, err := authorityStore.ClaimNextOperation(ctx, "worker-after-crash", time.Minute, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("claim second attempt: %v", err)
	}
	chapterContent, _ := json.Marshal(chapter)
	canonContent, _ := json.Marshal(domain.CanonFact{
		ID: "chapter-1-outcome", Kind: domain.CanonEvent, SubjectID: "chapter-1",
		Predicate: "event.chapter_outcome", Value: json.RawMessage(`"完成已有工作稿"`), SourceChapterID: chapter.ID,
	})
	proposalArgs, _ := json.Marshal(map[string]any{
		"reason": "继续并提交已有工作稿", "workspace_key": artifact.Key,
		"patches": []domain.Patch{
			{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}, Operation: domain.PatchPut, Content: chapterContent},
			{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "chapter-1-outcome"}, Operation: domain.PatchPut, Content: canonContent},
		},
	})
	model := &recoveryRuntimeModel{proposalArgs: proposalArgs, now: now.Add(7 * time.Second)}
	runtime := NewRuntime(model, "model", authorityStore)
	runtime.now = func() time.Time { return now.Add(8 * time.Second) }
	outcome, err := runtime.Execute(ctx, second, prompt.Compiled{
		StablePrefix: "stable", DynamicTail: "original dynamic task", Tools: worker.Tools,
		ProfileDigest: snapshot.ExecutionProfileDigest, Snapshot: snapshot,
	})
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
	snapshot := domain.ExecutionSnapshot{
		ExecutionProfileDigest: "review-profile", BaseRevision: 3,
		CoreProtocolVersion: "core-v1", WorkerProfileVersion: worker.ID + "@" + worker.Version,
		ToolSchemaDigest: "tools", PromptDigest: "prompt", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalAuto, ApprovalPolicyDigest: "auto",
	}
	operation := domain.Operation{
		ID: "review-1", Kind: domain.OperationReviewRange,
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-review"},
		State:  domain.OperationQueued,
		RunID:  createRuntimeTestRun(t, ctx, authorityStore, "book-review", now), Snapshot: snapshot,
		Input:     json.RawMessage(`{"range":{"chapter_ids":["chapter-1","chapter-2"]},"verify_intent":true,"directives":[{"id":"hook","scope":"project","text":"每章结尾留钩子","status":"active"}]}`),
		CreatedAt: now, UpdatedAt: now,
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
	outcome, err := runtime.Execute(ctx, operation, prompt.Compiled{
		StablePrefix: "stable", DynamicTail: "review task", Tools: worker.Tools,
		ProfileDigest: snapshot.ExecutionProfileDigest, Snapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("execute review: %v", err)
	}
	if outcome.Proposal != nil || len(outcome.Verdict) == 0 {
		t.Fatalf("outcome = %#v, want verdict-only", outcome)
	}
	var verdict domain.ReviewVerdict
	if err := json.Unmarshal(outcome.Verdict, &verdict); err != nil {
		t.Fatalf("decode verdict: %v", err)
	}
	if verdict.Status != domain.ReviewPass || verdict.Revision != 3 ||
		len(verdict.ChapterIDs) != 2 || len(verdict.Findings) != 1 ||
		verdict.ReviewKey != "review/findings" || !verdict.IntentSatisfied() ||
		len(verdict.Directives) != 1 || !verdict.DirectivesSatisfied() {
		t.Fatalf("verdict = %#v", verdict)
	}
	artifact, err := authorityStore.GetWorkspaceArtifact(ctx, operation.ID, verdict.ReviewKey)
	if err != nil || artifact.MediaType != domain.ReviewArtifactMediaType {
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
	run := domain.CreationRun{
		ID: "run:" + projectID, ProjectID: projectID,
		Goal: domain.CreationRunGoal{Premise: "测试创作", TargetChapters: 3},
		Strategy: domain.CreationRunStrategy{
			PlanWindowChapters: 3, ReviewCadence: domain.ReviewPerPlanWindow, AutoRepairBudget: 3,
		},
		Preset: domain.CreationRunPreset{
			Source: "test", Digest: "test-preset", Approval: domain.ApprovalAuto,
		},
		State: domain.RunRunning, CreatedAt: now, UpdatedAt: now,
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
	operation := domain.Operation{
		ID: "op-1", RunID: "run-1",
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-live"},
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
	operation := domain.Operation{
		ID: "op-2", RunID: "run-2",
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-mix"},
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
	operation := domain.Operation{
		ID: "op-9", RunID: "run-9",
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-9"},
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
	target domain.AuthorityTarget,
	base domain.Revision,
	at time.Time,
	patches ...domain.Patch,
) domain.Proposal {
	return domain.Proposal{
		ID: id, Target: target, BaseRevision: base,
		Author: domain.Author{Kind: domain.AuthorUser, ID: "user-1"},
		Reason: "seed", Patches: patches, ApprovalState: domain.ApprovalApproved,
		DecidedBy: &domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, DecidedAt: &at, CreatedAt: at,
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
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-semantic"}
	seed := approvedRuntimeProposal("seed-semantic", target, 0, now,
		domain.Patch{
			Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
			Operation: domain.PatchPut, Content: json.RawMessage(`{"premise":"守住底线"}`),
		},
		domain.Patch{
			Document:  domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-bottom-line"},
			Operation: domain.PatchPut,
			Content:   json.RawMessage(`{"id":"hero-bottom-line","kind":"world_rule","subject_id":"hero","predicate":"rule.bottom_line","new_value":"不伤无辜"}`),
		},
	)
	pending := seed
	pending.ApprovalState, pending.DecidedBy, pending.DecidedAt = domain.ApprovalPending, nil, nil
	if _, err := authorityStore.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save seed: %v", err)
	}
	if _, err := authorityStore.CommitProposal(ctx, seed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	operation := domain.Operation{
		ID: "semantic-operation", Kind: domain.OperationWriteChapter, Target: target,
		State: domain.OperationQueued, RunID: createRuntimeTestRun(t, ctx, authorityStore, target.ID, now),
		Snapshot: domain.ExecutionSnapshot{
			ExecutionProfileDigest: "profile", BaseRevision: 1,
			CoreProtocolVersion: "core-v1", WorkerProfileVersion: "writer.compose@1",
			ToolSchemaDigest: "tools", PromptDigest: "prompt", ModelConfigDigest: "model",
			ApprovalPolicy: domain.ApprovalAuto, ApprovalPolicyDigest: "auto",
		},
		Input: json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`), CreatedAt: now, UpdatedAt: now,
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
	report, err := runtime.AnalyzeSemanticCompliance(ctx, operation, domain.Proposal{
		Patches: []domain.Patch{{
			Document:  domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-1"},
			Operation: domain.PatchPut, Content: json.RawMessage(`{"candidate":"正文"}`),
		}},
	}, []domain.OwnershipRule{{
		Target: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-bottom-line"}, Control: domain.ControlLocked,
	}})
	if err != nil {
		t.Fatalf("analyze semantic compliance: %v", err)
	}
	if report.Status != domain.SemanticCompliancePass || !model.usedJSONSchema {
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
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "semantic-book"}
	seed := approvedRuntimeProposal("seed-semantic", target, 0, now, domain.Patch{
		Document: domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, Operation: domain.PatchPut,
		Content: json.RawMessage(`{"premise":"旧事实已经进入正文"}`),
	})
	pending := seed
	pending.ApprovalState, pending.DecidedBy, pending.DecidedAt = domain.ApprovalPending, nil, nil
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
	proposal := domain.Proposal{
		ID: "semantic-change", Target: target, BaseRevision: 1,
		Author: domain.Author{Kind: domain.AuthorUser, ID: "user-1"}, Reason: "修改前提",
		Patches: []domain.Patch{{
			Document: domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, Operation: domain.PatchPut,
			Content: json.RawMessage(`{"premise":"新的前提"}`),
		}}, ApprovalState: domain.ApprovalPending, CreatedAt: now.Add(time.Minute),
	}
	reportJSON, err := runtime.Analyze(ctx, proposal, change.StructuralImpact{
		Direct: []domain.DocumentRef{{Kind: domain.DocumentIntent, ID: "root"}},
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
	operation := domain.Operation{
		ID: "rewrite-affected", Kind: domain.OperationRewriteAffected,
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}, State: domain.OperationQueued,
		RunID: createRuntimeTestRun(t, ctx, authorityStore, "book-1", now),
		Snapshot: domain.ExecutionSnapshot{
			ExecutionProfileDigest: "profile", BaseRevision: 3, CoreProtocolVersion: "core-v1",
			WorkerProfileVersion: "writer.revise_affected@1", ToolSchemaDigest: "tools", PromptDigest: "prompt",
			ModelConfigDigest: "model", ApprovalPolicy: domain.ApprovalManual, ApprovalPolicyDigest: "manual",
		},
		Input:     json.RawMessage(`{"chapter_ids":["chapter-1","chapter-2"],"base_revision":3,"resolution_proposal_id":"change-1","reason":"同步旧事实"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimOperationForModel(ctx, operation.ID, "worker-1", "model", time.Minute, now)
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	chapters := []domain.ManuscriptChapter{
		{ID: "chapter-1", PlanNodeID: "plan-1", Number: 1, Title: "第一章", Blocks: []domain.ManuscriptBlock{{ID: "block-1", Text: "新正文一"}}},
		{ID: "chapter-2", PlanNodeID: "plan-2", Number: 2, Title: "第二章", Blocks: []domain.ManuscriptBlock{{ID: "block-2", Text: "新正文二"}}},
	}
	var patches []domain.Patch
	keys := make([]string, 0, len(chapters))
	for index, chapter := range chapters {
		content, err := json.Marshal(chapter)
		if err != nil {
			t.Fatal(err)
		}
		key := "chapter/" + chapter.ID
		keys = append(keys, key)
		if _, err := authorityStore.PutWorkspaceArtifact(ctx, domain.WorkspaceArtifact{
			OperationID: operation.ID, Key: key, MediaType: workspace.ChapterMediaType,
			Content: content, UpdatedAt: now,
		}, 0, operation.Attempt); err != nil {
			t.Fatalf("put workspace chapter: %v", err)
		}
		patches = append(patches,
			domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}, Operation: domain.PatchPut, Content: content},
			domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: fmt.Sprintf("fact-%d", index+1)}, Operation: domain.PatchPut,
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
	operation := domain.Operation{
		ID: "write-counted", Kind: domain.OperationWriteChapter,
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}, State: domain.OperationQueued,
		RunID: createRuntimeTestRun(t, ctx, authorityStore, "book-1", now),
		Snapshot: domain.ExecutionSnapshot{
			ExecutionProfileDigest: "profile", BaseRevision: 2, CoreProtocolVersion: "core-v1",
			WorkerProfileVersion: "writer.compose@1", ToolSchemaDigest: "tools", PromptDigest: "prompt",
			ModelConfigDigest: "model", ApprovalPolicy: domain.ApprovalAuto, ApprovalPolicyDigest: "auto",
		},
		Input:     json.RawMessage(`{"chapter_plan_id":"plan-1","directives":[{"id":"length","scope":"project","text":"每章十字左右","constraints":{"target_words":10},"status":"active"}]}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimOperationForModel(ctx, operation.ID, "worker-1", "model", time.Minute, now)
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	runtime := NewRuntime(nil, "model", authorityStore)
	submit := func(text string, version int64) error {
		chapter := domain.ManuscriptChapter{
			ID: "chapter-1", PlanNodeID: "plan-1", Number: 1, Title: "标题不计入字数",
			Blocks: []domain.ManuscriptBlock{{ID: "block-1", Text: text}},
		}
		content, _ := json.Marshal(chapter)
		if _, err := authorityStore.PutWorkspaceArtifact(ctx, domain.WorkspaceArtifact{
			OperationID: operation.ID, Key: "chapter/chapter-1", MediaType: workspace.ChapterMediaType,
			Content: content, UpdatedAt: now,
		}, version, operation.Attempt); err != nil {
			t.Fatalf("put workspace chapter: %v", err)
		}
		patches := []domain.Patch{
			{Document: domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}, Operation: domain.PatchPut, Content: content},
			{Document: domain.DocumentRef{Kind: domain.DocumentCanon, ID: "fact-1"}, Operation: domain.PatchPut,
				Content: json.RawMessage(`{"id":"fact-1","kind":"event","subject_id":"hero","predicate":"event.done","new_value":true,"source_chapter_id":"chapter-1"}`)},
		}
		return runtime.validateSubmissionArtifact(ctx, operation, []string{"chapter/chapter-1"}, "", patches)
	}
	if err := submit("太短", 0); !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "9-11") {
		t.Fatalf("short chapter err = %v", err)
	}
	if err := submit(strings.Repeat("长", 12), 1); !errors.Is(err, domain.ErrInvalid) {
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
	snapshot := domain.ExecutionSnapshot{
		ExecutionProfileDigest: "plan-profile", BaseRevision: 1,
		CoreProtocolVersion: "core-v1", WorkerProfileVersion: worker.ID + "@" + worker.Version,
		ToolSchemaDigest: "tools", PromptDigest: "prompt", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalAuto, ApprovalPolicyDigest: "auto",
	}
	operation := domain.Operation{
		ID: "plan-1", Kind: domain.OperationDevelopPlan,
		Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-plan"},
		State:  domain.OperationQueued,
		RunID:  createRuntimeTestRun(t, ctx, authorityStore, "book-plan", now), Snapshot: snapshot,
		Input:     json.RawMessage(`{"intent":"规划开篇","requested_chapters":1}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	patchFor := func(node domain.PlanNode) domain.Patch {
		content, err := json.Marshal(node)
		if err != nil {
			t.Fatalf("marshal plan node: %v", err)
		}
		return domain.Patch{
			Document:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: node.ID},
			Operation: domain.PatchPut, Content: content,
		}
	}
	volume := domain.PlanNode{ID: "volume-1", Kind: domain.PlanVolume, Order: 1, Title: "第一卷", Summary: "开端"}
	arc := domain.PlanNode{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Order: 1, Title: "第一幕", Summary: "启程"}
	chapterOne := domain.PlanNode{ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "出发"}
	chapterTwo := domain.PlanNode{ID: "chapter-plan-2", Kind: domain.PlanChapter, ParentID: "arc-1", Order: 2, Title: "第二章", Summary: "多余"}
	overshoot, _ := json.Marshal(map[string]any{
		"reason":  "多规划一章",
		"patches": []domain.Patch{patchFor(volume), patchFor(arc), patchFor(chapterOne), patchFor(chapterTwo)},
	})
	exact, _ := json.Marshal(map[string]any{
		"reason":  "按请求规划一章",
		"patches": []domain.Patch{patchFor(volume), patchFor(arc), patchFor(chapterOne)},
	})
	model := &planRuntimeModel{steps: []json.RawMessage{overshoot, exact}, now: now.Add(2 * time.Second)}
	runtime := NewRuntime(model, "model", authorityStore)
	runtime.now = func() time.Time { return now.Add(3 * time.Second) }
	outcome, err := runtime.Execute(ctx, operation, prompt.Compiled{
		StablePrefix: "stable", DynamicTail: "plan task", Tools: worker.Tools,
		ProfileDigest: snapshot.ExecutionProfileDigest, Snapshot: snapshot,
	})
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
