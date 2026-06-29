package eval

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/host"
)

// fakeEngine 模拟 host：Abort 后像 waitDone 那样向 done 发送一次。done 带 1 缓冲，
// 测试据此断言 drive 是否 drain 了 Done（len(done)==0 即已消费）——这是防 send-on-closed-channel
// panic 的关键不变量。
type fakeEngine struct {
	events chan host.Event
	stream chan string
	done   chan struct{}

	mu      sync.Mutex
	snap    host.UISnapshot
	aborted bool
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		events: make(chan host.Event, 4),
		stream: make(chan string),
		done:   make(chan struct{}, 1),
	}
}

func (f *fakeEngine) Events() <-chan host.Event { return f.events }
func (f *fakeEngine) Stream() <-chan string     { return f.stream }
func (f *fakeEngine) Done() <-chan struct{}     { return f.done }

func (f *fakeEngine) Snapshot() host.UISnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeEngine) Abort() bool {
	f.mu.Lock()
	f.aborted = true
	f.mu.Unlock()
	select { // 模拟 waitDone：abort 触发后向 done 发送一次
	case f.done <- struct{}{}:
	default:
	}
	return true
}

func (f *fakeEngine) wasAborted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.aborted
}

// 超时路径必须 Abort 后 drain 到 Done 再返回超时错误——否则 RunCase 的 Close 会与
// waitDone 竞争关闭 done 通道而 panic（Codex review #1）。
func TestDriveTimeoutDrainsToDone(t *testing.T) {
	f := newFakeEngine()
	err := drive(f, 1, RunOptions{Timeout: 30 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("超时应返回超时错误，得到 %v", err)
	}
	if !f.wasAborted() {
		t.Fatal("超时应触发 Abort")
	}
	if len(f.done) != 0 {
		t.Fatal("drive 必须 drain Done 后才返回（否则与 Close 竞争关闭通道 panic）")
	}
}

// 达到章数上限：Abort 后 drain 到 Done，返回 nil（正常截停，不是超时）。
func TestDriveCapStopsAndDrains(t *testing.T) {
	f := newFakeEngine()
	f.mu.Lock()
	f.snap = host.UISnapshot{CompletedCount: 1}
	f.mu.Unlock()
	f.events <- host.Event{Category: "SYSTEM", Summary: "committed"} // 触发 cap 检查

	err := drive(f, 1, RunOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("正常截停应返回 nil，得到 %v", err)
	}
	if !f.wasAborted() {
		t.Fatal("达到章数上限应 Abort")
	}
	if len(f.done) != 0 {
		t.Fatal("应 drain Done 后返回")
	}
}

// 引擎自然 Done（书写完）：无需 Abort，返回 nil。
func TestDriveNaturalDoneReturnsNil(t *testing.T) {
	f := newFakeEngine()
	f.done <- struct{}{}

	err := drive(f, 1, RunOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("自然完成应返回 nil，得到 %v", err)
	}
	if f.wasAborted() {
		t.Fatal("自然完成不应 Abort")
	}
}
