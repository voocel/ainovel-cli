package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// SessionStore 追加式记录 LLM 对话历史到 JSONL 文件。
// 大体积内容（小说正文、完整上下文）用 [session_compact: ...] 占位标记替代。
type SessionStore struct {
	io      *IO
	mu      sync.Mutex
	seq     map[string]int    // agent 运行序号（无法提取章节号时用）
	taskKey map[string]string // "agentName|task" → suffix，同一 run 复用同一文件
	tools   map[string]string // "rel|tool_use_id" → 工具名，见 rememberToolNames
}

func NewSessionStore(io *IO) *SessionStore {
	return &SessionStore{
		io:      io,
		seq:     make(map[string]int),
		taskKey: make(map[string]string),
		tools:   make(map[string]string),
	}
}

// ModelLookup 在 logger 写入时按 agent 名查"当时生效"的 provider/model。
// 用 func 类型而不是 interface，方便调用方用闭包注入归一规则（如 architect_short → architect）。
// 返回空字符串表示未知，调用方仍照常写入但不带 _meta，replay 时退回 ModelSet fallback。
type ModelLookup func(agentName string) (provider, model string)

// SubAgentLogger 返回子代理的 OnMessage 回调。
func (s *SessionStore) SubAgentLogger(lookup ModelLookup) func(agentName, task string, msg agentcore.Message) {
	return func(agentName, task string, msg agentcore.Message) {
		rel, err := s.subAgentPath(agentName, task)
		if err != nil {
			slog.Warn("session log failed", "agent", agentName, "err", err)
			return
		}
		var meta *sessionLogMeta
		if lookup != nil {
			meta = lookupMeta(lookup, agentName)
		}
		if err := s.logEntry(rel, msg, meta); err != nil {
			slog.Warn("session log failed", "agent", agentName, "err", err)
		}
	}
}

func lookupMeta(lookup ModelLookup, agentName string) *sessionLogMeta {
	provider, model := lookup(agentName)
	if provider == "" && model == "" {
		return nil
	}
	return &sessionLogMeta{Provider: provider, Model: model}
}

// LogCoCreate 追加一条共创对话日志到 meta/sessions/cocreate.jsonl。
// 共创阶段还没绑定具体小说，统一落到 OutputDir 默认根（output/novel）下，
// 与正式创作的 agents/* 同位，方便排查。
func (s *SessionStore) LogCoCreate(entry any) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal cocreate session: %w", err)
	}
	data = append(data, '\n')
	return s.io.AppendLine("meta/sessions/cocreate.jsonl", data)
}

// Log 追加一条消息到指定路径，自动压缩大内容。
// 不携带 _meta；仅 cocreate 等无角色路径使用。
func (s *SessionStore) Log(rel string, msg agentcore.Message) error {
	return s.logEntry(rel, msg, nil)
}

// sessionLogEntry 嵌入 agentcore.Message + 可选 _meta。
// agentcore.Message 是 plain struct（只有 UnmarshalJSON，无 MarshalJSON），嵌入后
// json marshal 自动展开到顶层，blocks 由 litellm 的块类型各自带上 "type" 编码；
// _meta 通过 omitempty 控制——只有 assistant + Usage != nil 时才注入，user/tool
// 消息不带 _meta，旧 jsonl 解析时 _meta=nil 是 noop。
type sessionLogEntry struct {
	agentcore.Message
	Meta *sessionLogMeta `json:"_meta,omitempty"`
}

type sessionLogMeta struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// logEntry 序列化消息并按需附加 _meta。lookupMeta 已计算好的 meta 传进来；
// 函数内部判断只对"产生了 LLM 用量"的消息（assistant + Usage != nil）写入 meta，
// 其它消息保持纯净 agentcore.Message 序列化形态。
func (s *SessionStore) logEntry(rel string, msg agentcore.Message, meta *sessionLogMeta) error {
	s.rememberToolNames(rel, msg)
	compacted := compactMessage(msg, func(id string) string { return s.toolName(rel, id) })
	entry := sessionLogEntry{Message: compacted}
	if compacted.Role == litellm.RoleAssistant && compacted.Usage != nil {
		entry.Meta = usageMeta(compacted)
		if entry.Meta == nil {
			entry.Meta = meta
		}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal session message: %w", err)
	}
	data = append(data, '\n')
	return s.io.AppendLine(rel, data)
}

// usageMeta 取 _meta 的 provider/model。agentcore v1.8.8 把这两个字段从 Usage
// 移到了消息顶层（Usage 只留 token 计数，且字段名改为 input_tokens/output_tokens/
// reasoning_tokens/cache_read_tokens/cache_write_tokens；旧 input/output/cache_read/
// cache_write/total_tokens 已不存在，本文件不读计数，随嵌入消息原样落盘）。
func usageMeta(m agentcore.Message) *sessionLogMeta {
	if m.Usage == nil || (m.Provider == "" && m.Model == "") {
		return nil
	}
	return &sessionLogMeta{
		Provider: m.Provider,
		Model:    m.Model,
	}
}

// maxPendingToolNames 是未等到结果的 tool_use 名上限。
const maxPendingToolNames = 2048

// rememberToolNames 记下本条消息里 tool_use 块的工具名，供其后 tool result 的压缩
// 取用：agentcore v1.8.8 的 tool result 块只带 tool_use_id，工具名留在 assistant
// 消息的 tool_use 块上。
func (s *SessionStore) rememberToolNames(rel string, msg agentcore.Message) {
	var uses []litellm.ToolUseBlock
	for _, block := range msg.Blocks {
		if use, ok := block.(litellm.ToolUseBlock); ok && use.ID != "" && use.Name != "" {
			uses = append(uses, use)
		}
	}
	if len(uses) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tools) >= maxPendingToolNames {
		// 兜底：结果迟迟不落盘（run 中途放弃）时不无限增长，丢名只影响占位措辞。
		s.tools = make(map[string]string)
	}
	for _, use := range uses {
		s.tools[rel+"|"+use.ID] = use.Name
	}
}

// toolName 取走 tool result 对应的工具名（一次即删，tool_use id 不会复用）。
func (s *SessionStore) toolName(rel, id string) string {
	if id == "" {
		return ""
	}
	key := rel + "|" + id
	s.mu.Lock()
	defer s.mu.Unlock()
	name := s.tools[key]
	delete(s.tools, key)
	return name
}

// subAgentPath 根据 agentName+task 生成文件路径。
func (s *SessionStore) subAgentPath(agentName, task string) (string, error) {
	suffix := extractChapter(task)
	if suffix != "" {
		return fmt.Sprintf("meta/sessions/agents/%s-%s.jsonl", agentName, suffix), nil
	}
	key := agentName + "|" + task
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.taskKey[key]; ok {
		return fmt.Sprintf("meta/sessions/agents/%s-%s.jsonl", agentName, cached), nil
	}
	if _, ok := s.seq[agentName]; !ok {
		seq, err := s.maxAgentSequence(agentName)
		if err != nil {
			return "", err
		}
		s.seq[agentName] = seq
	}
	s.seq[agentName]++
	suffix = fmt.Sprintf("%03d", s.seq[agentName])
	s.taskKey[key] = suffix
	return fmt.Sprintf("meta/sessions/agents/%s-%s.jsonl", agentName, suffix), nil
}

func (s *SessionStore) maxAgentSequence(agentName string) (int, error) {
	entries, err := os.ReadDir(s.io.path("meta/sessions/agents"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read agent sessions: %w", err)
	}

	prefix := agentName + "-"
	maxSeq := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		seq, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl"))
		if err == nil && seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq, nil
}

var chapterRe = regexp.MustCompile(`第\s*(\d+)\s*章`)

func extractChapter(task string) string {
	m := chapterRe.FindStringSubmatch(task)
	if len(m) < 2 {
		return ""
	}
	n, _ := strconv.Atoi(m[1])
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("ch%02d", n)
}

// compactMessage 克隆消息并替换大内容。toolName 按 tool_use id 找回 tool result
// 的工具名（agentcore v1.8.8 的 tool result 块只带 tool_use_id）。
func compactMessage(m agentcore.Message, toolName func(id string) string) agentcore.Message {
	if len(m.Blocks) == 0 {
		return m
	}
	blocks := make([]litellm.Block, len(m.Blocks))
	copy(blocks, m.Blocks)

	for i, block := range blocks {
		switch b := block.(type) {
		case litellm.TextBlock:
			b.Text = compactText(m.Role, "", b.Text)
			blocks[i] = b
		case litellm.ToolUseBlock:
			blocks[i] = compactToolCall(b)
		case litellm.ToolResultBlock:
			blocks[i] = compactToolResult(b, m.Role, toolName)
		}
	}
	m.Blocks = blocks
	return m
}

// compactToolResult 压缩 tool result 内容里的 text 块。
func compactToolResult(tr litellm.ToolResultBlock, role litellm.Role, toolName func(id string) string) litellm.ToolResultBlock {
	if len(tr.Content) == 0 {
		return tr
	}
	name := ""
	if toolName != nil {
		name = toolName(tr.ToolUseID)
	}
	content := make([]litellm.Block, len(tr.Content))
	copy(content, tr.Content)
	for i, block := range content {
		if b, ok := block.(litellm.TextBlock); ok {
			b.Text = compactText(role, name, b.Text)
			content[i] = b
		}
	}
	tr.Content = content
	return tr
}

// compactText 压缩 tool result 的 text content。
func compactText(role litellm.Role, toolName, text string) string {
	if role != litellm.RoleTool || len(text) < 4096 {
		return text
	}
	switch toolName {
	case "novel_context":
		summary := extractJSONField(text, "_loading_summary")
		return fmt.Sprintf("[session_compact: novel_context %dB | %s]", len(text), summary)
	case "read_chapter":
		chars := utf8.RuneCountInString(text)
		return fmt.Sprintf("[session_compact: read_chapter %d字 | 见 chapters/]", chars)
	default:
		if len(text) > 8192 {
			chars := utf8.RuneCountInString(text)
			return fmt.Sprintf("[session_compact: %s %d字]", toolName, chars)
		}
		return text
	}
}

// compactToolCall 压缩 tool call 的 args 中大内容字段。
func compactToolCall(tc litellm.ToolUseBlock) litellm.ToolUseBlock {
	switch tc.Name {
	case "draft_chapter":
		return compactArgsContent(tc, "第N章正文", "drafts/")
	case "save_foundation":
		return compactFoundationArgs(tc)
	default:
		return tc
	}
}

func compactArgsContent(tc litellm.ToolUseBlock, label, ref string) litellm.ToolUseBlock {
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		return tc
	}
	contentRaw, ok := args["content"]
	if !ok || len(contentRaw) < 4096 {
		return tc
	}
	var content string
	if err := json.Unmarshal(contentRaw, &content); err != nil {
		// content 不是字符串（可能是 JSON 对象），用字节数
		placeholder := fmt.Sprintf("[session_compact: %s %dB | 见 %s]", label, len(contentRaw), ref)
		args["content"], _ = json.Marshal(placeholder)
	} else {
		chars := utf8.RuneCountInString(content)
		ch := extractJSONFieldInt(json.RawMessage(tc.Arguments), "chapter")
		if ch > 0 {
			label = fmt.Sprintf("第%d章正文", ch)
			ref = fmt.Sprintf("drafts/%02d.draft.md", ch)
		}
		placeholder := fmt.Sprintf("[session_compact: %s %d字 | 见 %s]", label, chars, ref)
		args["content"], _ = json.Marshal(placeholder)
	}
	data, err := json.Marshal(args)
	if err != nil {
		return tc
	}
	tc.Arguments = string(data)
	return tc
}

func compactFoundationArgs(tc litellm.ToolUseBlock) litellm.ToolUseBlock {
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		return tc
	}
	contentRaw, ok := args["content"]
	if !ok || len(contentRaw) < 4096 {
		return tc
	}
	typeName := "foundation"
	var t string
	if json.Unmarshal(args["type"], &t) == nil && t != "" {
		typeName = t
	}
	placeholder := fmt.Sprintf("[session_compact: %s %dB | 见 store]", typeName, len(contentRaw))
	args["content"], _ = json.Marshal(placeholder)
	data, err := json.Marshal(args)
	if err != nil {
		return tc
	}
	tc.Arguments = string(data)
	return tc
}

// extractJSONField 从 JSON 字符串中提取指定字段的字符串值。
func extractJSONField(jsonStr, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var val string
	if err := json.Unmarshal(raw, &val); err != nil {
		return string(raw)
	}
	return val
}

func extractJSONFieldInt(data json.RawMessage, field string) int {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return 0
	}
	raw, ok := m[field]
	if !ok {
		return 0
	}
	var val int
	if err := json.Unmarshal(raw, &val); err != nil {
		return 0
	}
	return val
}

// CompactTag 是占位标记前缀，方便搜索和还原。
const CompactTag = "[session_compact:"
