package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"

	"github.com/voocel/ainovel-cli/internal/arbiter"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/notify"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// engine 是确定性执行引擎:读事实 → Route → 前置校验 → 直接运行 Worker →
// 检查推进 → 循环;语义场景按需咨询 Arbiter。它执行决定,不参与文学判断
// (docs/engine-rfc.md)。单 goroutine 串行,控制状态只在循环边界变更。
type engine struct {
	store   *storepkg.Store
	workers *subagent.Tool

	arbiterModel    agentcore.ChatModel
	failurePrompt   string
	planStartPrompt string // 启动裁定系统提示词:裁定从未完成时引擎据 StartPrompt 现场补裁
	style           string // 风格名,补裁时传给 DecidePlanStart
	// reconsult 把过期干预送回 host 的完整裁定路径(持久化/审计/全量动作应用),
	// 异步执行——engine 只丢弃过期派单,不自行做残缺的重新裁定。
	reconsult func(text string)

	observer  *observer
	budget    *BudgetSentinel
	gate      *ChapterAdvanceGate
	refresh   func() // 每次 writer 派发前刷新 RestorePack
	emitEvent func(Event)
	notify    func(kind, level, title, body string)
	onPause   func(summary string) // 引擎自主暂停(僵局/失败裁定 abort):走 host 统一暂停语义(lifecycle=paused)
	onDone    func()               // run 结束(任何原因);host 据 store 事实定终态

	mu      sync.Mutex
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	running bool
	pending []controlOp       // 干预的控制态动作,边界提交
	next    *flow.Instruction // 下一轮优先执行的指令(plan_start / arbiter dispatch)
	// deferGateForNext 只与 next 同生共灭：hold+dispatch 必须先运行配对的
	// editor/writer，让它建立返工队列，随后 Gate 才能判断 rewrites_drained。
	deferGateForNext bool

	// 僵局追踪:上一轮执行后 Route 仍产生同一指令键即累计。
	// Router 指令是任务后置条件的投影；真正完成会让下一指令改变。
	lastKey string
	repeats int
	// 失败重试:同指令键仅重试一次,再败问 Arbiter。
	failedKey string
}

// deadlockConsultAt / deadlockAbortAt:repeats 达到前者问 Arbiter,达到后者硬熔断。
// 确定性 Engine 必须对无进展循环给出明确上界(RFC §5)。
const (
	deadlockConsultAt = 3
	deadlockAbortAt   = 5
)

// controlOp 是干预裁定中修改控制状态的动作(边界提交;RFC §3)。
// text/facts 保留原始咨询上下文:dispatch 对账失败时以新事实重询。
type controlOp struct {
	hold     *arbiter.AdvanceHoldOp
	reopen   *arbiter.ReopenOp
	dispatch *arbiter.DispatchOp
	text     string
	facts    arbiter.InterventionFacts
}

// start 启动引擎循环;已在运行则 no-op(返回 false)。
func (e *engine) start(initial *flow.Instruction) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = agentcore.WithToolProgress(ctx, e.observer.workerProgress)
	e.cancel = cancel
	e.running = true
	// initial 为空时不覆盖 e.next——停机期干预可能已通过 applyControlOp 排入
	// 裁定派单(如 editor 返工),start(nil) 抹掉它会让 Route 派 writer 续写,
	// 与用户意图相反。
	if initial != nil {
		e.next = initial
		e.deferGateForNext = false
	}
	e.lastKey, e.repeats, e.failedKey = "", 0, ""
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.run(ctx)
	}()
	return true
}

// abort 取消当前循环(暂停语义;checkpoint 保证无损)。
func (e *engine) abort() {
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// wait 等待当前 Engine goroutine 完整退出。Host.Close 会先 cancel 再调用它，
// 保证写工具和 runEnded 都结束后才关闭事件通道与退出进程。
func (e *engine) wait() {
	e.wg.Wait()
}

func (e *engine) isRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// enqueue 把干预的控制态动作排入边界队列(引擎运行中);返回 false 表示未运行,
// 调用方应立即自行执行。
func (e *engine) enqueue(op controlOp) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return false
	}
	e.pending = append(e.pending, op)
	return true
}

func (e *engine) run(ctx context.Context) {
	defer func() {
		e.mu.Lock()
		e.running = false
		e.cancel = nil
		leftover := e.pending
		e.pending = nil
		e.mu.Unlock()
		// 退出竞态:enqueue 与退出并发时残留的干预动作不得无声丢弃——
		// hold/reopen 是幂等的事实写入,用独立 ctx 补执行;dispatch 无引擎可派,
		// 恢复 PendingSteer 持久化(host 可能已按"入队成功"清除),下次
		// Resume/Continue 重放整条干预。
		for _, op := range leftover {
			if op.dispatch != nil {
				if op.text != "" {
					if err := e.store.RunMeta.SetPendingSteer(op.text); err != nil {
						slog.Warn("残留干预回存失败", "module", "engine", "err", err)
					}
				}
				e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
					Summary: "引擎已停,裁定派单未执行;干预已保留,继续创作时自动重新裁定"})
				op.dispatch = nil
			}
			if op.hold != nil || op.reopen != nil {
				if err := e.applyControlOp(context.Background(), op); err != nil {
					e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
						Summary: "引擎退出时补提干预失败: " + err.Error()})
				}
			}
		}
		e.onDone()
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		// hold+dispatch 必须先让配对派单建立返工事实；其它情况在派发前统一检查
		// Gate，保证 boundary hold 和无许可 review 不会多跑一个 Worker。
		deferGate := e.applyPendingOps(ctx) || e.nextDefersGate()
		if !deferGate {
			if e.gate.HandleBoundary() {
				return
			}
		}

		inst := e.takeNext()
		if inst == nil {
			state, err := flow.LoadState(e.store)
			if err != nil {
				e.pauseWithNotify(notify.KindWorkerFailure, "路由事实读取失败，已暂停: "+err.Error())
				return
			}
			inst = flow.Route(state)
		}
		if inst == nil {
			var err error
			inst, err = e.planStartFallback(ctx)
			if err != nil {
				e.pauseWithNotify(notify.KindPlanStart, "规划恢复事实读取失败，已暂停: "+err.Error())
				return
			}
		}
		if inst == nil {
			// 语义场景或终态:完本 → 确定性收尾;其余(Steering 残留等)
			// → 自然停机,等用户 Continue / 干预。
			return
		}
		replaced, err := e.precheck(inst)
		if err != nil {
			e.pauseWithNotify(notify.KindWorkerFailure, "派单前置校验失败，已暂停: "+err.Error())
			return
		}
		if replaced != nil {
			inst = replaced
		}
		allowed, gateErr := e.gate.Allow(inst)
		if gateErr != nil {
			e.pauseWithNotify(notify.KindAdvanceGate, "章节推进控制错误，已暂停: "+gateErr.Error())
			return
		}
		if !allowed {
			return
		}
		if stop := e.trackDeadlock(ctx, &inst); stop {
			return
		}
		if inst == nil {
			continue // 僵局裁定要求重算路由
		}

		err = e.runWorker(ctx, inst)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if stop := e.handleWorkerError(ctx, inst, err); stop {
				return
			}
		}

		// 政策边界:预算止损优先于验收/推进暂停。
		if e.budget.HandleBoundary() {
			return
		}
		if e.gate.HandleBoundary() {
			return
		}
	}
}

func (e *engine) takeNext() *flow.Instruction {
	e.mu.Lock()
	defer e.mu.Unlock()
	inst := e.next
	e.next = nil
	e.deferGateForNext = false
	return inst
}

func (e *engine) nextDefersGate() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.next != nil && e.deferGateForNext
}

// planStartFallback 覆盖规划事实缺位、Route 无法推导规划师的两个窗口:
//  1. 裁定已落盘、首个 save_foundation 尚未发生 → 按固化的 PlanStartRecord 续跑,
//     不重新裁定(RFC §6);首个 foundation 落盘后 tier 就位,补齐分支接管。
//  2. 裁定从未完成(启动时模型故障)但输入事实 StartPrompt 在 → 现场补裁。
//     这是首次裁定的重试,不违反"恢复不依赖重新裁定"——那条纪律针对已存在的裁定。
//     补裁失败走显式暂停:启动失败不允许无声停机。
func (e *engine) planStartFallback(ctx context.Context) (*flow.Instruction, error) {
	progress, err := e.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil {
		return nil, nil
	}
	if progress.Phase == domain.PhaseWriting || progress.Phase == domain.PhaseComplete {
		return nil, nil
	}
	meta, err := e.store.RunMeta.Load()
	if err != nil {
		return nil, fmt.Errorf("load run meta: %w", err)
	}
	if meta == nil || meta.PlanningTier != "" {
		return nil, nil
	}
	missing, err := e.store.FoundationMissing()
	if err != nil {
		return nil, fmt.Errorf("load foundation state: %w", err)
	}
	if len(missing) == 0 {
		return nil, nil
	}
	if meta.PlanStart != nil {
		return &flow.Instruction{
			Agent:  meta.PlanStart.Planner,
			Task:   meta.PlanStart.PlannerTask,
			Reason: "按已固化的启动裁定开始规划",
		}, nil
	}
	if meta.StartPrompt == "" {
		return nil, nil
	}
	return e.retryPlanStart(ctx, meta.StartPrompt), nil
}

// retryPlanStart 补裁启动决策并固化(裁定先落事实再执行,与 StartPrepared 同构)。
func (e *engine) retryPlanStart(ctx context.Context, prompt string) *flow.Instruction {
	start := time.Now()
	decision, derr := runObservedDecision(e.observer, "启动补裁", func() (arbiter.PlanStartDecision, error) {
		return arbiter.DecidePlanStart(ctx, e.arbiterModel, e.planStartPrompt, prompt, e.style)
	})
	rec := storepkg.DecisionRecord{Kind: "plan_start", Decider: "arbiter", Input: prompt,
		Reason: decision.Reason, DurationMs: time.Since(start).Milliseconds()}
	if derr == nil {
		if data, err := json.Marshal(decision); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	rec, recErr := e.store.Decisions.Append(rec)
	if recErr != nil {
		slog.Warn("启动补裁审计落盘失败", "module", "engine", "err", recErr)
	}
	if derr != nil {
		e.pauseWithNotify(notify.KindPlanStart, "启动裁定失败,已暂停(请检查模型/网络配置后继续): "+truncate(derr.Error(), 200))
		return nil
	}
	if err := e.store.RunMeta.SetPlanStart(domain.PlanStartRecord{
		RawPrompt: prompt, Planner: decision.Planner, PlannerTask: decision.Task, DecisionID: rec.ID,
	}); err != nil {
		e.pauseWithNotify(notify.KindPlanStart, "启动裁定无法落盘,已暂停: "+err.Error())
		return nil
	}
	e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info",
		Summary: fmt.Sprintf("启动裁定已补齐(规划师: %s——%s)", decision.Planner, decision.Reason)})
	return &flow.Instruction{Agent: decision.Planner, Task: decision.Task, Reason: decision.Reason}
}

// precheck 是原 ToolGate 的确定性化身:不合法的派发直接改写,无需教学文案。
func (e *engine) precheck(inst *flow.Instruction) (*flow.Instruction, error) {
	progress, err := e.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	if progress != nil && progress.Phase == domain.PhaseComplete {
		// 完本期唯一合法出路是 reopen(干预动作),任何派发直接丢弃。
		slog.Warn("完本期派发被丢弃", "module", "engine", "agent", inst.Agent)
		return &flow.Instruction{}, nil // 置空:下轮 Route 归 nil 自然停机
	}
	if inst.Agent == "writer" {
		if progress == nil || progress.Phase != domain.PhaseWriting {
			phase := "<nil>"
			if progress != nil {
				phase = string(progress.Phase)
			}
			return nil, fmt.Errorf("writer 仅能在 writing 阶段派发（当前 phase=%s）: %w", phase, errInvalidWriteTarget)
		}
		ch, err := writerTargetChapter(e.store)
		if err != nil {
			return nil, err
		}
		if ch > 0 {
			if err := tools.EnsureChapterExpanded(e.store, ch); err != nil {
				if !errors.Is(err, errs.ErrToolPrecondition) {
					return nil, err
				}
				// 目标章未展开 → 确定性改派 architect_long 展开(原 gate 的教学文案
				// 是说给 LLM 的;Engine 直接做正确的事)。
				return &flow.Instruction{
					Agent:  "architect_long",
					Task:   fmt.Sprintf("下一弧为骨架(%s)。调用 save_foundation(type=expand_arc) 展开下一弧;若当前卷已写完,改用 type=append_volume 追加并展开下一卷。", err),
					Reason: "写作目标章未展开,先展开再续写",
				}, nil
			}
		}
		e.refresh()
	}
	return nil, nil
}

// writerTargetChapter 推导 writer 下一次派发实际会写的章节(重写队列头,否则下一章)。
func writerTargetChapter(st *storepkg.Store) (int, error) {
	progress, err := st.Progress.Load()
	if err != nil {
		return 0, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil {
		return 0, fmt.Errorf("progress 未初始化")
	}
	if len(progress.PendingRewrites) > 0 {
		return progress.PendingRewrites[0], nil
	}
	return progress.NextChapter(), nil
}

// trackDeadlock 维护僵局计数：连续出现同一 Agent+Task 说明上一轮
// 没有满足路由后置条件。Worker 内部的 plan/draft/edit 等中间 checkpoint
// 只用于恢复和观测，不能重置 Engine 级计数（issue #84）。
// repeats 达阈值时咨询 Arbiter，硬上限直接熔断。
// 返回 stop=true 表示本轮应结束循环;inst 可能被 Arbiter 改写(reroute)或置 nil(重算)。
func (e *engine) trackDeadlock(ctx context.Context, inst **flow.Instruction) (stop bool) {
	in := *inst
	if in == nil || in.Agent == "" {
		*inst = nil
		return false
	}
	key := in.Agent + "\x00" + in.Task
	if key == e.lastKey {
		e.repeats++
	} else {
		e.lastKey, e.repeats = key, 1
	}
	if e.repeats < deadlockConsultAt {
		return false
	}
	if e.repeats >= deadlockAbortAt {
		e.pauseWithNotify(notify.KindDeadlock, fmt.Sprintf("僵局熔断: 指令连续 %d 次无进展(%s),已暂停等待人工介入", e.repeats, in.Agent))
		return true
	}
	// Arbiter 僵局咨询(repeats ∈ [consultAt, abortAt))。裁定 retry 不清零计数。
	facts := e.failureFacts("deadlock", in, nil)
	decision, err := runObservedDecision(e.observer, "僵局裁定", func() (arbiter.FailureDecision, error) {
		return arbiter.DecideFailure(ctx, e.arbiterModel, e.failurePrompt, facts)
	})
	e.recordFailureDecision("deadlock", in, facts, decision, err)
	if err != nil {
		e.pauseWithNotify(notify.KindDeadlock, "僵局裁定失败,已暂停等待人工介入: "+err.Error())
		return true
	}
	switch decision.Action {
	case "retry":
		return false
	case "reroute":
		*inst = &flow.Instruction{Agent: decision.Dispatch.Agent, Task: decision.Dispatch.Task, Reason: decision.Reason}
		return false
	default: // abort
		e.pauseWithNotify(notify.KindDeadlock, "僵局裁定: "+decision.Reason)
		return true
	}
}

// runWorker 直接运行一次子代理:DISPATCH 事件 + 进度中继 + 结果解析。
func (e *engine) runWorker(ctx context.Context, inst *flow.Instruction) error {
	slog.Info("engine 派发", "module", "engine", "agent", inst.Agent, "reason", inst.Reason)
	e.observer.dispatchStart(inst.Agent, inst.Task)
	// Writer 任务预标进行中(与旧 Dispatcher 一致:UI 大纲立即反映"▸ 进行中")。
	if inst.Agent == "writer" && inst.Chapter > 0 {
		if err := e.store.Progress.ValidateChapterWork(inst.Chapter); err != nil {
			e.observer.dispatchFinish(inst.Agent, true)
			return fmt.Errorf("%w: %w", errInvalidWriteTarget, err)
		}
		if err := e.store.Progress.StartChapter(inst.Chapter); err != nil {
			e.observer.dispatchFinish(inst.Agent, true)
			return fmt.Errorf("%w: 预标第 %d 章进行中失败: %w", errInvalidWriteTarget, inst.Chapter, err)
		}
	}

	// Worker 进度经 ctx ToolProgress 中继到 observer。
	runCtx := agentcore.WithToolProgress(ctx, func(p agentcore.ProgressPayload) {
		e.observer.workerProgress(p)
	})
	_, err := e.workers.Run(runCtx, inst.Agent, inst.Task)
	if err == nil {
		// 成功即清失败追踪:同键的下一次失败重新享有"先重试一次"额度。
		e.failedKey = ""
	}
	e.observer.dispatchFinish(inst.Agent, err != nil)
	return err
}

// handleWorkerError 对同一指令先重试一次，再把错误类型和当前事实交给 Arbiter。
// Engine 不硬编码哪些执行错误“必然无法恢复”；语义改派由模型决定，Store 边界继续
// 负责阻止不合法写入。
func (e *engine) handleWorkerError(ctx context.Context, inst *flow.Instruction, werr error) (stop bool) {
	msg := werr.Error()
	e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Agent: inst.Agent,
		Summary: truncate(fmt.Sprintf("%s 失败: %s", inst.Agent, msg), 120), Detail: msg, Level: "error"})

	key := inst.Agent + "\x00" + inst.Task
	if e.failedKey != key {
		// 首败:原指令重试一次(下一轮 Route 重算,事实驱动天然幂等)。
		e.failedKey = key
		return false
	}
	e.failedKey = ""
	facts := e.failureFacts("worker_failure", inst, werr)
	decision, err := runObservedDecision(e.observer, "失败裁定", func() (arbiter.FailureDecision, error) {
		return arbiter.DecideFailure(ctx, e.arbiterModel, e.failurePrompt, facts)
	})
	e.recordFailureDecision("worker_failure", inst, facts, decision, err)
	if err != nil {
		e.pauseWithNotify(notify.KindWorkerFailure, "失败裁定不可用,已暂停等待人工介入: "+msg+contentFilterAdvice(werr))
		return true
	}
	switch decision.Action {
	case "retry":
		return false
	case "reroute":
		e.mu.Lock()
		e.next = &flow.Instruction{Agent: decision.Dispatch.Agent, Task: decision.Dispatch.Task, Reason: decision.Reason}
		e.deferGateForNext = false
		e.mu.Unlock()
		return false
	default: // abort
		e.pauseWithNotify(notify.KindWorkerFailure, "失败裁定: "+decision.Reason+contentFilterAdvice(werr))
		return true
	}
}

// contentFilterAdvice 给内容审核拦截的暂停附上用户可执行的出路。
// 审核是服务商黑盒,预检/规避都不可行,能做的只有把决策递到用户手上;
// 拦截本身不提前熔断——换上下文重派对它有真实自愈率(ch21-24 实测),
// 走完"免费重试→仲裁"再暂停。
func contentFilterAdvice(werr error) string {
	if !errors.Is(werr, agentcore.ErrProviderContentFilter) {
		return ""
	}
	return "。这是服务商内容审核拦截(非本地错误),可选: /model 切到无审核层的服务商后输入「继续」;或修改本章草稿(drafts/)措辞后再继续;原样重试大概率仍被拦"
}

// errInvalidWriteTarget 标记 runWorker 前置校验拦下的非法写作目标，供错误链和
// Arbiter 事实保留稳定语义；是否重试或改派仍由统一失败流程决定。
var errInvalidWriteTarget = errors.New("非法写作目标")

func (e *engine) failureFacts(kind string, inst *flow.Instruction, workerErr error) arbiter.FailureFacts {
	f := arbiter.FailureFacts{Kind: kind, Agent: inst.Agent, Task: inst.Task, Repeats: e.repeats}
	if workerErr != nil {
		f.Error = workerErr.Error()
		f.ErrorKind = agentcore.ErrorKind(workerErr)
	}
	missing, err := e.store.FoundationMissing()
	if err != nil {
		f.FactWarnings = append(f.FactWarnings, "基础设定状态读取失败: "+err.Error())
	} else {
		f.FoundationGap = missing
	}
	p, err := e.store.Progress.Load()
	if err != nil {
		f.FactWarnings = append(f.FactWarnings, "创作进度读取失败: "+err.Error())
	}
	if p != nil {
		f.Phase = string(p.Phase)
		f.NextChapter = p.NextChapter()
		f.PendingQueue = p.PendingRewrites
	}
	return f
}

func (e *engine) recordFailureDecision(kind string, inst *flow.Instruction, facts arbiter.FailureFacts, d arbiter.FailureDecision, derr error) {
	rec := storepkg.DecisionRecord{Kind: kind, Decider: "arbiter", Input: inst.Agent + ": " + inst.Task, Reason: d.Reason}
	if data, err := json.Marshal(facts); err == nil {
		rec.Facts = data
	}
	if derr == nil {
		if data, err := json.Marshal(d); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	if _, err := e.store.Decisions.Append(rec); err != nil {
		slog.Warn("裁定审计落盘失败", "module", "engine", "kind", kind, "err", err)
	}
}

// applyPendingOps 在循环边界提交干预的控制态动作;循环排空——同步重询
// (reconsult)会在应用过程中追加新动作,必须在本边界内消化完,否则中间会
// 多派一个 worker(干预必须先于后续创作生效)。
// 返回是否有 hold+dispatch 必须先执行配对派单；该情况下调用方暂缓 Gate 检查。
func (e *engine) applyPendingOps(ctx context.Context) (deferGate bool) {
	for {
		e.mu.Lock()
		ops := e.pending
		e.pending = nil
		e.mu.Unlock()
		if len(ops) == 0 {
			return deferGate
		}
		for _, op := range ops {
			pairedHoldDispatch := op.hold != nil && !op.hold.Cancel && op.dispatch != nil
			err := e.applyControlOp(ctx, op)
			if err != nil {
				// 动作持久化失败:host 已按"入队成功"清除 PendingSteer,
				// 这里回存整条干预,恢复/继续时重新裁定重试(动作幂等 + 重询按新事实)。
				if op.text != "" {
					if serr := e.store.RunMeta.SetPendingSteer(op.text); serr != nil {
						slog.Warn("干预回存失败", "module", "engine", "err", serr)
					}
				}
				e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
					Summary: "干预动作执行失败,已保留;恢复/继续时自动重试"})
			} else if pairedHoldDispatch && e.nextDefersGate() {
				// 只有 hold 与配对派单都成功落地，才允许绕过本次 Gate。
				// hold 写入失败或派单因事实过期被丢弃时继续绕过，都会让
				// 未受保护的 Worker 前进。
				deferGate = true
			}
		}
	}
}

// applyControlOp 执行单个控制态动作(hold 直写 RunMeta、reopen 调工具内核、dispatch 先对账)。
// 引擎未运行时由 host 在干预路径直接调用;返回首个持久化失败(调用方据此决定是否
// 保留 PendingSteer 供恢复重放)。
func (e *engine) applyControlOp(ctx context.Context, op controlOp) error {
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	if op.dispatch != nil {
		// Expect 必须在 hold 等配对动作落盘前核对。否则派单过期后旧 hold
		// 会残留，并与按新事实重新裁定出的 hold 冲突，最终只暂停却漏做修改。
		fresh, err := arbiter.CollectInterventionFacts(e.store)
		if err != nil {
			return fmt.Errorf("刷新干预事实: %w", err)
		}
		if fresh.Phase != op.facts.Phase || fresh.Flow != op.facts.Flow ||
			fresh.QueueHead() != op.facts.QueueHead() {
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
				Summary: "裁定派单已过时(事实推进),以最新事实重新裁定"})
			e.recordStale(op)
			if op.text != "" && e.reconsult != nil {
				// 同步重询:干预必须先于后续创作生效——异步会让引擎在新裁定
				// 落地前又派一个 worker。新动作由 applyPendingOps 在本边界排空。
				e.reconsult(op.text)
			}
			return nil
		}
	}
	if op.hold != nil {
		if op.hold.Cancel {
			meta, err := e.store.RunMeta.Load()
			if err != nil {
				e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "读取一次性暂停失败: " + err.Error(), Level: "error"})
				return err
			}
			if meta != nil && meta.AdvanceHold != nil {
				if err := e.store.RunMeta.ClearAdvanceHold(*meta.AdvanceHold); err != nil {
					e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "取消一次性暂停失败: " + err.Error(), Level: "error"})
					return err
				}
			}
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "已取消一次性暂停", Level: "info"})
		} else {
			hold := domain.AdvanceHold{After: op.hold.After, Reason: op.hold.Reason}
			if err := e.store.RunMeta.SetAdvanceHold(hold); err != nil {
				e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "设置一次性暂停失败: " + err.Error(), Level: "error"})
				return err // hold 未落盘时关联 dispatch 不得执行
			}
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "已设置一次性暂停: " + op.hold.Reason, Level: "info"})
		}
	}
	if op.reopen != nil {
		args, _ := json.Marshal(map[string]any{"chapters": op.reopen.Chapters, "reason": op.reopen.Reason})
		if _, err := tools.NewReopenBookTool(e.store).Execute(ctx, args); err != nil {
			e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "重开返工失败: " + err.Error(), Level: "error"})
			fail(err)
		} else {
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
				Summary: fmt.Sprintf("已重开全书返工: 第 %v 章入队", op.reopen.Chapters), Level: "info"})
		}
	}
	if op.dispatch != nil {
		// Expect 已在任何配对状态写入前核对。CheckpointSeq 只留审计不参与
		// 对账：干预到达时 worker 多半正在跑，seq 必然推进。
		e.mu.Lock()
		// 已知窗口(best-effort 边界,见 engine-arbiter.md 澄清③):派单自此存于内存,
		// worker 启动前被硬杀(kill -9,defer 不执行)会丢失本次派单意图——
		// 正常退出/Abort 由 run 的 defer 回存 PendingSteer 兜底。
		e.next = &flow.Instruction{Agent: op.dispatch.Agent, Task: interventionDispatchTask(op.dispatch.Task, op.text), Reason: "用户干预裁定"}
		e.deferGateForNext = op.hold != nil && !op.hold.Cancel
		e.mu.Unlock()
	}
	return firstErr
}

// interventionDispatchTask 保留用户原始干预，避免 Arbiter 在转述任务时无意扩大
// 修改目标。下游可以读取更广上下文做判断，但只能把原文当作动作授权来源。
func interventionDispatchTask(task, original string) string {
	task = strings.TrimSpace(task)
	if strings.TrimSpace(original) == "" {
		return task
	}
	return task + "\n\n用户原始干预（本次修改授权的唯一来源；上下文只用于理解，不得扩大目标或范围）：\n" + original
}

func (e *engine) recordStale(op controlOp) {
	rec := storepkg.DecisionRecord{Kind: "decision_stale", Decider: "engine", Input: op.text}
	if data, err := json.Marshal(op.facts); err == nil {
		rec.Facts = data
	}
	if _, err := e.store.Decisions.Append(rec); err != nil {
		slog.Warn("stale 记录失败", "module", "engine", "err", err)
	}
}

// pauseWithNotify 引擎自主暂停(僵局熔断/失败裁定 abort):离屏通知 + 走 host 统一
// 暂停语义(onPause → abortWithEvent:lifecycle=paused + 屏内事件 + cancel ctx)。
func (e *engine) pauseWithNotify(kind, body string) {
	e.notify(kind, "warn", "ainovel: 引擎暂停", body)
	if e.onPause != nil {
		e.onPause(body)
		return
	}
	e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: body, Level: "warn"})
	e.abort()
}

// completionSummary 完本的确定性收尾报告(store 已有全部事实,不花 LLM 调用;RFC 末节)。
func completionSummary(st *storepkg.Store) string {
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return "创作完成"
	}
	var b strings.Builder
	name := progress.NovelName
	if name == "" {
		name = "本书"
	}
	fmt.Fprintf(&b, "《%s》创作完成: 共 %d 章 %d 字", name, len(progress.CompletedChapters), progress.TotalWordCount)
	return b.String()
}
