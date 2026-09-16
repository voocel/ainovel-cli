// Package llm 收口通用模型调用：单次结构化输出、严格解码与 usage 回传。
// 它不理解故事——提示词、输入输出契约与调用理由由各使用方就近持有
// （架构文档 §12.1 原则 4）；Agent 循环与工具执行仍归 capability。
package llm

import (
	"context"
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
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

// Structured 发起一次严格 JSON Schema 约束的调用并把响应严格解码到 out。
// 返回本次调用的 usage 供调用方落审计事件。
func Structured(ctx context.Context, chat agentcore.ChatModel, call Call, out any) (*agentcore.Usage, error) {
	if chat == nil {
		return nil, fmt.Errorf("structured call model is required: %w", model.ErrInvalid)
	}
	response, err := chat.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg(call.System),
		agentcore.UserMsg(call.Input),
	}, nil,
		agentcore.WithJSONSchema(call.Schema.Name, call.Schema.Description, call.Schema.JSON, true),
		agentcore.WithCallPromptCacheKey(call.CacheKey),
		agentcore.WithCallSessionID(call.SessionID),
	)
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
