package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/progress"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
)

// errorKind classifies a runtime error into a stable, short label for log
// filtering and alert routing. Returns "" when no special tag applies.
//
// err is the live error chain (may be nil after JSON serialization); msg is
// the rendered string fallback used when the chain has been flattened
// (e.g. inside sub-agent JSON results).
func errorKind(err error, msg string) string {
	if kind := chainErrorKind(err); kind != "" {
		return kind
	}
	if msg == "" {
		return ""
	}
	if err != nil {
		// 链上已经给不出类型，文本兜底时排除自身（避免重复分类同一串文本）。
		if msg == err.Error() {
			return ""
		}
	}
	return textErrorKind(msg)
}

// chainErrorKind 从活的错误链上取类型：agentcore 的终止哨兵优先，其次交给
// litellm 的 provider 错误类型。
func chainErrorKind(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, agentcore.ErrMaxTurns):
		return "max_turns"
	}
	if typ := litellm.ErrorTypeOf(err); typ != "" {
		return string(typ)
	}
	return textErrorKind(err.Error())
}

// textErrorKind 只在错误链被压平成字符串（子代理 JSON 结果）时才有信息可用。
func textErrorKind(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "tool argument validation failed"):
		return "tool_validation"
	case strings.Contains(lower, "too many concurrent requests"):
		return "overloaded"
	case strings.Contains(lower, "stream idle timeout"):
		return "stream_idle"
	// providerError 会把 litellm 的结构化类型附在文本末尾。
	// HTTP/2 INTERNAL_ERROR 本身没有可分类关键词，保留这个显式 network 标记即可。
	case strings.Contains(lower, "[network,"):
		return "network"
	// 供应商把网络错误扁平化成纯文本时（v1.8.3 由 ClassifyProvider 兜底），
	// 这些词仍应按 network 处理，否则瞬态故障会被当成语义失败计入僵局。
	case strings.Contains(lower, "eof"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "broken pipe"):
		return "network"
	}
	return ""
}

// 单调递增的事件 ID 计数器；配合时间戳生成稳定 ID。
var eventIDCounter uint64

func nextEventID() string {
	return fmt.Sprintf("e%d", atomic.AddUint64(&eventIDCounter, 1))
}

// activeCall 记录一次正在进行的调用的 ID、起点时间与 summary。
// summary 在完成事件时回填进 finish Event，保证 replay（runtime queue）能还原行内容。
type activeCall struct {
	id      string
	start   time.Time
	summary string
	depth   int
}

// observer 把 Engine 派发与 Worker 进度投影到 Host 的输出通道。
// 它是纯观察者,不参与任何控制决策。
type observer struct {
	emitEv  func(Event)
	emitD   func(string)
	emitC   func()
	store   *storepkg.Store // 用于 runtime queue 持久化（ReplayQueue 消费）
	agents  map[string]*agentState
	agentMu sync.Mutex

	streamThinking      bool
	lastThinkingByAgent map[string]string          // agent → 最近的累积 thinking 文本（用于提取增量 delta）
	dispatchStarts      map[string]*activeCall     // dispatched agent → 进行中的 DISPATCH 调用
	modelStarts         map[string]*activeCall     // agent → 进行中的模型响应
	toolStarts          map[string]*activeCall     // agent → 进行中的 TOOL 调用
	streamExtractors    map[string]*agentExtractor // agent → 当前工具调用 JSON 参数的内容抽取器
	streamTools         map[string]map[int]string  // agent → 流式块 index → 工具名（ToolUseDelta 只带 index）
	retryEvents         map[string]string          // retry scope → event ID，用同一行原地更新 (2/7)
	streamHasContent    bool                       // 当前 streamRound 是否已输出过内容（判断是否需要段落分隔）
	streamLastByte      byte                       // 最近一次流式输出的末字节（用于精确补齐换行）
}

// agentExtractor 记录某个 agent 当前正在抽取的工具名与抽取器实例。
// 工具名用于检测"新的工具调用开始了"，避免缓存被上一轮残留污染。
type agentExtractor struct {
	tool       string
	ext        *jsonFieldExtractor
	emittedAny bool // 本 extractor 是否已经产出过内容；用于首次输出前补段落分隔
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

func newObserver(s *storepkg.Store, emitEv func(Event), emitD func(string), emitC func()) *observer {
	return &observer{
		emitEv:              emitEv,
		emitD:               emitD,
		emitC:               emitC,
		store:               s,
		agents:              make(map[string]*agentState),
		lastThinkingByAgent: make(map[string]string),
		dispatchStarts:      make(map[string]*activeCall),
		modelStarts:         make(map[string]*activeCall),
		toolStarts:          make(map[string]*activeCall),
		streamExtractors:    make(map[string]*agentExtractor),
		streamTools:         make(map[string]map[int]string),
		retryEvents:         make(map[string]string),
	}
}

// ── Engine 直驱入口 ──
//
// Engine 直接运行 Worker，事件来源分为三条:
//  1. dispatchStart/dispatchFinish —— Engine 在派发边界直接调用(DISPATCH 行)
//  2. handleWorkerEvent —— Worker 运行事件的真实边界(模型响应/工具调用/重试)，
//     由 AgentLoop 的 Emit 转发，负责 MODEL 行与流式正文/thinking/工具参数
//  3. workerProgress —— 工具自己上报的进度(progress.Report)，由 handleToolUpdate
//     统一处理 TOOL/流式正文/thinking/retry/context 行

// dispatchStart 记录一次 Worker 派发开始并发 DISPATCH 行。
func (o *observer) dispatchStart(agent, task, reason string) {
	summary := dispatchSummary(agent, task)
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = ""
		a.summary = fmt.Sprintf("engine → %s", summary)
	})
	id := nextEventID()
	o.dispatchStarts[agent] = &activeCall{id: id, start: time.Now(), summary: summary}
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "DISPATCH",
		Agent:    agent,
		Summary:  summary,
		Detail:   dispatchDetail(task, reason),
		Level:    "info",
	})
}

// dispatchFinish 把 DISPATCH 行落成完成态并复位 Worker 状态;
// 清理该 Worker 名下未结束的 MODEL / TOOL 行。
func (o *observer) dispatchFinish(agent string, runErr error) {
	o.updateAgent(agent, func(a *agentState) {
		a.state = "idle"
		a.tool = ""
	})
	delete(o.lastThinkingByAgent, agent)
	o.clearStreamTool(agent)
	if call, ok := o.modelStarts[agent]; ok {
		delete(o.modelStarts, agent)
		o.emitCallFinish(call, "MODEL", agent, runErr)
	}
	if call, ok := o.toolStarts[agent]; ok {
		delete(o.toolStarts, agent)
		delete(o.streamExtractors, agent)
		o.emitCallFinish(call, "TOOL", agent, runErr)
	}
	if call, ok := o.dispatchStarts[agent]; ok {
		delete(o.dispatchStarts, agent)
		o.emitCallFinish(call, "DISPATCH", agent, runErr)
	}
	o.streamClear()
}

// workerProgress 把 Worker 的进度中继投影到 TOOL/流式正文/thinking/retry 行。
func (o *observer) workerProgress(p progress.Event) {
	o.handleToolUpdate(p)
}

func (o *observer) finalize() {
	o.agentMu.Lock()
	defer o.agentMu.Unlock()
	for _, a := range o.agents {
		a.state = "idle"
		a.tool = ""
	}
}

func (o *observer) retryEventID(scope string, attempt int) string {
	if strings.TrimSpace(scope) == "" {
		scope = "engine"
	}
	if o.retryEvents == nil {
		o.retryEvents = make(map[string]string)
	}
	if attempt <= 1 || o.retryEvents[scope] == "" {
		o.retryEvents[scope] = nextEventID()
	}
	return o.retryEvents[scope]
}

// emitAndLog 用于调用类事件的"开始"态：发给 TUI 但不写入 runtime queue，
// 避免 replay 时"开始一行、完成又一行"重复。slog 由 host.emitEvent 统一记录。
func (o *observer) emitAndLog(ev Event) {
	o.emitEv(ev)
}

// persistEvent 把事件写入 runtime queue（slog 由 host.emitEvent 统一记录）。
func (o *observer) persistEvent(ev Event) {
	if o.store == nil || o.store.Runtime == nil {
		return
	}
	priority := domain.RuntimePriorityBackground
	switch {
	case ev.Level == "error":
		priority = domain.RuntimePriorityControl
	case ev.Category == "SYSTEM" || ev.Category == "ERROR":
		priority = domain.RuntimePriorityControl
	}
	if _, err := o.store.Runtime.AppendQueue(domain.RuntimeQueueItem{
		Time:     ev.Time,
		Priority: priority,
		Category: ev.Category,
		Summary:  ev.Summary,
		Payload:  ev,
	}); err != nil {
		slog.Warn("运行事件持久化失败", "module", "observer", "category", ev.Category, "err", err)
	}
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
