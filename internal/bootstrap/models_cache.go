package bootstrap

import (
	"context"
	"maps"
	"slices"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

// openAICacheModel 保留 OpenAI 的自动前缀缓存与 prompt_cache_key，但不主动发送
// 显式断点：litellm v1.8.11 会把 cache_control 无条件编码为 prompt_cache_breakpoint，
// 部分模型/兼容端点拒绝该字段，Responses 的 tool_result 断点还会在本地被拒绝。
// 策略绑定实际客户端，热切换与 fallback 都遵循同一规则，不改写共享历史。
type openAICacheModel struct {
	*llm.LiteLLMAdapter
}

func (m *openAICacheModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.LiteLLMAdapter.Generate(ctx, withoutCacheControl(messages), tools, opts...)
}

func (m *openAICacheModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return m.LiteLLMAdapter.GenerateStream(ctx, withoutCacheControl(messages), tools, opts...)
}

func withoutCacheControl(messages []agentcore.Message) []agentcore.Message {
	cloned := slices.Clone(messages)
	for i := range cloned {
		if _, ok := cloned[i].Metadata["cache_control"]; !ok {
			continue
		}
		cloned[i].Metadata = maps.Clone(cloned[i].Metadata)
		delete(cloned[i].Metadata, "cache_control")
	}
	return cloned
}
