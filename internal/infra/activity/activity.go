// Package activity 是创作过程的进程内实时活动通道（工作台页面设计 §4 活动管道）。
// 它属于运行时平面：权威存储不经过这里，流式 delta 不落库、不参与恢复语义。
// Hub 只保留每个作品的最新活动快照，订阅者被唤醒后整读快照，不依赖收到每个通知。
package activity

import (
	"sync"
	"time"
)

// Kind 是发布侧事件类别，由 capability 从 agent 生命周期事件翻译而来。
type Kind string

const (
	ToolStart Kind = "tool_start"
	ToolEnd   Kind = "tool_end"
	// ToolDelta 表示工具参数 JSON 正在持续流入：只累计字节进度。正文预览
	// 由发布侧解析参数流后以 Prose 事件另行承载（§3 三层校正链不变）。
	ToolDelta Kind = "tool_delta"
	// Prose 是从正文工具参数流中提取的解转义正文增量（预览性质，权威正文
	// 仍以候选稿为准）。Text 承载增量，CallID 标记归属的工具调用。
	Prose Kind = "prose"
	// ProseStall 宣告一次正文直播中断（预览提取失去完整性，每调用至多一次）：
	// 成稿不受影响，但停更必须显式告知而非让预览静默冻结。
	ProseStall Kind = "prose_stall"
	// Text 是模型的说明性文字增量：折叠为辅助行，不进正文预览。
	Text Kind = "text"
	// Thinking 表示模型正在构思；内容本身不展示（不作产品承诺）。
	Thinking Kind = "thinking"
	Retry    Kind = "retry"
)

// Event 是一条带归属的活动事件；归属字段供消费侧做身份校验（交付契约 4）。
type Event struct {
	ProjectID   string
	RunID       string
	OperationID string
	Kind        Kind
	Tool        string // ToolStart/ToolEnd/ToolDelta：规范工具名
	CallID      string // 同一次工具调用的配对键：delta 与执行起止靠它对上
	Err         string // ToolEnd 出错摘要 / Retry 原因
	Attempt     int    // Retry：第几次
	Text        string // Text/Prose：文字增量
	Bytes       int    // ToolDelta：本次增量字节数
	At          time.Time
}

// Entry 是快照中的一行生命周期条目：一次工具调用（进行中或已收尾）或一次重试。
// Bytes 是这次调用已接收的参数字节数（不是正文字数）——按条目归属，
// 一条消息里多个调用各自计数，互不串行。
type Entry struct {
	OperationID string
	Kind        Kind
	Tool        string
	CallID      string
	Err         string
	Attempt     int
	Bytes       int
	Done        bool
	At          time.Time
}

// Snapshot 是某作品的最新活动快照。Entries 有界（旧条目被丢弃），连续 delta
// 只合并为条目进度不追加条目（交付契约 3）。
type Snapshot struct {
	ProjectID   string
	RunID       string
	OperationID string // 最近活动所属的任务
	Entries     []Entry
	// Note 是说明性文字折叠成的辅助行，只保留尾部。
	Note     string
	Thinking bool
	// ThinkingNote 是思考尾部片段（Provider 明确标记的思考文本原样截尾，
	// 不是摘要，§3：仅在提供时展示，不作可靠解释承诺）。
	ThinkingNote string
	// Prose 是当前正文调用已提取正文的尾部有界缓冲（保证合法 UTF-8）。
	// 用 []byte 而非 string：fold 在锁内、在模型流回调同步路径上执行，
	// append 摊还 O(增量)；string 拼接是每增量 O(全文) 的隐性平方开销。
	Prose []byte
	// ProseCallID 标记 Prose 归属的工具调用；换调用即整体重置，
	// 重试或二次落笔不残留上一稿。
	ProseCallID string
	// Seq 每次变更递增，消费侧可据此跳过重复渲染。
	Seq uint64
}

const (
	maxEntries  = 64
	maxNoteSize = 120
	// maxProseBytes 是正文预览缓冲上界（≈8000 中文字，整章有裕量）；
	// proseSlack 是摊还裁剪的松弛量：超过 max+slack 才裁回 max，避免每增量搬移。
	maxProseBytes = 24 << 10
	proseSlack    = 8 << 10
)

// Hub 保存进程内活动快照并向订阅者发送合并唤醒信号。发布永不阻塞：唤醒
// channel 容量为 1，满即合并（交付契约 1）；UI 消费停滞不得拖慢创作。
type Hub struct {
	mu        sync.Mutex
	snapshots map[string]*Snapshot
	subs      map[string]map[int]chan struct{}
	nextSub   int
}

func NewHub() *Hub {
	return &Hub{
		snapshots: make(map[string]*Snapshot),
		subs:      make(map[string]map[int]chan struct{}),
	}
}

// Publish 把事件折叠进对应作品的快照并唤醒订阅者。发送在锁内进行且非阻塞，
// 与取消（关闭 channel）互斥，因此既不会 panic 也不会阻塞发布方。
func (h *Hub) Publish(event Event) {
	if event.ProjectID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.snapshots[event.ProjectID]
	if snapshot == nil || snapshot.RunID != event.RunID {
		// 新一轮创作：旧快照整体作废，上一轮的条目不得冒充当前进展。
		fresh := &Snapshot{ProjectID: event.ProjectID, RunID: event.RunID}
		if snapshot != nil {
			fresh.Seq = snapshot.Seq
		}
		snapshot = fresh
		h.snapshots[event.ProjectID] = snapshot
	}
	snapshot.fold(event)
	snapshot.Seq++
	for _, wake := range h.subs[event.ProjectID] {
		select {
		case wake <- struct{}{}:
		default: // 上一个信号还没被消费：合并
		}
	}
}

// Subscribe 订阅作品的活动变化：返回容量 1 的合并唤醒信号与取消函数。
// 取消只结束订阅（channel 关闭，读端收到 ok=false），绝不取消创作，
// 取消后发布端不保留任何引用（交付契约 2）。
func (h *Hub) Subscribe(projectID string) (<-chan struct{}, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	wake := make(chan struct{}, 1)
	id := h.nextSub
	h.nextSub++
	if h.subs[projectID] == nil {
		h.subs[projectID] = make(map[int]chan struct{})
	}
	h.subs[projectID][id] = wake
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[projectID][id]; !ok {
			return
		}
		delete(h.subs[projectID], id)
		if len(h.subs[projectID]) == 0 {
			delete(h.subs, projectID)
		}
		close(wake)
	}
	return wake, cancel
}

// Snapshot 整读作品当前活动快照（副本）；ok=false 表示还没有任何活动。
func (h *Hub) Snapshot(projectID string) (Snapshot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.snapshots[projectID]
	if snapshot == nil {
		return Snapshot{}, false
	}
	copied := *snapshot
	copied.Entries = append([]Entry(nil), snapshot.Entries...)
	copied.Prose = append([]byte(nil), snapshot.Prose...)
	return copied, true
}

func (s *Snapshot) fold(event Event) {
	if s.OperationID != event.OperationID {
		s.OperationID = event.OperationID
		s.Note, s.Thinking, s.ThinkingNote = "", false, ""
		s.Prose, s.ProseCallID = nil, ""
	}
	switch event.Kind {
	case ToolStart:
		// 真实时序里参数流入先于执行：这次调用的进行中条目可能已由首个 delta
		// 建立（按 CallID 配对），不重复追加、不清已接收进度。
		if s.openIndex(event) < 0 {
			s.appendCall(event)
		}
		s.Thinking = false
	case ToolDelta:
		// 参数开始流入即建立进行中条目并按条目累计字节；名字未知的增量
		// 归属最近的进行中调用，完全无处归属时丢弃（无可展示对象）。
		index := s.openIndex(event)
		if index < 0 && event.Tool != "" {
			s.appendCall(event)
			index = len(s.Entries) - 1
		}
		if index < 0 {
			index = s.lastOpen()
		}
		if index >= 0 {
			s.Entries[index].Bytes += event.Bytes
		}
		s.Thinking = false
	case ToolEnd:
		if index := s.openIndex(event); index >= 0 {
			s.Entries[index].Done, s.Entries[index].Err = true, event.Err
		} else {
			// 起点条目已被丢弃：补一条完成条目，收尾事实不丢失。
			s.append(Entry{
				OperationID: event.OperationID, Kind: ToolStart, Tool: event.Tool,
				CallID: event.CallID, Err: event.Err, Done: true, At: event.At,
			})
		}
	case Prose:
		if s.ProseCallID != event.CallID {
			s.ProseCallID, s.Prose = event.CallID, nil
		}
		s.Prose = appendProse(s.Prose, event.Text)
		s.Thinking = false
	case ProseStall:
		// 一次性警示条目（同 Retry）：正文直播中断留痕于活动流。
		s.append(Entry{
			OperationID: event.OperationID, Kind: ProseStall,
			Tool: event.Tool, CallID: event.CallID, Done: true, At: event.At,
		})
	case Text:
		s.Note = tail(s.Note+event.Text, maxNoteSize)
		s.Thinking = false
	case Thinking:
		s.Thinking = true
		if event.Text != "" {
			s.ThinkingNote = tail(s.ThinkingNote+event.Text, maxNoteSize)
		}
	case Retry:
		s.append(Entry{
			OperationID: event.OperationID, Kind: Retry,
			Attempt: event.Attempt, Err: event.Err, Done: true, At: event.At,
		})
	}
}

// openIndex 定位这次调用对应的未收尾条目：双方都有 CallID 时严格按它配对
// （同名工具的多次调用互不混淆），缺 ID 时退化为同名配对。
func (s *Snapshot) openIndex(event Event) int {
	for index := len(s.Entries) - 1; index >= 0; index-- {
		entry := s.Entries[index]
		if entry.Kind != ToolStart || entry.Done {
			continue
		}
		if event.CallID != "" && entry.CallID != "" {
			if entry.CallID == event.CallID {
				return index
			}
			continue
		}
		if entry.Tool == event.Tool {
			return index
		}
	}
	return -1
}

// appendCall 为一次新工具调用建立进行中条目（字节从零起计）。
func (s *Snapshot) appendCall(event Event) {
	s.append(Entry{
		OperationID: event.OperationID, Kind: ToolStart,
		Tool: event.Tool, CallID: event.CallID, At: event.At,
	})
}

// lastOpen 取最近一条未收尾的调用条目下标；没有则 -1。
func (s *Snapshot) lastOpen() int {
	for index := len(s.Entries) - 1; index >= 0; index-- {
		if s.Entries[index].Kind == ToolStart && !s.Entries[index].Done {
			return index
		}
	}
	return -1
}

func (s *Snapshot) append(entry Entry) {
	s.Entries = append(s.Entries, entry)
	if len(s.Entries) > maxEntries {
		s.Entries = s.Entries[len(s.Entries)-maxEntries:]
	}
}

// appendProse 摊还地维护正文尾部缓冲：只在越过松弛界时裁回上界，
// 裁剪点向前跳过 UTF-8 连续字节对齐 rune 边界（输入是合法 UTF-8，最多跳 3 字节）。
func appendProse(buf []byte, text string) []byte {
	buf = append(buf, text...)
	if len(buf) <= maxProseBytes+proseSlack {
		return buf
	}
	cut := len(buf) - maxProseBytes
	for cut < len(buf) && buf[cut]&0xc0 == 0x80 {
		cut++
	}
	return append(buf[:0], buf[cut:]...)
}

func tail(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[len(runes)-limit:])
}
