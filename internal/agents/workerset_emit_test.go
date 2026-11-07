package agents

// WorkerSet.Run 的 Emit 链式接线与 history 累积回归。
//
// 这些行为只在真实的 agentcore run 里才成立（Emit 失败即中止、RunEnd 即便
// Emit 失败也会补发一次、MessageEnd 是 history 唯一入口），所以全部用真实
// provider adapter + litellmtest 流式应答驱动 WorkerSet.Run，不 mock WorkerSet。
//
// 钉死的契约：
//  1. 调用方的 Config.Emit 与 WorkerSet 内部的 observer/record 收到同一批事件
//     （计数一致），且调用方每个事件都先于内部收到 —— build.go 的接线依赖
//     "调用方可以先否决事件"，内部记录不能抢跑。
//  2. 调用方 Emit 返回 error 时 Run 原样带出该 error（errors.Is 可匹配），
//     且内部 observer/record 一条都收不到（被否决的事件不进 history、不落盘）。
//  3. 同一 Worker 的 history 跨 Run 累积：第 N 次 Run 的请求里带着前 N-1 次的任务。

import (
	"context"
	"errors"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

// emitTestModel 返回按脚本应答的模型与可回看请求的 provider。
func emitTestModel(t *testing.T, replies ...litellmtest.Reply) (agentcore.Model, *litellmtest.Provider) {
	t.Helper()
	p := litellmtest.New(replies...)
	c, err := litellm.New(p)
	if err != nil {
		t.Fatalf("litellm client: %v", err)
	}
	return agentcore.Model{Client: c, Request: litellm.Request{Model: "test"}}, p
}

// emitTestWorkers 组装一个单 Worker 的 WorkerSet，并把 caller emit 装到配置上。
func emitTestWorkers(t *testing.T, model agentcore.Model, emit func(agentcore.Event) error) *WorkerSet {
	t.Helper()
	cfg := agentcore.Config{Model: model, Emit: emit}
	return NewWorkerSet(map[string]agentcore.Config{"writer": cfg}, []string{"writer"})
}

// 调用方 Emit 先收到每个事件，内部 observer/record 后收到同一批事件。
func TestWorkerSetEmitChainsCallerBeforeInternal(t *testing.T) {
	model, _ := emitTestModel(t, litellmtest.Text("第一段回复"))

	var (
		callerEvents   int
		callerSawInner []int
		internalEvents []agentcore.Event
		recorded       []agentcore.Message
		observedAgent  []string
	)

	workers := emitTestWorkers(t, model, func(agentcore.Event) error {
		// 调用方此刻看到的内部计数，就是"内部收到几个"的快照：
		// 若链式顺序反过来，这里会等于 i+1 而不是 i。
		callerSawInner = append(callerSawInner, len(internalEvents))
		callerEvents++
		return nil
	})
	workers.SetEventObserver(func(agent string, ev agentcore.Event) {
		observedAgent = append(observedAgent, agent)
		internalEvents = append(internalEvents, ev)
	})
	workers.record = func(_, _ string, msg agentcore.Message) {
		recorded = append(recorded, msg)
	}

	text, err := workers.Run(context.Background(), "writer", "写一段开场")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if text != "第一段回复" {
		t.Fatalf("expected the scripted reply, got %q", text)
	}

	if callerEvents == 0 {
		t.Fatal("调用方 Emit 必须收到事件")
	}
	if callerEvents != len(internalEvents) {
		t.Fatalf("调用方与内部必须收到同一批事件：caller=%d internal=%d", callerEvents, len(internalEvents))
	}
	if len(callerSawInner) != callerEvents {
		t.Fatalf("每次调用方 Emit 都要记一次内部计数，got %d for %d events", len(callerSawInner), callerEvents)
	}
	for i, seen := range callerSawInner {
		if seen != i {
			t.Fatalf("第 %d 个事件：调用方收到时内部已收 %d 条，调用方必须先到", i, seen)
		}
	}
	for i, agent := range observedAgent {
		if agent != "writer" {
			t.Fatalf("第 %d 个事件归属 %q，want writer", i, agent)
		}
	}

	// record 只吃 MessageEnd，且与 observer 看到的那批 MessageEnd 完全一致。
	var ends []agentcore.Message
	for _, ev := range internalEvents {
		if me, ok := ev.(agentcore.MessageEnd); ok {
			ends = append(ends, me.Message)
		}
	}
	if len(ends) == 0 {
		t.Fatal("run 必须至少产生一条 MessageEnd（prompt 与回复各一条）")
	}
	if len(recorded) != len(ends) {
		t.Fatalf("record 必须收到每条 MessageEnd：record=%d MessageEnd=%d", len(recorded), len(ends))
	}
	for i := range ends {
		if ends[i].Text() != recorded[i].Text() {
			t.Fatalf("第 %d 条落盘消息与 observer 收到的不一致：%q vs %q", i, recorded[i].Text(), ends[i].Text())
		}
	}
	// 落盘的最后一条就是这次回复。
	if got := recorded[len(recorded)-1].Text(); got != "第一段回复" {
		t.Fatalf("最后一条落盘消息应为回复，got %q", got)
	}
}

// 调用方否决第一个事件时 run 立刻中止：错误原样带出，内部一条都收不到，
// 被否决的事件既不落盘也不进 history。
func TestWorkerSetEmitCallerErrorAbortsRun(t *testing.T) {
	model, provider := emitTestModel(t, litellmtest.Text("不该出现的回复"))

	boom := errors.New("caller refuses events")
	var (
		callerCalls    int
		internalEvents []agentcore.Event
		recorded       int
	)

	workers := emitTestWorkers(t, model, func(agentcore.Event) error {
		callerCalls++
		return boom
	})
	workers.SetEventObserver(func(_ string, ev agentcore.Event) {
		internalEvents = append(internalEvents, ev)
	})
	workers.record = func(_, _ string, _ agentcore.Message) { recorded++ }

	text, err := workers.Run(context.Background(), "writer", "写一段开场")
	if !errors.Is(err, boom) {
		t.Fatalf("Run 必须原样带出调用方 Emit 的错误，got %v", err)
	}
	if text != "" {
		t.Fatalf("run 失败时必须返回空文本，got %q", text)
	}
	if callerCalls == 0 {
		t.Fatal("调用方 Emit 必须被调用过（否则链没接上）")
	}
	// 实测调用方被问两次：prompt 的 MessageEnd 与收尾的 RunEnd（agentcore 的
	// RunEnd 即便 Emit 已失败也会再交付一次）。两次都必须被链拦住 —— 内部
	// observer 一条都收不到，这正是下面断言要钉的行为。
	if len(internalEvents) != 0 {
		t.Fatalf("调用方否决后内部 observer 必须一条都收不到，got %d", len(internalEvents))
	}
	if recorded != 0 {
		t.Fatalf("调用方否决后不得落盘任何消息，got %d", recorded)
	}
	// 第一个被否决的事件是 prompt 的 MessageEnd，模型还没被调用。
	if reqs := provider.Requests(); len(reqs) != 0 {
		t.Fatalf("Emit 否决必须发生在模型调用之前，provider 收到 %d 个请求", len(reqs))
	}
}

// 同一 Worker 的 history 跨 Run 累积：每次 Run 的请求都带着之前的任务，
// 落盘/观察的事件随之增长。防止回退成"每次 Run 从空 history 开始"。
func TestWorkerSetEmitHistoryAccumulatesAcrossRuns(t *testing.T) {
	model, provider := emitTestModel(t,
		litellmtest.Text("回复一"),
		litellmtest.Text("回复二"),
		litellmtest.Text("回复三"),
	)

	var (
		internalEvents int
		recorded       int
	)
	workers := emitTestWorkers(t, model, nil)
	workers.SetEventObserver(func(string, agentcore.Event) { internalEvents++ })
	workers.record = func(_, _ string, _ agentcore.Message) { recorded++ }

	tasks := []string{"第一章：开场", "第二章：升级", "第三章：收束"}
	replies := []string{"回复一", "回复二", "回复三"}
	eventsAfter := make([]int, len(tasks))
	for i, task := range tasks {
		text, err := workers.Run(context.Background(), "writer", task)
		if err != nil {
			t.Fatalf("Run %d: %v", i+1, err)
		}
		if text != replies[i] {
			t.Fatalf("Run %d 回复 %q，want %q", i+1, text, replies[i])
		}
		eventsAfter[i] = internalEvents
	}

	// 每次 Run 至少含 prompt MessageEnd + 回复 MessageEnd + RunEnd。
	if !(eventsAfter[0] < eventsAfter[1] && eventsAfter[1] < eventsAfter[2]) {
		t.Fatalf("事件数必须随 Run 递增，got %v", eventsAfter)
	}

	// 第 N 次请求带上前 N 次任务，且顺序保持。
	reqs := provider.Requests()
	if len(reqs) != len(tasks) {
		t.Fatalf("每次 Run 一次模型调用：requests=%d want %d", len(reqs), len(tasks))
	}
	for i := range tasks {
		for j := 0; j <= i; j++ {
			if !providerSaw(provider, tasks[j]) {
				t.Fatalf("第 %d 次请求应包含任务 %q", i+1, tasks[j])
			}
		}
		// 尚未下发的任务不能提前出现在历史里。
		for j := i + 1; j < len(tasks); j++ {
			for _, m := range reqs[i].Messages {
				for _, b := range m.Blocks {
					if tb, ok := b.(litellm.TextBlock); ok && tb.Text == tasks[j] {
						t.Fatalf("第 %d 次请求不应包含尚未下发的任务 %q", i+1, tasks[j])
					}
				}
			}
		}
	}

	// 内部 history 累积三条用户任务，顺序与下发一致。
	workers.mu.Lock()
	history := append([]agentcore.Message(nil), workers.history["writer"]...)
	workers.mu.Unlock()
	var userTexts []string
	for _, m := range history {
		if m.Role == litellm.RoleUser {
			userTexts = append(userTexts, m.Text())
		}
	}
	if len(userTexts) != len(tasks) {
		t.Fatalf("history 应累积 %d 条任务，got %d: %v", len(tasks), len(userTexts), userTexts)
	}
	for i := range tasks {
		if userTexts[i] != tasks[i] {
			t.Fatalf("history 第 %d 条任务 %q，want %q", i, userTexts[i], tasks[i])
		}
	}
	// 落盘点随 history 增长：每次 Run 至少一条 MessageEnd（回复）。
	if recorded < len(tasks) {
		t.Fatalf("record 至少收到 %d 条消息，got %d", len(tasks), recorded)
	}
}
