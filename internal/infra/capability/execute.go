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
)

// Execute 产出统一 OperationOutcome（D30）：Worker 工具集决定收尾方式——携带
// verdict_submit 的审阅/校验类以结构化 Verdict 收尾，其余以 Proposal 收尾。
func (r *Runtime) Execute(ctx context.Context, operation model.Operation) (outcome model.OperationOutcome, resultErr error) {
	if r.model == nil {
		return model.OperationOutcome{}, fmt.Errorf("capability model is required: %w", model.ErrInvalid)
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
	wantsVerdict := false
	for _, definition := range compiled.Tools {
		if definition.Name == prompt.ToolVerdictSubmit {
			wantsVerdict = true
		}
	}

	// 工具串行执行；提交与停止判定在同一循环内，结果只在循环结束后读取。
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
	cacheKey, err := prompt.CacheKey(operation.Target.ID, compiled.WorkerProfile, compiled.ProfileDigest, operation.ID)
	if err != nil {
		return model.OperationOutcome{}, err
	}

	messageIndex := 0
	var endSummary *agentcore.RunSummary
	executionCtx, stopExecution := context.WithCancelCause(ctx)
	defer stopExecution(nil)
	config := agentcore.LoopConfig{
		Model:              r.model,
		MaxToolConcurrency: 1,
		Middlewares:        []agentcore.ToolMiddleware{submissionGuard(stopExecution)},
		PromptCacheKey:     cacheKey,
		CacheLastMessage:   "ephemeral",
		// 提交结果先由循环持久化，再正常收尾，不额外请求模型，也不以取消冒充成功。
		StopAfterTool: func(name string) bool {
			return name == prompt.ToolProposalSubmit || name == prompt.ToolVerdictSubmit
		},
		CommitMessage: func(message agentcore.AgentMessage) error {
			messageIndex++
			payload, err := json.Marshal(message)
			if err != nil {
				return fmt.Errorf("encode agent message: %w", err)
			}
			_, err = r.store.AppendOperationEvent(ctx, model.OperationEvent{
				OperationID: operation.ID, StepID: "agent.message", Attempt: operation.Attempt,
				IdempotencyKey: fmt.Sprintf("agent-message:%d:%d", operation.Attempt, messageIndex),
				Kind:           "agent.message_committed", Payload: payload, CreatedAt: message.GetTimestamp(),
			})
			return err
		},
		StopGuard: func(context.Context, agentcore.StopInfo) agentcore.StopDecision {
			if submitted != nil || verdict != nil {
				return agentcore.StopDecision{Allow: true}
			}
			if wantsVerdict {
				return agentcore.StopDecision{
					InjectMessage: "任务尚未提交裁定。请完成审阅并调用 verdict_submit 提交覆盖请求范围的结构化裁定；如无法完成，明确返回工具错误。",
				}
			}
			return agentcore.StopDecision{
				InjectMessage: "任务尚未形成 Proposal。请继续使用工作区工具完成候选，并调用 proposal_submit；如无法完成，明确返回工具错误。",
			}
		},
	}
	recoveredMessages, lastFailure, err := r.restoreMessages(ctx, operation)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	workspaceArtifacts, err := r.store.ListWorkspaceArtifacts(ctx, operation.ID)
	if err != nil {
		return model.OperationOutcome{}, err
	}
	prose := newProseTracker() // 随本次执行生灭：正文预览的参数流提取状态
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
	var usage agentcore.Usage
	var executionErr error
	loopContext := agentcore.AgentContext{
		SystemBlocks: []agentcore.SystemBlock{{Text: compiled.StablePrefix, CacheControl: "ephemeral"}},
		Messages:     recoveredMessages,
		Tools:        tools,
	}
	// 始终读到通道关闭，包括取消路径，确保工具和消息落盘已结束。
	for event := range agentcore.AgentLoop(executionCtx, []agentcore.AgentMessage{agentcore.UserMsg(promptText)}, loopContext, config) {
		switch event.Type {
		case agentcore.EventMessageEnd:
			if message, ok := event.Message.(agentcore.Message); ok {
				usage.Add(message.Usage)
			}
		case agentcore.EventError:
			executionErr = event.Err
		case agentcore.EventAgentEnd:
			endSummary = event.Summary
		}
		r.publishActivity(operation, event, prose)
	}
	if cause := context.Cause(executionCtx); cause != nil {
		executionErr = cause
	}
	var errorText string
	if executionErr != nil {
		errorText = executionErr.Error()
	}
	endPayload, err := json.Marshal(struct {
		Summary *agentcore.RunSummary `json:"summary,omitempty"`
		Usage   agentcore.Usage       `json:"usage"`
		Error   string                `json:"error,omitempty"`
	}{Summary: endSummary, Usage: usage, Error: errorText})
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
			return model.OperationOutcome{}, endedWithout(endSummary, ErrNoVerdict)
		}
		payload, err := json.Marshal(*verdict)
		if err != nil {
			return model.OperationOutcome{}, fmt.Errorf("encode review verdict: %w", err)
		}
		return model.OperationOutcome{Verdict: payload}, nil
	}
	if submitted == nil {
		return model.OperationOutcome{}, endedWithout(endSummary, ErrNoProposal)
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

// publishActivity 把 agent 生命周期事件翻译成带归属的活动事件（页面设计 §3）。
// 参数流上报字节进度；正文工具的参数流经 prose 增量提取后以 Prose 事件发布
// 逐字预览（易失，权威正文仍以候选稿为准——三层校正链）。监听器同步执行，
// 这里只做 O(增量) 的翻译与非阻塞发布。
func (r *Runtime) publishActivity(operation model.Operation, event agentcore.Event, prose *proseTracker) {
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
	switch event.Type {
	case agentcore.EventTurnStart:
		out.Kind = activity.TurnStart
	case agentcore.EventMessageEnd:
		// 一条 assistant 消息结束（含中止）即本轮参数流终结：清空提取状态，
		// 生命周期以消息为界，无需容量上限。消息带用量时一并发布（状态行累计）。
		prose.finishMessage()
		usage := messageUsage(event.Message)
		if usage == nil {
			return
		}
		out.Kind = activity.Usage
		out.Usage = activity.UsageTotals{Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead}
		if usage.Cost != nil {
			out.Usage.Cost = usage.Cost.Total
		}
	case agentcore.EventToolExecStart:
		out.Kind, out.Tool, out.CallID = activity.ToolStart, event.Tool, event.ToolID
	case agentcore.EventToolExecEnd:
		out.Kind, out.Tool, out.CallID = activity.ToolEnd, event.Tool, event.ToolID
		if event.IsError {
			// 出错的 Result 是被 json.Marshal 过的字符串字面量：解掉外层引号
			// 与转义再截断，用户看到的是原始错误文本。
			raw := string(event.Result)
			var text string
			if json.Unmarshal(event.Result, &text) == nil {
				raw = text
			}
			out.Err = clipActivityText(raw)
		}
	case agentcore.EventMessageUpdate:
		switch event.DeltaKind {
		case agentcore.DeltaToolCall:
			if event.Delta == "" {
				return
			}
			// 真实时序里参数流入先于工具执行（ToolExecStart 在整条消息完成后
			// 才发生）：从部分消息取正在生成参数的工具名与调用 ID，让活动行
			// 在落笔的第一时间就出现，而不是等参数全部生成完。
			out.Kind, out.Bytes = activity.ToolDelta, len(event.Delta)
			out.Tool, out.CallID = streamingToolCall(event)
			text, stalled := prose.feed(out.Tool, out.CallID, event.Delta)
			if text != "" {
				r.activity.Publish(out) // 字节进度照旧
				out.Kind, out.Bytes, out.Text = activity.Prose, 0, text
			}
			if stalled {
				r.activity.Publish(out) // 先发前一种身份（字节进度或正文增量）
				out.Kind, out.Bytes, out.Text = activity.ProseStall, 0, ""
			}
		case agentcore.DeltaThinking:
			// 思考增量是 Provider 明确标记的推理文本（agentcore ReasoningDelta），
			// 原样截尾展示为思考片段——不是摘要，不作可靠解释承诺（§3）。
			out.Kind, out.Text = activity.Thinking, event.Delta
		default:
			if event.Delta == "" {
				return
			}
			out.Kind, out.Text = activity.Text, event.Delta
		}
	case agentcore.EventRetry:
		out.Kind = activity.Retry
		if event.RetryInfo != nil {
			out.Attempt = event.RetryInfo.Attempt
			if event.RetryInfo.Err != nil {
				out.Err = clipActivityText(event.RetryInfo.Err.Error())
			}
		}
	default:
		return
	}
	r.activity.Publish(out)
}

// messageUsage 取 assistant 消息上的用量；agentcore 的消息既可能以值也可能以指针进事件。
func messageUsage(message agentcore.AgentMessage) *agentcore.Usage {
	switch m := message.(type) {
	case agentcore.Message:
		return m.Usage
	case *agentcore.Message:
		return m.Usage
	}
	return nil
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

// streamingToolCall 取正在生成参数的工具调用的名字与调用 ID：agentcore 在
// toolcall delta 上携带 ToolID，按它在部分消息里精确定位，并行调用交错也不串。
// 消息里暂未回填该 ID 时只返回 ID（名字后到，proseTracker 会先按 ID 缓冲）。
func streamingToolCall(event agentcore.Event) (string, string) {
	msg, ok := event.Message.(agentcore.Message)
	if !ok || event.ToolID == "" {
		return "", event.ToolID
	}
	for _, call := range msg.ToolCalls() {
		if call.ID == event.ToolID {
			return call.Name, call.ID
		}
	}
	return "", event.ToolID
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
func endedWithout(endSummary *agentcore.RunSummary, cause error) error {
	if endSummary != nil {
		return fmt.Errorf("agent ended with %s after %d turns: %w", endSummary.EndReason, endSummary.TurnCount, cause)
	}
	return cause
}

// restoreMessages 把之前尝试已提交的对话导回本次会话，并取出最近一次失败原因。
func (r *Runtime) restoreMessages(ctx context.Context, operation model.Operation) ([]agentcore.AgentMessage, string, error) {
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
	return agentcore.ToAgentMessages(messages), lastFailure, nil
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
