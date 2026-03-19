package orchestrator

import "strings"

// jsonFieldExtractor 从流式 JSON 碎片中提取指定字段的字符串值。
//
// LLM 流式生成 tool call 时，参数是逐片段到达的（OpenAI/Anthropic）
// 或一次性到达的（Gemini）。本提取器用状态机逐字符扫描，
// 检测到目标 key 后提取其字符串值，处理 JSON 转义。
type jsonFieldExtractor struct {
	key      string // 匹配目标，如 `"content"` 或 `"task"`
	state    extractState
	matchPos int
	escape   bool
	buf      strings.Builder
}

type extractState int

const (
	stateScan    extractState = iota // 扫描，寻找目标 key
	stateColon                       // 已匹配 key，等冒号和开头引号
	stateExtract                     // 提取字符串值中
)

func newFieldExtractor(fieldName string) *jsonFieldExtractor {
	return &jsonFieldExtractor{key: `"` + fieldName + `"`}
}

// Feed 处理一段 delta，返回提取到的文本（可能为空）。
func (e *jsonFieldExtractor) Feed(delta string) string {
	e.buf.Reset()
	for _, r := range delta {
		switch e.state {
		case stateScan:
			e.feedScan(r)
		case stateColon:
			e.feedColon(r)
		case stateExtract:
			e.feedExtract(r)
		}
	}
	return e.buf.String()
}

func (e *jsonFieldExtractor) feedScan(r rune) {
	if e.matchPos < len(e.key) && byte(r) == e.key[e.matchPos] {
		e.matchPos++
		if e.matchPos == len(e.key) {
			e.state = stateColon
			e.matchPos = 0
		}
		return
	}
	e.matchPos = 0
	if byte(r) == e.key[0] {
		e.matchPos = 1
	}
}

func (e *jsonFieldExtractor) feedColon(r rune) {
	switch r {
	case ':', ' ', '\t':
		// 跳过
	case '"':
		e.state = stateExtract
		e.escape = false
	default:
		e.state = stateScan
		e.matchPos = 0
		if byte(r) == e.key[0] {
			e.matchPos = 1
		}
	}
}

func (e *jsonFieldExtractor) feedExtract(r rune) {
	if e.escape {
		e.escape = false
		switch r {
		case 'n':
			e.buf.WriteByte('\n')
		case 't':
			e.buf.WriteByte('\t')
		case 'r':
			e.buf.WriteByte('\r')
		case '"', '\\', '/':
			e.buf.WriteRune(r)
		default:
			e.buf.WriteByte('\\')
			e.buf.WriteRune(r)
		}
		return
	}
	switch r {
	case '\\':
		e.escape = true
	case '"':
		e.state = stateScan
		e.matchPos = 0
	default:
		e.buf.WriteRune(r)
	}
}

// Reset 重置状态（新 LLM 消息轮次时调用）。
func (e *jsonFieldExtractor) Reset() {
	e.state = stateScan
	e.matchPos = 0
	e.escape = false
}

// ThinkingSep 是思考文本与正文之间的分隔标记。
// streamFilter 在思考文本段前插入此标记，TUI 据此切换渲染样式。
const ThinkingSep = "\x02"

// streamFilter 区分 SubAgent 的文本回复和 JSON 工具调用。
// 文本回复标记为思考内容（前缀 ThinkingSep）；JSON 工具调用只提取指定字段。
//
// 判断依据：遇到 { 进入 JSON 模式（追踪大括号深度），
// 深度归零后回到文本模式。
type streamFilter struct {
	fieldExt   *jsonFieldExtractor
	mode       filterMode
	braceDepth int
	inString   bool // 在 JSON 字符串内（大括号不计数）
	escJSON    bool // JSON 字符串内的转义
	thinking   bool // 当前处于思考文本段
	buf        strings.Builder
}

type filterMode int

const (
	filterText filterMode = iota // 文本回复，直接透传
	filterJSON                   // JSON 工具调用，提取目标字段
)

func newStreamFilter(fieldName string) *streamFilter {
	return &streamFilter{fieldExt: newFieldExtractor(fieldName)}
}

// Feed 处理一段 delta，返回可展示文本。
// 文本回复直接输出；JSON 中的目标字段值被提取输出；其余 JSON 结构丢弃。
func (f *streamFilter) Feed(delta string) string {
	f.buf.Reset()
	for _, r := range delta {
		switch f.mode {
		case filterText:
			if r == '{' {
				f.thinking = false
				f.mode = filterJSON
				f.braceDepth = 1
				f.inString = false
				f.escJSON = false
				f.fieldExt.Reset()
				f.feedExtractor(r)
			} else {
				if !f.thinking {
					f.thinking = true
					f.buf.WriteString(ThinkingSep)
				}
				f.buf.WriteRune(r)
			}
		case filterJSON:
			f.feedExtractor(r)
			f.trackBraces(r)
		}
	}
	return f.buf.String()
}

// feedExtractor 将单个字符喂给 fieldExt，提取结果写入 buf。
func (f *streamFilter) feedExtractor(r rune) {
	if text := f.fieldExt.Feed(string(r)); text != "" {
		f.buf.WriteString(text)
	}
}

// trackBraces 追踪 JSON 大括号深度，深度归零时切回文本模式。
func (f *streamFilter) trackBraces(r rune) {
	if f.escJSON {
		f.escJSON = false
		return
	}
	if f.inString {
		switch r {
		case '\\':
			f.escJSON = true
		case '"':
			f.inString = false
		}
		return
	}
	switch r {
	case '"':
		f.inString = true
	case '{':
		f.braceDepth++
	case '}':
		f.braceDepth--
		if f.braceDepth <= 0 {
			f.mode = filterText
		}
	}
}

// Reset 重置状态。
func (f *streamFilter) Reset() {
	f.mode = filterText
	f.braceDepth = 0
	f.inString = false
	f.escJSON = false
	f.thinking = false
	f.fieldExt.Reset()
}
