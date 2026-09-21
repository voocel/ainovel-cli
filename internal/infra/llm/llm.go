// Package llm 收口通用模型调用：单次结构化输出、严格解码与 usage 回传。
// 它不理解故事——提示词、输入输出契约与调用理由由各使用方就近持有
// （架构文档 §12.1 原则 4）；Agent 循环与工具执行仍归 capability。
package llm

import (
	"context"
	"fmt"

	"github.com/voocel/agentcore"
	agentllm "github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
)

// Schema 是一次结构化调用的输出约束。
type Schema struct {
	Name        string
	Description string
	JSON        map[string]any
}

// Call 是一次单轮结构化调用的完整输入。CacheKey 与 SessionID 由调用方
// 按项目、契约版本与输入摘要派生，保证同一判断可复现。
type Call struct {
	System    string
	Input     string
	Schema    Schema
	CacheKey  string
	SessionID string
}

// Structured 用绑定的模型发起一次严格 JSON Schema 约束的调用并把响应严格解码到 out。
// 思考强度按绑定意图与模型能力折算。返回本次调用的 usage 供调用方落审计事件。
func Structured(ctx context.Context, binding models.Binding, call Call, out any) (*agentcore.Usage, error) {
	if binding.Chat == nil {
		return nil, fmt.Errorf("structured call model is required: %w", model.ErrInvalid)
	}
	options := []agentcore.CallOption{
		agentcore.WithJSONSchema(call.Schema.Name, call.Schema.Description, call.Schema.JSON, true),
		agentcore.WithCallPromptCacheKey(call.CacheKey),
		agentcore.WithCallSessionID(call.SessionID),
	}
	if thinking := EffectiveThinking(binding); thinking != "" {
		options = append(options, agentcore.WithThinking(thinking))
	}
	response, err := binding.Chat.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg(call.System),
		agentcore.UserMsg(call.Input),
	}, nil, options...)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("structured call %q returned no response", call.Schema.Name)
	}
	if err := model.DecodeStrict([]byte(response.Message.TextContent()), out); err != nil {
		return nil, fmt.Errorf("decode structured response %q: %w", call.Schema.Name, err)
	}
	return response.Message.Usage, nil
}

// EffectiveThinking 把思考强度意图折算成该模型接受的生效值：不支持的档位退回
// 自动（空），完全不支持思考的模型只有自动。意图保留在配置里，换回支持的模型即恢复。
func EffectiveThinking(binding models.Binding) agentcore.ThinkingLevel {
	level, _ := ThinkingPolicy(binding.Chat).Resolve(binding.Thinking)
	return level
}

// ThinkingPolicy 是模型可接受的思考档位；不支持思考的模型只剩自动。
func ThinkingPolicy(chat agentcore.ChatModel) agentllm.ThinkingPolicy {
	if provider, ok := chat.(agentllm.CapabilityProvider); ok && provider.Capabilities().Thinking.Supported == agentllm.SupportNo {
		return agentllm.ThinkingPolicy{Available: []agentcore.ThinkingLevel{agentllm.ThinkingAuto}}
	}
	return agentllm.ThinkingPolicyFor(chat)
}
