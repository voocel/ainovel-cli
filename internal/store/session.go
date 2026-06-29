package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/voocel/agentcore"
)

// SessionStore 追加式记录 LLM 对话Lịch sử到 JSONL Tập tin。
// 大体积内容（小说Chính văn、完整Ngữ cảnh）用 [session_compact: ...] 占位标记替代。
type SessionStore struct {
	io      *IO
	mu      sync.Mutex
	seq     map[string]int    // agent 运行序号（Không thể提取Chương号时用）
	taskKey map[string]string // "agentName|task" → suffix，同一 run 复用同一Tập tin
}

func NewSessionStore(io *IO) *SessionStore {
	return &SessionStore{io: io, seq: make(map[string]int), taskKey: make(map[string]string)}
}

// ModelLookup 在 logger 写入时按 agent 名查"当时生效"的 provider/model。
// 用 func 类型而不是 interface，方便调用方用闭包注入归一规则（如 architect_short → architect）。
// Quay lạiRỗng字符串表示Không rõ，调用方仍照常写入但不带 _meta，replay 时退回 ModelSet fallback。
type ModelLookup func(agentName string) (provider, model string)

// CoordinatorLogger Quay lại coordinator 的 OnMessage 回调。
// lookup 可为 nil，此时写入不带 _meta（兼容 cocreate 等Không có角色Cảnh）。
func (s *SessionStore) CoordinatorLogger(lookup ModelLookup) func(agentcore.AgentMessage) {
	return func(msg agentcore.AgentMessage) {
		var meta *sessionLogMeta
		if lookup != nil {
			meta = lookupMeta(lookup, "coordinator")
		}
		if err := s.logEntry("meta/sessions/coordinator.jsonl", msg, meta); err != nil {
			slog.Warn("session log failed", "agent", "coordinator", "err", err)
		}
	}
}

// SubAgentLogger Quay lại子Proxy的 OnMessage 回调。
func (s *SessionStore) SubAgentLogger(lookup ModelLookup) func(agentName, task string, msg agentcore.AgentMessage) {
	return func(agentName, task string, msg agentcore.AgentMessage) {
		rel := s.subAgentPath(agentName, task)
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
// 共创阶段还没绑定具体小说，统一落到 OutputDir Mặc định根（output/novel）下，
// 与正式创作的 coordinator.jsonl / agents/* 同位，方便排查。
func (s *SessionStore) LogCoCreate(entry any) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal cocreate session: %w", err)
	}
	data = append(data, '\n')
	return s.io.AppendLine("meta/sessions/cocreate.jsonl", data)
}

// Log 追加一条消息到指定Đường dẫn，自动压缩大内容。
// 不携带 _meta（向后兼容入口；仅 cocreate 等Không có角色Đường dẫn用）。
func (s *SessionStore) Log(rel string, msg agentcore.AgentMessage) error {
	return s.logEntry(rel, msg, nil)
}

// sessionLogEntry 嵌入 agentcore.Message + 可选 _meta。
// agentcore.Message 是 plain struct（Không có MarshalJSON），嵌入后 json marshal
// 自动Mở rộng到顶层；_meta 通过 omitempty 控制——只有 assistant + Usage != nil
// 时才注入，user/tool 消息不带 _meta，Cũ jsonl 解析时 _meta=nil 是 noop。
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
func (s *SessionStore) logEntry(rel string, msg agentcore.AgentMessage, meta *sessionLogMeta) error {
	m, ok := msg.(agentcore.Message)
	if !ok {
		return nil // 非 LLM 消息（如Tuỳ chỉnh类型）Bỏ qua
	}
	compacted := compactMessage(m)
	entry := sessionLogEntry{Message: compacted}
	if compacted.Role == agentcore.RoleAssistant && compacted.Usage != nil {
		entry.Meta = usageMeta(compacted.Usage)
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

func usageMeta(usage *agentcore.Usage) *sessionLogMeta {
	if usage == nil || (usage.Provider == "" && usage.Model == "") {
		return nil
	}
	return &sessionLogMeta{
		Provider: usage.Provider,
		Model:    usage.Model,
	}
}

// subAgentPath 根据 agentName+task 生成Tập tinĐường dẫn。
func (s *SessionStore) subAgentPath(agentName, task string) string {
	suffix := extractChapter(task)
	if suffix != "" {
		return fmt.Sprintf("meta/sessions/agents/%s-%s.jsonl", agentName, suffix)
	}
	key := agentName + "|" + task
	s.mu.Lock()
	if cached, ok := s.taskKey[key]; ok {
		s.mu.Unlock()
		return fmt.Sprintf("meta/sessions/agents/%s-%s.jsonl", agentName, cached)
	}
	s.seq[agentName]++
	suffix = fmt.Sprintf("%03d", s.seq[agentName])
	s.taskKey[key] = suffix
	s.mu.Unlock()
	return fmt.Sprintf("meta/sessions/agents/%s-%s.jsonl", agentName, suffix)
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

// compactMessage 克隆消息并替换大内容。
func compactMessage(m agentcore.Message) agentcore.Message {
	if len(m.Content) == 0 {
		return m
	}
	blocks := make([]agentcore.ContentBlock, len(m.Content))
	copy(blocks, m.Content)

	toolName := toolNameFromMeta(m.Metadata)

	for i := range blocks {
		switch blocks[i].Type {
		case agentcore.ContentText:
			blocks[i].Text = compactText(m.Role, toolName, blocks[i].Text)
		case agentcore.ContentToolCall:
			if blocks[i].ToolCall != nil {
				blocks[i].ToolCall = compactToolCall(blocks[i].ToolCall)
			}
		}
	}
	m.Content = blocks
	return m
}

func toolNameFromMeta(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	if v, ok := meta["tool_name"].(string); ok {
		return v
	}
	return ""
}

// compactText 压缩 tool result 的 text content。
func compactText(role agentcore.Role, toolName, text string) string {
	if role != agentcore.RoleTool || len(text) < 4096 {
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
func compactToolCall(tc *agentcore.ToolCall) *agentcore.ToolCall {
	switch tc.Name {
	case "draft_chapter":
		return compactArgsContent(tc, "第N章Chính văn", "drafts/")
	case "save_foundation":
		return compactFoundationArgs(tc)
	default:
		return tc
	}
}

func compactArgsContent(tc *agentcore.ToolCall, label, ref string) *agentcore.ToolCall {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(tc.Args, &args); err != nil {
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
		ch := extractJSONFieldInt(tc.Args, "chapter")
		if ch > 0 {
			label = fmt.Sprintf("第%d章Chính văn", ch)
			ref = fmt.Sprintf("drafts/%02d.draft.md", ch)
		}
		placeholder := fmt.Sprintf("[session_compact: %s %d字 | 见 %s]", label, chars, ref)
		args["content"], _ = json.Marshal(placeholder)
	}
	clone := *tc
	clone.Args, _ = json.Marshal(args)
	return &clone
}

func compactFoundationArgs(tc *agentcore.ToolCall) *agentcore.ToolCall {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(tc.Args, &args); err != nil {
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
	clone := *tc
	clone.Args, _ = json.Marshal(args)
	return &clone
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

// CompactTag 是占位标记前缀，方便Tìm kiếm和Hoàn nguyên。
const CompactTag = "[session_compact:"

// IsCompacted Kiểm tra文本Có czy không已被压缩。
func IsCompacted(text string) bool {
	return strings.HasPrefix(text, CompactTag)
}
