package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/app"
)

// 消息类型
type (
	eventMsg       app.UIEvent
	snapshotMsg    app.UISnapshot
	doneMsg        struct{}
	startResultMsg struct{ err error }
	steerResultMsg struct{}
	spinnerTickMsg time.Time
	streamDeltaMsg string   // 流式 token 增量
	streamClearMsg struct{} // 清空流式缓冲（新消息开始）
)

// --- Cmd 函数 ---

func listenEvents(rt *app.Runtime) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-rt.Events()
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func listenDone(rt *app.Runtime) tea.Cmd {
	return func() tea.Msg {
		<-rt.Done()
		return doneMsg{}
	}
}

func tickSnapshot(rt *app.Runtime) tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return snapshotMsg(rt.Snapshot())
	})
}

func fetchSnapshot(rt *app.Runtime) tea.Cmd {
	return func() tea.Msg {
		return snapshotMsg(rt.Snapshot())
	}
}

func checkResume(rt *app.Runtime) tea.Cmd {
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

func startRuntime(rt *app.Runtime, prompt string) tea.Cmd {
	return func() tea.Msg {
		err := rt.Start(prompt)
		return startResultMsg{err: err}
	}
}

func steerRuntime(rt *app.Runtime, text string) tea.Cmd {
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

func listenStream(rt *app.Runtime) tea.Cmd {
	return func() tea.Msg {
		delta, ok := <-rt.Stream()
		if !ok {
			return nil
		}
		return streamDeltaMsg(delta)
	}
}

func listenStreamClear(rt *app.Runtime) tea.Cmd {
	return func() tea.Msg {
		_, ok := <-rt.StreamClear()
		if !ok {
			return nil
		}
		return streamClearMsg{}
	}
}
