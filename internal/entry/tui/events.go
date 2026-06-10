package tui

import (
	"context"
	"time"

	"github.com/Accelerator-mzq/ainovel-cli/internal/diag"
	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/entry/startup"
	"github.com/Accelerator-mzq/ainovel-cli/internal/host"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

// 消息类型
type (
	eventMsg       host.Event
	snapshotMsg    host.UISnapshot
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
		exportPath string // 脱敏诊断文件绝对路径；空 = 导出失败
		finishedAt time.Time
	}
	askUserMsg       askUserRequest
	startResultMsg   struct{ err error }
	cocreateDeltaMsg struct {
		reqID int
		kind  string // host.CoCreateProgressThinking | host.CoCreateProgressReply
		text  string
	}
	// cocreateStreamItem 是 deltaCh 内部载荷，把流式 kind 与累积文本一起送达 TUI。
	cocreateStreamItem struct {
		kind string
		text string
	}
	cocreateDoneMsg struct {
		reqID int
		reply host.CoCreateReply
		err   error
	}
	steerResultMsg     struct{}
	continueResultMsg  struct{ err error }
	spinnerTickMsg     time.Time
	toolSpinnerTickMsg time.Time // 事件流工具 spinner 独立 tick（更快、独立于顶栏/星星）
	cursorTickMsg      time.Time // 流式光标独立 tick
	streamDeltaMsg     string    // 流式 token 增量
	streamClearMsg     struct{}  // 清空流式缓冲（新消息开始）
	streamFlushTickMsg struct{}  // 60fps 节流刷新流式面板（合并 token 级 delta）
	quitResetMsg       struct{}  // 双次 Ctrl+C 超时重置
)

// --- Cmd 函数 ---

func listenEvents(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-rt.Events()
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func listenDone(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		_, ok := <-rt.Done()
		if !ok {
			return nil
		}
		snap := rt.Snapshot()
		return doneMsg{complete: snap.Phase == "complete"}
	}
}

func tickSnapshot(rt *host.Host) tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return snapshotMsg(rt.Snapshot())
	})
}

func fetchSnapshot(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		return snapshotMsg(rt.Snapshot())
	}
}

func bootstrapRuntime(rt *host.Host) tea.Cmd {
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

func startRuntime(rt *host.Host, plan startup.Plan) tea.Cmd {
	return func() tea.Msg {
		err := rt.StartPrepared(plan.StartPrompt)
		return startResultMsg{err: err}
	}
}

func runCoCreate(rt *host.Host, state *cocreateState) tea.Cmd {
	history := state.session.History()
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	state.deltaCh = make(chan cocreateStreamItem, 64)
	state.doneCh = make(chan cocreateDoneMsg, 1)
	start := func() tea.Msg {
		go func() {
			reply, err := rt.CoCreateStream(ctx, history, func(kind, text string) {
				select {
				case state.deltaCh <- cocreateStreamItem{kind: kind, text: text}:
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
	// 抓取 channel 局部引用：避免后续 state.deltaCh 被 reassign 时
	// 旧 listen 闭包错读新 channel（虽然当前流程不触发，留作维护陷阱不应该）。
	reqID := state.reqID
	ch := state.deltaCh
	return func() tea.Msg {
		item, ok := <-ch
		if !ok {
			return nil
		}
		return cocreateDeltaMsg{reqID: reqID, kind: item.kind, text: item.text}
	}
}

func listenCoCreateDone(state *cocreateState) tea.Cmd {
	if state == nil || state.doneCh == nil {
		return nil
	}
	reqID := state.reqID
	ch := state.doneCh
	return func() tea.Msg {
		result, ok := <-ch
		if !ok {
			return nil
		}
		result.reqID = reqID
		return result
	}
}

func steerRuntime(rt *host.Host, text string) tea.Cmd {
	return func() tea.Msg {
		rt.Steer(text)
		return steerResultMsg{}
	}
}

func continueRuntime(rt *host.Host, text string) tea.Cmd {
	return func() tea.Msg {
		err := rt.Continue(text)
		return continueResultMsg{err: err}
	}
}

func abortRuntime(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		return abortResultMsg{stopped: rt.Abort()}
	}
}

func loadReport(dir string, reqID int) tea.Cmd {
	return func() tea.Msg {
		s := store.NewStore(dir)
		// Diagnose = 创作诊断 + 运行时检测，运行时 Finding 也进屏上报告。
		rep, rc := diag.Diagnose(s)
		// 复用 rep+rc 写出脱敏诊断文件（导出失败不影响屏上报告）。
		exportPath, _ := diag.WriteExport(s, rep, rc)
		return reportLoadedMsg{
			reqID:      reqID,
			report:     rep,
			exportPath: exportPath,
			finishedAt: time.Now(),
		}
	}
}

func tickSpinner() tea.Cmd {
	return tea.Tick(350*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

// tickToolSpinner 驱动事件流"进行中"行的 spinner。独立于 tickSpinner，节奏更快（150ms）。
func tickToolSpinner() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return toolSpinnerTickMsg(t)
	})
}

func tickCursor() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg {
		return cursorTickMsg(t)
	})
}

// tickStreamFlush 驱动流式面板节流刷新。streamDelta 不再每个 token 立即重渲，
// 而是 mark dirty；本 tick 每 16ms（~60fps）检查并合并刷新一次，把 LLM 高速流式
// 期的"每秒数十次全量重渲"压回 60 次上限。
func tickStreamFlush() tea.Cmd {
	return tea.Tick(16*time.Millisecond, func(t time.Time) tea.Msg {
		return streamFlushTickMsg{}
	})
}

func listenStream(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		delta, ok := <-rt.Stream()
		if !ok {
			return nil
		}
		// sentinel 派发为 streamClearMsg，保证与正常 delta 在同一通道里按 emit
		// 顺序到达 TUI。双通道时 clearCh 与 streamCh 之间无序，✻ header 经常被
		// 错塞到上一段 thinking 末尾。
		if delta == host.StreamClearSentinel {
			return streamClearMsg{}
		}
		return streamDeltaMsg(delta)
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
