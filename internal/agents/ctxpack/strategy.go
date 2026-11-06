package ctxpack

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
)

// SummaryPrompts 是压缩摘要用的一组提示词。默认取 Writer 版本；其它角色可整套替换，
// 保持"按角色定制摘要提示词"的能力。
type SummaryPrompts struct {
	// System 是摘要请求的 system prompt。
	System string
	// Summary 是整段历史（没有旧摘要）时的摘要指令。
	Summary string
	// Update 是把新历史合并进旧摘要时的指令。
	Update string
	// TurnPrefix 是只摘要一个未完结轮次前缀时的指令。
	TurnPrefix string
}

func (p SummaryPrompts) withDefaults() SummaryPrompts {
	if p.System == "" {
		p.System = WriterSummarySystemPrompt
	}
	if p.Summary == "" {
		p.Summary = WriterSummaryPrompt
	}
	if p.Update == "" {
		p.Update = WriterUpdateSummaryPrompt
	}
	if p.TurnPrefix == "" {
		p.TurnPrefix = WriterTurnPrefixPrompt
	}
	return p
}

// WriterCompactConfig 是 WriterCompactor 的配置。
type WriterCompactConfig struct {
	// Store 提供章节摘要/弧卷摘要/角色快照等持久事实，作为压缩素材。
	Store *store.Store
	// Restore 可选的恢复包；非 nil 时其缓存内容随摘要一起注入。
	Restore *WriterRestorePack
	// KeepRecentTokens 是压缩后原样保留的最近历史 token 预算。
	KeepRecentTokens int
	// SummaryTokenBudget 是 store 摘要素材的 token 预算。
	SummaryTokenBudget int
	// RestoreTokenBudget 是恢复包消息的 token 预算。
	RestoreTokenBudget int
	// MinToolResultTokens 是工具结果微压缩的门槛：0 用默认值，负数关闭该步骤。
	MinToolResultTokens int
	// Prompts 可覆盖默认的 Writer 摘要提示词。
	Prompts SummaryPrompts
}

// defaultMinToolResultTokens 是工具结果微压缩的默认门槛，沿用旧 ContextEngine 的
// ToolMicrocompact(MinResultTokens: 200)。
const defaultMinToolResultTokens = 200

// minCompactTokens 是值得压缩的最小被替换历史量：更小的压缩带来的摘要不比它省。
const minCompactTokens = 500

// keepRecentToolResults 是工具结果微压缩保护的最新结果数。
const keepRecentToolResults = 5

// clearedToolResult 替换被微压缩掉的工具结果。
const clearedToolResult = "[工具结果已清理以节省上下文]"

// WriterCompactor 实现 agentcore.Compactor：把旧历史整段替换成一份 checkpoint
// 摘要（优先用 store 持久事实，其次用模型摘要），保留最近原文，并按需追加恢复包。
//
// 压缩后的结构是：
//
//	[摘要消息][恢复包消息?][最近历史…]
//
// 最近历史里的陈旧工具结果会被就地清理（见 microcompact）。
type WriterCompactor struct {
	store               *store.Store
	restore             *WriterRestorePack
	keepRecentTokens    int
	summaryTokenBudget  int
	restoreTokenBudget  int
	minToolResultTokens int
	prompts             SummaryPrompts
}

// NewWriterCompactor 构造 writer 的压缩器。返回值可直接赋给 agentcore.Config.Compactor。
func NewWriterCompactor(cfg WriterCompactConfig) *WriterCompactor {
	if cfg.KeepRecentTokens <= 0 {
		cfg.KeepRecentTokens = 20000
	}
	if cfg.SummaryTokenBudget <= 0 {
		cfg.SummaryTokenBudget = defaultStoreSummaryBudgetTokens
	}
	if cfg.RestoreTokenBudget <= 0 {
		cfg.RestoreTokenBudget = restoreBudgetTokens
	}
	if cfg.MinToolResultTokens == 0 {
		cfg.MinToolResultTokens = defaultMinToolResultTokens
	}
	if cfg.MinToolResultTokens < 0 {
		cfg.MinToolResultTokens = 0
	}
	return &WriterCompactor{
		store:               cfg.Store,
		restore:             cfg.Restore,
		keepRecentTokens:    cfg.KeepRecentTokens,
		summaryTokenBudget:  cfg.SummaryTokenBudget,
		restoreTokenBudget:  cfg.RestoreTokenBudget,
		minToolResultTokens: cfg.MinToolResultTokens,
		prompts:             cfg.Prompts.withDefaults(),
	}
}

// Compact 实现 agentcore.Compactor。
func (c *WriterCompactor) Compact(ctx context.Context, history []agentcore.Message, call func([]agentcore.Message) agentcore.Call, focus string) (*agentcore.Compaction, error) {
	if len(history) == 0 {
		return nil, nil
	}
	cut := c.cutPoint(history)
	if cut <= 0 || cut >= len(history) {
		return nil, nil
	}

	replaced := history[:cut]
	kept := history[cut:]

	replacedTokens := estimateMessages(replaced)
	if replacedTokens < minCompactTokens {
		return nil, nil
	}

	summary, usage, err := c.summary(ctx, history, replaced, replacedTokens, call, focus)
	if err != nil {
		return nil, err
	}
	if summary == "" {
		return nil, nil
	}

	msgs := make([]agentcore.Message, 0, 2+len(kept))
	msgs = append(msgs, agentcore.SummaryMessage(summary))
	if restore, ok := c.restoreMessage(); ok {
		msgs = append(msgs, restore)
	}
	msgs = append(msgs, c.microcompact(kept)...)

	return &agentcore.Compaction{Messages: msgs, Replaced: cut, Usage: usage}, nil
}

// cutPoint 返回压缩后原样保留的历史起点：从末尾按 keepRecentTokens 回退，
// 至少保留最后一条消息，并且不把一条 assistant 工具调用与它的结果拆开。
// 保留预算不超过整段历史的一半，保证压缩总能推进（小窗口模型上尤其重要）。
func (c *WriterCompactor) cutPoint(history []agentcore.Message) int {
	budget := c.keepRecentTokens
	if half := estimateMessages(history) / 2; budget > half {
		budget = half
	}
	cut := len(history)
	for i := len(history) - 1; i >= 0; i-- {
		tokens := agentcore.EstimateMessage(history[i])
		if i < len(history)-1 && tokens > budget {
			break
		}
		budget -= tokens
		cut = i
	}
	// 保留段不能以工具结果开头：那说明切点在工具循环内部，回退到发起调用的消息。
	for cut > 0 && history[cut].Role == litellm.RoleTool {
		cut--
	}
	return cut
}

// summary 生成 checkpoint 摘要：store 持久事实优先（无需模型调用），不足时退回模型摘要。
func (c *WriterCompactor) summary(ctx context.Context, history, replaced []agentcore.Message, replacedTokens int, call func([]agentcore.Message) agentcore.Call, focus string) (string, *agentcore.Usage, error) {
	stored, ok, err := c.storeSummary(history, replacedTokens, focus)
	if err != nil {
		return "", nil, err
	}
	if ok {
		return stored, nil, nil
	}
	return c.llmSummary(ctx, history, replaced, call, focus)
}

// storeSummary 把 store 里的章节/弧卷摘要、计划、大纲、角色快照等渲染成摘要素材。
// 素材预算不超过被替换历史本身，压缩一定不会让上下文变大。
func (c *WriterCompactor) storeSummary(history []agentcore.Message, replacedTokens int, focus string) (string, bool, error) {
	budget := c.summaryTokenBudget
	if replacedTokens > 0 && budget > replacedTokens {
		budget = replacedTokens * 9 / 10
	}
	sections, ok, err := buildWriterStoreSummaryText(c.store, budget)
	if err != nil || !ok {
		return "", false, err
	}
	var sb strings.Builder
	sb.WriteString(storeSummaryPreamble)
	if task := leadingTask(history); task != "" {
		sb.WriteString("\n\n" + taskHeading + task)
	}
	if focus != "" {
		sb.WriteString("\n\n" + focusHeading + focus)
	}
	sb.WriteString("\n\n" + sections)
	return sb.String(), true, nil
}

// llmSummary 用模型摘要被替换的历史；store 素材不足时兜底。
func (c *WriterCompactor) llmSummary(ctx context.Context, history, replaced []agentcore.Message, call func([]agentcore.Message) agentcore.Call, focus string) (string, *agentcore.Usage, error) {
	if call == nil {
		return "", nil, nil
	}
	splitTurn := replaced[len(replaced)-1].Role == litellm.RoleTool
	instruction := c.instruction(previousSummary(replaced), leadingTask(history), focus, splitTurn)

	msgs := replaced
	var used litellm.Usage
	for attempt := 0; ; attempt++ {
		base := call(history)
		req := base.Request
		req.Messages = []litellm.Message{
			litellm.System(c.prompts.System),
			litellm.UserText("<conversation>\n" + serializeMessages(msgs) + "\n</conversation>\n\n" + instruction),
		}
		req.Tools, req.ToolChoice, req.ResponseFormat = nil, nil, nil
		resp, err := base.Client.Chat(ctx, req)
		if err == nil {
			used.Add(resp.Usage)
			// 被输出上限截断的摘要会假装整段历史都在里面。
			if resp.FinishReason == litellm.FinishReasonLength {
				return "", nil, errors.New("ctxpack: the summary was cut off at the output token limit")
			}
			text := stripAnalysisBlock(resp.Text())
			summary := extractTaggedBlock(text, "summary")
			if summary == "" {
				summary = strings.TrimSpace(text)
			}
			if summary == "" {
				return "", nil, errors.New("ctxpack: empty summary")
			}
			usage := &agentcore.Usage{Usage: used}
			return summary, usage, nil
		}
		if litellm.ErrorTypeOf(err) != litellm.ErrorTypeContextOverflow || attempt >= 3 {
			return "", nil, fmt.Errorf("ctxpack: summarize: %w", err)
		}
		next := dropOldestMessages(msgs)
		if len(next) == 0 || len(next) == len(msgs) {
			return "", nil, fmt.Errorf("ctxpack: summarize: %w", err)
		}
		msgs = next
	}
}

// instruction 组装摘要请求的指令部分。
func (c *WriterCompactor) instruction(previous, task, focus string, splitTurn bool) string {
	var instruction string
	switch {
	case previous != "":
		instruction = "<previous-summary>\n" + previous + "\n</previous-summary>\n\n" + c.prompts.Update
	case splitTurn:
		instruction = c.prompts.TurnPrefix
	default:
		instruction = c.prompts.Summary
	}
	if task != "" {
		instruction += "\n\n本次任务原文（写入「## 当前任务」一节，原样保留）：\n" + task
	}
	if focus != "" {
		instruction += "\n\n本次压缩需要特别关注：" + focus
	}
	return instruction
}

// restoreMessage 返回缓存的恢复包消息；读不到或放不下时跳过，不因此让压缩失败。
func (c *WriterCompactor) restoreMessage() (agentcore.Message, bool) {
	if c.restore == nil {
		return agentcore.Message{}, false
	}
	msg, ok, err := c.restore.buildMessage(c.restoreTokenBudget)
	if err != nil || !ok {
		return agentcore.Message{}, false
	}
	return msg, true
}

// microcompact 清理保留段里陈旧的工具结果（保护最新 keepRecentToolResults 条），
// 只替换内容，保留 ToolResultBlock 的 id 与错误标记，让工具循环仍然成对。
func (c *WriterCompactor) microcompact(msgs []agentcore.Message) []agentcore.Message {
	if c.minToolResultTokens <= 0 || len(msgs) == 0 {
		return msgs
	}
	var toolIdx []int
	for i, m := range msgs {
		if m.Role == litellm.RoleTool {
			toolIdx = append(toolIdx, i)
		}
	}
	if len(toolIdx) <= keepRecentToolResults {
		return msgs
	}
	out := slices.Clone(msgs)
	for _, i := range toolIdx[:len(toolIdx)-keepRecentToolResults] {
		m := out[i]
		result, ok := m.ToolResult()
		if !ok || agentcore.EstimateMessage(m) < c.minToolResultTokens {
			continue
		}
		blocks := slices.Clone(m.Blocks)
		for j, block := range blocks {
			if _, ok := block.(litellm.ToolResultBlock); ok {
				blocks[j] = litellm.ToolResultBlock{
					ToolUseID: result.ToolUseID,
					IsError:   result.IsError,
					Content:   []litellm.Block{litellm.Text(clearedToolResult)},
				}
			}
		}
		m.Blocks = blocks
		out[i] = m
	}
	return out
}

const (
	storeSummaryPreamble = "以下内容来自小说持久化 store，用于在压缩后恢复写作上下文。"
	taskHeading          = "## 当前任务\n"
	focusHeading         = "## 压缩关注点\n"
)

// leadingTask 取回协调器下发的任务：首次压缩来自首条 user 消息，之后来自上一份摘要。
// store 摘要与 LLM 摘要（WriterSummaryPrompt）都把"当前任务"作为固定一节，按下一个标题结束。
func leadingTask(msgs []agentcore.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	first := msgs[0]
	if first.Kind == agentcore.KindSummary {
		if _, rest, ok := strings.Cut(agentcore.SummaryText(first), taskHeading); ok {
			task, _, _ := strings.Cut(rest, "\n## ")
			return strings.TrimSpace(task)
		}
		return ""
	}
	if first.Role == litellm.RoleUser {
		return first.Text()
	}
	return ""
}

// estimateMessages 估一段历史的输入 token。
func estimateMessages(msgs []agentcore.Message) int {
	total := 0
	for _, m := range msgs {
		total += agentcore.EstimateMessage(m)
	}
	return total
}

// previousSummary 返回被替换历史里的上一份摘要文本。
func previousSummary(msgs []agentcore.Message) string {
	previous := ""
	for _, m := range msgs {
		if m.Kind == agentcore.KindSummary {
			previous = agentcore.SummaryText(m)
		}
	}
	return previous
}

// dropOldestMessages 丢掉最旧的一部分历史，用于摘要请求仍然溢出时重试。
func dropOldestMessages(msgs []agentcore.Message) []agentcore.Message {
	if len(msgs) < 2 {
		return nil
	}
	drop := max(len(msgs)/5, 1)
	rest := msgs[drop:]
	if len(rest) == 0 {
		return nil
	}
	return rest
}

// serializeMessages 把历史摊平成纯文本，供摘要请求使用。
func serializeMessages(msgs []agentcore.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case litellm.RoleUser:
			if text := m.Text(); text != "" {
				sb.WriteString("用户：\n" + text + "\n\n")
			}
		case litellm.RoleAssistant:
			for _, call := range m.ToolCalls() {
				sb.WriteString("助手调用工具 " + call.Name + "：" + truncateRunes(call.Arguments, 400) + "\n")
			}
			if text := m.Text(); text != "" {
				sb.WriteString("助手：\n" + text + "\n\n")
			}
		case litellm.RoleTool:
			if text := m.Text(); text != "" {
				sb.WriteString("工具结果：\n" + truncateRunes(text, 2000) + "\n\n")
			}
		}
	}
	return sb.String()
}

// truncateRunes 截断到 keep 个字符，超出时保留首尾。
func truncateRunes(text string, keep int) string {
	r := []rune(text)
	if len(r) <= keep {
		return text
	}
	head := keep / 2
	return fmt.Sprintf("%s\n[... 省略 %d 字 ...]\n%s", string(r[:head]), len(r)-keep, string(r[len(r)-(keep-head):]))
}

// stripAnalysisBlock 去掉模型在 <analysis> 里的思考。
func stripAnalysisBlock(text string) string {
	start := strings.Index(text, "<analysis>")
	end := strings.Index(text, "</analysis>")
	if start < 0 || end < start {
		return text
	}
	return strings.TrimSpace(text[:start] + text[end+len("</analysis>"):])
}

// extractTaggedBlock 取出 <tag>…</tag> 之间的内容。
func extractTaggedBlock(text, tag string) string {
	startTag, endTag := "<"+tag+">", "</"+tag+">"
	start := strings.Index(text, startTag)
	end := strings.Index(text, endTag)
	if start < 0 || end < start {
		return ""
	}
	return strings.TrimSpace(text[start+len(startTag) : end])
}
