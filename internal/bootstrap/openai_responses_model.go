package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore"
	agentllm "github.com/voocel/agentcore/llm"
	"github.com/voocel/litellm"
)

// openAIResponsesModel adapts litellm's OpenAI Responses API client to agentcore.ChatModel.
type openAIResponsesModel struct {
	*agentllm.BaseModel
	client *litellm.Client
	model  string
}

func newOpenAIResponsesModel(model string, client *litellm.Client) *openAIResponsesModel {
	modelInfo := agentllm.ModelInfo{
		Name:     model,
		Provider: client.ProviderName(),
		Capabilities: []string{
			string(agentllm.CapabilityChat),
			string(agentllm.CapabilityCompletion),
			string(agentllm.CapabilityStreaming),
			string(agentllm.CapabilityToolCalling),
		},
	}

	if caps, ok := litellm.GetModelCapabilities(model); ok {
		modelInfo.MaxTokens = caps.MaxOutputTokens
		modelInfo.ContextSize = caps.MaxInputTokens
	}
	if pricing, ok := litellm.GetModelPricing(model); ok {
		modelInfo.Pricing = &agentllm.ModelPricing{
			InputPerToken:  pricing.InputCostPerToken,
			OutputPerToken: pricing.OutputCostPerToken,
		}
	}

	return &openAIResponsesModel{
		BaseModel: agentllm.NewBaseModel(modelInfo, agentllm.DefaultGenerationConfig),
		client:    client,
		model:     model,
	}
}

func (m *openAIResponsesModel) ProviderName() string {
	return m.Info().Provider
}

func (m *openAIResponsesModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	cfg := m.GetConfig()

	req := &litellm.OpenAIResponsesRequest{
		Model:           m.model,
		Messages:        convertMessages(messages),
		Temperature:     &cfg.Temperature,
		MaxOutputTokens: &cfg.MaxTokens,
	}

	applyResponsesCallConfig(req, opts)
	applyResponsesToolConfig(req, tools)

	resp, err := m.client.Responses(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm: responses failed: %w", err)
	}

	msg := convertLiteLLMResponse(resp)
	if msg.Usage != nil {
		msg.Usage.Cost = agentllm.CalculateCost(m.Info().Pricing, msg.Usage)
	}
	return &agentcore.LLMResponse{Message: msg}, nil
}

func (m *openAIResponsesModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	cfg := m.GetConfig()

	req := &litellm.OpenAIResponsesRequest{
		Model:           m.model,
		Messages:        convertMessages(messages),
		Temperature:     &cfg.Temperature,
		MaxOutputTokens: &cfg.MaxTokens,
	}

	applyResponsesCallConfig(req, opts)
	applyResponsesToolConfig(req, tools)

	stream, err := m.client.ResponsesStream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm: responses stream failed: %w", err)
	}

	eventCh := make(chan agentcore.StreamEvent, 100)

	go func() {
		defer close(eventCh)
		defer stream.Close()

		var (
			partial  = agentcore.Message{Role: agentcore.RoleAssistant}
			textIdx  = -1
			thinkIdx = -1
		)

		resp, err := litellm.CollectStreamWithCallbacks(stream, litellm.StreamCallbacks{
			OnReasoningStart: func() {
				partial.Content = append(partial.Content, agentcore.ThinkingBlock(""))
				thinkIdx = len(partial.Content) - 1
				eventCh <- agentcore.StreamEvent{
					Type:         agentcore.StreamEventThinkingStart,
					ContentIndex: thinkIdx,
					Message:      partial,
				}
			},
			OnReasoning: func(content string) {
				if content == "" {
					return
				}
				partial.Content[thinkIdx].Thinking += content
				eventCh <- agentcore.StreamEvent{
					Type:         agentcore.StreamEventThinkingDelta,
					ContentIndex: thinkIdx,
					Delta:        content,
					Message:      partial,
				}
			},
			OnReasoningEnd: func(_ string) {
				eventCh <- agentcore.StreamEvent{
					Type:         agentcore.StreamEventThinkingEnd,
					ContentIndex: thinkIdx,
					Message:      partial,
				}
			},
			OnContentStart: func() {
				partial.Content = append(partial.Content, agentcore.TextBlock(""))
				textIdx = len(partial.Content) - 1
				eventCh <- agentcore.StreamEvent{
					Type:         agentcore.StreamEventTextStart,
					ContentIndex: textIdx,
					Message:      partial,
				}
			},
			OnContent: func(delta string) {
				partial.Content[textIdx].Text += delta
				eventCh <- agentcore.StreamEvent{
					Type:         agentcore.StreamEventTextDelta,
					ContentIndex: textIdx,
					Delta:        delta,
					Message:      partial,
				}
			},
			OnContentEnd: func(_ string) {
				eventCh <- agentcore.StreamEvent{
					Type:         agentcore.StreamEventTextEnd,
					ContentIndex: textIdx,
					Message:      partial,
				}
			},
			OnToolCallStart: func(_ *litellm.ToolCallDelta) {
				eventCh <- agentcore.StreamEvent{
					Type:    agentcore.StreamEventToolCallStart,
					Message: partial,
				}
			},
			OnToolCall: func(delta *litellm.ToolCallDelta) {
				if delta.ArgumentsDelta == "" {
					return
				}
				eventCh <- agentcore.StreamEvent{
					Type:    agentcore.StreamEventToolCallDelta,
					Delta:   delta.ArgumentsDelta,
					Message: partial,
				}
			},
			OnToolCallEnd: func(call litellm.ToolCall) {
				partial.Content = append(partial.Content, agentcore.ToolCallBlock(agentcore.ToolCall{
					ID:   call.ID,
					Name: call.Function.Name,
					Args: safeArgs(call.Function.Arguments),
				}))
				idx := len(partial.Content) - 1
				eventCh <- agentcore.StreamEvent{
					Type:         agentcore.StreamEventToolCallEnd,
					ContentIndex: idx,
					Message:      partial,
				}
			},
		})
		if err != nil {
			eventCh <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err}
			return
		}

		if resp != nil && (resp.Usage.TotalTokens > 0 || resp.Usage.PromptTokens > 0) {
			u := resp.Usage
			partial.Usage = &agentcore.Usage{
				Input:       u.PromptTokens,
				Output:      u.CompletionTokens,
				CacheRead:   u.CacheReadInputTokens,
				CacheWrite:  u.CacheCreationInputTokens,
				TotalTokens: u.TotalTokens,
			}
			partial.Usage.Cost = agentllm.CalculateCost(m.Info().Pricing, partial.Usage)
		}
		if resp != nil {
			partial.StopReason = mapStopReason(resp.FinishReason)
		}

		eventCh <- agentcore.StreamEvent{
			Type:       agentcore.StreamEventDone,
			Message:    partial,
			StopReason: partial.StopReason,
		}
	}()

	return eventCh, nil
}

func applyResponsesCallConfig(req *litellm.OpenAIResponsesRequest, opts []agentcore.CallOption) {
	callCfg := agentcore.ResolveCallConfig(opts)

	if callCfg.APIKey != "" {
		req.APIKey = callCfg.APIKey
	}

	if callCfg.ThinkingLevel != "" {
		if callCfg.ThinkingLevel == agentcore.ThinkingOff {
			req.ReasoningEffort = "none"
		} else {
			req.ReasoningEffort = string(callCfg.ThinkingLevel)
			req.ReasoningSummary = "auto"
		}
	}

	if callCfg.MaxTokens > 0 {
		req.MaxOutputTokens = &callCfg.MaxTokens
	}
}

func applyResponsesToolConfig(req *litellm.OpenAIResponsesRequest, tools []agentcore.ToolSpec) {
	if len(tools) == 0 {
		return
	}

	ltTools := make([]litellm.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		ltTools = append(ltTools, litellm.Tool{
			Type: "function",
			Function: litellm.FunctionDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
			DeferLoading: tool.DeferLoading,
		})
	}
	req.Tools = ltTools
}

func convertMessages(messages []agentcore.Message) []litellm.Message {
	out := make([]litellm.Message, len(messages))
	for i, msg := range messages {
		out[i] = convertSingleMessage(msg)
	}
	return out
}

func convertSingleMessage(msg agentcore.Message) litellm.Message {
	llmMsg := litellm.Message{Role: string(msg.Role)}

	if hasImageContent(msg.Content) {
		var parts []litellm.MessageContent
		for _, block := range msg.Content {
			switch block.Type {
			case agentcore.ContentText:
				parts = append(parts, litellm.TextContent(block.Text))
			case agentcore.ContentImage:
				if block.Image == nil {
					continue
				}
				imgURL := block.Image.URL
				if imgURL == "" {
					imgURL = "data:" + block.Image.MimeType + ";base64," + block.Image.Data
				}
				parts = append(parts, litellm.ImageContent(imgURL))
			}
		}
		llmMsg.Contents = parts
	} else {
		llmMsg.Content = msg.TextContent()
	}

	if cc, ok := msg.Metadata["cache_control"].(string); ok && cc != "" {
		llmMsg.CacheControl = &litellm.CacheControl{Type: cc}
	}

	if msg.Role == agentcore.RoleTool {
		if id, ok := msg.Metadata["tool_call_id"].(string); ok {
			llmMsg.ToolCallID = id
		}
		if isErr, ok := msg.Metadata["is_error"].(bool); ok {
			llmMsg.IsError = isErr
		}
		if hasToolRefBlocks(msg.Content) {
			llmMsg.Contents = convertToolRefContent(msg.Content)
			llmMsg.Content = ""
		}
	}

	toolCalls := msg.ToolCalls()
	if len(toolCalls) > 0 {
		llmMsg.ToolCalls = make([]litellm.ToolCall, len(toolCalls))
		for i, call := range toolCalls {
			llmMsg.ToolCalls[i] = litellm.ToolCall{
				ID:   call.ID,
				Type: "function",
				Function: litellm.FunctionCall{
					Name:      call.Name,
					Arguments: string(call.Args),
				},
			}
		}
	}

	return llmMsg
}

func hasImageContent(blocks []agentcore.ContentBlock) bool {
	for _, block := range blocks {
		if block.Type == agentcore.ContentImage && block.Image != nil {
			return true
		}
	}
	return false
}

func hasToolRefBlocks(blocks []agentcore.ContentBlock) bool {
	for _, block := range blocks {
		if block.Type == agentcore.ContentToolRef {
			return true
		}
	}
	return false
}

func convertToolRefContent(blocks []agentcore.ContentBlock) []litellm.MessageContent {
	var parts []litellm.MessageContent
	for _, block := range blocks {
		switch block.Type {
		case agentcore.ContentText:
			if block.Text != "" {
				parts = append(parts, litellm.TextContent(block.Text))
			}
		case agentcore.ContentToolRef:
			if block.ToolName != "" {
				parts = append(parts, litellm.ToolRefContent(block.ToolName))
			}
		}
	}
	return parts
}

func convertLiteLLMResponse(response *litellm.Response) agentcore.Message {
	var content []agentcore.ContentBlock

	if response.ReasoningContent != "" {
		content = append(content, agentcore.ThinkingBlock(response.ReasoningContent))
	}
	if response.Content != "" {
		content = append(content, agentcore.TextBlock(response.Content))
	}
	for _, call := range response.ToolCalls {
		content = append(content, agentcore.ToolCallBlock(agentcore.ToolCall{
			ID:   call.ID,
			Name: call.Function.Name,
			Args: safeArgs(call.Function.Arguments),
		}))
	}

	var usage *agentcore.Usage
	if response.Usage.TotalTokens > 0 {
		usage = &agentcore.Usage{
			Input:       response.Usage.PromptTokens,
			Output:      response.Usage.CompletionTokens,
			CacheRead:   response.Usage.CacheReadInputTokens,
			CacheWrite:  response.Usage.CacheCreationInputTokens,
			TotalTokens: response.Usage.TotalTokens,
		}
	}

	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    content,
		StopReason: mapStopReason(response.FinishReason),
		Usage:      usage,
	}
}

func mapStopReason(reason string) agentcore.StopReason {
	switch reason {
	case litellm.FinishReasonStop, "":
		return agentcore.StopReasonStop
	case litellm.FinishReasonLength:
		return agentcore.StopReasonLength
	case litellm.FinishReasonToolCall:
		return agentcore.StopReasonToolUse
	case litellm.FinishReasonError:
		return agentcore.StopReasonError
	default:
		return agentcore.StopReason(reason)
	}
}

func safeArgs(args string) json.RawMessage {
	if args == "" {
		return json.RawMessage("{}")
	}
	if !json.Valid([]byte(args)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(args)
}
