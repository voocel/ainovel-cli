package diag

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SkelEvent 是一条会话消息脱敏后的行为骨架：保留结构信号（角色 / 工具 / Lỗi /
// 重复指纹），所有自由文本（Chính văn、prompt、思考）一律打码。这是比
// store.compactMessage 更严的一层投影——后者按体积压（>4KB），这里不看体积，
// 任何文本都不出包。
type SkelEvent struct {
	Agent    string     // 来源会话：coordinator / writer-ch07 …
	Role     string     // assistant / tool / user
	Tools    []SkelTool // 该消息内的工具调用
	ErrClass string     // role=tool 且 is_error：Lỗi首行（框架Lỗi串，不含Chính văn）
	TextSha  string     // 打码Chính văn的短哈希；同 sha = 反复生成同一段（循环信号）
	Redacted int        // 本条打码的文本/思考块数（用于脱敏自检）
}

// SkelTool 是一次工具调用的脱敏投影。
type SkelTool struct {
	Name     string            // 工具名（结构信号，不含Chính văn）
	Args     map[string]string // key → 标量原值 / 短字符串带引号 / "<redacted len sha>"
	Invalid  bool              // ArgsInvalid：Mô hình发来的参数Không thể解析（#34 信号）
	ParseErr string            // ArgsParseError：解析Thất bại原因
}

// redactMessage 把一条 agentcore.Message 投影成行为骨架。
func redactMessage(agent string, m agentcore.Message) SkelEvent {
	ev := SkelEvent{Agent: agent, Role: string(m.Role)}
	isErr, _ := m.Metadata["is_error"].(bool)

	var text strings.Builder
	for _, b := range m.Content {
		switch b.Type {
		case agentcore.ContentText:
			// tool LỗiKết quả保留首行：这是我们自己的Lỗi串（如 InputValidationError），
			// 不含Chính văn，且是定位循环的关键。其余文本一律进打码池。
			if m.Role == agentcore.RoleTool && isErr && ev.ErrClass == "" {
				ev.ErrClass = firstLine(b.Text, 160)
				continue
			}
			if strings.TrimSpace(b.Text) != "" {
				text.WriteString(b.Text)
				ev.Redacted++
			}
		case agentcore.ContentThinking:
			if strings.TrimSpace(b.Thinking) != "" {
				text.WriteString(b.Thinking)
				ev.Redacted++
			}
		case agentcore.ContentToolCall:
			if b.ToolCall != nil {
				ev.Tools = append(ev.Tools, redactToolCall(b.ToolCall))
			}
		}
	}
	if t := text.String(); t != "" {
		ev.TextSha = shortHash(t)
	}
	return ev
}

// redactToolCall 投影一次工具调用：工具名 + 参数（值脱敏）+ 解析异常标记。
func redactToolCall(tc *agentcore.ToolCall) SkelTool {
	return SkelTool{
		Name:     tc.Name,
		Args:     redactArgs(tc.Args),
		Invalid:  tc.ArgsInvalid,
		ParseErr: tc.ArgsParseError,
	}
}

// redactArgs 把工具参数对象投影成 key → 脱敏值。非对象参数Quay lại nil
// （ArgsInvalid/ParseErr 已在 SkelTool 另行记录）。
func redactArgs(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = projectValue(v)
	}
	return out
}

// projectValue 按 JSON 类型投影单个参数值：
//   - 标量（数字 / bool / null）：原值即结构信号，保留（chapter: 7）
//   - 短的标识符型字符串：带引号保留，暴露类型（chapter: "7" ← #34 的字符串化数字信号）
//   - 含中文 / Rỗng格 / 长文本的字符串、对象、数组：打码为 <redacted …>（Chính văn零出包）
//   - 已是 [session_compact: …] 占位：安全且有信息，原样保留
func projectValue(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return ""
	}
	switch s[0] {
	case '"':
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return redactPlaceholder(s)
		}
		if strings.HasPrefix(str, store.CompactTag) {
			return str
		}
		// 只保留"像标识符/数字/枚举"的短值（chapter:"7"、type:"premise"、agent:"writer"）；
		// 任何含中文、Rỗng格或Khác符号的字符串都视为Chính văn，一律打码。
		if utf8.RuneCountInString(str) <= 32 && isStructuralToken(str) {
			return strconv.Quote(str)
		}
		return redactPlaceholder(str)
	case '{':
		return fmt.Sprintf("<redacted object len=%d>", len(raw))
	case '[':
		return fmt.Sprintf("<redacted array len=%d>", len(raw))
	default:
		return s
	}
}

// isStructuralToken 判断字符串Có czy không"像标识符"——纯 ASCII 的字母 / 数字 / `_-.:/`，
// Không cóRỗng格、Không có中文。用来区分结构信号（保留）与Chính văn片段（打码）。
func isStructuralToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.' || r == ':' || r == '/':
		default:
			return false
		}
	}
	return true
}

func redactPlaceholder(s string) string {
	return fmt.Sprintf("<redacted len=%d sha=%s>", utf8.RuneCountInString(s), shortHash(s))
}

// shortHash 取文本的短哈希；只用于"Có czy không同一段文本反复出现"的判断，非加密用途。
func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

// firstLine 取首行并按 rune 截断，供Lỗi串Tóm tắt。
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max]) + "…"
	}
	return s
}
