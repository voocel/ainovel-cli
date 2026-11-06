package agents

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// ErrUnknownWorker 报告 Run 请求了未注册的 Worker 名。
// 类型化是为了让 Engine 区分"配置错误、重试必然同错"（确定性错误）与瞬时失败。
var ErrUnknownWorker = errors.New("agents: unknown worker")

// StopHook 是 Worker 结束前的决策钩子，对应 agentcore.Config.OnStop：
//   - 返回空消息 = 允许结束；
//   - 返回消息 = 注入 history 继续跑（StopGuard 的"拦截催促"）；
//   - 返回 error = 以该错误结束本轮 run（StopGuard 的"升级终止"，
//     错误链可用 errors.Is(err, guard.ErrStopGuardEscalated) 匹配）。
type StopHook func(ctx context.Context, s agentcore.StopInfo) ([]agentcore.Message, error)

// WorkerSet 是一组可程序化调用的 Worker（architect_short/long、writer、editor）。
//
// 每个 Worker 一条独立 history，跨 Run 累积：agentcore.Run 是无状态的，
// 返回结束时的 history（含压缩替换），这里负责存回。Engine 只依赖这个类型化
// 入口，没有 LLM 工具层（docs/engine-rfc.md §1）。
type WorkerSet struct {
	mu    sync.Mutex
	cfgs  map[string]agentcore.Config
	order []string

	history map[string][]agentcore.Message
	locks   map[string]*sync.Mutex

	// stopFactories 按 run 的任务文本建立 OnStop，供任务感知的 StopGuard 使用
	// （editor 被派生成摘要时要否决"只做复核"的提前退出）。
	stopFactories map[string]func(task string) StopHook
	// record 是 MessageEnd 的落盘回调（store 的 session JSONL）。
	record func(agentName, task string, msg agentcore.Message)
	// observer 收到每个原始 run 事件，先于 record 调用。
	observer func(agent string, ev agentcore.Event)
}

// NewWorkerSet 用 agentcore.Config 组装 WorkerSet。order 决定 Names 的顺序；
// 为空时按名排序，保证确定性。
func NewWorkerSet(cfgs map[string]agentcore.Config, order []string) *WorkerSet {
	w := &WorkerSet{
		cfgs:          make(map[string]agentcore.Config, len(cfgs)),
		order:         slices.Clone(order),
		history:       make(map[string][]agentcore.Message, len(cfgs)),
		locks:         make(map[string]*sync.Mutex, len(cfgs)),
		stopFactories: make(map[string]func(task string) StopHook, len(cfgs)),
	}
	maps.Copy(w.cfgs, cfgs)
	if len(w.order) == 0 {
		w.order = slices.Sorted(maps.Keys(w.cfgs))
	}
	for name := range w.cfgs {
		w.locks[name] = &sync.Mutex{}
	}
	return w
}

// Names 返回 Worker 名，顺序稳定（构造时的 order）。
func (w *WorkerSet) Names() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.order)
}

// SetEventObserver 安装每个 run 的原始事件观察者（TUI 实时预览用）。
// 回调在 run 的执行 goroutine 上同步调用，必须非阻塞。
// 它在 MessageEnd 落盘之前收到事件，顺序与 run 一致。
func (w *WorkerSet) SetEventObserver(fn func(agent string, ev agentcore.Event)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.observer = fn
}

// SetThinking 覆盖某 Worker 的推理配置；零值 Thinking 表示清除覆盖，
// 沿用 provider 默认。下一次 Run 生效。
func (w *WorkerSet) SetThinking(name string, t litellm.Thinking) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cfg, ok := w.cfgs[name]
	if !ok {
		return
	}
	if t == (litellm.Thinking{}) {
		cfg.Model.Request.Thinking = nil
	} else {
		cfg.Model.Request.Thinking = &t
	}
	w.cfgs[name] = cfg
}

// Run 在 name 的 history 末尾追加 task 并运行，直到 run 结束。
// 返回最后一条 assistant 消息的文本；run 失败时返回空文本与错误
// （history 仍保存，供下一次 Run 续跑或排查）。
func (w *WorkerSet) Run(ctx context.Context, name, task string) (string, error) {
	w.mu.Lock()
	if _, ok := w.cfgs[name]; !ok {
		w.mu.Unlock()
		return "", fmt.Errorf("%w: %q（可用：%v）", ErrUnknownWorker, name, w.order)
	}
	lock := w.locks[name]
	if lock == nil {
		lock = &sync.Mutex{}
		w.locks[name] = lock
	}
	w.mu.Unlock()

	// 同一 Worker 的 history 只能串行推进；不同 Worker 之间互不阻塞。
	lock.Lock()
	defer lock.Unlock()

	w.mu.Lock()
	cfg := w.cfgs[name]
	history := w.history[name]
	stopFactory := w.stopFactories[name]
	observer, record := w.observer, w.record
	w.mu.Unlock()

	cfg.Emit = func(ev agentcore.Event) error {
		if observer != nil {
			observer(name, ev)
		}
		// MessageEnd 是消息进入 history 的唯一入口，也是会话落盘点；
		// 记录失败由 store 内部记日志，不阻断 run（沿用旧 OnMessage 语义）。
		if me, ok := ev.(agentcore.MessageEnd); ok && record != nil {
			record(name, task, me.Message)
		}
		return nil
	}
	if stopFactory != nil {
		cfg.OnStop = stopFactory(task)
	}

	next, err := agentcore.Run(ctx, cfg, history, agentcore.UserText(task))

	w.mu.Lock()
	w.history[name] = next
	w.mu.Unlock()

	if err != nil {
		return "", err
	}
	return agentcore.LastResponse(next).Text(), nil
}
