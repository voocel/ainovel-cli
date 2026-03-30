package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
)

// 消息类型
type (
	eventMsg       orchestrator.UIEvent
	snapshotMsg    orchestrator.UISnapshot
	doneMsg        struct{ complete bool } // complete=true 全书完成，false 出错停止
	askUserMsg     askUserRequest
	startResultMsg struct{ err error }
	steerResultMsg struct{}
	spinnerTickMsg time.Time
	streamDeltaMsg string   // 流式 token 增量
	streamClearMsg struct{} // 清空流式缓冲（新消息开始）
	quitResetMsg   struct{} // 双次 Ctrl+C 超时重置
)

// --- Cmd 函数 ---

func listenEvents(rt *orchestrator.Runtime) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-rt.Events()
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func listenDone(rt *orchestrator.Runtime) tea.Cmd {
	return func() tea.Msg {
		<-rt.Done()
		snap := rt.Snapshot()
		return doneMsg{complete: snap.Phase == "complete"}
	}
}

func tickSnapshot(rt *orchestrator.Runtime) tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return snapshotMsg(rt.Snapshot())
	})
}

func fetchSnapshot(rt *orchestrator.Runtime) tea.Cmd {
	return func() tea.Msg {
		return snapshotMsg(rt.Snapshot())
	}
}

func bootstrapRuntime(rt *orchestrator.Runtime) tea.Cmd {
	return func() tea.Msg {
		label, err := rt.Resume()
		if err != nil {
			return startResultMsg{err: err}
		}
		if label != "" {
			return startResultMsg{err: nil}
		}
		return nil
	}
}

func startRuntime(rt *orchestrator.Runtime, prompt string) tea.Cmd {
	return func() tea.Msg {
		err := rt.Start(prompt)
		return startResultMsg{err: err}
	}
}

func steerRuntime(rt *orchestrator.Runtime, text string) tea.Cmd {
	return func() tea.Msg {
		rt.Steer(text)
		return steerResultMsg{}
	}
}

func tickSpinner() tea.Cmd {
	return tea.Tick(350*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

func listenStream(rt *orchestrator.Runtime) tea.Cmd {
	return func() tea.Msg {
		delta, ok := <-rt.Stream()
		if !ok {
			return nil
		}
		return streamDeltaMsg(delta)
	}
}

func listenStreamClear(rt *orchestrator.Runtime) tea.Cmd {
	return func() tea.Msg {
		_, ok := <-rt.StreamClear()
		if !ok {
			return nil
		}
		return streamClearMsg{}
	}
}

func listenAskUser(bridge *askUserBridge) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-bridge.requests
		if !ok {
			return nil
		}
		return askUserMsg(req)
	}
}
