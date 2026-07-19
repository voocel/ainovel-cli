package host

import (
	"context"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// usageTrackedModel 给模型调用接上用量追踪:token/成本必须进入预算与 usage 系统,
// 否则预算上限对开销失明、UI 用量不准。记录身份用传入的 agentName——导入归 architect、
// 裁定归 arbiter(UsageTracker 对未知角色按 Default 价目计费)。
type usageTrackedModel struct {
	inner     agentcore.ChatModel
	agentName string
	record    func(agentName, task string, msg agentcore.AgentMessage)
}

func newUsageTrackedModel(inner agentcore.ChatModel, agentName string, record func(string, string, agentcore.AgentMessage)) agentcore.ChatModel {
	if record == nil {
		return inner
	}
	tracked := &usageTrackedModel{inner: inner, agentName: agentName, record: record}
	if capabilities, ok := inner.(llm.CapabilityProvider); ok {
		return &capabilityUsageTrackedModel{usageTrackedModel: tracked, capabilities: capabilities}
	}
	return tracked
}

// capabilityUsageTrackedModel 保留底层模型的可选能力接口。包装器不能把
// "不支持 thinking" 擦成 "能力未知"，否则上层会生成 provider 不接受的参数。
type capabilityUsageTrackedModel struct {
	*usageTrackedModel
	capabilities llm.CapabilityProvider
}

func (m *capabilityUsageTrackedModel) Capabilities() llm.Capabilities {
	return m.capabilities.Capabilities()
}

// JSONSchemaOverride 透传底层模型的 config json_schema 三态声明；inner 未携带
// 时返回 nil（"未配置"），不伪造能力。
func (m *capabilityUsageTrackedModel) JSONSchemaOverride() *bool {
	if o, ok := m.usageTrackedModel.inner.(interface{ JSONSchemaOverride() *bool }); ok {
		return o.JSONSchemaOverride()
	}
	return nil
}

func (m *capabilityUsageTrackedModel) StructuredOutputFacts() llmcontract.ModelFacts {
	if provider, ok := m.usageTrackedModel.inner.(interface {
		StructuredOutputFacts() llmcontract.ModelFacts
	}); ok {
		return provider.StructuredOutputFacts()
	}
	return llmcontract.ModelFacts{
		Capabilities:       m.Capabilities(),
		JSONSchemaOverride: m.JSONSchemaOverride(),
	}
}

func (m *usageTrackedModel) Generate(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	resp, err := m.inner.Generate(ctx, msgs, tools, opts...)
	if err == nil && resp != nil {
		m.record(m.agentName, "", resp.Message)
	}
	return resp, err
}

func (m *usageTrackedModel) GenerateStream(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	// Arbiter 只走 Generate;流式路径透传(若未来走流,usage 由消费端补记)。
	return m.inner.GenerateStream(ctx, msgs, tools, opts...)
}

func (m *usageTrackedModel) SupportsTools() bool { return m.inner.SupportsTools() }
