package host

import "strings"

// toolDisplay 配置一个工具的流式展示：
// - header: 首个字段输出前打印一次（如"【规划】"）
// - fields: JSON key → 展示标签；label 为空表示裸流，适合纯内容（draft_chapter.content）
//
// 非字符串字段（int / bool / array / object）和未列出的字段一律跳过。
type toolDisplay struct {
	header string
	fields map[string]string
}

// toolDisplays 定义每个工具在流式面板上展示哪些字段。
// 只覆盖 Writer/Editor/Architect 写作循环中产生的工具；读类工具和 coordinator 工具不展示。
var toolDisplays = map[string]*toolDisplay{
	"draft_chapter": {
		// 无 header、无 label，整章正文以 markdown 流出
		fields: map[string]string{"content": ""},
	},
	"plan_chapter": {
		header: "【规划】",
		fields: map[string]string{
			"title":       "标题",
			"goal":        "目标",
			"conflict":    "冲突",
			"hook":        "钩子",
			"emotion_arc": "情绪",
		},
	},
	"commit_chapter": {
		header: "【章节提交】",
		fields: map[string]string{
			"summary": "摘要",
		},
	},
	"save_review": {
		header: "【审阅】",
		fields: map[string]string{
			"verdict": "结论",
			"summary": "摘要",
		},
	},
	"save_arc_summary": {
		header: "【弧摘要】",
		fields: map[string]string{
			"title":   "标题",
			"summary": "摘要",
		},
	},
	"save_volume_summary": {
		header: "【卷摘要】",
		fields: map[string]string{
			"title":   "标题",
			"summary": "摘要",
		},
	},
	// Architect 的主力工具。content 为 Markdown 时（premise）流式输出；
	// content 为数组 / 对象时（outline / characters / world_rules 等）被状态机自动跳过，
	// 用户仍能看到 type / scale 标签，知道正在生成什么。
	"save_foundation": {
		header: "【设定】",
		fields: map[string]string{
			"type":    "类型",
			"scale":   "规模",
			"content": "",
		},
	},
}

// jsonFieldExtractor 是一个单遍流式 JSON 解析器，针对扁平对象。
// 它根据 toolDisplay 配置把目标字段的字符串值以"【标签】 值"的形式流式输出。
// 非目标字段（无论是 string / 数组 / 对象 / 数值）都被跳过。
type jsonFieldExtractor struct {
	display *toolDisplay

	phase  parsePhase
	keyBuf strings.Builder

	// 跳过结构化值的状态
	depth      int
	inInnerStr bool // 跳过结构体时遇到的内部字符串
	escape     bool

	// \uXXXX 解析缓存
	uHex []byte

	headerEmitted bool
	activeLabel   string
}

type parsePhase int

const (
	phaseObjectStart  parsePhase = iota // 等待顶层 {
	phaseBeforeKey                      // 等待下一个 key 的 "
	phaseInKey                          // 在 key 字符串内
	phaseAfterKey                       // 等待 :
	phaseBeforeValue                    // 等待 value 起始字符
	phaseStreamValue                    // 在目标字段的 string 值内（流式输出）
	phaseSkipString                     // 在非目标字段的 string 值内（跳过）
	phaseSkipStruct                     // 在 { 或 [ 值内（深度跳过）
	phaseSkipPrimitive                  // 在数字 / bool / null 值内
	phaseDone                           // 顶层 } 已出现
)

// newToolExtractor 为已知工具返回抽取器；未知工具返回 nil。
func newToolExtractor(tool string) *jsonFieldExtractor {
	cfg, ok := toolDisplays[tool]
	if !ok {
		return nil
	}
	return &jsonFieldExtractor{display: cfg}
}

// Done 报告是否已完成整个对象的解析。
func (e *jsonFieldExtractor) Done() bool { return e.phase == phaseDone }

// Feed 消费 delta，返回应该输出到面板的字符（已去转义、带标签装饰）。
func (e *jsonFieldExtractor) Feed(chunk string) string {
	if e.phase == phaseDone || chunk == "" {
		return ""
	}
	var out strings.Builder
	for i := 0; i < len(chunk); i++ {
		c := chunk[i]
		switch e.phase {
		case phaseObjectStart:
			if c == '{' {
				e.phase = phaseBeforeKey
			}
		case phaseBeforeKey:
			switch c {
			case '"':
				e.keyBuf.Reset()
				e.phase = phaseInKey
				e.escape = false
			case '}':
				e.phase = phaseDone
				return out.String()
			}
		case phaseInKey:
			if e.escape {
				e.keyBuf.WriteByte(c)
				e.escape = false
			} else if c == '\\' {
				e.escape = true
			} else if c == '"' {
				e.phase = phaseAfterKey
			} else {
				e.keyBuf.WriteByte(c)
			}
		case phaseAfterKey:
			if c == ':' {
				e.phase = phaseBeforeValue
			}
		case phaseBeforeValue:
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				continue
			}
			key := e.keyBuf.String()
			label, isTarget := e.display.fields[key]
			switch c {
			case '"':
				if isTarget {
					e.activeLabel = label
					e.emitFieldOpen(&out)
					e.phase = phaseStreamValue
				} else {
					e.phase = phaseSkipString
				}
				e.escape = false
			case '{', '[':
				e.phase = phaseSkipStruct
				e.depth = 1
				e.inInnerStr = false
				e.escape = false
			default:
				e.phase = phaseSkipPrimitive
			}
		case phaseStreamValue:
			if e.uHex != nil {
				e.uHex = append(e.uHex, c)
				if len(e.uHex) == 4 {
					if r, ok := parseHex4(e.uHex); ok {
						out.WriteRune(r)
					}
					e.uHex = nil
				}
				continue
			}
			if e.escape {
				e.writeUnescaped(&out, c)
				e.escape = false
				continue
			}
			if c == '\\' {
				e.escape = true
				continue
			}
			if c == '"' {
				if e.activeLabel != "" {
					out.WriteByte('\n')
				}
				e.activeLabel = ""
				e.phase = phaseBeforeKey
				continue
			}
			out.WriteByte(c)
		case phaseSkipString:
			if e.escape {
				e.escape = false
			} else if c == '\\' {
				e.escape = true
			} else if c == '"' {
				e.phase = phaseBeforeKey
			}
		case phaseSkipStruct:
			if e.inInnerStr {
				if e.escape {
					e.escape = false
				} else if c == '\\' {
					e.escape = true
				} else if c == '"' {
					e.inInnerStr = false
				}
			} else {
				switch c {
				case '"':
					e.inInnerStr = true
				case '{', '[':
					e.depth++
				case '}', ']':
					e.depth--
					if e.depth == 0 {
						e.phase = phaseBeforeKey
					}
				}
			}
		case phaseSkipPrimitive:
			if c == ',' {
				e.phase = phaseBeforeKey
			} else if c == '}' {
				e.phase = phaseDone
				return out.String()
			}
		case phaseDone:
			return out.String()
		}
	}
	return out.String()
}

// emitFieldOpen 在一个目标字段的值开始时输出 header（一次性）+ 标签前缀。
func (e *jsonFieldExtractor) emitFieldOpen(out *strings.Builder) {
	if !e.headerEmitted {
		if e.display.header != "" {
			out.WriteString(e.display.header)
			out.WriteByte('\n')
		}
		e.headerEmitted = true
	}
	if e.activeLabel != "" {
		out.WriteString("【")
		out.WriteString(e.activeLabel)
		out.WriteString("】 ")
	}
}

// writeUnescaped 处理 JSON 转义序列。
func (e *jsonFieldExtractor) writeUnescaped(out *strings.Builder, c byte) {
	switch c {
	case 'n':
		out.WriteByte('\n')
	case 't':
		out.WriteByte('\t')
	case 'r':
		out.WriteByte('\r')
	case '"':
		out.WriteByte('"')
	case '\\':
		out.WriteByte('\\')
	case '/':
		out.WriteByte('/')
	case 'b':
		out.WriteByte('\b')
	case 'f':
		out.WriteByte('\f')
	case 'u':
		e.uHex = make([]byte, 0, 4)
	default:
		out.WriteByte('\\')
		out.WriteByte(c)
	}
}

func parseHex4(b []byte) (rune, bool) {
	var r rune
	for _, d := range b {
		var v rune
		switch {
		case d >= '0' && d <= '9':
			v = rune(d - '0')
		case d >= 'a' && d <= 'f':
			v = rune(d-'a') + 10
		case d >= 'A' && d <= 'F':
			v = rune(d-'A') + 10
		default:
			return 0, false
		}
		r = r*16 + v
	}
	return r, true
}
