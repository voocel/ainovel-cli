package main

import (
	"context"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/host"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// wailsEventsEmit 隔离 Wails runtime 依赖，便于 app.go 统一经 emit 发射。
func wailsEventsEmit(ctx context.Context, name string, data ...any) {
	wailsruntime.EventsEmit(ctx, name, data...)
}

// 流式合并参数：攒够时长或字节数再过一次 IPC。40ms 对肉眼仍是连续的，
// 但把每秒 EventsEmit 次数从上百降到 ~25。
const (
	flushInterval   = 40 * time.Millisecond
	maxPendingBytes = 4096
)

// runPump 是单 goroutine 事件泵：在同一个 select 里消费 Host 的 Events / Stream / Done
// 三条 channel，投影成 Wails 运行时事件推给前端。
//
// 关键纪律：
//   - 流式文本与 clear sentinel 走同一 channel/同一 case，保序（clear 会切分流式轮次）。
//   - Host.Close() 会关闭三条 channel；把已关闭 channel 的局部变量置 nil 使该 case
//     永久阻塞（nil channel 永远 block），排空缓冲后三者皆 nil 时退出，避免 panic/空转。
//   - Host 侧 emit 非阻塞、满则丢最旧：事件流有损、流式可能有间隙。前端据此只做展示，
//     权威状态一律来自 Snapshot()（engine:snapshot / engine:done 携带），不从事件流重建。
//   - 流式文本先在本地合并再过 IPC（见 flushInterval），避免逐 token 打满通道。
func (a *App) runPump(ctx context.Context, h *host.Host) {
	events := h.Events()
	stream := h.Stream()
	done := h.Done()

	var pending strings.Builder
	flushTimer := time.NewTimer(flushInterval)
	if !flushTimer.Stop() {
		<-flushTimer.C
	}
	defer flushTimer.Stop()

	flush := func() {
		if pending.Len() == 0 {
			return
		}
		a.emit("engine:stream", pending.String())
		pending.Reset()
	}
	defer flush()

	for {
		select {
		case <-ctx.Done():
			return

		case ev, ok := <-events:
			if !ok {
				events = nil
				break
			}
			a.emit("engine:event", toEventDTO(ev))

		case s, ok := <-stream:
			if !ok {
				stream = nil
				flush()
				break
			}
			if s == host.StreamClearSentinel {
				// 轮次边界必须先把已攒的文本吐出去，否则它会被并进下一轮。
				flush()
				a.emit("engine:stream:clear")
			} else {
				pending.WriteString(s)
				// 攒到 4KB 立刻发；否则重置计时器，等 40ms 静默期一并发出。
				// Stop 返回 false 说明计时器已触发，排掉待读的那一次再 Reset，避免竞态。
				if !flushTimer.Stop() {
					select {
					case <-flushTimer.C:
					default:
					}
				}
				if pending.Len() >= maxPendingBytes {
					flush()
				} else {
					flushTimer.Reset(flushInterval)
				}
			}

		case <-flushTimer.C:
			flush()

		case _, ok := <-done:
			if !ok {
				done = nil
				break
			}
			a.emit("engine:done", h.Snapshot())
		}

		if events == nil && stream == nil && done == nil {
			return
		}
	}
}
