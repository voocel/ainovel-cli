// Package llm 收口通用模型调用：单次结构化输出、严格解码与 usage 回传。
// 它不理解故事——提示词、输入输出契约与调用理由由各使用方就近持有
// （架构文档 §12.1 原则 4）；Agent 循环与工具执行仍归 capability。
package llm

import (
	"context"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
	"github.com/voocel/litellm"
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

// Structured 用绑定的模型（含其思考强度）发起一次严格 JSON Schema 约束的调用，
// 并把响应严格解码到 out。返回本次调用的 usage 供调用方落审计事件。
func Structured(ctx context.Context, binding models.Binding, call Call, out any) (litellm.Usage, error) {
	format, err := litellm.NewResponseFormatJSONSchema(call.Schema.Name, call.Schema.Description, call.Schema.JSON)
	if err != nil {
		return litellm.Usage{}, fmt.Errorf("structured call %q schema: %w", call.Schema.Name, err)
	}
	format.JSONSchema.Strict = new(true)
	chat := binding.Routed(call.CacheKey, call.SessionID)
	request := chat.Request
	request.Messages = []litellm.Message{litellm.System(call.System), litellm.UserText(call.Input)}
	request.ResponseFormat = format
	response, err := chat.Client.Chat(ctx, request)
	if err != nil {
		return litellm.Usage{}, err
	}
	if err := model.DecodeStrict([]byte(response.Text()), out); err != nil {
		return litellm.Usage{}, fmt.Errorf("decode structured response %q: %w", call.Schema.Name, err)
	}
	return response.Usage, nil
}
