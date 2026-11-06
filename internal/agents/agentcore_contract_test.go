package agents

// agentcore 契约测试：把本项目依赖的框架行为钉成可执行断言。
// 每条测试标注依赖方；bump agentcore 前必须全绿——注释会过时，测试不会。
// 全部经 WorkerSet.Run 驱动——这是 Engine 的实际派发通道。
//
// v1.8.8 把旧的 StopGuard/StopDecision 三态改写为 Config.OnStop：
// 允许 = 返回空消息；拦截 = 返回消息（入 history，模型获得新一轮）；
// 升级 = 返回 error。以下契约随之改写，语义与 v1.8.3 版逐条对应。
//
// 已钉死的契约：
//  1. 工具返回 Result.Terminate 的终态退出会经过 OnStop，且 StopInfo.Terminated
//     为 true；OnStop 返回消息能把 run 拉回继续 —— guard/subagent_guards.go 的
//     任务感知 EditorStopGuard 依赖此行为兜住"被派生成摘要却只做了复核"的提前退出。
//  2. vendor 以 error 结束响应（StopError）直接终止 run，不触达 OnStop ——
//     guard 的 isHardStop 因此只需列 safety（Aborted 同理，由 ctx 取消产生）。
//  3. provider 拒答（FinishReasonSafety）会以正常停机路径触达 OnStop，
//     且 StopInfo.Message.Stop 保留 StopSafety —— isHardStop 的立即升级依赖此路径。
//  4. OnStop 返回消息后模型获得新一轮；返回包装 ErrStopGuardEscalated 的错误立即终止，
//     且错误链可被 errors.Is 匹配 —— guard 的"物理不可停机"与超限升级依赖此语义。
//  5. WorkerSet.Run 的错误保持类型化链：未注册 Worker 匹配 ErrUnknownWorker ——
//     host/engine.go 的 isDeterministicWorkerError 依赖此分类而非错误文案。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"

	"github.com/voocel/ainovel-cli/internal/agents/guard"
	"github.com/voocel/ainovel-cli/internal/store"
)

// contractModel 返回按脚本应答的模型句柄与可回看请求的 provider。
func contractModel(t *testing.T, replies ...litellmtest.Reply) (agentcore.Model, *litellmtest.Provider) {
	t.Helper()
	p := litellmtest.New(replies...)
	c, err := litellm.New(p)
	if err != nil {
		t.Fatalf("litellm client: %v", err)
	}
	return agentcore.Model{Client: c, Request: litellm.Request{Model: "test"}}, p
}

// contractTools 是终态工具：调用即返回 Result.Terminate，模拟 save_review 之类的
// "命中即收尾"工具。
func contractTool(name string) agentcore.Tool {
	return agentcore.Tool{
		Name:        name,
		Description: "contract test tool",
		Schema:      map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage) (agentcore.Result, error) {
			res := agentcore.TextResult("ok")
			res.Terminate = true
			return res, nil
		},
	}
}

// runContract 经 WorkerSet.Run 跑一次单 Worker。stop 非 nil 时作为该 Worker 的
// OnStop 工厂（Engine 的真实接线：按任务文本构建 guard）。
func runContract(t *testing.T, cfg agentcore.Config, task string, stop func(task string) StopHook) (string, error) {
	t.Helper()
	workers := NewWorkerSet(map[string]agentcore.Config{"contract": cfg}, []string{"contract"})
	if stop != nil {
		workers.stopFactories["contract"] = stop
	}
	return workers.Run(context.Background(), "contract", task)
}

// providerSaw 报告 provider 收到的任一请求里是否出现过 sub 文本。
func providerSaw(p *litellmtest.Provider, sub string) bool {
	for _, req := range p.Requests() {
		for _, m := range req.Messages {
			for _, b := range m.Blocks {
				if tb, ok := b.(litellm.TextBlock); ok && strings.Contains(tb.Text, sub) {
					return true
				}
			}
		}
	}
	return false
}

func contractConfig(model agentcore.Model) agentcore.Config {
	return agentcore.Config{
		Model:    model,
		System:   []litellm.Block{litellm.Text("test")},
		Tools:    []agentcore.Tool{contractTool("finish")},
		MaxTurns: 5,
	}
}

// 契约 1：终态工具退出经过 OnStop；StopInfo.Terminated 标出这次退出由工具触发；
// OnStop 返回消息后 run 继续。
// 依赖方：EditorStopGuard —— save_review 等终态工具命中后，任务感知 guard 必须
// 有机会把"产物未落盘"的提前退出拉回来。
func TestContract_TerminalToolExitConsultsStopGuard(t *testing.T) {
	model, provider := contractModel(t,
		litellmtest.Respond(litellm.ToolUseBlock{ID: "tc-finish", Name: "finish", Arguments: "{}"}),
		litellmtest.Text("done"),
	)

	var calls atomic.Int32
	var terminated atomic.Bool
	stop := func(string) StopHook {
		return func(_ context.Context, info agentcore.StopInfo) ([]agentcore.Message, error) {
			if info.Terminated {
				terminated.Store(true)
			}
			if calls.Add(1) == 1 {
				return []agentcore.Message{agentcore.UserText("还没落盘，继续")}, nil
			}
			return nil, nil
		}
	}

	text, err := runContract(t, contractConfig(model), "contract", stop)
	if err != nil {
		t.Fatalf("worker run: %v", err)
	}
	if !terminated.Load() {
		t.Fatal("终态工具退出必须以 StopInfo.Terminated=true 触达 OnStop")
	}
	if calls.Load() < 2 {
		t.Fatalf("终态工具退出必须触达 OnStop 且注入消息后继续（期望 ≥2 次咨询），got %d", calls.Load())
	}
	if text != "done" {
		t.Fatalf("注入后应跑完整轮并返回最后一条响应文本，got %q", text)
	}
	if !providerSaw(provider, "还没落盘") {
		t.Fatal("OnStop 注入的消息必须出现在下一轮请求里")
	}
}

// 契约 2：vendor 以 error 结束响应（StopError）时 run 直接失败，不触达 OnStop。
// 依赖方：guard 的 isHardStop 注释——只需处理会真正走到 OnStop 的拒答语义。
func TestContract_ErrorStopSkipsStopGuard(t *testing.T) {
	model, _ := contractModel(t, litellmtest.Reply{
		Blocks:       []litellm.Block{litellm.Text("dead")},
		FinishReason: litellm.FinishReasonError,
	})

	var calls atomic.Int32
	stop := func(string) StopHook {
		return func(context.Context, agentcore.StopInfo) ([]agentcore.Message, error) {
			calls.Add(1)
			return nil, nil
		}
	}

	if _, err := runContract(t, contractConfig(model), "contract", stop); err == nil {
		t.Fatal("vendor 以 error 结束响应时 run 必须失败")
	}
	if calls.Load() != 0 {
		t.Fatalf("StopError 停机不应触达 OnStop，got %d 次咨询", calls.Load())
	}
}

// 契约 3：provider 拒答（safety）走正常停机路径触达 OnStop，
// 且 StopInfo.Message.Stop 保留 StopSafety。依赖方：isHardStop 的立即升级。
func TestContract_SafetyStopReachesStopGuardWithReason(t *testing.T) {
	model, _ := contractModel(t, litellmtest.Reply{
		Blocks:       []litellm.Block{litellm.Text("refused")},
		FinishReason: litellm.FinishReasonSafety,
	})

	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	var seen atomic.Value
	g := guard.NewWriterStopGuard(st, nil)
	stop := func(string) StopHook {
		return func(ctx context.Context, info agentcore.StopInfo) ([]agentcore.Message, error) {
			seen.Store(info.Message.Stop)
			return g(ctx, info)
		}
	}

	_, err := runContract(t, contractConfig(model), "contract", stop)
	if got, _ := seen.Load().(agentcore.StopReason); got != agentcore.StopSafety {
		t.Fatalf("OnStop 应看到原始停机理 safety，got %v", got)
	}
	if !errors.Is(err, guard.ErrStopGuardEscalated) {
		t.Fatalf("升级应以可 errors.Is(guard.ErrStopGuardEscalated) 的错误浮出，got %v", err)
	}
}

// 契约 4：OnStop 返回消息让模型获得新一轮且注入内容在场；
// 返回错误立即终止，模型不再被调用。依赖方：Worker StopGuard 的
// "物理不可停机 + 连续超限升级"。
func TestContract_StopGuardInjectContinuesEscalateTerminates(t *testing.T) {
	model, provider := contractModel(t, litellmtest.Text("try stop"), litellmtest.Text("try stop"))

	var consulted atomic.Int32
	stop := func(string) StopHook {
		return func(context.Context, agentcore.StopInfo) ([]agentcore.Message, error) {
			if consulted.Add(1) == 1 {
				return []agentcore.Message{agentcore.UserText("禁止结束-契约")}, nil
			}
			return nil, errEscalated
		}
	}

	_, err := runContract(t, contractConfig(model), "contract", stop)
	if !errors.Is(err, errEscalated) {
		t.Fatalf("OnStop 返回的错误必须浮出 run，got %v", err)
	}
	if consulted.Load() != 2 {
		t.Fatalf("期望 OnStop 恰被咨询 2 次（1 注入 + 1 升级），got %d", consulted.Load())
	}
	if got := len(provider.Requests()); got != 2 {
		t.Fatalf("升级后模型不应再被调用，期望恰 2 次请求，got %d", got)
	}
	if !providerSaw(provider, "禁止结束-契约") {
		t.Fatal("OnStop 注入的消息必须出现在第二次请求里")
	}
}

var errEscalated = errors.New("escalated in contract test")

// 契约 5：WorkerSet.Run 的错误保持类型化链——未注册 Worker 以 ErrUnknownWorker
// 浮出。依赖方：host/engine.go 的 isDeterministicWorkerError（"重试必然同错→
// 直接暂停"的分类依赖 errors.Is,而非错误文案匹配）。
func TestContract_RunUnknownAgentIsTyped(t *testing.T) {
	model, _ := contractModel(t, litellmtest.Text("ok"))
	workers := NewWorkerSet(map[string]agentcore.Config{"writer": contractConfig(model)}, []string{"writer"})
	if _, err := workers.Run(context.Background(), "ghost", "contract"); !errors.Is(err, ErrUnknownWorker) {
		t.Fatalf("未注册 Worker 应匹配 ErrUnknownWorker，got %v", err)
	}
}
