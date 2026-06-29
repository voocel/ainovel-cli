package host

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
)

// 冷启动共创：从零澄清需求，产出整本书的创作指令。
const coCreateSystemPrompt = `你是一个小说共创助手。你的任务不是直接Bắt đầu写小说，而是通过多轮简短对话帮助用户澄清创作需求，并持续整理出一段可直接交给创作引擎的中文创作指令。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先回Áp dụng户的Nhập，再最多提出 1 到 2 个Hiện tại最关键的问题。如果信息已足够Bắt đầu创作，告诉用户可以按 Ctrl+S Bắt đầu。
</reply>

<draft>
Hiện tại完整的创作指令Bản nháp，使用 Markdown：直接从二级Tiêu đềBắt đầu，例如 "## 主题"、"## 关键要素"、"## 待澄清信息"；用项目符号列出要点。每一轮都要在已有结论上**累积更Mới**，吸收用户最Mới意图；即使本轮没有Mới增也要把完整Bản nháp原样再写一次——不要省略、不要写"（保持上一轮）"之类的占位。
</draft>
` + coCreateProtocolTail

// 阶段共创：小说已写了一Phần，规划"后续阶段"的走向。调用方需把Hiện tại故事Trạng tháiTóm tắt
// 追加到本 prompt 之后（"## Hiện tại故事Trạng thái" 段），让Mô hình在已写内容的基础上规划。
const stageCoCreateSystemPrompt = `你是一个小说"阶段共创"助手。这本小说已经写了一Phần（Tiến độ见下方"Hiện tại故事Trạng thái"）。用户Tạm dừng下来，想和你一起规划"后续阶段"的走向，再Tiếp tục创作。

你的任务不是续写Chính văn，而是通过多轮简短对话帮用户想清楚后面这一段（接下来若干章 / 下一弧 / 下一卷）要往哪走，并持续整理出一段"后续方向 brief"，供创作引擎据此推进。

铁律：所有建议必须与"Hiện tại故事Trạng thái"里已发生的剧情、Nhân vật、伏笔一致，绝不推翻或忽略已写内容；只规划"后续怎么走"，不重Mới设计整本书。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先回Áp dụng户的Nhập，再最多提出 1 到 2 个Hiện tại最关键的问题。如果后续方向已足够清晰，告诉用户可以按 Ctrl+S 把方向交给创作引擎、Tiếp tục创作。
</reply>

<draft>
Hiện tại完整的"后续方向 brief"，使用 Markdown：直接从二级Tiêu đềBắt đầu，例如 "## 后续走向"、"## 关键转折"、"## 要收的伏笔"、"## 节奏与篇幅"；用项目符号列出要点。每一轮都要在已有结论上**累积更Mới**，吸收用户最Mới意图；即使本轮没有Mới增也要把完整 brief 原样再写一次——不要省略、不要写"（保持上一轮）"之类的占位。
</draft>
` + coCreateProtocolTail

// coCreateProtocolTail 是两种共创模式共用的输出协议尾部（<ready> / <suggestions> + 输出规范）。
// 两模式只在开场语境与 <draft> 语义上不同，协议完全一致。
const coCreateProtocolTail = `
<ready>false</ready>

<suggestions>
1-3 条"用户接下来可能想说的话"，每行一条以 "- " 开头。这是用户卡壳时的引导，
按数字键填入Nhập框，用户可再Sửa后发送。

要求：
- 站在用户口吻，像用户对你说的话，不要写成助手反问。
- 每条不超过 25 字，多样化句式，避免千篇一律。
- 给倾向 / Chọn / 补充意图，不要一句话替用户写完整设定。
</suggestions>

输出规范：
- 必须使用四个 XML 标签：<reply> / <draft> / <ready> / <suggestions>，每个都必须完整开闭。
- 标签名只能小写英文，不要改写成 <REPLY> / <REWRITE> / <回复> 等任何变体。
- 标签外不要添加任何说明、思考或代码围栏。
- <draft> 内允许多行 Markdown，直接换行书写，不Cần任何转义。
- <ready> 只写 true 或 false。信息已足够时填 true。
- <ready>true</ready> 时 <suggestions> 可以为Rỗng（保留Rỗng标签 <suggestions></suggestions> 即可）。`

// CoCreateProgressKind 标识流式回调的内容类型。
const (
	CoCreateProgressThinking = "thinking"
	CoCreateProgressReply    = "reply"
)

// 四段式 XML 标签输出。XML 风格比方括号 marker 更鲁棒——Claude/GPT 训练数据里
// 大量 <thinking>...</thinking> 这类格式，Mô hình几乎不会把 <reply> 改写成 <REWRITE>
// 或Khác变体；闭合标签也让流式中段截断更精确（不依赖找下一个 marker 来断尾）。
const (
	tagReply       = "reply"
	tagDraft       = "draft"
	tagReady       = "ready"
	tagSuggestions = "suggestions"
)

func coCreateStream(ctx context.Context, models *bootstrap.ModelSet, sessions *store.SessionStore, sysPrompt string, history []CoCreateMessage, onProgress func(kind, text string)) (reply CoCreateReply, err error) {
	if len(history) == 0 {
		return CoCreateReply{}, fmt.Errorf("cocreate history is empty")
	}

	model := models.ForRole("thinking")
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
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

	var raw, thinking strings.Builder

	// 排查 "cocreate empty response" 等偶发问题Cần看到Mô hình实际Quay lại什么。
	// 每轮全程落盘到 <output>/meta/sessions/cocreate.jsonl，与正式创作的 session 日志同位。
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

	streamCh, err := model.GenerateStream(ctx, msgs, nil, agentcore.WithMaxTokens(2048))
	if err != nil {
		return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", err)
	}

	var streamed bool
	for ev := range streamCh {
		switch ev.Type {
		case agentcore.StreamEventThinkingDelta:
			thinking.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressThinking, thinking.String())
			}
		case agentcore.StreamEventTextDelta:
			streamed = true
			raw.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressReply, extractReplyPreview(raw.String()))
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

	// Channel fallback：思考型Mô hình（R1/GLM-Z1/QwQ 等）偶发把完整答案写进
	// reasoning_content 后没切回 final answer 通道，导致 raw 为Rỗng但 thinking 含
	// 完整四段。实测见 meta/sessions/cocreate.jsonl —— 直接拿 thinking 当 raw 解析，
	// 协议层已有降级处理（Không có [REPLY] 标记时整段当 reply），救场后 UI 体验Không có差别。
	rawText := raw.String()
	if strings.TrimSpace(rawText) == "" {
		if t := strings.TrimSpace(thinking.String()); t != "" {
			rawText = t
		}
	}
	reply, err = parseCoCreateResponse(rawText)
	return reply, err
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

// parseCoCreateResponse 解析 XML 标签输出。Mô hình若没遵守协议（直接说自然语言），
// 整段作为 reply 显示，draft 留Rỗng让 session 保留上一轮。
func parseCoCreateResponse(raw string) (CoCreateReply, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CoCreateReply{}, fmt.Errorf("cocreate empty response")
	}

	reply, draft, ready, suggestions := splitCoCreateMarkers(raw)
	if reply == "" {
		// Mô hình没遵守 XML 协议：整段作为 reply。
		return CoCreateReply{Message: raw, Prompt: "", Ready: false, Raw: raw}, nil
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
// 标签可能缺失（流式中段或Mô hình遗漏），缺失Phần对应字段为Rỗng / false / nil。
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
// 三种偶发故障Cảnh兜底，避免直接走降级丢字段：
//  1. 有开Không có闭（流式中段）→ 切到下一个已知开标签前
//  2. Không có开有闭（Mô hình typo，如 <suggestions> 写成 <uggestions>）→ 从最近一个已知
//     完整闭合标签的Kết thúc位置Bắt đầu，到 </tag> 之前
//  3. reply 完全Không có开标签（Mô hình直接以自然语言开篇，末尾贴 </reply>）→ 从开头到 </reply>
func extractTagContent(s, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	oIdx := strings.Index(s, open)
	if oIdx >= 0 {
		rest := s[oIdx+len(open):]
		if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
			return strings.TrimSpace(rest[:cIdx])
		}
		// 有开Không có闭 → 切到下一个已知开标签前
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

	// Không có开有闭 → 从最近一个已知完整闭合标签的Kết thúc位置Bắt đầu，到 </tag>。
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
// 最多保留 3 条；Rỗng行、过短（<2 字）、整行像 XML 标签的（typo 开标签兜底残留，
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
		// 整行像 XML 标签 → Bỏ qua（防 typo 开标签污染）
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

// isOrderedSuggestion 判断行首Có czy không形如 "1. " / "12. "（数字+点+Rỗng格）。
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
// Mô hình半遵守（漏 <reply> 开标签）时，开头到 </reply> 或 <draft> 都算 reply。
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
