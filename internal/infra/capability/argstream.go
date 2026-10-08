package capability

import (
	"sync"
	"unicode/utf8"
)

// 工具参数流式提取（页面设计 §3）：正文与结构化产出都以工具参数 JSON 的逐 token
// 裸片段流出。这里按一棵路径树在同一遍扫描里增量提取：正文路径下的字符串值解转义后
// 逐字发射（正文直播）；条目路径下的值收完整后整条交出原始 JSON（大纲的一章、一条
// 设定、一条审阅意见）。纯预览通道：权威内容在候选稿，因此容错语义是绝不 panic、
// 结构非法即永久停止发射、不做恢复式猜测（猜错会把 JSON 结构喷进预览，比预览停在
// 半途更糟）。

// tapPath 是从参数对象根出发的路径；步为 "" 表示数组元素通配。
type tapPath []string

type tapMode uint8

const (
	tapText tapMode = iota + 1 // 字符串值解转义后逐字发射
	tapItem                    // 值收完整后整条交出
)

// tapNode 是路径树的节点；item 是条目路径的序号，交出条目时据此认出它是哪一类。
type tapNode struct {
	next map[string]*tapNode
	mode tapMode
	item int
}

// tapTree 由至多一条正文路径与若干条目路径建成。
func tapTree(text tapPath, items []tapPath) *tapNode {
	root := &tapNode{}
	add := func(path tapPath, mode tapMode, item int) {
		node := root
		for _, step := range path {
			if node.next == nil {
				node.next = make(map[string]*tapNode)
			}
			child := node.next[step]
			if child == nil {
				child = &tapNode{}
				node.next[step] = child
			}
			node = child
		}
		node.mode, node.item = mode, item
	}
	if len(text) > 0 {
		add(text, tapText, 0)
	}
	for i, path := range items {
		add(path, tapItem, i)
	}
	return root
}

// capturedItem 是收完整的一个条目：item 是条目路径的序号，raw 是它的原始 JSON。
type capturedItem struct {
	item int
	raw  []byte
}

const (
	maxTapKey      = 64      // 键缓冲上界：超长键不可能匹配 schema，判死键即可
	maxTapDepth    = 32      // 容器栈上界：schema 深度不超过 5，留足裕量
	maxTapPending  = 8 << 10 // 工具名未知期间的片段缓冲上界，超限弃开头不无界缓存
	maxTapItemSize = 32 << 10
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

// tapFrame 是容器栈帧；node 是进入该容器时所在的路径树节点，压帧时由父帧一次算定、
// 生命周期内不可变，nil 为死路径（子树照常推进不发射）。
type tapFrame struct {
	isArray bool
	node    *tapNode
}

// tapCapture 是正在收集的条目：depth 是条目开始时的栈深，回到这个深度即收完；
// from 是条目在当前片段里的起点，跨片段时下一片从 0 接续。
type tapCapture struct {
	item    int
	depth   int
	from    int
	raw     []byte
	dropped bool // 超出单条上界：照常跟踪到收尾，但不交出
}

// argExtractor 服务一次工具调用；feed 为 O(片段长度)，无回扫无重扫。
type argExtractor struct {
	root      *tapNode
	stack     []tapFrame
	state     jsonState
	isKey     bool
	keyBuf    []byte
	keyOver   bool
	emitting  bool // 当前字符串值在正文路径上
	emitted   bool // 已发射过正文：后续正文值开启前补段落分隔
	hexN      int
	hexV      rune
	pendingHi rune   // 待配对的高代理项；0 表示无
	carry     []byte // 输出尾部不完整的 UTF-8 字节（≤3），下次 feed 前置
	out       []byte // feed 内复用的输出缓冲
	capture   *tapCapture
	items     []capturedItem
}

func newArgExtractor(root *tapNode) *argExtractor {
	return &argExtractor{root: root}
}

// feed 消费一个参数 JSON 片段，返回本次新解出的正文（保证合法 UTF-8，可为空）与
// 本次收完的条目（按文档顺序）。
func (e *argExtractor) feed(fragment string) (string, []capturedItem) {
	if e.state == sFailed {
		return "", nil
	}
	e.out = append(e.out[:0], e.carry...)
	e.carry = e.carry[:0]
	e.items = nil
	if e.capture != nil {
		e.capture.from = 0
	}
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
				node := e.childNode()
				if e.push(false, node) {
					e.beginCapture(node, len(e.stack)-1, i)
					e.state = sKey
				}
			case b == '[':
				node := e.childNode()
				if e.push(true, node) { // 数组首元素仍期待一个值，状态不变
					e.beginCapture(node, len(e.stack)-1, i)
				}
			case b == ']':
				e.pop(true) // 空数组是合法 JSON；栈顶不是数组才判死
				e.valueEnded(fragment, i+1)
			case b == '"':
				node := e.childNode()
				e.isKey = false
				e.emitting = node != nil && node.mode == tapText
				if e.emitting {
					if e.emitted {
						e.out = append(e.out, "\n\n"...)
					}
					e.emitted = true
				}
				e.beginCapture(node, len(e.stack), i)
				e.state = sStr
			case b == '-' || b >= '0' && b <= '9' || b == 't' || b == 'f' || b == 'n':
				e.beginCapture(e.childNode(), len(e.stack), i)
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
				e.valueEnded(fragment, i+1)
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
					e.valueEnded(fragment, i+1)
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
				e.valueEnded(fragment, i+1)
			case ']':
				e.pop(true)
				e.valueEnded(fragment, i+1)
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
			e.valueEnded(fragment, i)
		}
	}
	if e.state == sFailed {
		e.capture = nil
	} else if c := e.capture; c != nil {
		c.collect(fragment[c.from:])
	}
	complete, rest := splitIncompleteRune(e.out)
	e.carry = append(e.carry, rest...)
	if !utf8.Valid(complete) {
		// 原始内容里混入畸形字节（模型异常输出）：以 U+FFFD 替换非法序列，
		// 守住"返回值恒为合法 UTF-8"的出口保证。常态流不走这条慢路径。
		return sanitizeUTF8(complete), e.items
	}
	return string(complete), e.items
}

// beginCapture 在条目路径上的值开始时起收；条目之内不再嵌套起收。
func (e *argExtractor) beginCapture(node *tapNode, depth, at int) {
	if node == nil || node.mode != tapItem || e.capture != nil {
		return
	}
	e.capture = &tapCapture{item: node.item, depth: depth, from: at}
}

// valueEnded 在一个值收尾时调用：end 是它在当前片段里的结束位置（不含）。
func (e *argExtractor) valueEnded(fragment string, end int) {
	c := e.capture
	if c == nil || e.state == sFailed || len(e.stack) != c.depth {
		return
	}
	c.collect(fragment[c.from:end])
	if !c.dropped {
		e.items = append(e.items, capturedItem{item: c.item, raw: c.raw})
	}
	e.capture = nil
}

func (c *tapCapture) collect(chunk string) {
	if c.dropped {
		return
	}
	if len(c.raw)+len(chunk) > maxTapItemSize {
		c.raw, c.dropped = nil, true
		return
	}
	c.raw = append(c.raw, chunk...)
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

func (e *argExtractor) fail() {
	e.state, e.pendingHi = sFailed, 0
}

// childNode 算即将开始的值所在的路径树节点；只在值开始的瞬间调用一次。
func (e *argExtractor) childNode() *tapNode {
	if len(e.stack) == 0 {
		return e.root // 参数对象根本身
	}
	top := e.stack[len(e.stack)-1]
	if top.node == nil {
		return nil
	}
	if top.isArray {
		return top.node.next[""]
	}
	if e.keyOver {
		return nil
	}
	return top.node.next[string(e.keyBuf)]
}

func (e *argExtractor) push(isArray bool, node *tapNode) bool {
	if len(e.stack) >= maxTapDepth {
		e.fail()
		return false
	}
	e.stack = append(e.stack, tapFrame{isArray: isArray, node: node})
	return true
}

func (e *argExtractor) pop(isArray bool) {
	if len(e.stack) == 0 || e.stack[len(e.stack)-1].isArray != isArray {
		e.fail()
		return
	}
	e.stack = e.stack[:len(e.stack)-1]
	// 根对象完成后栈空且停留在 sAfterValue：其后任何非空白都会判死。
	e.state = sAfterValue
}

// emitRune 把一个解码后的字符送往当前归属：键缓冲或正文输出。
func (e *argExtractor) emitRune(r rune) {
	if e.isKey {
		var buf [utf8.UTFMax]byte
		e.appendKey(string(buf[:utf8.EncodeRune(buf[:], r)]))
		return
	}
	if e.emitting {
		e.out = utf8.AppendRune(e.out, r)
	}
}

func (e *argExtractor) appendKey(chunk string) {
	if e.keyOver {
		return
	}
	if len(e.keyBuf)+len(chunk) > maxTapKey {
		e.keyOver = true
		return
	}
	e.keyBuf = append(e.keyBuf, chunk...)
}

// acceptCodeUnit 处理收满的 \uXXXX 码元：代理对跨片段配对，孤立项发 U+FFFD。
func (e *argExtractor) acceptCodeUnit(cu rune) {
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

func (e *argExtractor) flushPendingHi() {
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

// argCall 是一次工具调用的独立提取状态：按 CallID 隔离，交错调用互不污染。
type argCall struct {
	known     bool // 工具已判明；extractor 为 nil 即没有可直播的部分，整调用跳过
	spec      streamSpec
	extractor *argExtractor
	pending   []byte
	dropped   bool
	announced bool // 本调用已宣告过直播中断（每调用至多一次）
}

// stall 在正文提取判死的第一时间返回 true（每调用至多一次）；条目提取判死只是
// 不再出新条目，权威内容仍在候选稿，不另行宣告。
func (c *argCall) stall() bool {
	if c.announced || len(c.spec.text) == 0 || c.extractor.state != sFailed {
		return false
	}
	c.announced = true
	return true
}

// argTracker 归属一次 agent 执行：每个调用一个独立提取器——A→B→A 交错不丢 A 的
// 状态，重试/二次落笔天然零残留；并缓冲个别 Provider 首块不带工具名的片段。状态
// 生命周期以一条 assistant 消息为界（参数流只存在于消息内，消息结束即全部完结，
// finishMessage 清空），无需任何容量上限。方法在事件回调里被串行调用；仍加锁防御
// agentcore 未来改多 goroutine 派发（零争用近零成本）。
type argTracker struct {
	mu    sync.Mutex
	specs map[string]streamSpec
	calls map[string]*argCall // 键为 CallID；"" 是身份未明期的暂存槽
}

func newArgTracker(specs map[string]streamSpec) *argTracker {
	return &argTracker{specs: specs, calls: make(map[string]*argCall)}
}

// finishMessage 在一条 assistant 消息结束（含中止）时清空本轮全部提取状态：
// 消息一结束参数流必然终结，残留状态只会是死重。
func (t *argTracker) finishMessage() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.calls)
}

// feed 消费一个参数增量；tool/callID 可为空（身份未明期先缓冲）。返回本次片段解出
// 的正文增量、收完的条目（已写成故事语言），以及是否在此刻发生"直播中断"——正文
// 工具的预览提取失去完整性（解析判死或起始片段已弃），每次调用至多宣告一次。
func (t *argTracker) feed(tool, callID, delta string) (string, []liveLine, bool) {
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
			call = &argCall{}
		}
		t.calls[callID] = call
	}
	if !call.known {
		if tool == "" {
			if !call.dropped {
				if len(call.pending)+len(delta) > maxTapPending {
					call.pending, call.dropped = nil, true
				} else {
					call.pending = append(call.pending, delta...)
				}
			}
			return "", nil, false
		}
		call.known = true
		spec, ok := t.specs[tool]
		if !ok {
			call.pending = nil
			return "", nil, false
		}
		call.spec = spec
		if call.dropped {
			// 开头已因缓冲超限丢弃：中途起播必然错乱，整调用放弃；正文工具宣告中断。
			call.announced = len(spec.text) > 0
			return "", nil, call.announced
		}
		call.extractor = newArgExtractor(tapTree(spec.text, spec.itemPaths()))
		if len(call.pending) > 0 {
			delta = string(call.pending) + delta
			call.pending = nil
		}
	}
	if call.extractor == nil {
		return "", nil, false
	}
	text, items := call.extractor.feed(delta)
	return text, call.spec.lines(items), call.stall()
}
