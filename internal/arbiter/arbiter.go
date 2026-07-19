// Package arbiter 是语义裁定层:按需唤醒的 LLM-as-function。
//
// 两平面对称(docs/engine-arbiter.md §二):
//
//	确定性平面:  flow.LoadState   → flow.Route     → Instruction
//	语义平面:    arbiter.Collect* → arbiter.Decide* → XxxDecision
//
// 纪律:Collect 集中 IO(从 store 读齐事实);Decide 除统一执行器管理的模型请求外无 IO,
// 可用历史 facts 离线重放;执行归 Engine。每场景一对函数 + 专属 Decision 类型,
// 场景不匹配的动作在类型上不可表达;剩余合法性由各类型的 Validate 拒绝——
// Arbiter 输出与一切 LLM 输出同样不可信,事实校验是最后一道门。
package arbiter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// decideMaxTokens 单次裁定的输出上限;裁定 JSON 很小,大头留给推理模型的思考预算
// (与 userrules.normalizeMaxTokens 同理)。
const decideMaxTokens = 8192

// decide 将场景契约与业务校验交给统一结构化执行器。除模型调用外无 IO。
func decide[T any](ctx context.Context, model agentcore.ChatModel, contract llmcontract.Contract, systemPrompt, payload string, validate func(*T) error) (T, error) {
	out, err := llmcontract.Execute(ctx, model, llmcontract.Request[T]{
		Contract:     contract,
		SystemPrompt: systemPrompt,
		Payload:      payload,
		Options:      []agentcore.CallOption{agentcore.WithMaxTokens(decideMaxTokens)},
		Validate:     validate,
		Agent:        "arbiter",
		Hooks: llmcontract.Hooks{
			Resolved: func(res llmcontract.Resolution) {
				slog.Debug("裁定协议选择", "module", "arbiter",
					"contract", contract.Name, "structured_mode", res.Mode,
					"capability_source", res.Source, "provider", res.Provider,
					"model", res.Model, "schema_fingerprint", contract.Fingerprint())
			},
			Correction: func(ev llmcontract.Correction) {
				slog.Warn("裁定输出自愈", "module", "arbiter", "attempt", ev.Attempt,
					"layer", ev.Layer, "structured_mode", ev.Mode, "err", ev.Err)
			},
		},
	})
	if err != nil {
		return out, fmt.Errorf("arbiter: %w", err)
	}
	return out, nil
}

// DispatchOp 是各场景共享的派单动作。
type DispatchOp struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

// workerNames 是合法派单目标(与 agents.BuildWorkers 注册的一致)。有序切片:
// 同时充当 schema enum(顺序确定保 fingerprint 稳定)与校验白名单。
var workerNames = []string{"architect_long", "architect_short", "writer", "editor"}

func (d *DispatchOp) validate() error {
	if d == nil {
		return nil
	}
	if !slices.Contains(workerNames, d.Agent) {
		return fmt.Errorf("dispatch.agent 非法: %q", d.Agent)
	}
	if strings.TrimSpace(d.Task) == "" {
		return fmt.Errorf("dispatch.task 不能为空")
	}
	return nil
}

// dispatchSchema 是 DispatchOp 的可空 schema 位:仅需要派单的动作给出对象,
// 其余情况为 null(strict 模式全字段 required,可选语义用 null 表达)。
func dispatchSchema(desc string) map[string]any {
	return llmcontract.Nullable(schema.Object(
		schema.Property("agent", schema.Enum(desc, workerNames...)).Required(),
		schema.Property("task", schema.String("交给该 worker 的完整任务描述")).Required(),
	))
}

// marshalPayload 序列化事实包;失败即程序错误,必须暴露——静默伪造空事实
// 会让模型基于假输入误判。
func marshalPayload(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("arbiter: 事实包序列化失败: %w", err)
	}
	return string(data), nil
}
