package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// errorKind classifies a runtime error into a stable, short label for log
// filtering and alert routing. Returns "" when no special tag applies.
//
// err is the live error chain (may be nil after JSON serialization); msg is
// the rendered string fallback used when the chain has been flattened
// (e.g. inside sub-agent JSON results).
func errorKind(err error, msg string) string {
	if err != nil && errors.Is(err, agentcore.ErrProviderStreamIdle) {
		return "stream_idle"
	}
	if msg != "" && agentcore.IsStreamIdleMessage(msg) {
		return "stream_idle"
	}
	return ""
}

// 单调递增的事件 ID 计数器；配合时间戳生成稳定 ID。
var eventIDCounter uint64

func nextEventID() string {
	return fmt.Sprintf("e%d", atomic.AddUint64(&eventIDCounter, 1))
}

// activeCall 记录一次Đang进行的调用（TOOL / DISPATCH）的 ID、起点时间与 summary。
// summary 在Hoàn thành事件时回填进 finish Event，保证 replay（runtime queue）能Hoàn nguyên行内容。
type activeCall struct {
	id      string
	start   time.Time
	summary string
	depth   int
}

// observer 订阅 coordinator 事件流并投影到 Host 的输出通道。
// 它是纯观察者,不参与任何控制决策。
type observer struct {
	unsub   func()
	emitEv  func(Event)
	emitD   func(string)
	emitC   func()
	store   *storepkg.Store // 用于 runtime queue 持久化（ReplayQueue 消费）
	agents  map[string]*agentState
	agentMu sync.Mutex

	// aborting 由 Host 在 Abort()/Close() 入口置位、Start/Resume/Continue 清位。
	// 置位期间所有 context-cancel 衍生的Lỗi事件被抑制（既是用户期望，也避免与
	// "用户手动Tạm dừng"事件重复）。真实异常（非 cancel）仍照常上报。
	aborting atomic.Bool

	streamThinking        bool
	lastThinkingByAgent   map[string]string          // agent → 最近的累积 thinking 文本（用于提取增量 delta）
	dispatchStarts        map[string]*activeCall     // dispatched agent → Đang thực hiện的 DISPATCH 调用
	currentDispatchTarget string                     // Hiện tạiĐang执行的 subagent 名（handleToolEnd 时 Args 可能为Rỗng）
	toolStarts            map[string]*activeCall     // agent → Đang thực hiện的 TOOL 调用
	streamExtractors      map[string]*agentExtractor // agent → Hiện tại工具调用 JSON 参数的内容抽取器
	streamArgPrefixes     map[string]string          // agent/tool → 参数流前缀，用于提前识别轻量标签
	streamArgLabels       map[string]string          // agent/tool → 已从参数流提前识别出的展示名
	streamHasContent      bool                       // Hiện tại streamRound Có czy không已输出过内容（判断Có czy khôngCần段落分隔）
	streamLastByte        byte                       // 最近一次流式输出的末字节（用于精确补齐换行）
}

// agentExtractor 记录某个 agent Hiện tạiĐang抽取的工具名与抽取器实例。
// 工具名用于检测"Mới的工具调用Bắt đầu了"，避免缓存被上一轮残留污染。
type agentExtractor struct {
	tool       string
	ext        *jsonFieldExtractor
	emittedAny bool // 本 extractor Có czy không已经产出过内容；用于首次输出前补段落分隔
}

type agentState struct {
	name    string
	state   string
	tool    string
	summary string
	turn    int
	context AgentContextSnapshot
	updated time.Time
}

func newObserver(coordinator *agentcore.Agent, s *storepkg.Store, emitEv func(Event), emitD func(string), emitC func()) *observer {
	o := &observer{
		emitEv:              emitEv,
		emitD:               emitD,
		emitC:               emitC,
		store:               s,
		agents:              make(map[string]*agentState),
		lastThinkingByAgent: make(map[string]string),
		dispatchStarts:      make(map[string]*activeCall),
		toolStarts:          make(map[string]*activeCall),
		streamExtractors:    make(map[string]*agentExtractor),
		streamArgPrefixes:   make(map[string]string),
		streamArgLabels:     make(map[string]string),
	}
	o.unsub = coordinator.Subscribe(o.handle)
	return o
}

func (o *observer) finalize() {
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	for _, a := range o.agents {
		a.state = "idle"
		a.tool = ""
	}
}

// setAborting 由 Host 在 Abort/Close/Start 等生命周期切换处调用，控制
// "context canceled" 类衍生事件Có czy khôngCần抑制（避免与"用户手动Tạm dừng"重复）。
func (o *observer) setAborting(v bool) { o.aborting.Store(v) }

// isCancellationNoise 判断一个LỗiCó czy không为 abort 引发的衍生噪声。
// 仅当 Host 处于 aborting 态时Quay lại true 才有意义——非 abort 期间的
// context.Canceled 可能反映真实问题（如外部 ctx 被Hủy），仍应上报。
func (o *observer) isCancellationNoise(err error, msg string) bool {
	if !o.aborting.Load() {
		return false
	}
	if err != nil && errors.Is(err, context.Canceled) {
		return true
	}
	return strings.Contains(strings.ToLower(msg), "context canceled")
}

// emitAndLog 用于调用类事件的"Bắt đầu"态：发给 TUI 但不写入 runtime queue，
// 避免 replay 时"Bắt đầu一行、Hoàn thành又一行"重复。slog 由 host.emitEvent 统一记录。
func (o *observer) emitAndLog(ev Event) {
	o.emitEv(ev)
}

// persistEvent 把事件写入 runtime queue（slog 由 host.emitEvent 统一记录）。
func (o *observer) persistEvent(ev Event) {
	if o.store == nil || o.store.Runtime == nil {
		return
	}
	priority := domain.RuntimePriorityBackground
	switch ev.Category {
	case "SYSTEM", "ERROR":
		priority = domain.RuntimePriorityControl
	}
	_, _ = o.store.Runtime.AppendQueue(domain.RuntimeQueueItem{
		Time:     ev.Time,
		Kind:     domain.RuntimeQueueUIEvent,
		Priority: priority,
		Category: ev.Category,
		Summary:  ev.Summary,
		Payload:  ev,
	})
}

func (o *observer) handle(ev agentcore.Event) {
	switch ev.Type {
	case agentcore.EventToolExecStart:
		o.handleToolStart(ev)
	case agentcore.EventToolExecUpdate:
		o.handleToolUpdate(ev)
	case agentcore.EventToolExecEnd:
		o.handleToolEnd(ev)
	case agentcore.EventMessageUpdate:
		o.handleMessageUpdate(ev)
	case agentcore.EventMessageEnd:
		o.streamClear()
	case agentcore.EventTurnStart:
		if ev.Progress != nil && ev.Progress.Kind == agentcore.ProgressTurnCounter {
			o.updateAgent(ev.Progress.Agent, func(a *agentState) {
				a.turn = ev.Progress.Turn
			})
		}
	case agentcore.EventRetry:
		if ev.RetryInfo != nil {
			msg := ""
			if ev.RetryInfo.Err != nil {
				msg = ev.RetryInfo.Err.Error()
			}
			prefix := fmt.Sprintf("Thử lại (%d/%d): ", ev.RetryInfo.Attempt, ev.RetryInfo.MaxRetries)
			retryEv := Event{
				Time:     time.Now(),
				Category: "SYSTEM",
				Summary:  prefix + truncate(msg, 80),
				Detail:   prefix + msg,
				Kind:     errorKind(ev.RetryInfo.Err, msg),
				Level:    "warn",
			}
			o.emitEv(retryEv)
			o.persistEvent(retryEv)
		}
	case agentcore.EventError:
		if ev.Err != nil {
			fullMsg := ev.Err.Error()
			if o.isCancellationNoise(ev.Err, fullMsg) {
				// 用户主动 abort 衍生的 ctx-cancel Lỗi；已有"用户手动Tạm dừng"事件，不再重复刷屏。
				o.flushActiveCalls(true)
				slog.Debug("suppressed cancel-derived error", "module", "agent", "msg", fullMsg)
				return
			}
			o.flushActiveCalls(true)
			errEv := Event{
				Time:     time.Now(),
				Category: "ERROR",
				Summary:  truncate(fullMsg, 120),
				Detail:   fullMsg,
				Kind:     errorKind(ev.Err, fullMsg),
				Level:    "error",
			}
			o.emitEv(errEv)
			o.persistEvent(errEv)
		}
	}
}

func (o *observer) handleMessageUpdate(ev agentcore.Event) {
	if ev.Delta == "" {
		return
	}
	if ev.DeltaKind == agentcore.DeltaToolCall {
		o.handleCoordinatorToolDelta(ev)
		return
	}
	o.emitStreamDelta(ev.Delta, ev.DeltaKind == agentcore.DeltaThinking)
}

func (o *observer) handleToolStart(ev agentcore.Event) {
	if ev.Tool == "" {
		return
	}
	agent := agentFromEvent(ev)

	// subagent 调用 → DISPATCH 事件（Đang thực hiện）
	if ev.Tool == "subagent" {
		sub := parseSubagentArgs(ev.Args)
		target := sub.agent
		if target == "" {
			target = "subagent"
		}
		dispatchSummary := dispatchSummary(target, sub.task)
		o.updateAgent(agent, func(a *agentState) {
			a.state = "working"
			a.tool = ev.Tool
			a.summary = fmt.Sprintf("%s → %s", agent, dispatchSummary)
		})
		o.currentDispatchTarget = target
		if call, ok := o.dispatchStarts["subagent"]; ok {
			delete(o.dispatchStarts, "subagent")
			o.dispatchStarts[target] = call
			o.updateDispatchSummary(target, dispatchSummary)
			return
		}
		id := nextEventID()
		o.dispatchStarts[target] = &activeCall{id: id, start: time.Now(), summary: dispatchSummary}
		o.emitAndLog(Event{
			ID:       id,
			Time:     time.Now(),
			Category: "DISPATCH",
			Agent:    agent,
			Summary:  dispatchSummary,
			Level:    "info",
		})
		return
	}

	// coordinator 自身工具（Đang thực hiện）
	toolName := displayToolName(ev.Tool, ev.Args)
	if _, ok := o.toolStarts[agent]; ok {
		o.updateToolCallSummary(agent, ev.Tool, toolName)
		return
	}
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = ev.Tool
		a.summary = fmt.Sprintf("%s → %s", agent, toolName)
	})
	id := nextEventID()
	o.toolStarts[agent] = &activeCall{id: id, start: time.Now(), summary: toolName}
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "TOOL",
		Agent:    agent,
		Summary:  toolName,
		Level:    "info",
	})
	o.emitFallbackStreamHeader(ev.Tool)
}

func (o *observer) handleToolUpdate(ev agentcore.Event) {
	if ev.Progress == nil {
		return
	}
	switch ev.Progress.Kind {
	case agentcore.ProgressToolDelta:
		if ev.Progress.Delta != "" {
			o.handleSubagentDelta(ev.Progress)
		}
	case agentcore.ProgressToolStart:
		// 子Proxy内部的工具调用（如 writer → draft_chapter）。
		// 注意：TOOL 行可能已经在流式识别阶段被 handleSubagentDelta 提前发出。
		// 此处：若已发 → 只更Mới summary（args 此时完整，能显示 "tool(第N章)"）；否则正常发。
		if ev.Progress.Agent == "" || ev.Progress.Tool == "" {
			break
		}
		toolName := displayToolName(ev.Progress.Tool, ev.Progress.Args)
		if _, ok := o.toolStarts[ev.Progress.Agent]; ok {
			o.updateToolCallSummary(ev.Progress.Agent, ev.Progress.Tool, toolName)
			o.updateAgent(ev.Progress.Agent, func(a *agentState) {
				a.state = "working"
				a.tool = ev.Progress.Tool
				a.summary = fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName)
			})
			break
		}
		// 未提前发过 → 正常流程
		// （非流式 tool args 的Mô hình不会触发 ensureSubagentToolStarted，
		// fallback header 必须在这条Đường dẫn上补一次，否则 read_chapter 这类
		// Không có extractor 的工具流式面板上就没有 ✻ 头部，紧贴前面思考一段。）
		id := nextEventID()
		o.toolStarts[ev.Progress.Agent] = &activeCall{id: id, start: time.Now(), summary: toolName, depth: 1}
		o.emitAndLog(Event{
			ID:       id,
			Time:     time.Now(),
			Category: "TOOL",
			Agent:    ev.Progress.Agent,
			Summary:  toolName,
			Level:    "info",
			Depth:    1,
		})
		o.updateAgent(ev.Progress.Agent, func(a *agentState) {
			a.state = "working"
			a.tool = ev.Progress.Tool
			a.summary = fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName)
		})
		o.emitFallbackStreamHeader(ev.Progress.Tool)
	case agentcore.ProgressToolEnd:
		delete(o.streamExtractors, ev.Progress.Agent)
		if ev.Progress.Agent == "" {
			return
		}
		call, ok := o.toolStarts[ev.Progress.Agent]
		if !ok {
			return
		}
		delete(o.toolStarts, ev.Progress.Agent)
		// 同 ID 更Mới事件：TUI 按 ID 定位原 TOOL 行，回填 FinishedAt / Duration。
		// Summary / Depth 也带上，保证 runtime queue replay 时能Hoàn nguyên完整行。
		finishEv := Event{
			ID:         call.id,
			Time:       call.start,
			FinishedAt: time.Now(),
			Category:   "TOOL",
			Agent:      ev.Progress.Agent,
			Summary:    call.summary,
			Level:      "info",
			Depth:      call.depth,
			Duration:   time.Since(call.start),
		}
		o.emitEv(finishEv)
		o.persistEvent(finishEv)
	case agentcore.ProgressThinking:
		o.handleThinkingProgress(ev)
	case agentcore.ProgressRetry:
		prefix := fmt.Sprintf("Thử lại (%d/%d): ", ev.Progress.Attempt, ev.Progress.MaxRetries)
		retryEv := Event{
			Time:     time.Now(),
			Category: "SYSTEM",
			Agent:    ev.Progress.Agent,
			Summary:  prefix + truncate(ev.Progress.Message, 80),
			Detail:   prefix + ev.Progress.Message,
			Kind:     errorKind(nil, ev.Progress.Message),
			Level:    "warn",
			Depth:    1,
		}
		o.emitEv(retryEv)
		o.persistEvent(retryEv)
	case agentcore.ProgressToolError:
		delete(o.streamExtractors, ev.Progress.Agent)
		msg := ev.Progress.Message
		if msg == "" {
			msg = "unknown error"
		}
		// 如果有Đang thực hiện的 TOOL 行，原地标记为Thất bại；否则独立追加 ERROR 行。
		if call, ok := o.toolStarts[ev.Progress.Agent]; ok {
			delete(o.toolStarts, ev.Progress.Agent)
			finishEv := Event{
				ID:         call.id,
				Time:       call.start,
				FinishedAt: time.Now(),
				Failed:     true,
				Category:   "TOOL",
				Agent:      ev.Progress.Agent,
				Summary:    call.summary,
				Level:      "error",
				Depth:      call.depth,
				Duration:   time.Since(call.start),
			}
			o.emitEv(finishEv)
			o.persistEvent(finishEv)
		}
		// 附加 ERROR 详情行（补充Lỗi信息，便于排查）
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Agent:    ev.Progress.Agent,
			Summary:  fmt.Sprintf("%s Lỗi: %s", ev.Progress.Tool, truncate(msg, 100)),
			Detail:   fmt.Sprintf("%s Lỗi: %s", ev.Progress.Tool, msg),
			Kind:     errorKind(nil, msg),
			Level:    "error",
			Depth:    1,
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
	case agentcore.ProgressContext:
		o.handleContextProgress(ev)
	}
}

// handleSubagentDelta 分流 subagent 的文本与工具调用参数：
// - DeltaText 直接作为 markdown 流出
// - DeltaToolCall 只对已知的长内容工具（如 draft_chapter.content）抽取字段流出；Khác工具的参数 JSON Tất cả丢弃
func (o *observer) handleSubagentDelta(p *agentcore.ProgressPayload) {
	if p.DeltaKind != agentcore.DeltaToolCall {
		o.emitStreamDelta(p.Delta, false)
		return
	}
	if p.Tool == "" {
		return // 工具名未就绪，下一个 delta 再试
	}

	// 流式识别到工具名时提前发 TOOL Đang thực hiện事件，让 spinner 覆盖整段 LLM 生成期间
	// （否则 draft_chapter 这类工具的"Đang thực hiện"只在真实 Execute 的几十毫秒里显示）。
	// 真正的 ProgressToolStart 到来时识别到 toolStarts 已有记录，只会补齐 summary。
	o.ensureSubagentToolStarted(p.Agent, p.Tool)
	o.updateToolCallSummaryFromDelta(p.Agent, p.Tool, p.Delta)

	cur, ok := o.streamExtractors[p.Agent]
	// 同工具调用 args 已闭合（顶层 } 命中）后，仍可能收到 trailing delta：
	// 某些 provider（deepseek-v4-flash 实测）会把单次 args 拆成多个 chunk，
	// 最末一个 chunk 在 `}` 之后还跟着Rỗng白或重复字符。此时若按"工具名匹配 +
	// Done 即重建"处理，Mới extractor 又会 emit 一次 ✻ header 并把尾段 token
	// 当作Mới args 解析。这些 delta 是冗余尾巴，丢弃即可。
	if ok && cur.tool == p.Tool && cur.ext.Done() {
		return
	}
	// 工具名变了或还没建过：Mới建。
	if !ok || cur.tool != p.Tool {
		ext := newToolExtractor(p.Tool)
		if ext == nil {
			delete(o.streamExtractors, p.Agent)
			return
		}
		cur = &agentExtractor{tool: p.Tool, ext: ext}
		o.streamExtractors[p.Agent] = cur
	}
	if emitted := cur.ext.Feed(p.Delta); emitted != "" {
		if !cur.emittedAny {
			cur.emittedAny = true
			// streamClear 让 extractor 的 ✻ header 落在Mới round 起点，配合
			// renderStreamContent 的 HasPrefix("✻") Kiểm tra走 renderAgentBlock 高亮
			// Đường dẫn；用 ensureStreamParagraphBreak 只插Rỗng行不开 round，✻ 仍会被
			// 前面的 thinking/Chính văn包住，落到 renderChapterBlock 用Mặc định色画掉。
			o.streamClear()
			// streamClear 防御性清Rỗng了 streamExtractors。Hiện tại cur 还要Tiếp tục Feed
			// 本工具调用后续的 delta，必须立刻把它重Mới登记回去；否则下一段 delta
			// 来时会Mới建 extractor，从 args 中段Bắt đầu解析（在嵌套对象的 `{` 处
			// 才进入 psBeforeKey），把 timeline_events.time / foreshadow_updates.id
			// 等当成顶层字段，TUI 上重复出现 ✻ header。
			o.streamExtractors[p.Agent] = cur
		}
		o.emitStreamDelta(emitted, false)
	}
}

func (o *observer) handleCoordinatorToolDelta(ev agentcore.Event) {
	msg, ok := ev.Message.(agentcore.Message)
	if !ok {
		return
	}
	call, ok := latestToolCall(msg)
	if !ok || call.Name == "" {
		return
	}
	if call.Name == "subagent" {
		o.ensureCoordinatorDispatchStarted(call)
		o.updateCoordinatorDispatchSummaryFromDelta(ev.Delta)
		return
	}
	o.ensureCoordinatorToolStarted(call.Name)
	o.updateToolCallSummaryFromDelta("coordinator", call.Name, ev.Delta)
}

func latestToolCall(msg agentcore.Message) (agentcore.ToolCall, bool) {
	calls := msg.ToolCalls()
	if len(calls) == 0 {
		return agentcore.ToolCall{}, false
	}
	return calls[len(calls)-1], true
}

func (o *observer) ensureCoordinatorToolStarted(tool string) {
	const agent = "coordinator"
	if tool == "" {
		return
	}
	if _, ok := o.toolStarts[agent]; ok {
		return
	}
	o.resetStreamArgLabel(agent, tool)
	id := nextEventID()
	o.toolStarts[agent] = &activeCall{id: id, start: time.Now(), summary: tool}
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = tool
		a.summary = fmt.Sprintf("%s → %s", agent, tool)
	})
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "TOOL",
		Agent:    agent,
		Summary:  tool,
		Level:    "info",
	})
	o.emitFallbackStreamHeader(tool)
}

func (o *observer) ensureCoordinatorDispatchStarted(call agentcore.ToolCall) {
	if _, ok := o.dispatchStarts["subagent"]; ok {
		return
	}
	o.resetStreamArgLabel("coordinator", call.Name)
	id := nextEventID()
	o.dispatchStarts["subagent"] = &activeCall{id: id, start: time.Now(), summary: "subagent"}
	o.currentDispatchTarget = "subagent"
	o.updateAgent("coordinator", func(a *agentState) {
		a.state = "working"
		a.tool = call.Name
		a.summary = "coordinator → subagent"
	})
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "DISPATCH",
		Agent:    "coordinator",
		Summary:  "subagent",
		Level:    "info",
	})
}

func (o *observer) updateCoordinatorDispatchSummaryFromDelta(delta string) {
	const key = "subagent"
	prefix := o.streamArgPrefixes[streamArgKey("coordinator", key)] + delta
	if len(prefix) > 1024 {
		prefix = prefix[:1024]
	}
	o.streamArgPrefixes[streamArgKey("coordinator", key)] = prefix

	agent := firstJSONStringField(prefix, "agent")
	if agent == "" {
		return
	}
	task := firstJSONStringField(prefix, "task")
	summary := dispatchSummary(agent, task)
	labelKey := streamArgKey("coordinator", key)
	if o.streamArgLabels[labelKey] == summary {
		return
	}
	o.streamArgLabels[labelKey] = summary
	o.updateDispatchSummary("subagent", summary)
}

func dispatchSummary(agent, task string) string {
	if agent == "" {
		agent = "subagent"
	}
	if task == "" {
		return agent
	}
	firstLine := strings.TrimSpace(strings.SplitN(task, "\n", 2)[0])
	if firstLine == "" {
		return agent
	}
	return agent + "（" + truncate(firstLine, 30) + "）"
}

func (o *observer) updateToolCallSummary(agent, tool, summary string) {
	if agent == "" || summary == "" {
		return
	}
	call, ok := o.toolStarts[agent]
	if !ok || call.summary == summary {
		return
	}
	call.summary = summary
	o.emitEv(Event{
		ID:       call.id,
		Time:     call.start,
		Category: "TOOL",
		Agent:    agent,
		Summary:  summary,
		Level:    "info",
		Depth:    call.depth,
	})
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = tool
		a.summary = fmt.Sprintf("%s → %s", agent, summary)
	})
}

func (o *observer) updateDispatchSummary(target, summary string) {
	if target == "" || summary == "" {
		return
	}
	call, ok := o.dispatchStarts[target]
	if !ok || call.summary == summary {
		return
	}
	call.summary = summary
	o.emitEv(Event{
		ID:       call.id,
		Time:     call.start,
		Category: "DISPATCH",
		Agent:    "coordinator",
		Summary:  summary,
		Level:    "info",
		Depth:    call.depth,
	})
}

func (o *observer) updateToolCallSummaryFromDelta(agent, tool, delta string) {
	key := streamArgKey(agent, tool)
	prefix := o.streamArgPrefixes[key] + delta
	if len(prefix) > 512 {
		prefix = prefix[:512]
	}
	o.streamArgPrefixes[key] = prefix

	summary := streamedToolLabel(tool, prefix)
	if summary == "" {
		return
	}
	if o.streamArgLabels[key] == summary {
		return
	}
	o.streamArgLabels[key] = summary
	o.updateToolCallSummary(agent, tool, summary)
}

func streamArgKey(agent, tool string) string {
	return agent + "\x00" + tool
}

func streamedToolLabel(tool, delta string) string {
	if tool != "save_foundation" || delta == "" {
		return ""
	}
	typ := firstJSONStringField(delta, "type")
	if typ == "" {
		return ""
	}
	return fmt.Sprintf("%s[%s]", tool, typ)
}

func firstJSONStringField(raw, field string) string {
	needle := `"` + field + `"`
	idx := strings.Index(raw, needle)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(needle):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t\r\n")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	var value strings.Builder
	escape := false
	for i := 1; i < len(rest); i++ {
		c := rest[i]
		if escape {
			value.WriteByte(c)
			escape = false
			continue
		}
		switch c {
		case '\\':
			escape = true
		case '"':
			return value.String()
		default:
			value.WriteByte(c)
		}
	}
	return ""
}

func (o *observer) handleThinkingProgress(ev agentcore.Event) {
	agent := ev.Progress.Agent
	thinking := ev.Progress.Thinking
	if agent == "" || thinking == "" {
		return
	}

	prev := o.lastThinkingByAgent[agent]
	delta := thinking
	if strings.HasPrefix(thinking, prev) {
		delta = thinking[len(prev):]
	}
	o.lastThinkingByAgent[agent] = thinking
	if delta == "" {
		return
	}
	o.emitStreamDelta(delta, true)
}

func (o *observer) handleContextProgress(ev agentcore.Event) {
	if ev.Progress == nil || len(ev.Progress.Meta) == 0 {
		return
	}
	var payload struct {
		Tokens        int     `json:"tokens"`
		ContextWindow int     `json:"context_window"`
		Percent       float64 `json:"percent"`
		Scope         string  `json:"scope"`
		Strategy      string  `json:"strategy"`
	}
	if json.Unmarshal(ev.Progress.Meta, &payload) != nil {
		return
	}

	agent := ev.Progress.Agent
	if agent == "" {
		agent = "coordinator"
	}

	// 更Mới agent Chụp（TUI 侧边栏始终可见）
	o.updateAgent(agent, func(a *agentState) {
		a.context = AgentContextSnapshot{
			Tokens:        payload.Tokens,
			ContextWindow: payload.ContextWindow,
			Percent:       payload.Percent,
			Scope:         payload.Scope,
			Strategy:      payload.Strategy,
		}
	})

	level := "info"
	if payload.Percent > 85 {
		level = "warn"
	}
	summary := fmt.Sprintf("%s Ngữ cảnh %.0f%% (%d/%d) 策略: %s", agent, payload.Percent, payload.Tokens, payload.ContextWindow, payload.Strategy)

	depth := 0
	if agent != "coordinator" {
		depth = 1
	}

	if payload.Strategy != "" {
		// 触发了压缩 → 事件流 + 日志
		ctxEv := Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: summary, Level: level, Depth: depth}
		o.emitEv(ctxEv)
		o.persistEvent(ctxEv)
	} else {
		// 普通使用率报告 → 仅日志
		slogLevel := slog.LevelInfo
		if level == "warn" {
			slogLevel = slog.LevelWarn
		}
		slog.Log(context.Background(), slogLevel, summary, "module", "context", "agent", agent)
	}
}

func (o *observer) emitCallFinish(call *activeCall, category, agentName string, failed bool) {
	if call == nil {
		return
	}
	level := "success"
	if failed {
		level = "error"
	}
	finishEv := Event{
		ID:         call.id,
		Time:       call.start,
		FinishedAt: time.Now(),
		Failed:     failed,
		Category:   category,
		Agent:      agentName,
		Summary:    call.summary,
		Level:      level,
		Depth:      call.depth,
		Duration:   time.Since(call.start),
	}
	o.emitEv(finishEv)
	o.persistEvent(finishEv)
}

func (o *observer) flushActiveCalls(failed bool) {
	for target, call := range o.dispatchStarts {
		o.emitCallFinish(call, "DISPATCH", target, failed)
		delete(o.dispatchStarts, target)
	}
	for agent, call := range o.toolStarts {
		o.emitCallFinish(call, "TOOL", agent, failed)
		delete(o.toolStarts, agent)
	}
	clear(o.streamExtractors)
	clear(o.streamArgPrefixes)
	clear(o.streamArgLabels)
	o.currentDispatchTarget = ""
}

func (o *observer) handleToolEnd(ev agentcore.Event) {
	agent := agentFromEvent(ev)
	// 工具Kết thúc：把Trạng thái切回 idle，否则侧边栏会永远停在 working。
	// 子Proxy派遣Kết thúc时 dispatchTarget 的Trạng thái会在下方另行清除。
	o.updateAgent(agent, func(a *agentState) {
		a.tool = ""
		a.state = "idle"
	})
	delete(o.lastThinkingByAgent, agent)

	// 取出Đang thực hiện的 DISPATCH 记录（handleToolEnd 的 ev.Args 可能为Rỗng，从 currentDispatchTarget 取）
	var dispatchCall *activeCall
	var dispatchTarget string
	if ev.Tool == "subagent" {
		dispatchTarget = o.currentDispatchTarget
		o.currentDispatchTarget = ""
		if dispatchTarget == "" {
			if sub := parseSubagentArgs(ev.Args); sub.agent != "" {
				dispatchTarget = sub.agent
			}
		}
		if dispatchTarget == "" {
			dispatchTarget = "subagent"
		}
		if call, ok := o.dispatchStarts[dispatchTarget]; ok {
			dispatchCall = call
			delete(o.dispatchStarts, dispatchTarget)
		}
		// 派遣Kết thúc：把子ProxyTrạng thái复位为 idle（Thành công/Thất bại/LỗiĐường dẫn都Cần此清理）
		if dispatchTarget != "subagent" {
			o.updateAgent(dispatchTarget, func(a *agentState) {
				a.state = "idle"
				a.tool = ""
			})
		}
	}

	// 取出 coordinator 直接工具（非 subagent）的Đang thực hiện记录（罕见，但保证Nhất quán）
	var toolCall *activeCall
	if ev.Tool != "subagent" {
		if call, ok := o.toolStarts[agent]; ok {
			toolCall = call
			delete(o.toolStarts, agent)
		}
	}

	// 统一的调用Hoàn thành态（Thành công/Thất bại），通过同 ID 更Mới原行
	emitFinish := func(call *activeCall, category, agentName string, failed bool) {
		o.emitCallFinish(call, category, agentName, failed)
	}
	emitDispatchFinish := func(failed bool) {
		emitFinish(dispatchCall, "DISPATCH", dispatchTarget, failed)
	}
	emitToolFinish := func(failed bool) {
		emitFinish(toolCall, "TOOL", agent, failed)
	}
	// 兜底：若 subagent Kết thúc时，该 subagent 内部还有未Hoàn thành的 TOOL 调用（比如 ensureSubagentToolStarted
	// 提前发了Đang thực hiện事件，但随后 abort/context cancel 让 ProgressToolEnd 没来），
	// 在这里强制发 finish，避免 TOOL 行永远"Đang thực hiện"。Trạng thái跟随 dispatch 同步。
	flushOrphanSubagentTool := func(failed bool) {
		if dispatchTarget == "" {
			return
		}
		call, ok := o.toolStarts[dispatchTarget]
		if !ok {
			return
		}
		delete(o.toolStarts, dispatchTarget)
		delete(o.streamExtractors, dispatchTarget)
		emitFinish(call, "TOOL", dispatchTarget, failed)
	}

	if ev.IsError {
		depth := 0
		if agent != "coordinator" {
			depth = 1
		}
		errText := ""
		if len(ev.Result) > 0 {
			errText = string(ev.Result)
		}
		// 用户主动 abort 衍生的 ctx-cancel：Trạng thái清理仍要走（dispatch / tool 行必须落回Hoàn thành态），
		// 但Bỏ qua独立 ERROR 行 + Lỗi日志，与 EventError Đường dẫn保持一致。
		if o.isCancellationNoise(nil, errText) {
			slog.Debug("suppressed cancel-derived tool error", "module", "agent", "tool", ev.Tool, "msg", errText)
			flushOrphanSubagentTool(true)
			emitDispatchFinish(true)
			emitToolFinish(true)
			return
		}
		summary := fmt.Sprintf("%s Thất bại", ev.Tool)
		detail := summary
		kind := ""
		if errText != "" {
			kind = errorKind(nil, errText)
			detail = fmt.Sprintf("%s → %s: %s", agent, ev.Tool, errText)
			summary += ": " + truncate(errText, 120)
		}
		flushOrphanSubagentTool(true)
		emitDispatchFinish(true)
		emitToolFinish(true)
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Agent:    agent,
			Summary:  summary,
			Detail:   detail,
			Kind:     kind,
			Level:    "error",
			Depth:    depth,
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
		return
	}

	if errEv, fullErr := o.subagentResultErrorEvent(ev); errEv != nil {
		if o.isCancellationNoise(nil, fullErr) {
			slog.Debug("suppressed cancel-derived subagent error", "module", "agent", "tool", ev.Tool, "msg", fullErr)
			flushOrphanSubagentTool(true)
			emitDispatchFinish(true)
			return
		}
		if dispatchTarget != "" && dispatchTarget != "subagent" {
			errEv.Agent = dispatchTarget
		}
		flushOrphanSubagentTool(true)
		emitDispatchFinish(true)
		o.emitEv(*errEv)
		o.persistEvent(*errEv)
		return
	}

	// subagent Thành côngHoàn thành → 更Mới原 DISPATCH 行为Hoàn thành态（带耗时）
	if ev.Tool == "subagent" {
		flushOrphanSubagentTool(false)
		emitDispatchFinish(false)
		return
	}

	// coordinator 直接工具Thành côngHoàn thành
	emitToolFinish(false)
}

func (o *observer) emitStreamDelta(delta string, thinking bool) {
	if delta == "" {
		return
	}
	if thinking != o.streamThinking {
		o.emitD(utils.ThinkingSep)
		o.streamThinking = thinking
	}
	o.emitD(delta)
	o.streamHasContent = true
	o.streamLastByte = delta[len(delta)-1]
}

// ensureSubagentToolStarted 在流式识别到 tool_call 首次出现时，提前为该 agent
// 登记一次Đang thực hiện的 TOOL 调用，使事件流的 spinner 覆盖"LLM 流式生成 tool_call
// 参数"这一段时间（通常占调用总耗时的 99%）。args 此时尚不完整，暂以纯工具名
// 为 summary；等真正的 ProgressToolStart 到来时会补齐带参数的 summary。
func (o *observer) ensureSubagentToolStarted(agent, tool string) {
	if agent == "" || tool == "" {
		return
	}
	if _, ok := o.toolStarts[agent]; ok {
		return // 已有Đang thực hiện调用，幂等
	}
	o.resetStreamArgLabel(agent, tool)
	id := nextEventID()
	o.toolStarts[agent] = &activeCall{
		id:      id,
		start:   time.Now(),
		summary: tool, // 先用纯工具名，ProgressToolStart 到来时可能更Mới为 tool(第N章)
		depth:   1,
	}
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "TOOL",
		Agent:    agent,
		Summary:  tool,
		Level:    "info",
		Depth:    1,
	})
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = tool
	})
	o.emitFallbackStreamHeader(tool)
}

func (o *observer) resetStreamArgLabel(agent, tool string) {
	key := streamArgKey(agent, tool)
	delete(o.streamArgPrefixes, key)
	delete(o.streamArgLabels, key)
}

// emitFallbackStreamHeader 给未Cấu hình extractor 的工具补一行 ✻ Tiêu đề到流面板。
// 三条Đường dẫn都要调用以保证一致：
//  1. ensureSubagentToolStarted —— subagent 流式 tool args（DeltaToolCall）
//  2. handleToolUpdate ProgressToolStart —— subagent 非流式 tool args
//  3. handleToolStart —— coordinator 自身工具
//
// 缺任何一条，同一个工具就会"writer 调有 ✻、coordinator 调没 ✻"或反过来。
func (o *observer) emitFallbackStreamHeader(tool string) {
	if _, has := toolDisplays[tool]; has {
		return // 有 extractor，header 由 extractor 自行输出
	}
	o.streamClear()
	o.emitStreamDelta(streamHeaderFallback(tool)+"\n", false)
}

// streamHeaderFallback 为未Cấu hình extractor 的工具生成流式 header 文本，
// 让用户即使对轻量Đọc类工具也能看到"在调用什么"。
//
// 前缀 "✻ " 是约定的"agent 调度块"标记 — TUI 的 renderStreamContent 见到这个
// 前缀会走 renderAgentBlock Đường dẫn渲染（图标 + 高亮 label + 分隔线），
// 否则会落到Chính văn块Đường dẫn用终端Mặc định色，header 看起来就是普通Chính văn不醒目。
func streamHeaderFallback(tool string) string {
	label := tool
	switch tool {
	case "ask_user":
		label = "向用户提问"
	}
	return "✻ " + label
}

// streamClear 通知 TUI 开启Mới一轮 streamRound，同时Đặt lại与段落分隔相关的Trạng thái。
// 逻辑上Mới round 是"Rỗng stream"，否则下一次首个 extractor emit 会误补前导Rỗng行。
//
// streamThinking 必须一并Đặt lại：emitStreamDelta 用 streamThinking 跨调用追踪
// 上一段是不是思考。Mới round 内还没输出过任何内容，下一次 emit(thinking=false)
// 不应该再插入 ThinkingSep。否则 fallback header（如 ✻ 读Chương）会被 \x02
// 抢先占头，renderStreamContent 的 HasPrefix("✻") 失配，整段落到Chính vănĐường dẫn
// 再被 ThinkingSep 切分为思考段，title 颜色被画成思考色。
func (o *observer) streamClear() {
	o.emitC()
	o.streamHasContent = false
	o.streamLastByte = 0
	o.streamThinking = false
	// 上一轮的 subagent Kết thúc前 ProgressToolEnd 已 delete，这里防御性清Rỗng。
	if len(o.streamExtractors) > 0 {
		o.streamExtractors = make(map[string]*agentExtractor)
	}
}

func (o *observer) subagentResultErrorEvent(ev agentcore.Event) (*Event, string) {
	if ev.Tool != "subagent" || len(ev.Result) == 0 {
		return nil, ""
	}
	sub := parseSubagentArgs(ev.Args)
	errMsg := parseSubagentResultError(ev.Result)
	if errMsg == "" {
		return nil, ""
	}

	target := "subagent"
	if sub.agent != "" {
		target = sub.agent
	}
	fullErr := fmt.Sprintf("%s Thất bại: %s", target, errMsg)
	return &Event{
		Time:     time.Now(),
		Category: "ERROR",
		Agent:    "coordinator",
		Summary:  fmt.Sprintf("%s Thất bại: %s", target, truncate(errMsg, 120)),
		Detail:   fullErr,
		Kind:     errorKind(nil, errMsg),
		Level:    "error",
	}, fullErr
}

func (o *observer) updateAgent(name string, fn func(*agentState)) {
	if name == "" {
		return
	}
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	a, ok := o.agents[name]
	if !ok {
		a = &agentState{name: name, state: "idle"}
		o.agents[name] = a
	}
	fn(a)
	a.updated = time.Now()
}

func (o *observer) agentSnapshots() []AgentSnapshot {
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	snaps := make([]AgentSnapshot, 0, len(o.agents))
	for _, a := range o.agents {
		snaps = append(snaps, AgentSnapshot{
			Name:      a.name,
			State:     a.state,
			Summary:   a.summary,
			Tool:      a.tool,
			Turn:      a.turn,
			Context:   a.context,
			UpdatedAt: a.updated,
		})
	}
	return snaps
}

func agentFromEvent(ev agentcore.Event) string {
	if ev.Progress != nil && ev.Progress.Agent != "" {
		return ev.Progress.Agent
	}
	return "coordinator"
}

func displayToolName(tool string, args json.RawMessage) string {
	if len(args) == 0 {
		return tool
	}
	switch tool {
	case "save_foundation":
		var p struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(args, &p) == nil && p.Type != "" {
			return fmt.Sprintf("%s[%s]", tool, p.Type)
		}
	case "commit_chapter", "plan_chapter", "draft_chapter", "check_consistency":
		var p struct {
			Chapter int `json:"chapter"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			return fmt.Sprintf("%s(第%d章)", tool, p.Chapter)
		}
	case "save_review":
		var p struct {
			Chapter int    `json:"chapter"`
			Scope   string `json:"scope"`
			Verdict string `json:"verdict"`
		}
		if json.Unmarshal(args, &p) == nil {
			label := ""
			switch p.Scope {
			case "arc":
				label = "本弧"
			case "global":
				label = "全局"
			default:
				if p.Chapter > 0 {
					label = fmt.Sprintf("第%d章", p.Chapter)
				}
			}
			if label == "" {
				return tool
			}
			if p.Verdict != "" {
				return fmt.Sprintf("%s(%s·%s)", tool, label, p.Verdict)
			}
			return fmt.Sprintf("%s(%s)", tool, label)
		}
	case "novel_context":
		var p struct {
			Chapter int `json:"chapter"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			return fmt.Sprintf("%s(第%d章)", tool, p.Chapter)
		}
	case "read_chapter":
		var p struct {
			Chapter   int    `json:"chapter"`
			Source    string `json:"source"`
			Character string `json:"character"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			suffix := ""
			if p.Character != "" {
				suffix = "·" + p.Character + "对话"
			} else if p.Source == "draft" {
				suffix = "·Bản nháp"
			}
			return fmt.Sprintf("%s(第%d章%s)", tool, p.Chapter, suffix)
		}
	}
	return tool
}

type subagentInvocation struct {
	agent string
	task  string
}

func parseSubagentResultError(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	// 主流Lỗi：{"error": "..."} 对象（unknown agent / invalid model / 子Proxy执行Thất bại）
	var obj struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(result, &obj); err == nil && obj.Error != "" {
		return obj.Error
	}
	// 兼容 agentcore SubAgentTool 的裸字符串LỗiQuay lại：
	// "Invalid parameters: ..." / "background mode requires ..." / "Too many parallel tasks ..."
	// 这些是 tool 层参数校验Thất bại，is_error=false 但内容是Lỗi说明，需识别为Lỗi避免误判为Thành công。
	var s string
	if json.Unmarshal(result, &s) == nil && isSubagentErrorString(s) {
		return s
	}
	return ""
}

var subagentErrorPrefixes = []string{
	"Invalid parameters",
	"background mode requires",
	"Too many parallel tasks",
}

func isSubagentErrorString(s string) bool {
	for _, p := range subagentErrorPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func parseSubagentArgs(args json.RawMessage) subagentInvocation {
	if len(args) == 0 {
		return subagentInvocation{}
	}
	var p struct {
		Agent string `json:"agent"`
		Task  string `json:"task"`
	}
	if json.Unmarshal(args, &p) == nil && p.Agent != "" {
		return subagentInvocation{agent: p.Agent, task: p.Task}
	}
	return subagentInvocation{}
}
