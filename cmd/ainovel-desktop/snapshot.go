package main

import (
	"context"
	"time"

	"github.com/voocel/ainovel-cli/internal/host"
)

// 快照推送节奏：运行中 1s，停机 2s。Snapshot() 取 h.mu + 少量 store 读，1Hz 无压力；
// 停机时状态基本不变，退避以省开销。
const (
	snapshotIntervalRunning = time.Second
	snapshotIntervalIdle    = 2 * time.Second
)

// runSnapshotLoop 周期性把聚合状态推给前端（engine:snapshot）。
// 事件流有损且只作展示，权威状态一律由这里和 engine:done 携带的快照提供。
func (a *App) runSnapshotLoop(ctx context.Context, h *host.Host) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			snap := h.Snapshot()
			a.emit("engine:snapshot", snap)
			next := snapshotIntervalIdle
			if snap.IsRunning {
				next = snapshotIntervalRunning
			}
			timer.Reset(next)
		}
	}
}
