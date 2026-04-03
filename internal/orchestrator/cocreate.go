package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

type CoCreateMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CoCreateReply struct {
	Message string
	Prompt  string
	Ready   bool
}

type coCreatePayload struct {
	Reply       string `json:"reply"`
	DraftPrompt string `json:"draft_prompt"`
	Ready       bool   `json:"ready"`
}

const coCreateSystemPrompt = `你是一个小说共创助手。你的任务不是直接开始写小说，而是通过多轮简短对话帮助用户澄清创作需求，并持续整理出一段可直接交给创作引擎的中文创作指令。

要求：
1. 每轮先用自然中文回应用户，再最多提出 1 到 2 个当前最关键的问题。
2. 如果信息已经足够开始创作，就不要继续追问，明确告诉用户可以开始，并把 ready 设为 true。
3. 持续输出一段"当前创作指令草稿"，它必须是完整的中文创作要求，后续会被直接送入小说创作流程。
4. draft_prompt 必须使用清晰的 Markdown 结构来组织信息，例如标题、二级标题、项目符号，方便用户快速确认关键信息。
5. 用户若修改方向，你必须吸收修改，更新草稿，而不是固执沿用旧版本。
6. 只输出 JSON，不要附加解释。reply 是自然中文，draft_prompt 是 Markdown 文本。

输出 JSON 格式：
{
  "reply": "给用户看的自然中文回复",
  "draft_prompt": "整理后的完整创作指令草稿",
  "ready": true
}`

// CoCreateStream 使用多轮对话帮助用户澄清创作需求。
// ctx 由调用方控制生命周期，取消时 LLM 请求立即终止。
func (eng *Engine) CoCreateStream(ctx context.Context, history []CoCreateMessage, onReply func(string)) (CoCreateReply, error) {
	if len(history) == 0 {
		return CoCreateReply{}, fmt.Errorf("cocreate history is empty")
	}

	model := eng.models.ForRole("thinking")
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	msgs := []agentcore.Message{agentcore.SystemMsg(coCreateSystemPrompt)}
	for _, item := range history {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Role)) {
		case "assistant":
			msgs = append(msgs, assistantMsg(content))
		default:
			msgs = append(msgs, agentcore.UserMsg(content))
		}
	}

	streamCh, err := model.GenerateStream(ctx, msgs, nil, agentcore.WithMaxTokens(1200))
	if err != nil {
		return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", err)
	}

	var raw strings.Builder
	var streamed bool
	for ev := range streamCh {
		switch ev.Type {
		case agentcore.StreamEventTextDelta:
			streamed = true
			raw.WriteString(ev.Delta)
			if onReply != nil {
				onReply(extractReplyPreview(raw.String()))
			}
		case agentcore.StreamEventDone:
			if !streamed {
				raw.WriteString(ev.Message.TextContent())
			}
		case agentcore.StreamEventError:
			if ev.Err != nil {
				return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", ev.Err)
			}
			return CoCreateReply{}, fmt.Errorf("cocreate generate failed")
		}
	}
	return parseCoCreateResponse(raw.String())
}

func assistantMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(text)},
		Timestamp: time.Now(),
	}
}

func parseCoCreateResponse(raw string) (CoCreateReply, error) {
	var payload coCreatePayload
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &payload); err != nil {
		return CoCreateReply{}, fmt.Errorf("parse cocreate response: %w", err)
	}

	reply := strings.TrimSpace(payload.Reply)
	prompt := strings.TrimSpace(payload.DraftPrompt)
	if reply == "" {
		return CoCreateReply{}, fmt.Errorf("cocreate response missing reply")
	}
	return CoCreateReply{
		Message: reply,
		Prompt:  prompt,
		Ready:   payload.Ready,
	}, nil
}

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		return raw[start : end+1]
	}
	return raw
}

// extractReplyPreview 从不完整的 JSON 流中提取 reply 字段值做预览。
// 只处理 \n \" \\ 三种常见转义，完整解析由 parseCoCreateResponse 负责。
func extractReplyPreview(raw string) string {
	const marker = `"reply":"`
	start := strings.Index(raw, marker)
	if start < 0 {
		return ""
	}
	rest := raw[start+len(marker):]
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\\' && i+1 < len(rest) {
			i++
			switch rest[i] {
			case 'n':
				b.WriteByte('\n')
			case '"', '\\', '/':
				b.WriteByte(rest[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(rest[i])
			}
			continue
		}
		if rest[i] == '"' {
			break
		}
		b.WriteByte(rest[i])
	}
	return b.String()
}
