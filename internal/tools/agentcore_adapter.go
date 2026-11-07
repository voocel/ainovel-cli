package tools

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// Tool 是本包自己的工具形态：与 agentcore v1.8.3 时代的方法名保持一致，
// 但不再依赖框架，只描述「名字 + 描述 + schema + 执行」。
//
// v1.8.8 的 agentcore.Tool 是结构体而不是接口，因此本包工具先实现这里的方法，
// 装配时再经 ToAgentcore / ToAgentcoreAll 转成框架工具。这样 internal/tools
// 不必 import internal/agents，也不必让每个工具文件改写成 agentcore.Tool 字面量。
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	// Execute 返回给模型看的 JSON 结果；error 表示调用失败（错误文本会回给模型）。
	Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// 以下可选接口沿用旧 agentcore.Tool 的可选能力。
//
// 注意：v1.8.8 的 agentcore.Tool 只有静态字段（Label、Parallel、Deferred），
// 旧接口里 ReadOnly(args) / ConcurrencySafe(args) 这种「按调用参数决定」的能力
// 无法逐调用表达。适配器只能在装配时用 nil 参数探测一次，因此：
//   - ConcurrencySafe 必须实现成与参数无关的常量（本包所有实现都是 `_ json.RawMessage`），
//     探测值即整轮生效的 Parallel 值；
//   - ReadOnly 在新 API 里没有对应字段，适配器不映射（旧框架只把它当并发调度的
//     次选依据，本包所有工具都同时实现了 ConcurrencySafe，故静态取值不受影响）；
//   - 装配后 args 相关的能力不再生效，若将来某工具需要按参数区分，只能在 Run 内部自行串行化。

// ToolLabeler 提供人类可读的工具名（TUI 展示用），沿用旧 agentcore 的接口名。
type ToolLabeler interface{ Label() string }

// ReadOnlyTool 声明工具是纯读（无副作用）。
// 新 API 无对应字段，仅装配期可读；旧框架曾以它作为并发调度的次选依据
// （ConcurrencySafeTool > ReadOnlyTool > false），v1.8.8 只有静态 Parallel，
// 因此适配器只认 ConcurrencySafeTool，ReadOnlyTool 不再参与调度决策。
type ReadOnlyTool interface {
	ReadOnly(args json.RawMessage) bool
}

// ConcurrencySafeTool 声明工具可与同轮其它并发调用并行；映射为 agentcore.Tool.Parallel。
type ConcurrencySafeTool interface {
	ConcurrencySafe(args json.RawMessage) bool
}

// StrictSchemaTool 声明要求 Provider 保证参数符合 schema。
// v1.8.8 无对应开关（框架恒按 Tool.Schema 校验参数），适配器忽略之。
type StrictSchemaTool interface{ StrictSchema() bool }

// ToAgentcore 把本包工具适配成 agentcore v1.8.8 的工具。
//
// 映射规则：
//   - Execute 的 JSON 结果 → agentcore.Result 的文本块（等价于 agentcore.JSONResult；
//     直接以 JSON 原文入块，不做重编码，避免数字精度/字段顺序变化）；
//   - error → IsError=true 的结果块，并把错误文本回给模型，同时返回 nil error，
//     让框架按「工具失败但可继续」处理（Run 返回 error 时框架会丢弃 Result 内容，
//     这里保留原文更直观，行为等价）；
//   - Label 透传；ConcurrencySafe(nil) 为 true 时置 Parallel；
//   - StrictSchema 无对应字段，忽略；
//   - ReadOnly 无对应字段，不映射（见上方可选接口说明）。
func ToAgentcore(t Tool) agentcore.Tool {
	out := agentcore.Tool{
		Name:        t.Name(),
		Description: t.Description(),
		Schema:      t.Schema(),
		Run: func(ctx context.Context, args json.RawMessage) (agentcore.Result, error) {
			raw, err := t.Execute(ctx, args)
			if err != nil {
				return agentcore.ErrorResult(err.Error()), nil
			}
			if len(bytes.TrimSpace(raw)) == 0 {
				return agentcore.TextResult(""), nil
			}
			// 直接以 JSON 原文入文本块（等价于 agentcore.JSONResult，但不重编码，
			// 避免数字精度与字段顺序被改动）；非 JSON 文本同样按原文交给模型。
			return agentcore.Result{Content: []litellm.Block{litellm.Text(string(raw))}}, nil
		},
	}
	if l, ok := t.(ToolLabeler); ok {
		out.Label = l.Label()
	}
	if s, ok := t.(ConcurrencySafeTool); ok {
		// 拿不到 args：装配期只能探测一次，返回 false（或工具未实现该接口）即保守串行。
		out.Parallel = s.ConcurrencySafe(nil)
	}
	return out
}

// ToAgentcoreAll 批量适配，保持顺序。
func ToAgentcoreAll(ts []Tool) []agentcore.Tool {
	out := make([]agentcore.Tool, len(ts))
	for i, t := range ts {
		out[i] = ToAgentcore(t)
	}
	return out
}
