package host

// Engine 端到端集成测试(engine-rfc.md §7 原型验收):
// 真实 store + 真实 Worker 工具 + 脚本化 ChatModel,验证
//  1. Route 驱动的完整写书链路:写第1章 → 写第2章 → 完本 → 引擎自然停机
//  2. Worker 失败路径:重试一次 → Arbiter worker_failure 裁定 abort → 暂停 + 审计落盘
//  3. 僵局路径:同指令无进展 ×3 → Arbiter deadlock 裁定 → 审计落盘 → abort 停机

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/internal/arbiter"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// scriptedChatModel 按回调产出响应的最小 ChatModel。
type scriptedChatModel struct {
	fn func(msgs []agentcore.Message) agentcore.Message
}

func TestFailureFactsKeepPartialStateAndWarnings(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("test", 3); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "premise.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	e := &engine{store: st}
	workerErr := fmt.Errorf("writer exhausted: %w", agentcore.ErrMaxTurns)
	facts := e.failureFacts("worker_failure", &flow.Instruction{Agent: "writer", Task: "续写"}, workerErr)
	if facts.ErrorKind != "max_turns" || facts.Phase != string(domain.PhaseInit) {
		t.Fatalf("应保留错误类型和可读取的进度事实: %+v", facts)
	}
	if len(facts.FactWarnings) == 0 {
		t.Fatalf("不可读的基础事实必须作为告警交给 Arbiter: %+v", facts)
	}
}

func TestInterventionDispatchTaskPreservesOriginalAuthority(t *testing.T) {
	const task = "检查重复内容并安排必要返工"
	const original = "  后续不要重复解释能力来源；不要改动无关内容。\n"

	got := interventionDispatchTask(task, original)
	if !strings.Contains(got, task) {
		t.Fatalf("派单任务丢失: %q", got)
	}
	if !strings.Contains(got, original) {
		t.Fatalf("用户原始干预未被逐字保留: %q", got)
	}
	if !strings.Contains(got, "修改授权的唯一来源") {
		t.Fatalf("缺少授权边界说明: %q", got)
	}
}

func (m *scriptedChatModel) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{Message: m.fn(msgs)}, nil
}

func (m *scriptedChatModel) GenerateStream(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	resp, _ := m.Generate(ctx, msgs, tools, opts...)
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message, StopReason: resp.Message.StopReason}
	close(ch)
	return ch, nil
}

func (m *scriptedChatModel) SupportsTools() bool { return true }

// editThenCancelModel 复现 #84：每次 Worker 都成功产生一个内容不同的
// edit checkpoint，随后在同一 run 内返回 context canceled，始终没有 commit。
type editThenCancelModel struct {
	edits atomic.Int32
}

func (m *editThenCancelModel) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == agentcore.RoleTool {
		return nil, context.Canceled
	}
	n := int(m.edits.Add(1))
	return &agentcore.LLMResponse{Message: testToolCallMsg("edit_chapter", map[string]any{
		"chapter":    1,
		"old_string": fmt.Sprintf("版本%d", n-1),
		"new_string": fmt.Sprintf("版本%d", n),
	})}, nil
}

func (m *editThenCancelModel) GenerateStream(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.Generate(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message, StopReason: resp.Message.StopReason}
	close(ch)
	return ch, nil
}

func (m *editThenCancelModel) SupportsTools() bool { return true }

func testToolCallMsg(name string, args any) agentcore.Message {
	data, _ := json.Marshal(args)
	return agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{
			ID: "tc-" + name, Name: name, Args: data,
		})},
		StopReason: agentcore.StopReasonToolUse,
	}
}

func testTextMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: agentcore.StopReasonStop,
	}
}

var chapterRe = regexp.MustCompile(`写第 (\d+) 章`)

// scriptedWriterModel 按对话内已有的 tool 结果数决定下一步,
// 走完整 plan → draft → check → commit 序列(真实工具,真实落盘)。
func scriptedWriterModel() *scriptedChatModel {
	return &scriptedChatModel{fn: func(msgs []agentcore.Message) agentcore.Message {
		chapter := 0
		toolResults := 0
		for _, m := range msgs {
			if m.Role == agentcore.RoleUser {
				if match := chapterRe.FindStringSubmatch(m.TextContent()); match != nil {
					chapter, _ = strconv.Atoi(match[1])
				}
			}
			if m.Role == agentcore.RoleTool {
				toolResults++
			}
		}
		switch toolResults {
		case 0:
			return testToolCallMsg("plan_chapter", map[string]any{
				"chapter": chapter, "title": fmt.Sprintf("第%d章", chapter),
				"goal": "推进主线", "conflict": "主角遇阻", "hook": "悬念收尾",
			})
		case 1:
			return testToolCallMsg("draft_chapter", map[string]any{
				"chapter": chapter, "mode": "write",
				"content": strings.Repeat(fmt.Sprintf("第%d章的正文段落，主角在黑暗中摸索前行。", chapter), 20),
			})
		case 2:
			return testToolCallMsg("check_consistency", map[string]any{"chapter": chapter})
		default:
			return testToolCallMsg("commit_chapter", map[string]any{
				"chapter": chapter, "summary": fmt.Sprintf("第%d章摘要", chapter),
				"characters": []string{"主角"}, "key_events": []string{"推进"},
				"hook_type": "crisis",
			})
		}
	}}
}

// newTestEngine 组装带真实 store/observer 的引擎;返回引擎、事件采集与完成信号。
func newTestEngine(t *testing.T, st *storepkg.Store, workers *subagent.Tool, arbiterModel agentcore.ChatModel) (*engine, *[]Event, chan struct{}) {
	t.Helper()
	if err := st.RunMeta.Init("default", "test", "test"); err != nil {
		t.Fatalf("init run meta: %v", err)
	}
	var mu sync.Mutex
	events := &[]Event{}
	done := make(chan struct{}, 1)
	obs := newObserver(st, func(ev Event) {
		mu.Lock()
		*events = append(*events, ev)
		mu.Unlock()
	}, func(string) {}, func() {})
	e := &engine{
		store:           st,
		workers:         workers,
		arbiterModel:    arbiterModel,
		failurePrompt:   "sys",
		planStartPrompt: "sys",
		style:           "default",
		observer:        obs,
		refresh:         func() {},
		emitEvent: func(ev Event) {
			mu.Lock()
			*events = append(*events, ev)
			mu.Unlock()
		},
		notify: func(string, string, string, string) {},
		onDone: func() {
			select {
			case done <- struct{}{}:
			default:
			}
		},
	}
	e.gate = NewChapterAdvanceGate(st, func(string) { e.abort() }, func(string, string) {})
	return e, events, done
}

func waitEngineDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("引擎未在期限内停机")
	}
}

func mustInterventionFacts(t *testing.T, st *storepkg.Store) arbiter.InterventionFacts {
	t.Helper()
	facts, err := arbiter.CollectInterventionFacts(st)
	if err != nil {
		t.Fatalf("CollectInterventionFacts: %v", err)
	}
	return facts
}

func TestEngine_ReviewPermitWritesExactlyOneNewChapter(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("逐章验收试书", 3); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "一", CoreEvent: "a"},
		{Chapter: 2, Title: "二", CoreEvent: "b"},
		{Chapter: 3, Title: "三", CoreEvent: "c"},
	}); err != nil {
		t.Fatal(err)
	}
	writer := subagent.Config{
		Name: "writer", Description: "test writer", Model: scriptedWriterModel(), SystemPrompt: "test",
		Tools: []agentcore.Tool{
			tools.NewPlanChapterTool(st), tools.NewDraftChapterTool(st),
			tools.NewCheckConsistencyTool(st), tools.NewCommitChapterTool(st),
		},
		MaxTurns: 10, StopAfterTools: []string{"commit_chapter"},
	}
	e, _, done := newTestEngine(t, st, subagent.New(writer), nil)
	if err := st.RunMeta.SetAdvanceMode(domain.ChapterAdvanceReview); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.GrantAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: %v", err)
	}
	if len(progress.CompletedChapters) != 1 || progress.CompletedChapters[0] != 1 {
		t.Fatalf("一个许可必须恰好只稳定一个新章: %v", progress.CompletedChapters)
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvancePermitChapter != 0 {
		t.Fatalf("稳定提交后许可必须消费: %+v", meta)
	}
}

func TestEngine_StalePairedDispatchDoesNotBypassHold(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("过期派单试书", 3); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	e, _, _ := newTestEngine(t, st, subagent.New(), nil)
	e.pending = []controlOp{{
		hold:     &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAtBoundary, Reason: "先停下"},
		dispatch: &arbiter.DispatchOp{Agent: "editor", Task: "过期任务"},
		facts:    arbiter.InterventionFacts{Phase: string(domain.PhaseOutline)},
	}}

	if e.applyPendingOps(context.Background()) {
		t.Fatal("事实过期的配对派单未落入 next 时不得绕过 Gate")
	}
	if e.next != nil || e.deferGateForNext {
		t.Fatalf("过期派单不得留下可执行指令: next=%+v defer=%v", e.next, e.deferGateForNext)
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvanceHold != nil {
		t.Fatalf("配对派单过期时不得留下孤立 hold: %+v", meta.AdvanceHold)
	}
	if e.gate.HandleBoundary() {
		t.Fatal("无孤立 hold 时 Gate 不应伪造暂停")
	}
}

// TestEngine_WritesBookToCompletion 完整链路:两章非分层书从 writing 写到 complete。
func TestEngine_WritesBookToCompletion(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("引擎试书", 2); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "第一章", CoreEvent: "开端"},
		{Chapter: 2, Title: "第二章", CoreEvent: "终局"},
	}); err != nil {
		t.Fatalf("outline: %v", err)
	}

	writer := subagent.Config{
		Name: "writer", Description: "test writer",
		Model:        scriptedWriterModel(),
		SystemPrompt: "test",
		Tools: []agentcore.Tool{
			tools.NewPlanChapterTool(st),
			tools.NewDraftChapterTool(st),
			tools.NewCheckConsistencyTool(st),
			tools.NewCommitChapterTool(st),
		},
		MaxTurns:       10,
		StopAfterTools: []string{"commit_chapter"},
	}
	e, events, done := newTestEngine(t, st, subagent.New(writer), nil)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: %v", err)
	}
	if progress.Phase != domain.PhaseComplete {
		t.Fatalf("两章写满应完本, got phase=%s completed=%v", progress.Phase, progress.CompletedChapters)
	}
	if len(progress.CompletedChapters) != 2 {
		t.Fatalf("应完成 2 章, got %v", progress.CompletedChapters)
	}
	// 事件形状:每章一条 DISPATCH(engine 发起),TOOL 行来自进度中继
	var dispatches, toolRows int
	for _, ev := range *events {
		switch ev.Category {
		case "DISPATCH":
			dispatches++
		case "TOOL":
			toolRows++
		}
	}
	if dispatches < 2 {
		t.Fatalf("应至少 2 条 DISPATCH 事件, got %d", dispatches)
	}
	if toolRows == 0 {
		t.Fatal("Worker 工具进度未经中继投影(TOOL 行缺失)")
	}
}

// TestEngine_WorkerFailureConsultsArbiterAndAborts 失败路径:
// 空转 writer 被 StopGuard 升级 → 重试一次 → Arbiter 裁定 abort → 暂停 + 审计。
func TestEngine_WorkerFailureConsultsArbiterAndAborts(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("失败试书", 2); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一", CoreEvent: "s"}}); err != nil {
		t.Fatalf("outline: %v", err)
	}

	var runs atomic.Int32
	// writer 每轮只回文字不落盘 → guard.NewWriterStopGuard 连续拦截后升级 → Execute 报错
	idle := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg("我写完了(其实什么都没做)")
	}}
	writer := subagent.Config{
		Name: "writer", Description: "idle writer",
		Model: idle, SystemPrompt: "test", MaxTurns: 20,
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			runs.Add(1)
			return failNTimesGuard()
		},
	}
	// Arbiter 裁定 abort
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg(`{"action":"abort","dispatch":null,"reason":"writer 反复空转,建议人工检查模型配置"}`)
	}}
	e, _, done := newTestEngine(t, st, subagent.New(writer), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	if got := runs.Load(); got != 2 {
		t.Fatalf("首败应重试一次(共 2 次 spawn), got %d", got)
	}
	recs, err := st.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var found bool
	for _, r := range recs {
		if r.Kind == "worker_failure" && r.Decider == "arbiter" {
			found = true
			if !strings.Contains(string(r.Decision), "abort") {
				t.Fatalf("裁定内容应含 abort: %s", r.Decision)
			}
		}
	}
	if !found {
		t.Fatalf("worker_failure 裁定必须落盘: %+v", recs)
	}
}

// failNTimesGuard 立即升级的 StopGuard(模拟空转熔断)。
func failNTimesGuard() agentcore.StopGuard {
	return func(context.Context, agentcore.StopInfo) agentcore.StopDecision {
		return agentcore.StopDecision{Allow: false, Escalate: true}
	}
}

// TestEngine_RetriesUnfinishedPlanStart 启动裁定失败后的自愈路径:StartPrompt 已落盘、
// PlanStart 缺位(启动时模型故障)→ 引擎起动时现场补裁 → 固化 PlanStartRecord → 派发规划师。
// 规划师不落盘 → 走既有僵局路径停机,证明补裁后引擎回到正常轨道。
func TestEngine_RetriesUnfinishedPlanStart(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("", 0); err != nil {
		t.Fatalf("progress: %v", err)
	}
	// 模拟 StartPrepared 失败现场:输入事实在,裁定事实缺位。
	if err := st.RunMeta.SetStartPrompt("凡人修仙"); err != nil {
		t.Fatalf("start prompt: %v", err)
	}

	// Arbiter:首次调用是补裁(plan_start),之后是僵局咨询(abort 收尾)。
	var arbCalls atomic.Int32
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		if arbCalls.Add(1) == 1 {
			return testTextMsg(`{"planner":"architect_long","task":"围绕凡人修仙规划三卷框架","reason":"长篇修仙题材"}`)
		}
		return testTextMsg(`{"action":"abort","dispatch":null,"reason":"规划师空转,停机"}`)
	}}
	// 规划师成功返回但不落任何盘 → Route 始终返回同一补齐指令 → 僵局。
	architect := subagent.Config{
		Name: "architect_long", Description: "idle planner",
		Model: &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
			return testTextMsg("已规划(其实没有落盘)")
		}},
		SystemPrompt: "test", MaxTurns: 3,
	}
	e, events, done := newTestEngine(t, st, subagent.New(architect), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil || meta.PlanStart == nil {
		t.Fatalf("补裁后 PlanStart 必须固化, meta=%+v err=%v", meta, err)
	}
	if meta.PlanStart.Planner != "architect_long" || meta.PlanStart.RawPrompt != "凡人修仙" || meta.PlanStart.DecisionID == "" {
		t.Fatalf("PlanStartRecord 字段不完整: %+v", meta.PlanStart)
	}
	recs, err := st.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var planStartRec bool
	for _, r := range recs {
		if r.Kind == "plan_start" && strings.Contains(string(r.Decision), "architect_long") {
			planStartRec = true
		}
	}
	if !planStartRec {
		t.Fatalf("补裁必须留下 plan_start 审计: %+v", recs)
	}
	var dispatched, healed bool
	for _, ev := range *events {
		if ev.Category == "DISPATCH" {
			dispatched = true
		}
		if strings.Contains(ev.Summary, "启动裁定已补齐") {
			healed = true
		}
	}
	if !dispatched || !healed {
		t.Fatalf("补裁后应派发规划师并回显补齐事件, dispatched=%v healed=%v", dispatched, healed)
	}
}

// TestEngine_PlanStartRetryFailurePauses 补裁失败不允许无声停机:
// Arbiter 持续不可用 → 显式暂停回显 + plan_start 审计带 error + 零派发。
func TestEngine_PlanStartRetryFailurePauses(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("", 0); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.RunMeta.SetStartPrompt("凡人修仙"); err != nil {
		t.Fatalf("start prompt: %v", err)
	}

	var e *engine
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		e.abort() // 模拟宿主取消持续失败的调用，失败路径由 context 明确结束。
		return testTextMsg("这不是 JSON")
	}}
	e, events, done := newTestEngine(t, st, subagent.New(), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	for _, ev := range *events {
		if ev.Category == "DISPATCH" {
			t.Fatal("补裁失败不得派发任何 worker")
		}
	}
	var paused bool
	for _, ev := range *events {
		if strings.Contains(ev.Summary, "启动裁定失败") {
			paused = true
		}
	}
	if !paused {
		t.Fatalf("补裁失败必须显式回显暂停原因, events=%+v", *events)
	}
	recs, err := st.Decisions.Recent(5)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var errRec bool
	for _, r := range recs {
		if r.Kind == "plan_start" && r.Error != "" && len(r.Decision) == 0 {
			errRec = true
		}
	}
	if !errRec {
		t.Fatalf("失败裁定必须带 error 落盘: %+v", recs)
	}
}

// TestEngine_DeadlockConsultsArbiter 僵局路径:规划补齐指令连续重现
// → 第 3 次咨询 Arbiter → abort 停机 + deadlock 审计。
func TestEngine_DeadlockConsultsArbiter(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("僵局试书", 3); err != nil {
		t.Fatalf("progress: %v", err)
	}
	// 规划期 + tier 已知 + 缺项恒在 → Route 每轮产出同一补齐指令
	if err := st.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("tier: %v", err)
	}

	// architect 无守卫、成功返回但不落任何盘 → Route 指令恒定
	lazy := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg("知道了(什么也不做)")
	}}
	architect := subagent.Config{
		Name: "architect_long", Description: "lazy architect",
		Model: lazy, SystemPrompt: "test", MaxTurns: 5,
	}
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg(`{"action":"abort","dispatch":null,"reason":"规划师反复无产出"}`)
	}}
	e, _, done := newTestEngine(t, st, subagent.New(architect), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	recs, err := st.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var found bool
	for _, r := range recs {
		if r.Kind == "deadlock" && r.Decider == "arbiter" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deadlock 裁定必须落盘: %+v", recs)
	}
}

// TestEngine_IntermediateCheckpointsDoNotMaskDeadlock 锁定 #84：Writer 反复修改
// 草稿会产生新 digest 和新 edit checkpoint，但只要 Route 仍是同一个
// "写第 1 章"，就说明 Engine 级后置条件(commit)未完成，必须继续累计僵局。
func TestEngine_IntermediateCheckpointsDoNotMaskDeadlock(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("#84 回归", 1); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "第一章", CoreEvent: "开端"}}); err != nil {
		t.Fatalf("outline: %v", err)
	}
	if err := st.Drafts.SaveDraft(1, "版本0 正文初稿"); err != nil {
		t.Fatalf("draft: %v", err)
	}

	writerModel := &editThenCancelModel{}
	writer := subagent.Config{
		Name: "writer", Description: "edit then cancel writer",
		Model: writerModel, SystemPrompt: "test",
		Tools:    []agentcore.Tool{tools.NewEditChapterTool(st)},
		MaxTurns: 5,
	}
	// 即使 Arbiter 对 worker_failure / deadlock 一直要求 retry，现有第 5 次
	// 硬熔断也必须在派发前截停，不得被 edit checkpoint 重置。
	arb := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		return testTextMsg(`{"action":"retry","dispatch":null,"reason":"继续重试"}`)
	}}
	e, _, done := newTestEngine(t, st, subagent.New(writer), arb)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	if got := writerModel.edits.Load(); got != deadlockAbortAt-1 {
		t.Fatalf("deadlock 应在第 %d 次派发前硬熔断，实际 edit %d 次", deadlockAbortAt, got)
	}
	var edits int
	for _, cp := range st.Checkpoints.All() {
		if cp.Scope.Matches(domain.ChapterScope(1)) && cp.Step == "edit" {
			edits++
		}
	}
	if edits != deadlockAbortAt-1 {
		t.Fatalf("应保留 %d 条不同的 edit checkpoint，实际 %d", deadlockAbortAt-1, edits)
	}
	recs, err := st.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	var hasWorkerFailure, hasDeadlock bool
	for _, rec := range recs {
		switch rec.Kind {
		case "worker_failure":
			hasWorkerFailure = true
		case "deadlock":
			hasDeadlock = true
		}
	}
	if !hasWorkerFailure || !hasDeadlock {
		t.Fatalf("应先记录 worker_failure 再记录 deadlock: %+v", recs)
	}
}

// TestEngine_PauseWithEditorDispatchWaitsForRewriteQueue 修复验证(评审阻断2):
// Arbiter 返工裁定 = 停靠点 + 派 editor 入队。停靠点必须等 editor 建立返工队列、
// writer 重写排空之后才消费——不能在 editor 执行前被"队列已排空"误判消费。
func TestEngine_PauseWithEditorDispatchWaitsForRewriteQueue(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("返工试书", 3); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "一", CoreEvent: "a"},
		{Chapter: 2, Title: "二", CoreEvent: "b"},
		{Chapter: 3, Title: "三", CoreEvent: "c"},
	}); err != nil {
		t.Fatalf("outline: %v", err)
	}
	// 第 1 章已完成(将被返工);writer worker 会先重写它,然后停靠点消费。
	if err := st.Progress.StartChapter(1); err != nil {
		t.Fatalf("start ch1: %v", err)
	}
	if err := st.Progress.MarkChapterComplete(1, 1200, "crisis", "quest"); err != nil {
		t.Fatalf("complete ch1: %v", err)
	}

	// editor:一次 save_review(verdict=rewrite, affected=[1]) 把第 1 章入队。
	editorModel := &scriptedChatModel{fn: func(msgs []agentcore.Message) agentcore.Message {
		toolResults := 0
		for _, m := range msgs {
			if m.Role == agentcore.RoleTool {
				toolResults++
			}
		}
		if toolResults == 0 {
			return testToolCallMsg("save_review", map[string]any{
				"chapter": 1, "scope": "chapter",
				"dimensions": []map[string]any{
					{"dimension": "consistency", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "character", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "pacing", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "continuity", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "foreshadow", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "hook", "score": 85, "comment": "达标(引用:原文)"},
					{"dimension": "aesthetic", "score": 55, "comment": "语气不符(引用:原文第一段)"},
				},
				"issues":  []map[string]any{{"severity": "major", "description": "语气", "evidence": "原文", "suggestion": "改冷"}},
				"verdict": "rewrite", "summary": "第1章语气需重写",
				"affected_chapters": []int{1},
			})
		}
		return testTextMsg("done")
	}}
	editor := subagent.Config{
		Name: "editor", Description: "test editor", Model: editorModel,
		SystemPrompt: "test", MaxTurns: 6,
		Tools:          []agentcore.Tool{tools.NewSaveReviewTool(st)},
		StopAfterTools: []string{"save_review"},
	}
	writer := subagent.Config{
		Name: "writer", Description: "test writer", Model: scriptedWriterModel(),
		SystemPrompt: "test",
		Tools: []agentcore.Tool{
			tools.NewPlanChapterTool(st),
			tools.NewDraftChapterTool(st),
			tools.NewCheckConsistencyTool(st),
			tools.NewCommitChapterTool(st),
		},
		MaxTurns: 10, StopAfterTools: []string{"commit_chapter"},
	}

	e, _, done := newTestEngine(t, st, subagent.New(editor, writer), nil)
	// 模拟 Arbiter 返工裁定:hold + dispatch editor(引擎未运行 → 立即应用)。
	e.applyControlOp(context.Background(), controlOp{
		hold:     &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAfterRewritesDrained, Reason: "重写第1章语气,改完暂停验收"},
		dispatch: &arbiter.DispatchOp{Agent: "editor", Task: "复核第 1 章:语气改冷,save_review(verdict=rewrite, affected_chapters=[1])"},
		facts:    mustInterventionFacts(t, st),
	})
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: %v", err)
	}
	// 核心断言①:停靠点没有在 editor 入队前消费——第 1 章确实经历了重写
	//(重写 commit 会把它从队列 drain 掉)。
	if len(progress.PendingRewrites) != 0 {
		t.Fatalf("返工队列应已排空, got %v", progress.PendingRewrites)
	}
	if progress.ChapterWordCounts[1] == 1200 {
		t.Fatal("第 1 章应被真实重写(字数应变化)")
	}
	// 核心断言②:排空后停靠点消费,引擎暂停——第 2 章不应被续写。
	if len(progress.CompletedChapters) != 1 {
		t.Fatalf("停靠点应在续写第 2 章前暂停, completed=%v", progress.CompletedChapters)
	}
	meta, _ := st.RunMeta.Load()
	if meta != nil && meta.AdvanceHold != nil {
		t.Fatalf("一次性暂停应已消费, got %+v", meta.AdvanceHold)
	}
}

// TestEngine_BoundaryHoldDoesNotDispatchAnotherWorker 回归：
// 用户干预只裁定出 boundary hold（无派单）时，引擎必须在当前边界立即
// 消费 hold 并暂停，不得再多写一章。
func TestEngine_BoundaryHoldDoesNotDispatchAnotherWorker(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("暂停试书", 3); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "一", CoreEvent: "a"},
		{Chapter: 2, Title: "二", CoreEvent: "b"},
		{Chapter: 3, Title: "三", CoreEvent: "c"},
	}); err != nil {
		t.Fatalf("outline: %v", err)
	}

	writer := subagent.Config{
		Name: "writer", Description: "test writer", Model: scriptedWriterModel(),
		SystemPrompt: "test",
		Tools: []agentcore.Tool{
			tools.NewPlanChapterTool(st),
			tools.NewDraftChapterTool(st),
			tools.NewCheckConsistencyTool(st),
			tools.NewCommitChapterTool(st),
		},
		MaxTurns: 10, StopAfterTools: []string{"commit_chapter"},
	}
	e, _, done := newTestEngine(t, st, subagent.New(writer), nil)
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	// 第 1 章写作期间到达 hold-only 干预（与真实 Steer 时序一致）。
	e.enqueue(controlOp{
		hold:  &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAtBoundary, Reason: "先停一下我看看"},
		facts: mustInterventionFacts(t, st),
	})
	waitEngineDone(t, done)

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: %v", err)
	}
	// 干预在第 1 章运行中到达 → 第 1 章写完;停靠点在边界立即消费 → 第 2 章不得开写。
	if n := len(progress.CompletedChapters); n > 1 {
		t.Fatalf("boundary hold 后不得再多写一章, completed=%v", progress.CompletedChapters)
	}
	meta, _ := st.RunMeta.Load()
	if meta != nil && meta.AdvanceHold != nil {
		t.Fatalf("一次性暂停应已消费, got %+v", meta.AdvanceHold)
	}
}

// TestEngine_ExitRaceRestoresPendingDispatch 回归(评审阻断3):
// 干预入队与引擎退出竞态时,残留的裁定派单不得无声丢弃——PendingSteer 必须回存,
// pause 类事实动作必须补执行。
func TestEngine_ExitRaceRestoresPendingDispatch(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Progress.Init("竞态试书", 2); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}

	// worker 挂起直到 ctx 取消:制造"入队后引擎被 abort"的窗口。
	blocked := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		time.Sleep(50 * time.Millisecond)
		return testTextMsg("...")
	}}
	writer := subagent.Config{Name: "writer", Description: "slow", Model: blocked, SystemPrompt: "t", MaxTurns: 100}
	// 需要 outline 让 Route 派 writer
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一", CoreEvent: "a"}, {Chapter: 2, Title: "二", CoreEvent: "b"}}); err != nil {
		t.Fatalf("outline: %v", err)
	}
	e, _, done := newTestEngine(t, st, subagent.New(writer), nil)

	if !e.start(nil) {
		t.Fatal("engine start")
	}
	// worker 运行中:入队 pause+dispatch,随即 abort(动作永远等不到下个边界)。
	e.enqueue(controlOp{
		hold:     &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAfterRewritesDrained, Reason: "验收"},
		dispatch: &arbiter.DispatchOp{Agent: "writer", Task: "重写第 1 章"},
		text:     "重写第1章然后停下来",
		facts:    mustInterventionFacts(t, st),
	})
	e.abort()
	waitEngineDone(t, done)

	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil {
		t.Fatalf("load meta: %v", err)
	}
	if meta.PendingSteer != "重写第1章然后停下来" {
		t.Fatalf("残留派单必须回存 PendingSteer 供恢复重放, got %q", meta.PendingSteer)
	}
	if meta.AdvanceHold == nil {
		t.Fatal("hold 事实动作应在退出清理中补执行")
	}
}
