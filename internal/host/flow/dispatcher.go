package flow

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/voocel/agentcore"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// Dispatcher 订阅 Coordinator 事件，在子代理返回时计算路由并下达 Host 指令。
//
// 生命周期：Attach 返回一个 detach 函数；关闭 Host 时调用释放订阅。
type Dispatcher struct {
	coordinator *agentcore.Agent
	store       *storepkg.Store

	enabled atomic.Bool // 由 Host 控制是否派发（启动完成前应关）

	// 去重：记住最近一次派发的 Agent+Task，完全相同时跳过。
	// 主要挡两种情况：
	//   1. LLM 纯文字 turn 偶尔也会触发 ToolExecEnd?（防御）
	//   2. 多次 subagent 调用间状态未推进（例：subagent 报错后 coordinator 重派，Router 也会再次派同一章）
	// FollowUp 语义是 append，若不去重会把同一条指令重复压进 followUpQ，污染 Coordinator 上下文。
	lastMu   sync.Mutex
	lastSent *Instruction
}

// NewDispatcher 创建 Dispatcher。使用前需调用 Attach 订阅事件。
func NewDispatcher(coordinator *agentcore.Agent, store *storepkg.Store) *Dispatcher {
	d := &Dispatcher{coordinator: coordinator, store: store}
	return d
}

// Enable 打开路由派发；关闭时 EventToolExecEnd 到达不会发 FollowUp。
// Host 在 Start/Resume 完成首条 prompt 之后启用，避免与启动流程冲突。
func (d *Dispatcher) Enable()  { d.enabled.Store(true) }
func (d *Dispatcher) Disable() { d.enabled.Store(false) }

// Attach 订阅 Coordinator 事件；返回的函数在关闭时调用以解绑。
func (d *Dispatcher) Attach() func() {
	return d.coordinator.Subscribe(d.handle)
}

func (d *Dispatcher) handle(ev agentcore.Event) {
	if !d.enabled.Load() {
		return
	}
	// 精确触发点：子代理调用成功返回。
	// 不用 EventTurnEnd，因为 agentcore 每次 LLM call 完成都会 emit TurnEnd，
	// 会把同一条指令重复压进 followUpQ；查询类 Steer 由 coordinator.md 约束在
	// 同一 turn 内继续调 subagent，从而命中这个触发点。
	if ev.Type != agentcore.EventToolExecEnd || ev.Tool != "subagent" || ev.IsError {
		return
	}
	d.Dispatch()
}

// Dispatch 立即计算路由并下达指令；可被 Host 在特殊时机（如 Resume 后）主动调用。
func (d *Dispatcher) Dispatch() {
	state := LoadState(d.store)
	inst := Route(state)
	if inst == nil {
		return
	}
	if d.dedupe(inst) {
		slog.Debug("flow router skip duplicate", "module", "host.flow", "agent", inst.Agent, "task", inst.Task)
		return
	}
	// Writer 任务：在派发同一刻把章节标为进行中，UI 右侧大纲立即反映"▸ 进行中"，
	// 不用等 plan_chapter 真正执行（plan_chapter 会再调一次 StartChapter，幂等）。
	if inst.Agent == "writer" && inst.Chapter > 0 && d.store != nil {
		if err := d.store.Progress.StartChapter(inst.Chapter); err != nil {
			slog.Warn("flow router pre-mark in-progress failed", "module", "host.flow", "chapter", inst.Chapter, "err", err)
		}
	}
	msg := FormatMessage(inst)
	slog.Debug("flow router dispatch", "module", "host.flow", "agent", inst.Agent, "reason", inst.Reason)
	d.coordinator.FollowUp(agentcore.UserMsg(msg))
}

// dedupe 返回 true 表示本次指令与上次相同，应跳过。
// 用 Agent+Task 相等性（不比 Reason，因为 Reason 是给人看的辅助文本）。
func (d *Dispatcher) dedupe(next *Instruction) bool {
	d.lastMu.Lock()
	defer d.lastMu.Unlock()
	if d.lastSent != nil && d.lastSent.Agent == next.Agent && d.lastSent.Task == next.Task {
		return true
	}
	copy := *next
	d.lastSent = &copy
	return false
}

// ResetDedupe 清空去重缓存。Resume / 新 Start 时 Host 调用，确保恢复或新建后能派发首条指令。
func (d *Dispatcher) ResetDedupe() {
	d.lastMu.Lock()
	defer d.lastMu.Unlock()
	d.lastSent = nil
}
