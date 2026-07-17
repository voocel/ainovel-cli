package host

import "testing"

// 退出期竞态：Close() 关闭 h.events/h.streamCh 后，未登记的同步 emit 者
// （Abort/abortWithEvent、预算哨兵）仍可能并发 emit。emitEvent/emitDelta 作为唯一
// 漏斗必须兜住"向已关闭通道发送"的 panic，而不是让进程在退出期崩溃。
func TestEmitAfterCloseDoesNotPanic(t *testing.T) {
	h := &Host{
		events:   make(chan Event, 1),
		streamCh: make(chan string, 1),
	}
	close(h.events)
	close(h.streamCh)

	// 若漏斗未兜住关通道发送，这两行会 panic 导致测试失败。
	h.emitEvent(Event{Summary: "after close"})
	h.emitDelta("after close")
}
