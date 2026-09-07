package capability

import (
	"sync"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/capability/prompt"
)

// 工具参数流式正文提取（页面设计 §3 逐字预览）：正文以工具参数 JSON 的
// 逐 token 裸片段流出，这里从任意字节切分的片段流中增量提取目标路径下
// 字符串值的解转义文本。纯预览通道：权威正文在候选稿，因此容错语义是
// 绝不 panic、结构非法即永久停止发射、不做恢复式猜测（猜错会把 JSON
// 结构喷进预览，比预览停在半途更糟）。

// prosePath 是从参数对象根出发的正文路径；步为 "" 表示数组元素通配。
type prosePath []string

// prosePaths 与 writer 工具 schema（prompt/profiles.go）同生共死。
var prosePaths = map[string]prosePath{
	prompt.ToolWorkspacePutChapter:   {"chapter", "blocks", "", "text"},
	prompt.ToolWorkspaceReplaceBlock: {"text"},
}

const (
	maxProseKey     = 64      // 键缓冲上界：超长键不可能匹配 schema，判死键即可
	maxProseDepth   = 32      // 容器栈上界：schema 深度 3，十倍裕量
	maxProsePending = 8 << 10 // 工具名未知期间的片段缓冲上界，超限弃开头不无界缓存
)

type jsonState uint8

const (
	sValue      jsonState = iota // 期待一个值：{ [ " 数字 - t f n
	sKey                         // 对象内期待 " 或 }
	sStr                         // 字符串内部（isKey 区分键/值，emitting 决定发射）
	sStrEsc                      // 字符串内 \ 之后
	sStrHex                      // \u 之后收集 4 位十六进制
	sColon                       // 键结束后期待 :
	sAfterValue                  // 值收尾后期待 , } ]
	sPrimitive                   // number/true/false/null 吞噬中
	sFailed                      // 永久失败：只吞字节，恒不发射
)

// proseFrame 是容器栈帧；matched 是进入该容器时访问链已匹配的路径步数，
// 压帧时由父帧一次算定、生命周期内不可变，-1 为死路径（子树照常推进不发射）。
type proseFrame struct {
	isArray bool
	matched int
}

// proseExtractor 服务一次工具调用；feed 为 O(片段长度)，无回扫无重扫。
type proseExtractor struct {
	path      prosePath
	stack     []proseFrame
	state     jsonState
	isKey     bool
	keyBuf    []byte
	keyOver   bool
	emitting  bool // 当前字符串值在目标路径上
	emitted   bool // 已发射过目标值：后续目标值开启前补段落分隔
	hexN      int
	hexV      rune
	pendingHi rune   // 待配对的高代理项；0 表示无
	carry     []byte // 输出尾部不完整的 UTF-8 字节（≤3），下次 feed 前置
	out       []byte // feed 内复用的输出缓冲
}

func newProseExtractor(path prosePath) *proseExtractor {
	return &proseExtractor{path: path}
}

// feed 消费一个参数 JSON 片段，返回本次新解出的正文（保证合法 UTF-8，可为空）。
func (e *proseExtractor) feed(fragment string) string {
	if e.state == sFailed {
		return ""
	}
	e.out = append(e.out[:0], e.carry...)
	e.carry = e.carry[:0]
	for i := 0; i < len(fragment) && e.state != sFailed; {
		b := fragment[i]
		switch e.state {
		case sValue:
			if isJSONSpace(b) {
				i++
				continue
			}
			switch {
			case b == '{':
				if e.push(false) {
					e.state = sKey
				}
			case b == '[':
				e.push(true) // 数组首元素仍期待一个值，状态不变
			case b == ']':
				e.pop(true) // 空数组是合法 JSON；栈顶不是数组才判死
			case b == '"':
				e.isKey = false
				e.emitting = e.childMatched() == len(e.path)
				if e.emitting {
					if e.emitted {
						e.out = append(e.out, "\n\n"...)
					}
					e.emitted = true
				}
				e.state = sStr
			case b == '-' || b >= '0' && b <= '9' || b == 't' || b == 'f' || b == 'n':
				e.state = sPrimitive
			default:
				e.fail()
			}
			i++
		case sKey:
			if isJSONSpace(b) {
				i++
				continue
			}
			switch b {
			case '"':
				e.keyBuf, e.keyOver, e.isKey = e.keyBuf[:0], false, true
				e.state = sStr
			case '}':
				e.pop(false)
			default:
				e.fail()
			}
			i++
		case sStr:
			switch b {
			case '"':
				e.flushPendingHi()
				if e.isKey {
					e.state = sColon
				} else {
					e.emitting = false
					e.state = sAfterValue
				}
				i++
			case '\\':
				e.state = sStrEsc
				i++
			default:
				// 块拷贝：扫到下一个定界符整段处理。原始控制字节宽容放行当
				// 内容（个别模型漏转义换行，判死损失整段预览不值）；UTF-8
				// 连续字节不与 ASCII 定界符相撞，逐字节扫描安全。
				e.flushPendingHi()
				j := i + 1
				for j < len(fragment) && fragment[j] != '"' && fragment[j] != '\\' {
					j++
				}
				if e.isKey {
					e.appendKey(fragment[i:j])
				} else if e.emitting {
					e.out = append(e.out, fragment[i:j]...)
				}
				i = j
			}
		case sStrEsc:
			switch b {
			case '"', '\\', '/':
				e.flushPendingHi()
				e.emitRune(rune(b))
				e.state = sStr
			case 'b', 'f', 'n', 'r', 't':
				e.flushPendingHi()
				e.emitRune(unescapeJSON(b))
				e.state = sStr
			case 'u':
				// pendingHi 保留：这可能是低代理项的开头。
				e.hexN, e.hexV = 0, 0
				e.state = sStrHex
			default:
				e.fail()
			}
			i++
		case sStrHex:
			digit := hexDigit(b)
			if digit < 0 {
				e.fail()
				continue
			}
			e.hexV = e.hexV<<4 | rune(digit)
			if e.hexN++; e.hexN == 4 {
				e.acceptCodeUnit(e.hexV)
				e.state = sStr
			}
			i++
		case sColon:
			if isJSONSpace(b) {
				i++
				continue
			}
			if b == ':' {
				e.state = sValue
			} else {
				e.fail()
			}
			i++
		case sAfterValue:
			if isJSONSpace(b) {
				i++
				continue
			}
			switch b {
			case ',':
				if len(e.stack) == 0 {
					e.fail()
				} else if e.stack[len(e.stack)-1].isArray {
					e.state = sValue
				} else {
					e.state = sKey
				}
			case '}':
				e.pop(false)
			case ']':
				e.pop(true)
			default:
				e.fail()
			}
			i++
		case sPrimitive:
			if b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
				b == '.' || b == '+' || b == '-' {
				i++
				continue
			}
			// 定界符结束字面量：同一轮重新消费该字节。
			e.state = sAfterValue
		}
	}
	complete, rest := splitIncompleteRune(e.out)
	e.carry = append(e.carry, rest...)
	if !utf8.Valid(complete) {
		// 原始内容里混入畸形字节（模型异常输出）：以 U+FFFD 替换非法序列，
		// 守住"返回值恒为合法 UTF-8"的出口保证。常态流不走这条慢路径。
		return sanitizeUTF8(complete)
	}
	return string(complete)
}

func sanitizeUTF8(b []byte) string {
	out := make([]byte, 0, len(b)+utf8.UTFMax)
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			out = utf8.AppendRune(out, utf8.RuneError)
		} else {
			out = append(out, b[:size]...)
		}
		b = b[size:]
	}
	return string(out)
}

func (e *proseExtractor) fail() {
	e.state, e.pendingHi = sFailed, 0
}

// childMatched 算即将开始的值的路径匹配深度；只在值开始的瞬间调用一次。
func (e *proseExtractor) childMatched() int {
	if len(e.stack) == 0 {
		return 0 // 参数对象根本身
	}
	top := e.stack[len(e.stack)-1]
	if top.matched < 0 || top.matched >= len(e.path) {
		return -1
	}
	if top.isArray {
		if e.path[top.matched] == "" {
			return top.matched + 1
		}
		return -1
	}
	if e.keyOver {
		return -1
	}
	if e.path[top.matched] == string(e.keyBuf) {
		return top.matched + 1
	}
	return -1
}

func (e *proseExtractor) push(isArray bool) bool {
	if len(e.stack) >= maxProseDepth {
		e.fail()
		return false
	}
	e.stack = append(e.stack, proseFrame{isArray: isArray, matched: e.childMatched()})
	return true
}

func (e *proseExtractor) pop(isArray bool) {
	if len(e.stack) == 0 || e.stack[len(e.stack)-1].isArray != isArray {
		e.fail()
		return
	}
	e.stack = e.stack[:len(e.stack)-1]
	// 根对象完成后栈空且停留在 sAfterValue：其后任何非空白都会判死。
	e.state = sAfterValue
}

// emitRune 把一个解码后的字符送往当前归属：键缓冲或正文输出。
func (e *proseExtractor) emitRune(r rune) {
	if e.isKey {
		var buf [utf8.UTFMax]byte
		e.appendKey(string(buf[:utf8.EncodeRune(buf[:], r)]))
		return
	}
	if e.emitting {
		e.out = utf8.AppendRune(e.out, r)
	}
}

func (e *proseExtractor) appendKey(chunk string) {
	if e.keyOver {
		return
	}
	if len(e.keyBuf)+len(chunk) > maxProseKey {
		e.keyOver = true
		return
	}
	e.keyBuf = append(e.keyBuf, chunk...)
}

// acceptCodeUnit 处理收满的 \uXXXX 码元：代理对跨片段配对，孤立项发 U+FFFD。
func (e *proseExtractor) acceptCodeUnit(cu rune) {
	if e.pendingHi != 0 {
		if cu >= 0xdc00 && cu <= 0xdfff {
			r := 0x10000 + (e.pendingHi-0xd800)<<10 + (cu - 0xdc00)
			e.pendingHi = 0
			e.emitRune(r)
			return
		}
		e.pendingHi = 0
		e.emitRune(utf8.RuneError)
	}
	switch {
	case cu >= 0xd800 && cu <= 0xdbff:
		e.pendingHi = cu
	case cu >= 0xdc00 && cu <= 0xdfff:
		e.emitRune(utf8.RuneError)
	default:
		e.emitRune(cu)
	}
}

func (e *proseExtractor) flushPendingHi() {
	if e.pendingHi != 0 {
		e.pendingHi = 0
		e.emitRune(utf8.RuneError)
	}
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func unescapeJSON(b byte) rune {
	switch b {
	case 'b':
		return '\b'
	case 'f':
		return '\f'
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	default:
		return '\t'
	}
}

func hexDigit(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	default:
		return -1
	}
}

// splitIncompleteRune 把尾部不完整的 UTF-8 序列切出来（≤3 字节）；
// 找不到起始字节的畸形尾部原样放行，交显示层兜底。
func splitIncompleteRune(b []byte) (complete, rest []byte) {
	for back := 1; back <= 3 && back <= len(b); back++ {
		c := b[len(b)-back]
		if c&0xc0 == 0x80 {
			continue
		}
		var need int
		switch {
		case c&0x80 == 0:
			need = 1
		case c&0xe0 == 0xc0:
			need = 2
		case c&0xf0 == 0xe0:
			need = 3
		case c&0xf8 == 0xf0:
			need = 4
		default:
			return b, nil
		}
		if back < need {
			return b[:len(b)-back], b[len(b)-back:]
		}
		return b, nil
	}
	return b, nil
}

// proseTracker 归属一次 agent 执行：按 CallID 换提取器实例（重试或二次落笔
// 零残留），并缓冲个别 Provider 首块不带工具名的片段。方法在事件回调里被
// 串行调用；仍加锁防御 agentcore 未来改多 goroutine 派发（零争用近零成本）。
// proseCall 是一次工具调用的独立提取状态：按 CallID 隔离，交错调用互不污染。
type proseCall struct {
	known     bool // 工具已判明；extractor 为 nil 即非正文工具，整调用跳过
	extractor *proseExtractor
	pending   []byte
	dropped   bool
	announced bool // 本调用已宣告过直播中断（每调用至多一次）
}

// stall 在提取器判死的第一时间返回 true（每调用至多一次）。
func (c *proseCall) stall() bool {
	if c.announced || c.extractor.state != sFailed {
		return false
	}
	c.announced = true
	return true
}

// proseTracker 归属一次 agent 执行：每个调用一个独立提取器——A→B→A 交错
// 不丢 A 的状态，重试/二次落笔天然零残留；并缓冲个别 Provider 首块不带
// 工具名的片段。状态生命周期以一条 assistant 消息为界（参数流只存在于消息
// 内，消息结束即全部完结，finishMessage 清空），无需任何容量上限。方法在
// 事件回调里被串行调用；仍加锁防御 agentcore 未来改多 goroutine 派发
// （零争用近零成本）。
type proseTracker struct {
	mu    sync.Mutex
	calls map[string]*proseCall // 键为 CallID；"" 是身份未明期的暂存槽
}

func newProseTracker() *proseTracker {
	return &proseTracker{calls: make(map[string]*proseCall)}
}

// finishMessage 在一条 assistant 消息结束（含中止）时清空本轮全部提取状态：
// 消息一结束参数流必然终结，残留状态只会是死重。
func (t *proseTracker) finishMessage() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.calls)
}

// feed 消费一个参数增量；tool/callID 可为空（身份未明期先缓冲）。
// 返回本次片段解出的正文增量，以及是否在此刻发生"直播中断"——正文工具的
// 预览提取失去完整性（解析判死或起始片段已弃），每次调用至多宣告一次；
// 权威正文在候选稿，中断只影响预览，但必须显式告知而非静默停更。
func (t *proseTracker) feed(tool, callID, delta string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	call := t.calls[callID]
	if call == nil {
		if callID != "" {
			// 身份未明期（键 ""）缓冲的开头归属第一个明确的调用：顺序流式
			// 下无 ID 片段只出现在流开局。
			if orphan := t.calls[""]; orphan != nil {
				delete(t.calls, "")
				call = orphan
			}
		}
		if call == nil {
			call = &proseCall{}
		}
		t.calls[callID] = call
	}
	if !call.known {
		if tool == "" {
			if !call.dropped {
				if len(call.pending)+len(delta) > maxProsePending {
					call.pending, call.dropped = nil, true
				} else {
					call.pending = append(call.pending, delta...)
				}
			}
			return "", false
		}
		call.known = true
		path, ok := prosePaths[tool]
		if !ok {
			call.pending = nil
			return "", false
		}
		if call.dropped {
			// 正文工具但开头已因缓冲超限丢弃：中途起播必然错乱，整调用放弃并宣告。
			call.announced = true
			return "", true
		}
		call.extractor = newProseExtractor(path)
		if len(call.pending) > 0 {
			buffered := call.extractor.feed(string(call.pending))
			call.pending = nil
			return buffered + call.extractor.feed(delta), call.stall()
		}
	}
	if call.extractor == nil {
		return "", false
	}
	return call.extractor.feed(delta), call.stall()
}
