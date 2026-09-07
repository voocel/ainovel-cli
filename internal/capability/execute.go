package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/activity"
	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Execute 产出统一 OperationOutcome（D30）：Worker 工具集决定收尾方式——携带
// verdict_submit 的审阅/校验类以结构化 Verdict 收尾，其余以 Proposal 收尾。
func (r *Runtime) Execute(
	ctx context.Context,
	operation domain.Operation,
	compiled prompt.Compiled,
) (domain.OperationOutcome, error) {
	if r.model == nil {
		return domain.OperationOutcome{}, fmt.Errorf("capability model is required: %w", domain.ErrInvalid)
	}
	if operation.State != domain.OperationRunning {
		return domain.OperationOutcome{}, fmt.Errorf("operation %q is %s: %w", operation.ID, operation.State, store.ErrStateConflict)
	}
	if operation.Snapshot.ModelConfigDigest != r.modelDigest {
		return domain.OperationOutcome{}, fmt.Errorf("operation model config does not match capability runtime: %w", store.ErrStateConflict)
	}
	if operation.Snapshot.ExecutionProfileDigest != compiled.ProfileDigest || operation.Snapshot != compiled.Snapshot {
		return domain.OperationOutcome{}, fmt.Errorf("operation execution profile does not match compiled profile: %w", store.ErrStateConflict)
	}
	wantsVerdict := false
	for _, definition := range compiled.Tools {
		if definition.Name == prompt.ToolVerdictSubmit {
			wantsVerdict = true
		}
	}

	var submissionMu sync.Mutex
	var submitted *domain.Proposal
	var verdict *domain.ReviewVerdict
	tools, err := r.toolsFor(operation, compiled.Tools, func(proposal domain.Proposal) (domain.Proposal, error) {
		submissionMu.Lock()
		defer submissionMu.Unlock()
		if submitted != nil {
			same, err := sameSubmission(*submitted, proposal)
			if err != nil {
				return domain.Proposal{}, err
			}
			if !same {
				return domain.Proposal{}, fmt.Errorf("operation already submitted a different proposal: %w", store.ErrIdempotencyConflict)
			}
			return *submitted, nil
		}
		copy := proposal
		submitted = &copy
		return copy, nil
	}, func(candidate domain.ReviewVerdict) (domain.ReviewVerdict, error) {
		submissionMu.Lock()
		defer submissionMu.Unlock()
		if verdict != nil {
			same, err := sameVerdict(*verdict, candidate)
			if err != nil {
				return domain.ReviewVerdict{}, err
			}
			if !same {
				return domain.ReviewVerdict{}, fmt.Errorf("operation already submitted a different verdict: %w", store.ErrIdempotencyConflict)
			}
			return *verdict, nil
		}
		copy := candidate
		verdict = &copy
		return copy, nil
	})
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	cacheKey, err := prompt.CacheKey(
		operation.Target.ID, operation.Snapshot.WorkerProfileVersion,
		operation.Snapshot.ExecutionProfileDigest, operation.ID,
	)
	if err != nil {
		return domain.OperationOutcome{}, err
	}

	messageIndex := 0
	var endSummary *agentcore.RunSummary
	agent := agentcore.NewAgent(
		agentcore.WithModel(r.model),
		agentcore.WithSystemBlocks([]agentcore.SystemBlock{{Text: compiled.StablePrefix, CacheControl: "ephemeral"}}),
		agentcore.WithTools(tools...),
		agentcore.WithMaxToolConcurrency(1),
		agentcore.WithPromptCacheKey(cacheKey),
		agentcore.WithCacheLastMessage("ephemeral"),
		agentcore.WithMessageCommitter(func(message agentcore.AgentMessage) error {
			messageIndex++
			payload, err := json.Marshal(message)
			if err != nil {
				return fmt.Errorf("encode agent message: %w", err)
			}
			_, err = r.store.AppendOperationEvent(ctx, domain.OperationEvent{
				OperationID: operation.ID, StepID: "agent.message", Attempt: operation.Attempt,
				IdempotencyKey: fmt.Sprintf("agent-message:%d:%d", operation.Attempt, messageIndex),
				Kind:           "agent.message_committed", Payload: payload, CreatedAt: message.GetTimestamp(),
			})
			return err
		}),
		agentcore.WithStopGuard(func(context.Context, agentcore.StopInfo) agentcore.StopDecision {
			submissionMu.Lock()
			defer submissionMu.Unlock()
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
		}),
	)
	recoveredMessages, err := r.restoreMessages(ctx, operation, agent)
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	workspaceArtifacts, err := r.store.ListWorkspaceArtifacts(ctx, operation.ID)
	if err != nil {
		return domain.OperationOutcome{}, err
	}
	prose := newProseTracker() // 随本次执行生灭：正文预览的参数流提取状态
	agent.Subscribe(func(event agentcore.Event) {
		if event.Type == agentcore.EventAgentEnd {
			endSummary = event.Summary
		}
		r.publishActivity(operation, event, prose)
	})
	// 恢复提示只能追加在完整任务上下文之后，不得替换 Dynamic Tail：restart 的
	// 新任务同样必须拿到 project_rules、story_context 与 operation_task。
	promptText := compiled.DynamicTail
	if recoveredMessages > 0 || len(workspaceArtifacts) > 0 {
		promptText = fmt.Sprintf(
			"%s\n\n继续已有 Operation 工作区：当前是第 %d 次尝试，已恢复 %d 条消息和 %d 个工件。先用 workspace_list 核对版本，从已有工件继续，不要从头重写。",
			compiled.DynamicTail, operation.Attempt, recoveredMessages, len(workspaceArtifacts),
		)
	}
	if err := agent.Prompt(ctx, promptText); err != nil {
		return domain.OperationOutcome{}, err
	}
	agent.WaitForIdle()
	state := agent.State()
	endPayload, err := json.Marshal(struct {
		Summary *agentcore.RunSummary `json:"summary,omitempty"`
		Usage   agentcore.Usage       `json:"usage"`
		Error   string                `json:"error,omitempty"`
	}{Summary: endSummary, Usage: state.TotalUsage, Error: state.Error})
	if err != nil {
		return domain.OperationOutcome{}, fmt.Errorf("encode agent run summary: %w", err)
	}
	if _, err := r.store.AppendOperationEvent(ctx, domain.OperationEvent{
		OperationID: operation.ID, StepID: "agent.end", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("agent-end:%d", operation.Attempt),
		Kind:           "agent.run_ended", Payload: endPayload, CreatedAt: r.now(),
	}); err != nil {
		return domain.OperationOutcome{}, err
	}
	if state.Error != "" {
		return domain.OperationOutcome{}, fmt.Errorf("capability agent failed: %s", state.Error)
	}
	submissionMu.Lock()
	defer submissionMu.Unlock()
	if wantsVerdict {
		if verdict == nil {
			return domain.OperationOutcome{}, endedWithout(endSummary, ErrNoVerdict)
		}
		payload, err := json.Marshal(*verdict)
		if err != nil {
			return domain.OperationOutcome{}, fmt.Errorf("encode review verdict: %w", err)
		}
		return domain.OperationOutcome{Verdict: payload}, nil
	}
	if submitted == nil {
		return domain.OperationOutcome{}, endedWithout(endSummary, ErrNoProposal)
	}
	return domain.OperationOutcome{Proposal: submitted}, nil
}

// publishActivity 把 agent 生命周期事件翻译成带归属的活动事件（页面设计 §3）。
// 参数流上报字节进度；正文工具的参数流经 prose 增量提取后以 Prose 事件发布
// 逐字预览（易失，权威正文仍以候选稿为准——三层校正链）。监听器同步执行，
// 这里只做 O(增量) 的翻译与非阻塞发布。
func (r *Runtime) publishActivity(operation domain.Operation, event agentcore.Event, prose *proseTracker) {
	if r.activity == nil {
		return
	}
	out := activity.Event{
		ProjectID: operation.Target.ID, RunID: operation.RunID,
		OperationID: operation.ID, At: r.now(),
	}
	switch event.Type {
	case agentcore.EventMessageEnd:
		// 一条 assistant 消息结束（含中止）即本轮参数流终结：清空提取状态，
		// 生命周期以消息为界，无需容量上限。
		prose.finishMessage()
		return
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

func (r *Runtime) restoreMessages(ctx context.Context, operation domain.Operation, agent *agentcore.Agent) (int, error) {
	if operation.Attempt <= 1 {
		return 0, nil
	}
	events, err := r.store.ListOperationEvents(ctx, operation.ID)
	if err != nil {
		return 0, err
	}
	messages := make([]agentcore.Message, 0)
	for _, event := range events {
		if event.Kind != "agent.message_committed" || event.Attempt >= operation.Attempt {
			continue
		}
		var message agentcore.Message
		if err := json.Unmarshal(event.Payload, &message); err != nil {
			return 0, fmt.Errorf("restore agent message at event %d: %w", event.Sequence, err)
		}
		messages = append(messages, message)
	}
	if len(messages) == 0 {
		return 0, nil
	}
	if err := agent.ImportMessages(messages); err != nil {
		return 0, fmt.Errorf("restore agent conversation: %w", err)
	}
	return len(messages), nil
}

func sameVerdict(left, right domain.ReviewVerdict) (bool, error) {
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

func sameSubmission(left, right domain.Proposal) (bool, error) {
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
