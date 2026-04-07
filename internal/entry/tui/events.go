package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
	"github.com/voocel/ainovel-cli/internal/store"
)

// 消息类型
type (
	eventMsg       orchestrator.UIEvent
	snapshotMsg    orchestrator.UISnapshot
	doneMsg        struct{ complete bool } // complete=true 全书完成，false 出错停止
	abortResultMsg struct{ stopped bool }
	bootstrapMsg   struct {
		replay  []domain.RuntimeQueueItem
		resumed bool
		err     error
	}
	reportLoadedMsg struct {
		reqID      int
		report     diag.Report
		finishedAt time.Time
	}
	askUserMsg       askUserRequest
	startResultMsg   struct{ err error }
	cocreateDeltaMsg struct {
		reqID int
		text  string
	}
	cocreateDoneMsg struct {
		reqID int
		reply orchestrator.CoCreateReply
		err   error
	}
	steerResultMsg    struct{}
	continueResultMsg struct{ err error }
	spinnerTickMsg    time.Time
	cursorTickMsg     time.Time // 流式光标独立 tick
	streamDeltaMsg    string    // 流式 token 增量
	streamClearMsg    struct{} // 清空流式缓冲（新消息开始）
	quitResetMsg      struct{} // 双次 Ctrl+C 超时重置
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
		_, ok := <-rt.Done()
		if !ok {
			return nil
		}
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
		replay, err := rt.ReplayQueue(0)
		if err != nil {
			return bootstrapMsg{err: err}
		}
		label, err := rt.Resume()
		if err != nil {
			return bootstrapMsg{replay: replay, err: err}
		}
		if label == "" && len(replay) == 0 {
			return nil
		}
		return bootstrapMsg{replay: replay, resumed: label != ""}
	}
}

func startRuntime(rt *orchestrator.Runtime, plan startup.Plan) tea.Cmd {
	return func() tea.Msg {
		err := rt.StartPrepared(plan.StartPrompt)
		return startResultMsg{err: err}
	}
}

func runCoCreate(rt *orchestrator.Runtime, state *cocreateState) tea.Cmd {
	history := state.session.History()
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	state.deltaCh = make(chan string, 64)
	state.doneCh = make(chan cocreateDoneMsg, 1)
	start := func() tea.Msg {
		go func() {
			reply, err := rt.CoCreateStream(ctx, history, func(text string) {
				select {
				case state.deltaCh <- text:
				default:
				}
			})
			state.doneCh <- cocreateDoneMsg{reply: reply, err: err}
			close(state.deltaCh)
			close(state.doneCh)
		}()
		return nil
	}
	return tea.Batch(start, listenCoCreateDelta(state), listenCoCreateDone(state))
}

func listenCoCreateDelta(state *cocreateState) tea.Cmd {
	if state == nil || state.deltaCh == nil {
		return nil
	}
	reqID := state.reqID
	return func() tea.Msg {
		delta, ok := <-state.deltaCh
		if !ok {
			return nil
		}
		return cocreateDeltaMsg{reqID: reqID, text: delta}
	}
}

func listenCoCreateDone(state *cocreateState) tea.Cmd {
	if state == nil || state.doneCh == nil {
		return nil
	}
	reqID := state.reqID
	return func() tea.Msg {
		result, ok := <-state.doneCh
		if !ok {
			return nil
		}
		result.reqID = reqID
		return result
	}
}

func steerRuntime(rt *orchestrator.Runtime, text string) tea.Cmd {
	return func() tea.Msg {
		rt.Steer(text)
		return steerResultMsg{}
	}
}

func continueRuntime(rt *orchestrator.Runtime, text string) tea.Cmd {
	return func() tea.Msg {
		err := rt.Continue(text)
		return continueResultMsg{err: err}
	}
}

func abortRuntime(rt *orchestrator.Runtime) tea.Cmd {
	return func() tea.Msg {
		return abortResultMsg{stopped: rt.Abort()}
	}
}

func loadReport(dir string, reqID int) tea.Cmd {
	return func() tea.Msg {
		s := store.NewStore(dir)
		return reportLoadedMsg{
			reqID:      reqID,
			report:     diag.Analyze(s),
			finishedAt: time.Now(),
		}
	}
}

func tickSpinner() tea.Cmd {
	return tea.Tick(350*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

func tickCursor() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg {
		return cursorTickMsg(t)
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
