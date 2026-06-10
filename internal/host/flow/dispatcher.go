package flow

import (
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	storepkg "github.com/Accelerator-mzq/ainovel-cli/internal/store"
	"github.com/voocel/agentcore"
)

// maxCandidateAttempts 是单个 persona 候选连续失败的弃权阈值（并发失败收敛）。
const maxCandidateAttempts = 3

// dispatchCoordinator 是 Dispatcher 依赖的 coordinator 能力子集，便于测试注入 fake。
type dispatchCoordinator interface {
	Subscribe(fn func(agentcore.Event)) func()
	FollowUp(msg agentcore.AgentMessage)
}

// Dispatcher 订阅 Coordinator 事件，在子代理返回时计算路由并下达 Host 指令。
//
// 生命周期：Attach 返回一个 detach 函数；关闭 Host 时调用释放订阅。
type Dispatcher struct {
	coordinator dispatchCoordinator
	store       *storepkg.Store

	enabled atomic.Bool // 由 Host 控制是否派发（启动完成前应关）

	// 竞稿配置；零值（nil Personas）表示未启用，降级为原 LoadState 路径。
	contest ContestConfig

	// 去重：记住最近一次派发指令的键，完全相同时跳过。
	// 主要挡两种情况：
	//   1. LLM 纯文字 turn 偶尔也会触发 ToolExecEnd?（防御）
	//   2. 多次 subagent 调用间状态未推进（例：subagent 报错后 coordinator 重派，Router 也会再次派同一章）
	// FollowUp 语义是 append，若不去重会把同一条指令重复压进 followUpQ，污染 Coordinator 上下文。
	//
	// lastMu 同时保护 lastKey、lastCandChapter、lastCandPersonas 三者：
	// Dispatch 可能被事件 goroutine（consumeLoop 投递）与 Host 主动调用并发进入。
	lastMu  sync.Mutex
	lastKey string

	// 无进展失败收敛（仅并发候选用，内存态、重启清空）：
	// 上一批并发派出的 pending persona 及其所属章。
	lastCandChapter  int
	lastCandPersonas []string
}

// NewDispatcher 创建 Dispatcher。使用前需调用 Attach 订阅事件。
func NewDispatcher(coordinator *agentcore.Agent, store *storepkg.Store) *Dispatcher {
	d := &Dispatcher{coordinator: coordinator, store: store}
	return d
}

// Enable 打开路由派发；关闭时 EventToolExecEnd 到达不会发 FollowUp。
// Host 在 Start/Resume 完成首条 prompt 之后启用，避免与启动流程冲突。
func (d *Dispatcher) Enable() { d.enabled.Store(true) }

// SetContest 注入竞稿配置；Host 在启用竞稿时调用。
func (d *Dispatcher) SetContest(cfg ContestConfig) { d.contest = cfg }

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
	state := LoadStateWithContest(d.store, d.contest)

	// 竞稿提升：有 verdict 未提升时内联提升，再重读。
	if state.ContestEnabled && state.ContestChapter > 0 && state.HasVerdict && !state.IsPromoted {
		if PromoteIfNeeded(d.store, d.contest, state.ContestChapter) {
			state = LoadStateWithContest(d.store, d.contest)
		}
	}

	// 并发候选失败收敛：上一批仍缺候选者计失败，超阈值弃权后重读。
	if state.ContestEnabled && state.ContestConcurrent && state.ContestChapter > 0 && !state.HasVerdict {
		// 持锁快照内存态，避免与本函数末尾的写入并发（Dispatch 可能被事件 goroutine 与
		// Host 主动调用并发进入）。快照后立即释放锁，绝不持锁调 store。
		d.lastMu.Lock()
		lastChapter := d.lastCandChapter
		lastPersonas := d.lastCandPersonas
		d.lastMu.Unlock()
		if lastChapter == state.ContestChapter {
			if failed := failedFromLastBatch(lastPersonas, state); len(failed) > 0 {
				changed, err := d.store.Contest.RecordAttempts(state.ContestChapter, failed, maxCandidateAttempts)
				if err != nil {
					slog.Warn("contest record attempts failed", "module", "host.flow", "chapter", state.ContestChapter, "err", err)
				} else if changed {
					state = LoadStateWithContest(d.store, d.contest)
				}
				// 无进展失败：清空去重键，让本轮候选批作为"显式重试"重新下发。
				// 否则相同批次会被 dedupe 拦截、不发 FollowUp，弃权计数只能靠 StopGuard
				// 非确定性唤醒推进（终审 I-1）。清空后由 Host 确定性驱动重试，累计到阈值
				// 触发弃权、reload 后 Route 产出更小批/降级指令，循环有界收敛。
				d.lastMu.Lock()
				d.lastKey = ""
				d.lastMu.Unlock()
			}
		}
	}

	inst := Route(state)
	if inst == nil {
		return
	}
	if d.dedupe(inst) {
		slog.Debug("flow router skip duplicate", "module", "host.flow", "agent", inst.Agent, "task", inst.Task)
		return
	}

	// 记录本次并发候选批的 pending，供下次失败收敛判定；
	// 非批量派发（单派/judge）清零内存态，防止陈旧 lastCand 误判失败。
	d.lastMu.Lock()
	if len(inst.Batch) > 0 {
		personas := make([]string, 0, len(inst.Batch))
		for _, t := range inst.Batch {
			personas = append(personas, strings.TrimPrefix(t.Agent, "writer_"))
		}
		d.lastCandChapter = inst.Chapter
		d.lastCandPersonas = personas
	} else {
		d.lastCandChapter = 0
		d.lastCandPersonas = nil
	}
	d.lastMu.Unlock()

	// Writer / 批量 writer 任务：派发同刻把章节标为进行中（UI 即时反映）。
	if (strings.HasPrefix(inst.Agent, "writer") || len(inst.Batch) > 0) && inst.Chapter > 0 && d.store != nil {
		if err := d.store.Progress.StartChapter(inst.Chapter); err != nil {
			slog.Warn("flow router pre-mark in-progress failed", "module", "host.flow", "chapter", inst.Chapter, "err", err)
		}
	}

	msg := FormatMessage(inst)
	slog.Debug("flow router dispatch", "module", "host.flow", "agent", inst.Agent, "reason", inst.Reason)
	d.coordinator.FollowUp(agentcore.UserMsg(msg))
}

// dedupeKey 为指令生成去重键：批量按全部 (Agent,Task) 有序拼接，单派按 Agent+Task。
func dedupeKey(i *Instruction) string {
	if len(i.Batch) > 0 {
		parts := make([]string, len(i.Batch))
		for k, t := range i.Batch {
			parts[k] = t.Agent + "\x00" + t.Task
		}
		return "batch\x01" + strings.Join(parts, "\x02")
	}
	return i.Agent + "\x00" + i.Task
}

// failedFromLastBatch 返回上一批派出但仍缺候选且未弃权的 persona（视为本轮失败）。
// 与 PendingCandidates 的区别：本函数作用于上一批 dispatcher 实际派出的 persona 子集，而非全量 Personas。
func failedFromLastBatch(lastPersonas []string, s State) []string {
	var failed []string
	for _, p := range lastPersonas {
		if s.Abandoned[p] {
			continue
		}
		if !s.CandidatesReady[p] {
			failed = append(failed, p)
		}
	}
	return failed
}

// dedupe 返回 true 表示本次指令与上次相同，应跳过。
func (d *Dispatcher) dedupe(next *Instruction) bool {
	d.lastMu.Lock()
	defer d.lastMu.Unlock()
	key := dedupeKey(next)
	if d.lastKey != "" && d.lastKey == key {
		return true
	}
	d.lastKey = key
	return false
}

// ResetDedupe 清空去重缓存与无进展状态。Resume / 新 Start 时 Host 调用。
func (d *Dispatcher) ResetDedupe() {
	d.lastMu.Lock()
	defer d.lastMu.Unlock()
	d.lastKey = ""
	d.lastCandChapter = 0
	d.lastCandPersonas = nil
}

// PromoteIfNeeded 在"有 verdict 且未提升"时执行中选稿提升，返回是否发生了提升。
// 纯 store 操作，幂等：已提升或无 verdict 时返回 false。
func PromoteIfNeeded(store *storepkg.Store, cfg ContestConfig, chapter int) bool {
	if len(cfg.Personas) < 2 || chapter <= 0 {
		return false
	}
	v, err := store.Contest.LoadVerdict(chapter)
	if err != nil || v == nil || v.Promoted {
		return false
	}
	if err := store.Contest.PromoteCandidate(chapter, v.Winner); err != nil {
		slog.Warn("contest promote failed", "module", "host.flow", "chapter", chapter, "winner", v.Winner, "err", err)
		return false
	}
	return true
}
