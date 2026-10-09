package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
	"github.com/voocel/ainovel-cli/internal/infra/store"
	"github.com/voocel/ainovel-cli/internal/infra/workspace"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/catalog"
	"github.com/voocel/litellm/litellmtest"
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
	seedRuntimeProject(t, ctx, authorityStore, target, now)

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
	runtime := NewRuntime(authorityStore)
	runtime.now = func() time.Time { return now.Add(2 * time.Second) }

	read := testTool(t, runtime, operation, "writer.compose@1", prompt.ToolAuthorityRead, nil)
	// 回查按故事语言定位（D66）：章号、名称，结果里没有文档 ID 与存储信封。
	chapterView, err := read(ctx, json.RawMessage(`{"chapter":1}`))
	if err != nil || !strings.Contains(string(chapterView), `"chapter":1`) || strings.Contains(string(chapterView), "chapter-plan-1") {
		t.Fatalf("chapter read = %s, %v", chapterView, err)
	}
	entityView, err := read(ctx, json.RawMessage(`{"entity":"主角"}`))
	if err != nil || !strings.Contains(string(entityView), `"predicate":"state.origin"`) || strings.Contains(string(entityView), `"hero"`) {
		t.Fatalf("entity read = %s, %v", entityView, err)
	}
	if _, err := read(ctx, json.RawMessage(`{"chapter":2}`)); !errors.Is(err, domainmodel.ErrNotFound) || !strings.Contains(err.Error(), "第 2 章") {
		t.Fatalf("missing chapter err = %v", err)
	}
	for _, raw := range []string{`{"chapter":1,"entity":"主角"}`, `{"kind":"intent","id":"root","revision":1}`} {
		if _, err := read(ctx, json.RawMessage(raw)); err == nil {
			t.Fatalf("read %s accepted", raw)
		}
	}

	// 章的身份由宿主按任务确定：模型只写标题与段落，回执只有键与版本。
	putChapter := testTool(t, runtime, operation, "writer.compose@1", prompt.ToolWorkspacePutChapter, nil)
	receipt, err := putChapter(ctx, json.RawMessage(`{"key":"draft","chapter":{"title":"山门","blocks":[{"id":"p-1","text":"他抵达山门……"}]}}`))
	if err != nil || string(receipt) != `{"key":"draft","version":1}` {
		t.Fatalf("put chapter = %s, %v", receipt, err)
	}
	stored, err := authorityStore.GetWorkspaceArtifact(ctx, operation.ID, "draft")
	if err != nil {
		t.Fatal(err)
	}
	var chapter domainmodel.ManuscriptChapter
	if err := json.Unmarshal(stored.Content, &chapter); err != nil || chapter.ID != "chapter-plan-1" || chapter.PlanNodeID != "chapter-plan-1" ||
		chapter.Number != 1 || chapter.Author != domainmodel.AuthorAI {
		t.Fatalf("host-filled chapter = %+v, %v", chapter, err)
	}

	var submitted domainmodel.Proposal
	submitTool := testTool(t, runtime, operation, "writer.compose@1", prompt.ToolProposalSubmit, func(proposal domainmodel.Proposal) (domainmodel.Proposal, error) {
		submitted = proposal
		return proposal, nil
	})
	submit := func(version int, facts ...map[string]any) (json.RawMessage, error) {
		submitted = domainmodel.Proposal{}
		raw, err := json.Marshal(map[string]any{"reason": "提交本章", "workspace_key": "draft", "workspace_version": version, "facts": facts})
		if err != nil {
			t.Fatal(err)
		}
		return submitTool(ctx, raw)
	}
	// 同一主体+谓词就是同一事实（D61）：宿主按自然键并入已有节点并从冻结基线补 old_value。
	if _, err := submit(1, map[string]any{"subject": "主角", "predicate": "state.origin", "value": "孤儿"}); err != nil {
		t.Fatalf("keyed fact must merge into the existing node: %v", err)
	}
	var merged domainmodel.CanonFact
	if err := json.Unmarshal(submitted.Patches[1].Content, &merged); err != nil || submitted.Patches[1].Document.ID != "hero-origin" ||
		string(merged.PreviousValue) != `"农家子"` || merged.SourceChapterID != "chapter-plan-1" {
		t.Fatalf("merged patch = %+v (%v)", submitted.Patches[1], err)
	}
	result, err := submit(1, map[string]any{"subject": "主角", "predicate": "event.chapter_outcome", "value": "抵达山门"})
	if err != nil || string(result) != `{"status":"submitted"}` {
		t.Fatalf("submit = %s, %v", result, err)
	}
	if submitted.OperationID != operation.ID || submitted.BaseRevision != 1 || len(submitted.Patches) != 2 {
		t.Fatalf("submitted proposal = %#v", submitted)
	}
	// 故事依赖由宿主按事实变化写入（D40）。
	var written domainmodel.ManuscriptChapter
	if err := json.Unmarshal(submitted.Patches[0].Content, &written); err != nil || len(written.DependsOn) != 1 || written.DependsOn[0].ID != "hero" {
		t.Fatalf("manuscript dependencies = %+v, %v", written.DependsOn, err)
	}
	if _, err := authorityStore.GetProposal(ctx, submitted.ID); !errors.Is(err, domainmodel.ErrNotFound) {
		t.Fatalf("capability wrote authority proposal directly: %v", err)
	}
	for _, tc := range []struct {
		name    string
		version int
		facts   []map[string]any
		want    string
	}{
		{"stale version", 2, []map[string]any{{"subject": "主角", "predicate": "event.x", "value": "x"}}, "version mismatch"},
		{"invalid version", 0, []map[string]any{{"subject": "主角", "predicate": "event.x", "value": "x"}}, "positive version"},
		{"unknown subject", 1, []map[string]any{{"subject": "路人", "predicate": "event.x", "value": "x"}}, "实体「路人」不存在"},
		{"canon still required", 1, nil, "Canon Delta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := submit(tc.version, tc.facts...)
			if err == nil || !strings.Contains(err.Error(), tc.want) || submitted.ID != "" {
				t.Fatalf("error=%v submitted=%s", err, submitted.ID)
			}
			// 回给模型的错误只有故事标签。
			if strings.Contains(err.Error(), `"chapter-plan-1"`) || strings.Contains(err.Error(), `"hero"`) {
				t.Fatalf("internal id leaked: %v", err)
			}
		})
	}
	legacy, _ := json.Marshal(map[string]any{"reason": "旧协议", "workspace_key": "draft", "workspace_version": 1, "patches": []any{}})
	if _, err := submitTool(ctx, legacy); err == nil {
		t.Fatal("document patches accepted from the model")
	}

	workspaceRead := testTool(t, runtime, operation, "writer.compose@1", prompt.ToolWorkspaceRead, nil)
	readBack, err := workspaceRead(ctx, json.RawMessage(`{"key":"draft"}`))
	if err != nil {
		t.Fatal(err)
	}
	var readable struct {
		Content narrative.ChapterDraft `json:"content"`
		Version int64                  `json:"version"`
	}
	if err := json.Unmarshal(readBack, &readable); err != nil || len(readable.Content.Blocks) != 1 || readable.Content.Title != "山门" ||
		readable.Version != 1 || strings.Contains(string(readBack), "plan_node_id") {
		t.Fatalf("workspace must read back the draft shape: %s, %v", readBack, err)
	}
	if _, err := workspaceRead(ctx, json.RawMessage(`{"key":"ch-1"}`)); !errors.Is(err, domainmodel.ErrNotFound) || !strings.Contains(err.Error(), `"ch-1"`) {
		t.Fatalf("missing workspace err = %v, want the key", err)
	}
}

// testTool 构造单个工具，故事按任务冻结基线加载。
func testTool(t *testing.T, runtime *Runtime, operation domainmodel.Operation, worker, name string, submit func(domainmodel.Proposal) (domainmodel.Proposal, error)) toolFunc {
	t.Helper()
	execute, err := runtime.toolExecutor(operation, worker, name, runtime.storyLoader(operation), submit, nil)
	if err != nil {
		t.Fatalf("build %s: %v", name, err)
	}
	return execute
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
	target := domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-recovery"}
	seedRuntimeProject(t, ctx, authorityStore, target, now)
	operation := domainmodel.Operation{
		ID: "recover-draft", Kind: domainmodel.OperationWriteChapter, Target: target,
		State: domainmodel.OperationQueued,
		RunID: createRuntimeTestRun(t, ctx, authorityStore, "book-recovery", now),
		Snapshot: runtimeSnapshot(task, 1, domainmodel.ApprovalManual,
			seedRuntimeProfile(t, ctx, authorityStore, "book-recovery", worker, task, 1)),
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
		ctx, first.ID, "chapter/chapter-1", chapter, nil, first.Attempt, now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatalf("write recoverable draft: %v", err)
	}
	previous := agentcore.UserText("这是崩溃前已经提交到会话日志的原始任务")
	previous.Time = now.Add(3 * time.Second)
	payload, _ := json.Marshal(previous)
	if _, err := authorityStore.AppendOperationEvent(ctx, domainmodel.OperationEvent{
		OperationID: first.ID, StepID: "agent.message", Attempt: first.Attempt,
		IdempotencyKey: "agent-message:1:1", Kind: "agent.message_committed",
		Payload: payload, CreatedAt: previous.Time,
	}); err != nil {
		t.Fatalf("persist previous message: %v", err)
	}
	previousReply := agentcore.Message{
		Role: litellm.RoleAssistant, Blocks: []litellm.Block{litellm.Text("草稿已保存")},
		Usage: &agentcore.Usage{Usage: litellm.Usage{InputTokens: 100, OutputTokens: 20}}, Time: previous.Time,
	}
	payload, _ = json.Marshal(previousReply)
	if _, err := authorityStore.AppendOperationEvent(ctx, domainmodel.OperationEvent{
		OperationID: first.ID, StepID: "agent.message", Attempt: first.Attempt,
		IdempotencyKey: "agent-message:1:2", Kind: "agent.message_committed",
		Payload: payload, CreatedAt: previousReply.Time,
	}); err != nil {
		t.Fatal(err)
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
	proposalArgs, _ := json.Marshal(map[string]any{
		"reason": "继续并提交已有工作稿", "workspace_key": artifact.Key, "workspace_version": artifact.Version,
		"facts": []map[string]any{{"subject": "主角", "predicate": "event.chapter_outcome", "value": "完成已有工作稿"}},
	})
	model := recoveryScript(proposalArgs)
	runtime := boundRuntime(authorityStore, testChat(model))
	runtime.now = func() time.Time { return now.Add(8 * time.Second) }
	outcome, err := runtime.Execute(ctx, second)
	if err != nil {
		t.Fatalf("resume runtime: %v", err)
	}
	if outcome.Proposal == nil || len(outcome.Proposal.Patches) != 2 || outcome.Proposal.Patches[0].Document.ID != chapter.ID {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 2 {
		t.Fatalf("successful submission made extra model calls: %d", len(model.Requests()))
	}
	firstRequest := model.Requests()[0].Messages
	foundPrevious, foundRecovery, foundFailure := false, false, false
	for _, message := range firstRequest {
		text := requestText(message)
		foundPrevious = foundPrevious || text == previous.Text()
		foundRecovery = foundRecovery || strings.Contains(text, "先用 workspace_list")
		foundFailure = foundFailure || strings.Contains(text, "上一次尝试失败原因：simulated process failure")
	}
	if !foundPrevious || !foundRecovery || !foundFailure {
		t.Fatalf("restored request missing history, recovery instruction or failure reason: %#v", firstRequest)
	}
	events, err := authorityStore.ListOperationEvents(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind != "agent.run_ended" || event.Attempt != second.Attempt {
			continue
		}
		var end struct{ Usage agentcore.Usage }
		if err := json.Unmarshal(event.Payload, &end); err != nil {
			t.Fatal(err)
		}
		// 当前测试模型没有计费用量，恢复的历史消息不能再计费。
		if end.Usage.InputTokens != 0 || end.Usage.OutputTokens != 0 {
			t.Fatalf("restored usage counted again: %+v", end.Usage)
		}
		return
	}
	t.Fatal("missing resumed run summary")
}

type eventStore struct {
	runtimeStore
	events []domainmodel.OperationEvent
}

func (s eventStore) ListOperationEvents(context.Context, string) ([]domainmodel.OperationEvent, error) {
	return s.events, nil
}

// 旧版本落盘的消息形如 {"role":…,"content":[…]}：解成新消息没有内容，不能当空消息带进会话。
func TestRestoreRejectsMessagesWithoutContent(t *testing.T) {
	old := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"原始任务"}],"timestamp":"2026-08-18T13:00:00Z"}`)
	runtime := &Runtime{store: eventStore{events: []domainmodel.OperationEvent{
		{Sequence: 7, Attempt: 1, Kind: "agent.message_committed", Payload: old},
	}}}
	_, _, err := runtime.restoreMessages(context.Background(), domainmodel.Operation{ID: "op", Attempt: 2})
	if !errors.Is(err, domainmodel.ErrInvalid) || !strings.Contains(err.Error(), "event 7") {
		t.Fatalf("restore err = %v, want the stale message rejected", err)
	}
}

// 连接中途断开的回合以 StopError 记下，可以一个字都没有：续跑照常恢复，不当成旧版本消息。
func TestRestoreKeepsFailedTurnsWithoutContent(t *testing.T) {
	task := json.RawMessage(`{"role":"user","blocks":[{"type":"text","text":"写第 1 章"}]}`)
	failed := json.RawMessage(`{"role":"assistant","blocks":null,"stop":"error","provider":"openai","model":"m"}`)
	runtime := &Runtime{store: eventStore{events: []domainmodel.OperationEvent{
		{Sequence: 3, Attempt: 1, Kind: "agent.message_committed", Payload: task},
		{Sequence: 7, Attempt: 1, Kind: "agent.message_committed", Payload: failed},
	}}}
	messages, _, err := runtime.restoreMessages(context.Background(), domainmodel.Operation{ID: "op", Attempt: 2})
	if err != nil || len(messages) != 2 || messages[1].Stop != agentcore.StopError {
		t.Fatalf("restore = %d messages, err = %v", len(messages), err)
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
	task := json.RawMessage(`{"chapter_ids":["chapter-1","chapter-2"],"requirements":[{"id":"directive:hook","text":"每章结尾留钩子"},{"id":"intent:forbidden:0","text":"不写感情线","settle":true}],"basis":{"documents":[{"ref":{"kind":"manuscript","id":"chapter-1"},"revision":1}]}}`)
	target := domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-review"}
	seedWrittenChapters(t, ctx, authorityStore, target, now, 2)
	operation := domainmodel.Operation{
		ID: "review-1", Kind: domainmodel.OperationReviewRange,
		Target: target,
		State:  domainmodel.OperationQueued,
		RunID:  createRuntimeTestRun(t, ctx, authorityStore, "book-review", now),
		Snapshot: runtimeSnapshot(task, 1, domainmodel.ApprovalAuto,
			seedRuntimeProfile(t, ctx, authorityStore, "book-review", worker, task, 1)),
		Input: task, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateOperation(ctx, operation); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = authorityStore.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	// 递进式纠错：漏掉要求核验被拒 → 必须下结论的项给 pending 被拒 → 完整裁定通过。
	// 章节范围与发现由宿主从任务输入和审阅记录填入（D60），模型不再复述，
	// "漏审范围""与记录不一致"这两类错误在结构上已不可能发生。
	findings := []map[string]any{{"chapter": 2, "severity": "note", "note": "第二章节奏偏慢"}}
	review, _ := json.Marshal(map[string]any{
		"key": "review/findings", "findings": findings,
	})
	noChecks, _ := json.Marshal(map[string]any{"status": "pass", "review_key": "review/findings"})
	unsettled, _ := json.Marshal(map[string]any{"status": "pass", "review_key": "review/findings", "checks": []map[string]any{
		{"id": "directive:hook", "status": "pending"}, {"id": "intent:forbidden:0", "status": "pending"},
	}})
	complete, _ := json.Marshal(map[string]any{"status": "pass", "review_key": "review/findings", "checks": []map[string]any{
		{"id": "directive:hook", "status": "pending"}, {"id": "intent:forbidden:0", "status": "satisfied"},
	}})
	// 每次回应计 10 入 3 出，按单价折合 0.01。
	replies := []litellmtest.Reply{callReply("put-review", "workspace_put_review", review)}
	for i, step := range []json.RawMessage{noChecks, unsettled, complete} {
		replies = append(replies, callReply(fmt.Sprintf("verdict-%d", i+1), "verdict_submit", step))
	}
	for i := range replies {
		replies[i].Usage = litellm.Usage{InputTokens: 10, OutputTokens: 3}
	}
	model := litellmtest.New(replies...)
	chat := testChat(model)
	chat.Request.Model = "verdict-model"
	chat.Pricing = &catalog.Pricing{Rates: catalog.Rates{Input: 0.0007, Output: 0.001}}
	runtime := boundRuntime(authorityStore, chat)
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
	if verdict.Status != domainmodel.ReviewPass || verdict.Revision != 1 ||
		len(verdict.ChapterIDs) != 2 || len(verdict.Findings) != 1 || verdict.Findings[0].ChapterID != "chapter-2" ||
		verdict.ReviewKey != "review/findings" || len(verdict.Checks) != 2 ||
		verdict.CheckStatus("intent:forbidden:0") != domainmodel.CheckSatisfied || verdict.CheckStatus("directive:hook") != domainmodel.CheckPending {
		t.Fatalf("verdict = %#v", verdict)
	}
	artifact, err := authorityStore.GetWorkspaceArtifact(ctx, operation.ID, verdict.ReviewKey)
	if err != nil || artifact.MediaType != domainmodel.ReviewArtifactMediaType {
		t.Fatalf("review artifact = %#v, %v", artifact, err)
	}
	// 活动通道（页面设计 §4）：执行过程以带归属的事件发布——两次被拒的提交
	// 呈现为出错条目（模型自纠可见），最终提交成功收尾。
	feed, ok := hub.Snapshot("book-review")
	if !ok || feed.RunID != operation.RunID || feed.OperationID != operation.ID {
		t.Fatalf("activity feed identity = %#v ok=%v", feed, ok)
	}
	if len(feed.Tasks) != 1 || !feed.Tasks[0].Done || feed.Tasks[0].Err != "" || feed.Tasks[0].OperationID != operation.ID {
		t.Fatalf("successful execution did not close its task: %+v", feed.Tasks)
	}
	if feed.Tasks[0].Role != "editor" {
		t.Fatalf("the task must carry the role of the worker that ran it: %q", feed.Tasks[0].Role)
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
	if rejected != 2 || accepted != 1 {
		t.Fatalf("verdict_submit activity: rejected=%d accepted=%d entries=%#v", rejected, accepted, feed.Entries)
	}
	// 成功提交直接正常收尾，不再请求第五次模型回应。
	if len(model.Requests()) != 4 || feed.Usage.Input != 40 || feed.Usage.Output != 12 || feed.Usage.Cost < 0.039 || feed.Usage.Cost > 0.041 {
		t.Fatalf("usage totals = %#v", feed.Usage)
	}
	// 用量按服务端上报的模型归档（右栏按模型分列的依据）。
	if len(feed.Models) != 1 || feed.Models[0].Model != "verdict-model" || feed.Models[0].Provider != "test" || feed.Models[0].Messages != 4 || feed.ActiveModel != "verdict-model" {
		t.Fatalf("per-model usage = %#v active=%q", feed.Models, feed.ActiveModel)
	}
	if task := feed.Tasks[0]; task.Turns != 4 || task.Calls != 4 {
		t.Fatalf("task counters = %+v", task)
	}
}

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
			ReviewCadence: domainmodel.ReviewPerPlanWindow, AutoRepairBudget: 3,
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

// recoveryScript 先列工作区，再按 proposalArgs 提交，提交成功即收尾：多一次调用就越出脚本。
func recoveryScript(proposalArgs json.RawMessage) *litellmtest.Provider {
	return litellmtest.New(
		callReply("list-workspace", "workspace_list", json.RawMessage(`{}`)),
		callReply("submit-existing", "proposal_submit", proposalArgs),
	)
}

// callReply 是只调用一个工具的回应。
func callReply(id, name string, args json.RawMessage) litellmtest.Reply {
	return litellmtest.Respond(litellm.ToolUseBlock{ID: id, Name: name, Arguments: string(args)})
}

// requestText 是请求里一条消息的文本。
func requestText(message litellm.Message) string {
	var text strings.Builder
	for _, block := range message.Blocks {
		if b, ok := block.(litellm.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return text.String()
}

// delta 把一个流式增量包成 agent 事件。
func delta(event litellm.Event) agentcore.Event { return agentcore.MessageDelta{Event: event} }

// TestPublishActivityTranslatesStreamingToolDeltas 守卫真实事件时序（agentcore
// 在整条回复完成后才执行工具）：参数流阶段就要有带工具名的进行中条目
// 与字节进度，ToolStart 不重复建条、ToolEnd 收尾。
func TestPublishActivityTranslatesStreamingToolDeltas(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	hub := activity.NewHub()
	runtime := &Runtime{now: func() time.Time { return now }, activity: hub}
	operation := domainmodel.Operation{
		ID: "op-1", RunID: "run-1",
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-live"},
	}
	stream := newLiveStream(nil)
	call := agentcore.ToolCall{ID: "call-1", Name: "workspace_put_chapter"}
	runtime.publishActivity(operation, delta(litellm.BlockStart{Index: 0, Block: litellm.ToolUseBlock{ID: call.ID, Name: call.Name}}), stream)
	runtime.publishActivity(operation, delta(litellm.ToolUseDelta{Index: 0, Arguments: `{"chapter":{"id":"c1","blocks":[{"id":"p1","text":"正文`}), stream)
	snapshot, ok := hub.Snapshot("book-live")
	if !ok || len(snapshot.Entries) != 1 || snapshot.Entries[0].Tool != "workspace_put_chapter" ||
		snapshot.Entries[0].Done || snapshot.Entries[0].Bytes == 0 {
		t.Fatalf("streaming snapshot = %#v ok=%v", snapshot, ok)
	}
	// 逐字预览：参数流里的正文被解出并归属本次调用（页面设计 §3）。
	if string(snapshot.Prose) != "正文" || snapshot.ProseCallID != "call-1" {
		t.Fatalf("prose = %q callID = %q", snapshot.Prose, snapshot.ProseCallID)
	}
	runtime.publishActivity(operation, delta(litellm.ToolUseDelta{Index: 0, Arguments: `，一句接一句"}]}}`}), stream)
	runtime.publishActivity(operation, agentcore.ToolStart{Call: call}, stream)
	runtime.publishActivity(operation, agentcore.ToolEnd{Call: call, Result: agentcore.TextResult("{}")}, stream)
	snapshot, _ = hub.Snapshot("book-live")
	if len(snapshot.Entries) != 1 || !snapshot.Entries[0].Done {
		t.Fatalf("after exec = %#v", snapshot.Entries)
	}
	if string(snapshot.Prose) != `正文，一句接一句` {
		t.Fatalf("prose after close = %q", snapshot.Prose)
	}
}

// TestPublishActivityAttributesDeltaByBlock 守卫交错归属：并行调用并存时按块序号
// 精确定位所属调用，不取"最后一个"——正文增量归属正文调用，哪怕回复里后面又开了
// 别的调用。
func TestPublishActivityAttributesDeltaByBlock(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	hub := activity.NewHub()
	runtime := &Runtime{now: func() time.Time { return now }, activity: hub}
	operation := domainmodel.Operation{
		ID: "op-2", RunID: "run-2",
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-mix"},
	}
	stream := newLiveStream(nil)
	runtime.publishActivity(operation, delta(litellm.BlockStart{Index: 0, Block: litellm.ToolUseBlock{ID: "call-a", Name: "workspace_put_chapter"}}), stream)
	runtime.publishActivity(operation, delta(litellm.BlockStart{Index: 1, Block: litellm.ToolUseBlock{ID: "call-b", Name: "authority_read"}}), stream)
	runtime.publishActivity(operation, delta(litellm.ToolUseDelta{Index: 0, Arguments: `{"chapter":{"blocks":[{"text":"甲稿正文`}), stream)
	snapshot, ok := hub.Snapshot("book-mix")
	if !ok || string(snapshot.Prose) != "甲稿正文" || snapshot.ProseCallID != "call-a" {
		t.Fatalf("prose = %q callID = %q ok=%v", snapshot.Prose, snapshot.ProseCallID, ok)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Tool != "workspace_put_chapter" ||
		snapshot.Entries[0].CallID != "call-a" {
		t.Fatalf("entries = %#v", snapshot.Entries)
	}
}

// TestPublishActivityThinkingTextAndToolError 守卫两处翻译语义：思考增量作为
// 推理文本入快照；出错的工具结果原文进条目。
func TestPublishActivityThinkingTextAndToolError(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	hub := activity.NewHub()
	runtime := &Runtime{now: func() time.Time { return now }, activity: hub}
	operation := domainmodel.Operation{
		ID: "op-9", RunID: "run-9",
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-9"},
	}
	stream := newLiveStream(nil)
	runtime.publishActivity(operation, delta(litellm.ReasoningDelta{Text: "回顾伏笔"}), stream)
	runtime.publishActivity(operation, agentcore.ToolEnd{
		Call: agentcore.ToolCall{ID: "c1", Name: "workspace_put_chapter"}, Result: agentcore.ErrorResult(`章节段落 "p-9" 不存在`),
	}, stream)
	snapshot, ok := hub.Snapshot("book-9")
	if !ok || snapshot.ThinkingNote != "回顾伏笔" {
		t.Fatalf("thinking note = %q ok=%v", snapshot.ThinkingNote, ok)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Err != `章节段落 "p-9" 不存在` {
		t.Fatalf("entries = %#v", snapshot.Entries)
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
	reply := litellmtest.Respond(litellm.ReasoningBlock{Text: "底线是不伤无辜，候选正文没有越界"}, litellm.Text(`{"status":"pass","findings":[]}`))
	reply.Usage = litellm.Usage{InputTokens: 10, OutputTokens: 3}
	model := litellmtest.New(reply)
	runtime := boundRuntime(authorityStore, testChat(model))
	runtime.now = func() time.Time { return now.Add(2 * time.Second) }
	hub := activity.NewHub()
	runtime.SetActivitySink(hub)
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
	if report.Status != domainmodel.SemanticCompliancePass || !usedJSONSchema(model) {
		t.Fatalf("report = %#v, json schema = %v", report, usedJSONSchema(model))
	}
	// 收尾阶段的模型判断同样进活动流：画面上是"核对语义合规"这一步与它的思考，
	// 结构化输出只报接收进度，用量记入本轮。
	feed, ok := hub.Snapshot(operation.Target.ID)
	if !ok || len(feed.Entries) != 1 || feed.Entries[0].Tool != "semantic_compliance" || !feed.Entries[0].Done ||
		feed.Entries[0].Err != "" || feed.Entries[0].Bytes != len(`{"status":"pass","findings":[]}`) {
		t.Fatalf("compliance activity = %#v ok=%v", feed.Entries, ok)
	}
	if len(feed.Output) != 1 || feed.Output[0].Kind != activity.Thinking || string(feed.Output[0].Text) != "底线是不伤无辜，候选正文没有越界" {
		t.Fatalf("compliance thinking = %#v", feed.Output)
	}
	if feed.Usage.Input != 10 || feed.Usage.Output != 3 || feed.Waiting || len(feed.Models) != 1 || feed.Models[0].Provider != "test" {
		t.Fatalf("compliance usage = %#v models = %#v waiting = %v", feed.Usage, feed.Models, feed.Waiting)
	}
	events, err := authorityStore.ListOperationEvents(ctx, operation.ID)
	if err != nil {
		t.Fatalf("list operation events: %v", err)
	}
	if len(events) != 2 || events[1].Kind != "semantic.compliance_checked" {
		t.Fatalf("events = %#v", events)
	}
	// usage 经 llm.Structured 回传后落审计事件，不能在收口时丢掉。
	var checked struct {
		Usage litellm.Usage `json:"usage"`
	}
	if err := json.Unmarshal(events[1].Payload, &checked); err != nil || checked.Usage.InputTokens != 10 || checked.Usage.OutputTokens != 3 {
		t.Fatalf("usage not recorded in compliance event: %s", events[1].Payload)
	}
}

// usedJSONSchema 报告模型的首次调用是否要了 JSON Schema 约束的输出。
func usedJSONSchema(model *litellmtest.Provider) bool {
	requests := model.Requests()
	return len(requests) > 0 && requests[0].ResponseFormat != nil && requests[0].ResponseFormat.Type == litellm.ResponseFormatJSONSchema
}

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
	model := litellmtest.New(litellmtest.Text(`{
		"status":"conflict",
		"findings":[{"document":{"kind":"intent","id":"root"},"explanation":"新前提与旧前提冲突"}],
		"options":[
			{"strategy":"rewrite_affected","chapter_ids":["chapter-1"],"explanation":"重写受影响章节"},
			{"strategy":"reinterpret_future","explanation":"在后文重新解释"},
			{"strategy":"abandon","explanation":"放弃变更"}
		]}`))
	runtime := boundRuntime(authorityStore, testChat(model))
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
	var report domainmodel.SemanticImpactReport
	if err := json.Unmarshal(reportJSON, &report); err != nil || report.Status != domainmodel.SemanticImpactConflict || !usedJSONSchema(model) {
		t.Fatalf("report = %#v, schema = %v, error = %v", report, usedJSONSchema(model), err)
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
	operation, err = authorityStore.ClaimOperationForExecutor(ctx, operation.ID, "worker-1", prompt.ExecutorIdentity, time.Minute, now)
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	chapters := []domainmodel.ManuscriptChapter{
		{ID: "chapter-1", PlanNodeID: "plan-1", Number: 1, Title: "第一章", Author: domainmodel.AuthorAI, Blocks: []domainmodel.ManuscriptBlock{{ID: "block-1", Text: "新正文一"}}},
		{ID: "chapter-2", PlanNodeID: "plan-2", Number: 2, Title: "第二章", Author: domainmodel.AuthorAI, Blocks: []domainmodel.ManuscriptBlock{{ID: "block-2", Text: "新正文二"}}},
	}
	versions := make(map[string]int64, len(chapters))
	for _, chapter := range chapters {
		content, err := json.Marshal(chapter)
		if err != nil {
			t.Fatal(err)
		}
		key := "chapter/" + chapter.ID
		versions[key] = 1
		if _, err := authorityStore.PutWorkspaceArtifact(ctx, domainmodel.WorkspaceArtifact{
			OperationID: operation.ID, Key: key, MediaType: workspace.ChapterMediaType,
			Content: content, UpdatedAt: now,
		}, nil, operation.Attempt); err != nil {
			t.Fatalf("put workspace chapter: %v", err)
		}
	}
	runtime := NewRuntime(authorityStore)
	drafts, err := runtime.workspaceDrafts(ctx, operation, versions)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 2 || drafts[0].ID != "chapter-1" || drafts[1].Blocks[0].Text != "新正文二" {
		t.Fatalf("batch did not preserve both manuscripts: %+v", drafts)
	}
	if _, err := runtime.workspaceDrafts(ctx, operation, nil); err == nil {
		t.Fatal("affected rewrite without drafts accepted")
	}
	// 只引用部分章节在工作区层面是一致的；是否覆盖任务的全部章节由任务提交契约判定
	// （model.ValidateChange，工具边界经 changes.Validate 执行）。
}

func TestRuntimePlanSubmissionEnforcesLengthBound(t *testing.T) {
	// §6.3 篇幅上界前移到工具边界（D67 章数由规划者决定，上界仍是硬边界）：越过固定
	// 篇幅在 proposal_submit 当场被拒，模型在同一会话内纠正后重新提交，而不是收尾时
	// 把整个 Operation 打死。
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
	task := json.RawMessage(`{"intent":"规划开篇","fixed_chapters":1}`)
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
	// 规划按序号编辑卷弧章（D66），ID 与顺序由宿主给。
	structure := map[string]any{
		"volumes": []map[string]any{{"volume": 1, "title": "第一卷", "summary": "开端"}},
		"arcs":    []map[string]any{{"arc": 1, "volume": 1, "title": "第一幕", "summary": "启程"}},
	}
	submission := func(reason string, chapters ...map[string]any) json.RawMessage {
		args := map[string]any{"reason": reason, "chapters": chapters}
		maps.Copy(args, structure)
		raw, _ := json.Marshal(args)
		return raw
	}
	overshoot := submission("越过固定篇幅",
		map[string]any{"chapter": 1, "arc": 1, "title": "第一章", "summary": "出发"},
		map[string]any{"chapter": 2, "arc": 1, "title": "第二章", "summary": "多余"})
	exact := submission("按固定篇幅规划一章", map[string]any{"chapter": 1, "arc": 1, "title": "第一章", "summary": "出发"})
	model := litellmtest.New(callReply("plan-submit-1", "proposal_submit", overshoot), callReply("plan-submit-2", "proposal_submit", exact))
	runtime := boundRuntime(authorityStore, testChat(model))
	runtime.now = func() time.Time { return now.Add(3 * time.Second) }
	outcome, err := runtime.Execute(ctx, operation)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if outcome.Proposal == nil || len(outcome.Proposal.Patches) != 3 {
		t.Fatalf("outcome = %#v, want corrected 3-patch proposal", outcome)
	}
	if len(model.Requests()) != 2 {
		t.Fatalf("model requests = %d, want overshoot rejected then corrected resubmission", len(model.Requests()))
	}
}

// runtimeSnapshot 按 D45 形状冻结快照；config 为已落盘的 Execution Profile 摘要，
// 只走工具校验不执行模型的用例可以用任意非空值。
func runtimeSnapshot(input json.RawMessage, base domainmodel.Revision, policy domainmodel.ApprovalPolicy, config string) domainmodel.ExecutionSnapshot {
	return domainmodel.ExecutionSnapshot{
		Executor: prompt.ExecutorIdentity, BaseRevision: base, InputDigest: domainmodel.Digest(input),
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
		Task: task, BaseRevision: base, ProjectOverlayRevision: base,
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
	if operation, err = authorityStore.ClaimOperationForExecutor(ctx, operation.ID, "worker-1", prompt.ExecutorIdentity, time.Minute, now); err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	chapter := domainmodel.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "plan-1", Number: 1, Title: "第一章", Author: domainmodel.AuthorAI,
		Blocks: []domainmodel.ManuscriptBlock{{ID: "block-1", Text: "新正文"}},
	}
	content, _ := json.Marshal(chapter)
	if _, err := authorityStore.PutWorkspaceArtifact(ctx, domainmodel.WorkspaceArtifact{
		OperationID: operation.ID, Key: "chapter/chapter-1", MediaType: workspace.ChapterMediaType, Content: content, UpdatedAt: now,
	}, nil, operation.Attempt); err != nil {
		t.Fatalf("put workspace chapter: %v", err)
	}
	runtime := NewRuntime(authorityStore)
	var last domainmodel.Proposal
	submitTool := testTool(t, runtime, operation, "writer.revise@1", prompt.ToolProposalSubmit, func(proposal domainmodel.Proposal) (domainmodel.Proposal, error) {
		last = proposal
		return proposal, nil
	})
	submit := func(fields map[string]any) error {
		args := map[string]any{"reason": "重写提交", "workspace_key": "chapter/chapter-1", "workspace_version": 1}
		maps.Copy(args, fields)
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		_, err = submitTool(ctx, raw)
		return err
	}
	fresh := []map[string]any{{"subject": "主角", "predicate": "event.left", "value": true}}
	// 漏掉重申报：错误用故事标签指出是哪条事实，模型不需要知道它的 ID。
	err = submit(map[string]any{"facts": fresh})
	if !errors.Is(err, domainmodel.ErrStructuralConflict) || !strings.Contains(err.Error(), "must redeclare canon 「主角」event.done（第 1 章）") ||
		strings.Contains(err.Error(), `"fact-1"`) {
		t.Fatalf("missing redeclaration err = %v", err)
	}
	foreign := []map[string]any{{"subject": "主角", "predicate": "event.elsewhere", "value": true, "chapter": 9}}
	if err := submit(map[string]any{"facts": foreign}); !errors.Is(err, domainmodel.ErrInvalid) || !strings.Contains(err.Error(), "第 9 章") {
		t.Fatalf("foreign source err = %v", err)
	}
	done := []map[string]any{{"subject": "主角", "predicate": "event.done"}}
	for name, fields := range map[string]map[string]any{
		"confirm and add": {"facts": fresh, "confirm_facts": done},
		"confirm only":    {"confirm_facts": done},
		"remove and add":  {"facts": fresh, "remove_facts": done},
	} {
		if err := submit(fields); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// 解析只看冻结基线：之后的用户修改不会成为确认的原值。
	edited, _ := json.Marshal(domainmodel.CanonFact{ID: "fact-1", Kind: domainmodel.CanonEvent, SubjectID: "hero", Predicate: "event.done",
		PreviousValue: json.RawMessage(`true`), Value: json.RawMessage(`false`), SourceChapterID: "chapter-1"})
	userEdit := approvedRuntimeProposal("user-edit", target, 1, now, domainmodel.Patch{
		Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "fact-1"}, Operation: domainmodel.PatchPut, Content: edited})
	pending := userEdit
	pending.ApprovalState, pending.DecidedBy, pending.DecidedAt = domainmodel.ApprovalPending, nil, nil
	if _, err := authorityStore.SaveProposal(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := authorityStore.CommitProposal(ctx, userEdit); err != nil {
		t.Fatal(err)
	}
	frozen := testTool(t, runtime, operation, "writer.revise@1", prompt.ToolProposalSubmit, func(proposal domainmodel.Proposal) (domainmodel.Proposal, error) {
		last = proposal
		return proposal, nil
	})
	raw, _ := json.Marshal(map[string]any{"reason": "确认", "workspace_key": "chapter/chapter-1", "workspace_version": 1, "confirm_facts": done})
	if _, err := frozen(ctx, raw); err != nil {
		t.Fatal(err)
	}
	var confirmed domainmodel.CanonFact
	if err := json.Unmarshal(last.Patches[1].Content, &confirmed); err != nil || string(confirmed.Value) != "true" || string(confirmed.PreviousValue) != "true" {
		t.Fatalf("confirmation must copy the frozen baseline: %s, %v", last.Patches[1].Content, err)
	}
}

// seedWrittenChapters 提交一个写完 count 章的最小作品：正文 chapter-N 实现计划 plan-N。
func seedWrittenChapters(t *testing.T, ctx context.Context, authorityStore *store.Store, target domainmodel.AuthorityTarget, now time.Time, count int) {
	t.Helper()
	document := func(ref domainmodel.DocumentRef, value any) domainmodel.Patch {
		content, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return domainmodel.Patch{Document: ref, Operation: domainmodel.PatchPut, Content: content}
	}
	patches := []domainmodel.Patch{
		document(domainmodel.DocumentRef{Kind: domainmodel.DocumentIntent, ID: "root"}, domainmodel.Intent{Premise: "凡人修仙"}),
		document(domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: "volume-1"}, domainmodel.PlanNode{ID: "volume-1", Kind: domainmodel.PlanVolume, Title: "卷一", Summary: "入道"}),
		document(domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: "arc-1"}, domainmodel.PlanNode{ID: "arc-1", Kind: domainmodel.PlanArc, ParentID: "volume-1", Title: "弧一", Summary: "山门"}),
	}
	for number := 1; number <= count; number++ {
		plan, chapter := fmt.Sprintf("plan-%d", number), fmt.Sprintf("chapter-%d", number)
		patches = append(patches,
			document(domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: plan}, domainmodel.PlanNode{
				ID: plan, Kind: domainmodel.PlanChapter, ParentID: "arc-1", Order: number, Title: fmt.Sprintf("第%d章", number), Summary: "推进"}),
			document(domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: chapter}, domainmodel.ManuscriptChapter{
				ID: chapter, PlanNodeID: plan, Number: number, Title: fmt.Sprintf("第%d章", number), Author: domainmodel.AuthorAI,
				Blocks: []domainmodel.ManuscriptBlock{{ID: "b1", Text: "正文"}}}),
		)
	}
	seed := approvedRuntimeProposal("seed", target, 0, now, patches...)
	pending := seed
	pending.ApprovalState, pending.DecidedBy, pending.DecidedAt = domainmodel.ApprovalPending, nil, nil
	if _, err := authorityStore.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save seed: %v", err)
	}
	if _, err := authorityStore.CommitProposal(ctx, seed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

// seedRuntimeProject 提交一个结构完整的最小作品（intent、卷/弧/章计划、实体 hero、
// 一条状态事实）到 revision 1：工具边界会跑 change 引擎的结构校验，夹具必须真实。
func seedRuntimeProject(t *testing.T, ctx context.Context, authorityStore *store.Store, target domainmodel.AuthorityTarget, now time.Time) {
	t.Helper()
	document := func(ref domainmodel.DocumentRef, value any) domainmodel.Patch {
		content, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return domainmodel.Patch{Document: ref, Operation: domainmodel.PatchPut, Content: content}
	}
	seed := approvedRuntimeProposal("seed", target, 0, now,
		document(domainmodel.DocumentRef{Kind: domainmodel.DocumentIntent, ID: "root"}, domainmodel.Intent{Premise: "凡人修仙"}),
		document(domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: "volume-1"}, domainmodel.PlanNode{ID: "volume-1", Kind: domainmodel.PlanVolume, Title: "卷一", Summary: "入道"}),
		document(domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: "arc-1"}, domainmodel.PlanNode{ID: "arc-1", Kind: domainmodel.PlanArc, ParentID: "volume-1", Title: "弧一", Summary: "山门"}),
		document(domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: "chapter-plan-1"}, domainmodel.PlanNode{ID: "chapter-plan-1", Kind: domainmodel.PlanChapter, ParentID: "arc-1", Order: 1, Title: "第一章", Summary: "抵达山门"}),
		document(domainmodel.DocumentRef{Kind: domainmodel.DocumentEntity, ID: "hero"}, domainmodel.Entity{ID: "hero", Kind: domainmodel.EntityCharacter, Name: "主角"}),
		document(domainmodel.DocumentRef{Kind: domainmodel.DocumentCanon, ID: "hero-origin"}, domainmodel.CanonFact{
			ID: "hero-origin", Kind: domainmodel.CanonState, SubjectID: "hero", Predicate: "state.origin", Value: json.RawMessage(`"农家子"`),
		}),
	)
	pending := seed
	pending.ApprovalState, pending.DecidedBy, pending.DecidedAt = domainmodel.ApprovalPending, nil, nil
	if _, err := authorityStore.SaveProposal(ctx, pending); err != nil {
		t.Fatalf("save seed: %v", err)
	}
	if _, err := authorityStore.CommitProposal(ctx, seed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

// testChat 是脚本化 Provider 上名为 "model" 的模型。
func testChat(p litellm.Provider) agentcore.Model {
	client, err := litellm.New(p)
	if err != nil {
		panic(err)
	}
	return agentcore.Model{Client: client, Request: litellm.Request{Model: "model"}}
}

// boundRuntime 是测试里的常驻 Runtime 加一套默认绑定；模型摘要固定为 "model"。
func boundRuntime(authorityStore *store.Store, chat agentcore.Model) *Runtime {
	runtime := NewRuntime(authorityStore)
	runtime.Bind(models.Bindings{Default: models.Binding{Provider: "test", Model: "model", Digest: "model", Chat: chat}})
	return runtime
}

// TestPublishActivityStreamsItemsAndToolDetail 守卫结构化产出的直播：规划提交的参数
// 一边生成，一边按条进入同一栏目的输出块；参数收齐时条目补上作用对象。
func TestPublishActivityStreamsItemsAndToolDetail(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	hub := activity.NewHub()
	runtime := &Runtime{now: func() time.Time { return now }, activity: hub}
	operation := domainmodel.Operation{
		ID: "op-plan", RunID: "run-plan", Kind: domainmodel.OperationDevelopPlan,
		Target: domainmodel.AuthorityTarget{Kind: domainmodel.AuthorityProject, ID: "book-plan"},
	}
	stream := newLiveStream(nil)
	call := agentcore.ToolCall{ID: "call-p", Name: "proposal_submit", Args: json.RawMessage(`{"reason":"首次规划"}`)}
	runtime.publishActivity(operation, delta(litellm.BlockStart{Index: 0, Block: litellm.ToolUseBlock{ID: call.ID, Name: call.Name}}), stream)
	for _, piece := range []string{`{"reason":"首次规划","chapters":[{"chapter":1,"title":"无人`, `签收"},{"chap`, `ter":2,"title":"雨夜来客"}],"entities":[{"name":"陈渡","kind":"character"}]}`} {
		runtime.publishActivity(operation, delta(litellm.ToolUseDelta{Index: 0, Arguments: piece}), stream)
	}
	runtime.publishActivity(operation, agentcore.ToolStart{Call: call}, stream)
	snapshot, _ := hub.Snapshot("book-plan")
	if len(snapshot.Output) != 2 || snapshot.Output[0].Section != "大纲" || string(snapshot.Output[0].Text) != "第 1 章 · 无人签收\n第 2 章 · 雨夜来客" ||
		snapshot.Output[1].Section != "设定" || string(snapshot.Output[1].Text) != "陈渡（人物）" {
		t.Fatalf("item blocks = %#v", snapshot.Output)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Detail != "首次规划" || snapshot.Entries[0].Bytes == 0 {
		t.Fatalf("entries = %#v", snapshot.Entries)
	}
}
