package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
)

// 冷启动共创：从零澄清需求，产出整本书的创作指令。
const coCreateSystemPrompt = `你是一个小说共创助手。你的任务不是直接开始写小说，而是通过多轮简短对话帮助用户澄清创作需求，并持续整理出一段可直接交给创作引擎的中文创作指令。

## 联网搜索

你有 ` + "`web_search`" + ` 工具：当用户问到需要外部资料才能答好的问题（特定流派套路、题材常识、行业背景、时事参考等），先调用 ` + "`web_search(query=...)`" + ` 拉取真实资料，再基于结果给出回复。工具返回 ` + "`summary`" + `（中文总结）+ ` + "`links`" + `（参考链接）；若返回空 ` + "`hint`" + ` 说明上游不支持，告诉用户本次未能联网，继续基于已有知识工作，不要重试。无需外部资料的常规澄清不要硬搜，浪费 token。

工具调用与最终输出相互独立：调工具那轮不会输出 XML，系统拿到工具结果后会再让你续生成 XML。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先回应用户的输入，再最多提出 1 到 2 个当前最关键的问题。如果信息已足够开始创作，告诉用户可以按 Ctrl+S 开始。
</reply>

<draft>
当前完整的创作指令草稿，使用 Markdown：直接从二级标题开始，例如 "## 主题"、"## 关键要素"、"## 待澄清信息"；用项目符号列出要点。每一轮都要在已有结论上**累积更新**，吸收用户最新意图；即使本轮没有新增也要把完整草稿原样再写一次——不要省略、不要写"（保持上一轮）"之类的占位。
</draft>
` + coCreateProtocolTail

// 阶段共创：小说已写了一部分，规划"后续阶段"的走向。调用方需把当前故事状态摘要
// 追加到本 prompt 之后（"## 当前故事状态" 段），让模型在已写内容的基础上规划。
const stageCoCreateSystemPrompt = `你是一个小说"阶段共创"助手。这本小说已经写了一部分（进度见下方"当前故事状态"）。用户暂停下来，想和你一起规划"后续阶段"的走向，再继续创作。

你的任务不是续写正文，而是通过多轮简短对话帮用户想清楚后面这一段（接下来若干章 / 下一弧 / 下一卷）要往哪走，并持续整理出一段"后续方向 brief"，供创作引擎据此推进。

铁律：所有建议必须与"当前故事状态"里已发生的剧情、人物、伏笔一致，绝不推翻或忽略已写内容；只规划"后续怎么走"，不重新设计整本书。

## 联网搜索

你有 ` + "`web_search`" + ` 工具：当用户问到需要外部资料才能答好的问题（特定流派发展史、力量体系参考、连载套路变迁、行业/历史背景等），先调用 ` + "`web_search(query=...)`" + ` 拉取真实资料，再基于结果给出回复。工具返回 ` + "`summary`" + `（中文总结）+ ` + "`links`" + `（参考链接）；若返回空 ` + "`hint`" + ` 说明上游不支持，告诉用户本次未能联网，继续基于已有知识工作，不要重试。无需外部资料的常规规划不要硬搜，浪费 token。

工具调用与最终输出相互独立：调工具那轮不会输出 XML，系统拿到工具结果后会再让你续生成 XML。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先回应用户的输入，再最多提出 1 到 2 个当前最关键的问题。如果后续方向已足够清晰，告诉用户可以按 Ctrl+S 把方向交给创作引擎、继续创作。
</reply>

<draft>
当前完整的"后续方向 brief"，使用 Markdown：直接从二级标题开始，例如 "## 后续走向"、"## 关键转折"、"## 要收的伏笔"、"## 节奏与篇幅"；用项目符号列出要点。每一轮都要在已有结论上**累积更新**，吸收用户最新意图；即使本轮没有新增也要把完整 brief 原样再写一次——不要省略、不要写"（保持上一轮）"之类的占位。
</draft>
` + coCreateProtocolTail

// coCreateProtocolTail 是两种共创模式共用的输出协议尾部（<ready> / <suggestions> + 输出规范）。
// 两模式只在开场语境与 <draft> 语义上不同，协议完全一致。
const coCreateProtocolTail = `
<ready>false</ready>

<suggestions>
1-3 条"用户接下来可能想说的话"，每行一条以 "- " 开头。这是用户卡壳时的引导，
按数字键填入输入框，用户可再编辑后发送。

要求：
- 站在用户口吻，像用户对你说的话，不要写成助手反问。
- 每条不超过 25 字，多样化句式，避免千篇一律。
- 给倾向 / 选择 / 补充意图，不要一句话替用户写完整设定。
</suggestions>

输出规范：
- 必须使用四个 XML 标签：<reply> / <draft> / <ready> / <suggestions>，每个都必须完整开闭。
- 标签名只能小写英文，不要改写成 <REPLY> / <REWRITE> / <回复> 等任何变体。
- 标签外不要添加任何说明、思考或代码围栏。
- <draft> 内允许多行 Markdown，直接换行书写，不需要任何转义。
- <ready> 只写 true 或 false。信息已足够时填 true。
- <ready>true</ready> 时 <suggestions> 可以为空（保留空标签 <suggestions></suggestions> 即可）。`

// CoCreateProgressKind 标识流式回调的内容类型。
const (
	CoCreateProgressThinking = "thinking"
	CoCreateProgressReply    = "reply"
)

// 四段式 XML 标签输出。XML 风格比方括号 marker 更鲁棒——Claude/GPT 训练数据里
// 大量 <thinking>...</thinking> 这类格式，模型几乎不会把 <reply> 改写成 <REWRITE>
// 或其他变体；闭合标签也让流式中段截断更精确（不依赖找下一个 marker 来断尾）。
const (
	tagReply       = "reply"
	tagDraft       = "draft"
	tagReady       = "ready"
	tagSuggestions = "suggestions"
)

func coCreateStream(ctx context.Context, models *bootstrap.ModelSet, sessions *store.SessionStore, sysPrompt string, history []CoCreateMessage, onProgress func(kind, text string), webSearchTool webSearchExecuter) (reply CoCreateReply, err error) {
	if len(history) == 0 {
		return CoCreateReply{}, fmt.Errorf("cocreate history is empty")
	}

	model := models.ForRole("thinking")
	// 总预算：1 次主答 (~30s) + 至多 3 次工具往返（每次 web_search 实测 8-38s + 续生成 ~30s ≈ 60s）
	// = 30 + 3×60 = 210s 最坏情况。给 300s 留 ~90s 余量，覆盖极端慢路由。
	// 旧值 180s 不够——LLM 调 2-3 次 web_search 就把预算耗光，正在跑的调用被 ctx.DeadlineExceeded
	// 切断，错误冒泡到用户那变成"上游服务超时"。
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()

	msgs := []agentcore.Message{agentcore.SystemMsg(sysPrompt)}
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

	// 共创模式下的 web_search 工具规格。LLM 看到这个工具就会自主决定何时调用。
	// cocoCreate 没有完整的 agent loop（GenerateStream 不会自己跑 tool_use → tool_result
	// 续生成），下面手动实现一个 mini loop：每次流结束检测 tool_call，执行后把
	// assistant(tool_use) + tool_result 加回 msgs 再续生成。
	var toolSpecs []agentcore.ToolSpec
	if webSearchTool != nil {
		toolSpecs = []agentcore.ToolSpec{webSearchToolSpec()}
	}
	const maxCoCreateTurns = 4 // 1 次主答 + 至多 3 次工具往返，避免 LLM 死循环

	var raw, thinking strings.Builder
	start := time.Now()
	defer func() {
		if sessions == nil {
			return
		}
		_ = sessions.LogCoCreate(coCreateLogEntry{
			Time:         time.Now(),
			DurationMS:   time.Since(start).Milliseconds(),
			InputHistory: history,
			RawResponse:  raw.String(),
			RawLen:       len([]rune(raw.String())),
			Thinking:     thinking.String(),
			ParsedReply:  reply.Message,
			ParsedDraft:  reply.Prompt,
			ParsedReady:  reply.Ready,
			ParsedSugs:   reply.Suggestions,
			Error:        errString(err),
		})
	}()

	for turn := 0; turn < maxCoCreateTurns; turn++ {
		streamCh, err := model.GenerateStream(ctx, msgs, toolSpecs, agentcore.WithMaxTokens(8192))
		if err != nil {
			return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", err)
		}

		var (
			turnText      strings.Builder
			streamed      bool
			pendingCalls  []agentcore.ToolCall
			finalMsg      agentcore.Message
		)
		for ev := range streamCh {
			switch ev.Type {
			case agentcore.StreamEventThinkingDelta:
				thinking.WriteString(ev.Delta)
				if onProgress != nil {
					onProgress(CoCreateProgressThinking, thinking.String())
				}
			case agentcore.StreamEventTextDelta:
				streamed = true
				turnText.WriteString(ev.Delta)
				if onProgress != nil {
					onProgress(CoCreateProgressReply, extractReplyPreview(turnText.String()))
				}
			case agentcore.StreamEventToolCallEnd:
				if ev.CompletedToolCall != nil {
					pendingCalls = append(pendingCalls, *ev.CompletedToolCall)
				}
			case agentcore.StreamEventDone:
				finalMsg = ev.Message
				if !streamed {
					turnText.WriteString(ev.Message.TextContent())
				}
			case agentcore.StreamEventError:
				if ev.Err != nil {
					return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", ev.Err)
				}
				return CoCreateReply{}, fmt.Errorf("cocreate generate failed")
			}
		}

		// 把这一轮的文本并入总 raw。多轮工具往返时通常只有最后一轮输出 XML 协议，
		// 但稳妥起见用 += 拼接：parseCoCreateResponse 会兜底找不到 XML 标签的情况。
		raw.WriteString(turnText.String())

		// 没工具调用：mini loop 结束
		if len(pendingCalls) == 0 || webSearchTool == nil {
			break
		}

		// 有工具调用：执行 web_search，把 assistant(tool_use) + tool_result 加回 msgs 续生成。
		// finalMsg 已含模型输出的 tool_use 块（agentcore 保证 Done 时 Message 完整）。
		msgs = append(msgs, finalMsg)
		for _, tc := range pendingCalls {
			if tc.Name != "web_search" {
				continue
			}
			result, execErr := webSearchTool.Execute(ctx, tc.Args)
			if execErr != nil {
				result, _ = json.Marshal(map[string]any{
					"query": "",
					"hint":  "工具执行失败：" + execErr.Error(),
				})
			}
			// agentcore.ToolResultMsg 把 tool_result 包成 user 角色带 tool_result 块的消息，
			// 符合 Anthropic 协议（tool_result 必须在 user message 里）。
			msgs = append(msgs, agentcore.ToolResultMsg(tc.ID, result, false))
		}
	}

	// Channel fallback：思考型模型（R1/GLM-Z1/QwQ 等）偶发把完整答案写进
	// reasoning_content 后没切回 final answer 通道，导致 raw 为空但 thinking 含
	// 完整四段。实测见 meta/sessions/cocreate.jsonl —— 直接拿 thinking 当 raw 解析，
	// 协议层已有降级处理（无 [REPLY] 标记时整段当 reply），救场后 UI 体验无差别。
	rawText := raw.String()
	if strings.TrimSpace(rawText) == "" {
		if t := strings.TrimSpace(thinking.String()); t != "" {
			rawText = t
		}
	}
	reply, err = parseCoCreateResponse(rawText)
	return reply, err
}

// webSearchExecuter 抽象 *tools.WebSearchTool 的 Execute 方法，让 cocreate_test
// 可以注入 fake。nil 表示不挂搜索（cold start 早期或测试场景）。
type webSearchExecuter interface {
	Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// webSearchToolSpec 是 cocreate 传给 LLM 的 web_search 工具规格。
// 与 tools.WebSearchTool.Schema() 同构，但 ToolSpec 是 agentcore 的 wire 格式。
// 复用 tools 包的描述保持一致，避免 LLM 看到两个版本的 web_search。
func webSearchToolSpec() agentcore.ToolSpec {
	return agentcore.ToolSpec{
		Name:        "web_search",
		Description: "联网搜索：获取外部资料、流派套路、时事常识、参考资料等。返回模型基于搜索结果生成的总结和原始链接。能否真正联网取决于代理当前路由到的上游是否支持服务端 web_search；不支持时返回空结果与 hint，调用方应基于已有知识继续工作。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "搜索关键词（中英文均可，过长会被模型自行精炼）",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "返回链接上限，默认 8（已截取最相关的前 N 条）",
				},
			},
			"required": []string{"query"},
		},
	}
}

// coCreateLogEntry 是写入 meta/sessions/cocreate.jsonl 的一行结构。
// 字段命名贴近 jsonl 直查习惯（snake_case），方便 jq 过滤。
type coCreateLogEntry struct {
	Time         time.Time         `json:"time"`
	DurationMS   int64             `json:"duration_ms"`
	InputHistory []CoCreateMessage `json:"input_history"`
	RawResponse  string            `json:"raw_response"`
	RawLen       int               `json:"raw_len"`
	Thinking     string            `json:"thinking,omitempty"`
	ParsedReply  string            `json:"parsed_reply"`
	ParsedDraft  string            `json:"parsed_draft"`
	ParsedReady  bool              `json:"parsed_ready"`
	ParsedSugs   []string          `json:"parsed_sugs,omitempty"`
	Error        string            `json:"error,omitempty"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func assistantMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(text)},
		Timestamp: time.Now(),
	}
}

// parseCoCreateResponse 解析 XML 标签输出。模型若没遵守协议（直接说自然语言），
// 整段作为 reply 显示，draft 留空让 session 保留上一轮。
//
// ★ 部分遵守协议的 rescue 路径（修历史 bug）：
// 旧版只要 reply=="" 就直接 Prompt="" 返回,连已成功解析的 draft 一起丢弃。
// 这会让 LLM 写了 <draft> 但漏了 <reply>>(常见于 max_tokens 截断 / 模型半遵守协议)
// 时整份大纲消失——用户视角看到的是"本轮 draft 为空",但 raw 里其实有完整内容。
// 现行逻辑:只有 reply+draft 双空才走"整段当 reply"降级;reply 缺失但 draft 有时
// 给一个 fallback reply,保住 draft 不被丢弃。
func parseCoCreateResponse(raw string) (CoCreateReply, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CoCreateReply{}, fmt.Errorf("cocreate empty response")
	}

	reply, draft, ready, suggestions := splitCoCreateMarkers(raw)

	// 双空:模型完全没遵守协议,整段作为 reply。
	if reply == "" && draft == "" {
		return CoCreateReply{Message: raw, Prompt: "", Ready: false, Raw: raw}, nil
	}

	// reply 缺失但 draft 已解析出来:不要丢弃 draft！给个 fallback reply,
	// 让用户在 TUI 看到本轮 AI 没给对话文本,但右栏 brief 已经更新。
	if reply == "" {
		reply = "_（本轮模型未输出 <reply> 段,但已更新 <draft>;查看右栏当前创作指令）_"
	}

	return CoCreateReply{
		Message:     reply,
		Prompt:      draft,
		Ready:       ready,
		Suggestions: suggestions,
		Raw:         raw,
	}, nil
}

// splitCoCreateMarkers 按四个 XML 标签切分文本。
// 标签可能缺失（流式中段或模型遗漏），缺失部分对应字段为空 / false / nil。
// 缺失闭标签时，extractTagContent 会取到字符串末尾，仍尽力解析。
func splitCoCreateMarkers(s string) (reply, draft string, ready bool, suggestions []string) {
	reply = extractTagContent(s, tagReply)
	draft = extractTagContent(s, tagDraft)
	readyStr := strings.ToLower(extractTagContent(s, tagReady))
	ready = readyStr == "true" || readyStr == "yes"
	suggestions = parseSuggestions(extractTagContent(s, tagSuggestions))
	return
}

// extractTagContent 从 s 中抠出 <tag>...</tag> 之间的文本。
// 三种偶发故障场景兜底，避免直接走降级丢字段：
//  1. 有开无闭（流式中段）→ 切到下一个已知开标签前
//  2. 无开有闭（模型 typo，如 <suggestions> 写成 <uggestions>）→ 从最近一个已知
//     完整闭合标签的结束位置开始，到 </tag> 之前
//  3. reply 完全无开标签（模型直接以自然语言开篇，末尾贴 </reply>）→ 从开头到 </reply>
func extractTagContent(s, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	oIdx := strings.Index(s, open)
	if oIdx >= 0 {
		rest := s[oIdx+len(open):]
		if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
			return strings.TrimSpace(rest[:cIdx])
		}
		// 有开无闭 → 切到下一个已知开标签前
		for _, other := range []string{"<reply>", "<draft>", "<ready>", "<suggestions>"} {
			if other == open {
				continue
			}
			if idx := strings.Index(rest, other); idx >= 0 {
				rest = rest[:idx]
			}
		}
		return strings.TrimSpace(rest)
	}

	// 无开有闭 → 从最近一个已知完整闭合标签的结束位置开始，到 </tag>。
	if cIdx := strings.Index(s, closeTag); cIdx >= 0 {
		prefix := s[:cIdx]
		start := 0
		for _, t := range []string{"</reply>", "</draft>", "</ready>", "</suggestions>"} {
			if t == closeTag {
				continue
			}
			if i := strings.LastIndex(prefix, t); i >= 0 {
				if end := i + len(t); end > start {
					start = end
				}
			}
		}
		return strings.TrimSpace(prefix[start:])
	}
	return ""
}

// parseSuggestions 把 <suggestions> 段每行抠出来，去掉 "- " / "* " / "1. " 等列表前缀。
// 最多保留 3 条；空行、过短（<2 字）、整行像 XML 标签的（typo 开标签兜底残留，
// 例如 <uggestions>）忽略。
func parseSuggestions(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 整行像 XML 标签 → 跳过（防 typo 开标签污染）
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			continue
		}
		// 剥列表前缀
		switch {
		case strings.HasPrefix(line, "- "):
			line = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "* "):
			line = strings.TrimSpace(line[2:])
		case isOrderedSuggestion(line):
			line = stripOrderedPrefix(line)
		}
		if len([]rune(line)) < 2 {
			continue
		}
		out = append(out, line)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// isOrderedSuggestion 判断行首是否形如 "1. " / "12. "（数字+点+空格）。
func isOrderedSuggestion(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

func stripOrderedPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) {
		return line
	}
	return strings.TrimSpace(line[i+2:])
}

// extractReplyPreview 流式预览：raw 还在生长时给 UI 一段可显示的文本。
// 找到 <reply> 之后的内容，切到 </reply> 或下一个开标签 <draft> 之前。
// 模型半遵守（漏 <reply> 开标签）时，开头到 </reply> 或 <draft> 都算 reply。
func extractReplyPreview(raw string) string {
	trimmed := strings.TrimSpace(raw)
	open := "<" + tagReply + ">"
	closeTag := "</" + tagReply + ">"
	draftOpen := "<" + tagDraft + ">"

	rest := trimmed
	if rIdx := strings.Index(trimmed, open); rIdx >= 0 {
		rest = trimmed[rIdx+len(open):]
	}
	if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
		return strings.TrimSpace(rest[:cIdx])
	}
	if dIdx := strings.Index(rest, draftOpen); dIdx >= 0 {
		rest = rest[:dIdx]
	}
	return strings.TrimSpace(rest)
}
