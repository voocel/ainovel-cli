package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/litellm"
)

// Execute 产出统一 OperationOutcome（D30）：Worker 工具集决定收尾方式——携带
// verdict_submit 的审阅/校验类以结构化 Verdict 收尾，其余以 Proposal 收尾。
func (r *Runtime) Execute(ctx context.Context, operation model.Operation) (outcome model.OperationOutcome, resultErr error) {
	if !r.Bound() {
		return model.OperationOutcome{}, fmt.Errorf("capability model is not bound: %w", model.ErrInvalid)
	}
	if operation.State != model.OperationRunning {
		return model.OperationOutcome{}, fmt.Errorf("operation %q is %s: %w", operation.ID, operation.State, model.ErrStateConflict)
	}
	if operation.Snapshot.Executor != r.Identity() {
		return model.OperationOutcome{}, fmt.Errorf("operation executor %q does not match runtime %q: %w", operation.Snapshot.Executor, r.Identity(), model.ErrStateConflict)
	}
	task, err := model.DecodeTaskInput(operation.Kind, operation.Input)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	scope := taskActivityScope(task)
	r.publishTask(operation, activity.TaskStart, nil, scope)
	defer func() { r.publishTask(operation, activity.TaskEnd, resultErr, scope) }()
	compiled, err := r.prompts.Load(ctx, operation.Snapshot.ConfigDigest)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	// 本次尝试的模型按 Worker 角色取绑定，读一次沿用到结束（D57）。
	worker, err := prompt.BuiltinWorkerProfile(prompt.WorkerID(compiled.WorkerProfile))
	if err != nil {
		return model.OperationOutcome{}, err
	}
	binding, _ := r.bindingFor(worker.ModelRole)
	wantsVerdict := false
	for _, definition := range compiled.Tools {
		if definition.Name == prompt.ToolVerdictSubmit {
			wantsVerdict = true
		}
	}

	// 工具串行执行（都不声明可并行）；提交与停止判定在同一循环内，结果只在循环结束后读取。
	var submitted *model.Proposal
	var verdict *model.ReviewVerdict
	tools, err := r.toolsFor(operation, compiled, func(proposal model.Proposal) (model.Proposal, error) {
		if submitted != nil {
			same, err := sameSubmission(*submitted, proposal)
			if err != nil {
				return model.Proposal{}, err
			}
			if !same {
				return model.Proposal{}, fmt.Errorf("operation already submitted a different proposal: %w", model.ErrIdempotencyConflict)
			}
			return *submitted, nil
		}
		copy := proposal
		submitted = &copy
		return copy, nil
	}, func(candidate model.ReviewVerdict) (model.ReviewVerdict, error) {
		if verdict != nil {
			same, err := sameVerdict(*verdict, candidate)
			if err != nil {
				return model.ReviewVerdict{}, err
			}
			if !same {
				return model.ReviewVerdict{}, fmt.Errorf("operation already submitted a different verdict: %w", model.ErrIdempotencyConflict)
			}
			return *verdict, nil
		}
		copy := candidate
		verdict = &copy
		return copy, nil
	})
	if err != nil {
		return model.OperationOutcome{}, err
	}
	// 会话血统混入模型配置摘要：换模型或思考强度后的尝试是新会话（§7 缓存纪律）。
	cacheKey, err := prompt.CacheKey(operation.Target.ID, compiled.WorkerProfile, compiled.ProfileDigest, operation.ID+":"+binding.Digest)
	if err != nil {
		return model.OperationOutcome{}, err
	}

	recoveredMessages, lastFailure, err := r.restoreMessages(ctx, operation)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	// 每次尝试记下实际使用的模型：快照不再冻结模型配置，历史由这条事件解释（D57）。
	startPayload, err := json.Marshal(struct {
		Role         string `json:"role"`
		Provider     string `json:"provider"`
		Model        string `json:"model"`
		Thinking     string `json:"thinking,omitempty"`
		ConfigDigest string `json:"config_digest"`
	}{Role: worker.ModelRole, Provider: binding.Provider, Model: binding.Model, Thinking: binding.Thinking, ConfigDigest: binding.Digest})
	if err != nil {
		return model.OperationOutcome{}, fmt.Errorf("encode agent run start: %w", err)
	}
	if _, err := r.store.AppendOperationEvent(ctx, model.OperationEvent{
		OperationID: operation.ID, StepID: "agent.start", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("agent-start:%d", operation.Attempt),
		Kind:           "agent.run_started", Payload: startPayload, CreatedAt: r.now(),
	}); err != nil {
		return model.OperationOutcome{}, err
	}
	workspaceArtifacts, err := r.store.ListWorkspaceArtifacts(ctx, operation.ID)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	// 恢复提示只能追加在完整任务上下文之后，不得替换 Dynamic Tail：restart 的
	// 新任务同样必须拿到 project_rules、story_context 与 operation_task。
	promptText := compiled.DynamicTail
	if len(recoveredMessages) > 0 || len(workspaceArtifacts) > 0 || lastFailure != "" {
		promptText = fmt.Sprintf(
			"%s\n\n继续已有 Operation 工作区：当前是第 %d 次尝试，已恢复 %d 条消息和 %d 个工件。先用 workspace_list 核对版本，从已有工件继续，不要从头重写。",
			compiled.DynamicTail, operation.Attempt, len(recoveredMessages), len(workspaceArtifacts),
		)
	}
	// 重开的会话必须知道上次为什么失败（D56）：否则只是换个尝试号重复同样的错。
	if lastFailure != "" {
		promptText += fmt.Sprintf("\n上一次尝试失败原因：%s。先针对这个原因修正，再继续。", lastFailure)
	}

	executionCtx, stopExecution := context.WithCancelCause(ctx)
	defer stopExecution(nil)
	stream := newLiveStream() // 随本次执行生灭：流式工具调用的身份与正文预览的提取状态
	messageIndex := 0
	var usage agentcore.Usage
	var end agentcore.RunEnd
	config := agentcore.Config{
		Model:      binding.Routed(cacheKey, ""),
		System:     []litellm.Block{litellm.TextBlock{Text: compiled.StablePrefix, Cache: &litellm.CacheControl{}}},
		Tools:      tools,
		Middleware: []agentcore.ToolMiddleware{submissionGuard(stopExecution)},
		Cache:      &litellm.CacheControl{},
		// 每条进入会话的消息先落盘：落盘失败即停止本次执行，提交结果因此先于收尾持久化。
		Emit: func(event agentcore.Event) error {
			switch e := event.(type) {
			case agentcore.MessageEnd:
				messageIndex++
				payload, err := json.Marshal(e.Message)
				if err != nil {
					return fmt.Errorf("encode agent message: %w", err)
				}
				if _, err := r.store.AppendOperationEvent(ctx, model.OperationEvent{
					OperationID: operation.ID, StepID: "agent.message", Attempt: operation.Attempt,
					IdempotencyKey: fmt.Sprintf("agent-message:%d:%d", operation.Attempt, messageIndex),
					Kind:           "agent.message_committed", Payload: payload, CreatedAt: e.Message.Time,
				}); err != nil {
					return err
				}
				usage.Add(e.Message.Usage)
			case agentcore.RunEnd:
				end = e
			}
			r.publishActivity(operation, event, stream)
			return nil
		},
		// 提交工具成功即收尾（Terminate），不额外请求模型；未提交就想停则提醒继续。
		OnStop: func(context.Context, agentcore.StopInfo) ([]agentcore.Message, error) {
			switch {
			case submitted != nil || verdict != nil:
				return nil, nil
			case wantsVerdict:
				return []agentcore.Message{agentcore.UserText("任务尚未提交裁定。请完成审阅并调用 verdict_submit 提交覆盖请求范围的结构化裁定；如无法完成，明确返回工具错误。")}, nil
			}
			return []agentcore.Message{agentcore.UserText("任务尚未形成 Proposal。请继续使用工作区工具完成候选，并调用 proposal_submit；如无法完成，明确返回工具错误。")}, nil
		},
	}
	_, executionErr := agentcore.Run(executionCtx, config, recoveredMessages, agentcore.UserText(promptText))
	var errorText string
	if executionErr != nil {
		errorText = executionErr.Error()
	}
	endPayload, err := json.Marshal(struct {
		Reason      agentcore.EndReason `json:"reason"`
		Turns       int                 `json:"turns"`
		ToolCalls   int                 `json:"tool_calls"`
		FailedCalls int                 `json:"failed_calls"`
		Usage       agentcore.Usage     `json:"usage"`
		Error       string              `json:"error,omitempty"`
	}{Reason: end.Reason, Turns: end.Turns, ToolCalls: end.ToolCalls, FailedCalls: end.FailedCalls, Usage: usage, Error: errorText})
	if err != nil {
		return model.OperationOutcome{}, fmt.Errorf("encode agent run summary: %w", err)
	}
	if _, err := r.store.AppendOperationEvent(ctx, model.OperationEvent{
		OperationID: operation.ID, StepID: "agent.end", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("agent-end:%d", operation.Attempt),
		Kind:           "agent.run_ended", Payload: endPayload, CreatedAt: r.now(),
	}); err != nil {
		return model.OperationOutcome{}, err
	}
	if executionErr != nil {
		return model.OperationOutcome{}, fmt.Errorf("capability agent failed: %w", executionErr)
	}
	if wantsVerdict {
		if verdict == nil {
			return model.OperationOutcome{}, endedWithout(end, ErrNoVerdict)
		}
		payload, err := json.Marshal(*verdict)
		if err != nil {
			return model.OperationOutcome{}, fmt.Errorf("encode review verdict: %w", err)
		}
		return model.OperationOutcome{Verdict: payload}, nil
	}
	if submitted == nil {
		return model.OperationOutcome{}, endedWithout(end, ErrNoProposal)
	}
	return model.OperationOutcome{Proposal: submitted}, nil
}

func (r *Runtime) publishTask(operation model.Operation, kind activity.Kind, err error, scope activity.Scope) {
	if r.activity == nil {
		return
	}
	event := activity.Event{ProjectID: operation.Target.ID, RunID: operation.RunID, OperationID: operation.ID, Kind: kind, Attempt: operation.Attempt, At: r.now()}
	event.Scope = scope
	event.TaskKind = string(operation.Kind)
	if spec, specErr := model.KindSpec(operation.Kind); specErr == nil {
		event.TaskLabel = spec.Label
	}
	if err != nil {
		event.Err = clipActivityText(err.Error())
	}
	r.activity.Publish(event)
}

func taskActivityScope(task model.TaskInput) activity.Scope {
	switch input := task.(type) {
	case *model.WriteChapterInput:
		return activity.Scope{ChapterNumber: input.ChapterNumber, PlanNodeID: input.ChapterPlanID}
	case *model.RewriteChapterInput:
		return activity.Scope{ChapterNumber: input.ChapterNumber, PlanNodeID: input.ChapterPlanID, ChapterIDs: []string{input.ChapterID}}
	case *model.ReviewRangeInput:
		return activity.Scope{ChapterIDs: input.ChapterIDs}
	case *model.RewriteAffectedInput:
		return activity.Scope{ChapterIDs: input.ChapterIDs}
	case *model.ReviseCanonInput:
		return activity.Scope{ChapterIDs: []string{input.ChapterID}}
	default:
		return activity.Scope{}
	}
}

// liveStream 是一次执行的流式状态：回复里开过的工具调用与正文预览的提取状态。
// 块序号逐条回复重用，开块时覆盖，不必清空。
type liveStream struct {
	calls map[int]litellm.ToolUseBlock
	prose *proseTracker
}

func newLiveStream() *liveStream {
	return &liveStream{calls: make(map[int]litellm.ToolUseBlock), prose: newProseTracker()}
}

// publishActivity 把 agent 生命周期事件翻译成带归属的活动事件（页面设计 §3）。
// 参数流上报字节进度；正文工具的参数流经 prose 增量提取后以 Prose 事件发布
// 逐字预览（易失，权威正文仍以候选稿为准——三层校正链）。监听器同步执行，
// 这里只做 O(增量) 的翻译与非阻塞发布。
func (r *Runtime) publishActivity(operation model.Operation, event agentcore.Event, stream *liveStream) {
	if r.activity == nil {
		return
	}
	out := activity.Event{
		ProjectID: operation.Target.ID, RunID: operation.RunID,
		OperationID: operation.ID, At: r.now(),
	}
	if spec, err := model.KindSpec(operation.Kind); err == nil {
		out.TaskLabel = spec.Label
	}
	switch e := event.(type) {
	case agentcore.MessageStart:
		out.Kind = activity.TurnStart
	case agentcore.MessageEnd:
		// 一条消息结束（含中止）即本轮参数流终结：清空提取状态，生命周期以
		// 消息为界，无需容量上限。消息带用量时一并发布（状态行累计）。
		stream.prose.finishMessage()
		usage := e.Message.Usage
		if usage == nil {
			return
		}
		out.Kind = activity.Usage
		out.Usage = activity.UsageTotals{Input: usage.InputTokens, Output: usage.OutputTokens, CacheRead: usage.CacheReadTokens}
		out.Model, out.Provider = e.Message.Model, e.Message.Provider
		if usage.Cost != nil {
			out.Usage.Cost = usage.Cost.Total
		}
	case agentcore.ToolStart:
		out.Kind, out.Tool, out.CallID = activity.ToolStart, e.Call.Name, e.Call.ID
	case agentcore.ToolEnd:
		out.Kind, out.Tool, out.CallID = activity.ToolEnd, e.Call.Name, e.Call.ID
		if e.Result.IsError {
			out.Err = clipActivityText(e.Result.Text())
		}
	case agentcore.MessageDelta:
		switch d := e.Event.(type) {
		case litellm.BlockStart:
			if call, ok := d.Block.(litellm.ToolUseBlock); ok {
				stream.calls[d.Index] = call
			}
			return
		case litellm.ToolUseDelta:
			if d.Arguments == "" {
				return
			}
			// 参数流入先于工具执行（ToolStart 在整条回复完成后才发生）：按块序号
			// 取正在生成参数的工具名与调用 ID，让活动行在落笔的第一时间就出现。
			// 个别网关开块时还不带名字，proseTracker 会先缓冲。
			call := stream.calls[d.Index]
			out.Kind, out.Bytes = activity.ToolDelta, len(d.Arguments)
			out.Tool, out.CallID = call.Name, call.ID
			text, stalled := stream.prose.feed(out.Tool, out.CallID, d.Arguments)
			if text != "" {
				r.activity.Publish(out) // 字节进度照旧
				out.Kind, out.Bytes, out.Text = activity.Prose, 0, text
			}
			if stalled {
				r.activity.Publish(out) // 先发前一种身份（字节进度或正文增量）
				out.Kind, out.Bytes, out.Text = activity.ProseStall, 0, ""
			}
		case litellm.ReasoningDelta:
			// 思考增量是 Provider 明确标记的推理文本，原样截尾展示为思考片段——
			// 不是摘要，不作可靠解释承诺（§3）。
			out.Kind, out.Text = activity.Thinking, d.Text
		case litellm.TextDelta:
			if d.Text == "" {
				return
			}
			out.Kind, out.Text = activity.Text, d.Text
		default:
			return
		}
	case agentcore.Retry:
		out.Kind, out.Attempt = activity.Retry, e.Attempt
		out.Err = clipActivityText(e.Err.Error())
	default:
		return
	}
	r.activity.Publish(out)
}

// publishStage 把 Agent 循环之外的单次模型判断（如收尾时的语义合规）也发到活动流：
// 收尾同样可能等模型十几秒，画面不能静止。
func (r *Runtime) publishStage(operation model.Operation, kind activity.Kind, stage string, err error) {
	if r.activity == nil {
		return
	}
	out := activity.Event{
		ProjectID: operation.Target.ID, RunID: operation.RunID, OperationID: operation.ID,
		Kind: kind, Tool: stage, CallID: operation.ID + ":" + stage, At: r.now(),
	}
	if err != nil {
		out.Err = clipActivityText(err.Error())
	}
	r.activity.Publish(out)
}

func clipActivityText(text string) string {
	const limit = 200
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// endedWithout 把缺少收尾提交的结局包装成可诊断错误，保留 agent 的结束原因。
func endedWithout(end agentcore.RunEnd, cause error) error {
	return fmt.Errorf("agent ended with %s after %d turns: %w", end.Reason, end.Turns, cause)
}

// restoreMessages 把之前尝试已提交的对话导回本次会话，并取出最近一次失败原因。
func (r *Runtime) restoreMessages(ctx context.Context, operation model.Operation) ([]agentcore.Message, string, error) {
	if operation.Attempt <= 1 {
		return nil, "", nil
	}
	events, err := r.store.ListOperationEvents(ctx, operation.ID)
	if err != nil {
		return nil, "", err
	}
	messages := make([]agentcore.Message, 0)
	var lastFailure string
	for _, event := range events {
		if event.Attempt >= operation.Attempt {
			continue
		}
		switch event.Kind {
		case "agent.message_committed":
			var message agentcore.Message
			if err := json.Unmarshal(event.Payload, &message); err != nil {
				return nil, "", fmt.Errorf("restore agent message at event %d: %w", event.Sequence, err)
			}
			// 旧版本写下的消息没有 blocks：解出来是空消息，宁可报错也不带进会话。
			if len(message.Blocks) == 0 {
				return nil, "", fmt.Errorf("restore agent message at event %d: no content blocks (written by an older version; restart the task): %w", event.Sequence, model.ErrInvalid)
			}
			messages = append(messages, message)
		case "operation.transitioned":
			var transition struct {
				To      model.OperationState `json:"to"`
				Message string               `json:"message"`
			}
			if err := json.Unmarshal(event.Payload, &transition); err != nil {
				return nil, "", fmt.Errorf("restore operation transition at event %d: %w", event.Sequence, err)
			}
			if transition.To == model.OperationFailed && transition.Message != "" {
				lastFailure = transition.Message
			}
		}
	}
	return messages, lastFailure, nil
}

func sameVerdict(left, right model.ReviewVerdict) (bool, error) {
	leftPayload, err := json.Marshal(left)
	if err != nil {
		return false, fmt.Errorf("encode existing verdict submission: %w", err)
	}
	rightPayload, err := json.Marshal(right)
	if err != nil {
		return false, fmt.Errorf("encode repeated verdict submission: %w", err)
	}
	return bytes.Equal(leftPayload, rightPayload), nil
}

func sameSubmission(left, right model.Proposal) (bool, error) {
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	leftPayload, err := json.Marshal(left)
	if err != nil {
		return false, fmt.Errorf("encode existing proposal submission: %w", err)
	}
	rightPayload, err := json.Marshal(right)
	if err != nil {
		return false, fmt.Errorf("encode repeated proposal submission: %w", err)
	}
	return bytes.Equal(leftPayload, rightPayload), nil
}
