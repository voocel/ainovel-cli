package tui

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
	"github.com/voocel/ainovel-cli/internal/tools"
)

const maxEvents = 500

type focusPane int

const (
	focusEvents focusPane = iota
	focusStream
	focusDetail
)

type appMode int

const (
	modeNew     appMode = iota // 等待用户输入小说需求
	modeRunning                // 正在创作（包括出错停止，输入可恢复）
	modeDone                   // 创作完成
)

// 顶栏 spinner 帧序列
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Model 是 TUI 的顶层状态。
type Model struct {
	runtime      *orchestrator.Runtime
	askBridge    *askUserBridge
	askState     *askUserState
	cocreate     *cocreateState
	help         *helpState
	modelSwitch  *modelSwitchState
	report       *reportState
	compItems    []commandPaletteItem
	compIdx      int
	compActive   bool
	snapshot     orchestrator.UISnapshot
	events       []orchestrator.UIEvent
	viewport     viewport.Model   // 事件流 viewport
	streamVP     viewport.Model   // 流式输出 viewport
	detailVP     viewport.Model   // 右侧详情 viewport
	streamBuf    *strings.Builder // 流式文本累积缓冲
	streamRounds []string
	textarea     textarea.Model
	width        int
	height       int
	autoScroll   bool
	streamScroll bool // 流式面板自动跟随
	focusPane    focusPane
	hoverPane    focusPane
	hoverActive  bool
	mode         appMode
	startupMode  startupMode
	cocreateSeq  int
	err          error
	spinnerIdx   int
	streamRound  int  // 流式输出轮次计数
	quitPending  bool // 双次 Ctrl+C 退出确认
	abortPending bool // 等待 Done 回来的手动暂停
}

// NewModel 创建 TUI Model。
func NewModel(rt *orchestrator.Runtime, bridge *askUserBridge) Model {
	ta := textarea.New()
	ta.Placeholder = placeholderForNewMode(startupModeQuick)
	ta.CharLimit = 500
	ta.SetHeight(1)
	ta.MaxHeight = 1
	ta.ShowLineNumbers = false
	ta.Focus()

	// Enter 不换行（由 Update 处理提交）
	ta.KeyMap.InsertNewline.SetEnabled(false)

	vp := viewport.New(80, 20)
	vp.SetContent("")

	svp := viewport.New(80, 10)
	svp.SetContent("")

	dvp := viewport.New(40, 20)
	dvp.SetContent("")

	return Model{
		runtime:      rt,
		askBridge:    bridge,
		autoScroll:   true,
		streamScroll: true,
		mode:         modeNew,
		startupMode:  startupModeQuick,
		textarea:     ta,
		viewport:     vp,
		streamVP:     svp,
		detailVP:     dvp,
		streamBuf:    &strings.Builder{},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		listenEvents(m.runtime),
		listenAskUser(m.askBridge),
		listenDone(m.runtime),
		listenStream(m.runtime),
		listenStreamClear(m.runtime),
		tickSnapshot(m.runtime),
		bootstrapRuntime(m.runtime),
		tickSpinner(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.textarea.SetWidth(m.currentInputWidth())
		m.updateViewportSize()
		m.refreshDetailViewport()
		return m, nil

	case tea.KeyMsg:
		if m.askState != nil {
			if msg.Type == tea.KeyCtrlC {
				if m.quitPending {
					return m, tea.Quit
				}
				m.quitPending = true
				return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
			}
			m.quitPending = false
			return m.handleAskUserKey(msg)
		}
		if m.cocreate != nil {
			if msg.Type == tea.KeyCtrlC {
				if m.quitPending {
					return m, tea.Quit
				}
				m.quitPending = true
				return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
			}
			m.quitPending = false
			return m.handleCoCreateKey(msg)
		}
		if m.help != nil {
			if msg.Type == tea.KeyCtrlC {
				if m.quitPending {
					return m, tea.Quit
				}
				m.quitPending = true
				return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
			}
			m.quitPending = false
			return m.handleHelpKey(msg)
		}
		if m.modelSwitch != nil {
			if msg.Type == tea.KeyCtrlC {
				if m.quitPending {
					return m, tea.Quit
				}
				m.quitPending = true
				return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
			}
			m.quitPending = false
			return m.handleModelSwitchKey(msg)
		}
		if m.report != nil {
			if msg.Type == tea.KeyCtrlC {
				if m.quitPending {
					return m, tea.Quit
				}
				m.quitPending = true
				return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
			}
			m.quitPending = false
			return m.handleReportKey(msg)
		}
		// 双次 Ctrl+C 退出
		if msg.Type == tea.KeyCtrlC {
			if m.quitPending {
				return m, tea.Quit
			}
			m.quitPending = true
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
		}
		m.quitPending = false
		if m.compActive {
			switch msg.Type {
			case tea.KeyEsc:
				m.clearCommandPalette()
				return m, nil
			case tea.KeyUp:
				if m.compIdx > 0 {
					m.compIdx--
				}
				return m, nil
			case tea.KeyDown:
				if m.compIdx < len(m.compItems)-1 {
					m.compIdx++
				}
				return m, nil
			case tea.KeyTab:
				m.acceptCommandCompletion()
				return m, nil
			case tea.KeyEnter:
				text := strings.TrimSpace(m.textarea.Value())
				itemCount := len(m.compItems)
				item, ok := m.acceptCommandCompletion()
				if !ok {
					return m, nil
				}
				if item.AutoExecute && (itemCount == 1 || strings.EqualFold(text, "/"+item.Name)) {
					m.textarea.Reset()
					return m.handleSlashCommand(slashCommand{name: item.Name})
				}
				return m, nil
			}
		}
		switch msg.Type {
		case tea.KeyEscape:
			if m.mode == modeRunning && m.snapshot.IsRunning {
				return m, abortRuntime(m.runtime)
			}
			m.textarea.Reset()
			m.clearCommandPalette()
			return m, nil
		case tea.KeyCtrlL:
			m.events = nil
			m.viewport.SetContent("")
			m.viewport.GotoTop()
			m.streamBuf.Reset()
			m.streamRounds = nil
			m.streamVP.SetContent("")
			m.streamVP.GotoTop()
			m.streamRound = 0
			return m, nil
		case tea.KeyTab:
			if m.mode == modeNew {
				if m.cocreate != nil {
					return m, nil
				}
				if m.startupMode == startupModeQuick {
					m.startupMode = startupModeCoCreate
				} else {
					m.startupMode = startupModeQuick
				}
				m.textarea.Placeholder = placeholderForNewMode(m.startupMode)
				return m, nil
			}
			m.focusPane = (m.focusPane + 1) % 3
			return m, nil
		case tea.KeyEnter:
			text := strings.TrimSpace(m.textarea.Value())
			if text == "" {
				return m, nil
			}
			m.clearCommandPalette()
			if cmd, ok := parseSlashCommand(text); ok {
				m.textarea.Reset()
				return m.handleSlashCommand(cmd)
			}
			m.textarea.Reset()
			switch m.mode {
			case modeNew:
				m.err = nil
				if m.startupMode == startupModeQuick {
					return m, startRuntime(m.runtime, text)
				}
				m.cocreate = newCoCreateState(text)
				cmd := m.sendCoCreate()
				return m, cmd
			case modeRunning:
				return m, steerRuntime(m.runtime, text)
			}
			return m, nil
		case tea.KeyUp, tea.KeyPgUp:
			if m.focusPane == focusStream {
				m.streamScroll = false
				var cmd tea.Cmd
				m.streamVP, cmd = m.streamVP.Update(msg)
				return m, cmd
			}
			if m.focusPane == focusDetail {
				var cmd tea.Cmd
				m.detailVP, cmd = m.detailVP.Update(msg)
				return m, cmd
			}
			m.autoScroll = false
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		case tea.KeyDown, tea.KeyPgDown:
			if m.focusPane == focusStream {
				var cmd tea.Cmd
				m.streamVP, cmd = m.streamVP.Update(msg)
				if m.streamVP.AtBottom() {
					m.streamScroll = true
				}
				return m, cmd
			}
			if m.focusPane == focusDetail {
				var cmd tea.Cmd
				m.detailVP, cmd = m.detailVP.Update(msg)
				return m, cmd
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			if m.viewport.AtBottom() {
				m.autoScroll = true
			}
			return m, cmd
		case tea.KeyEnd:
			if m.focusPane == focusStream {
				m.streamScroll = true
				m.streamVP.GotoBottom()
			} else if m.focusPane == focusDetail {
				m.detailVP.GotoBottom()
			} else {
				m.autoScroll = true
				m.viewport.GotoBottom()
			}
			return m, nil
		}
		// 普通字符输入 → 转发给 textarea（过滤终端转义序列残片）
		if msg.Type == tea.KeyRunes && (containsSGRFragment(string(msg.Runes)) || isCSILeak(msg.Runes)) {
			return m, nil
		}
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		m.updateCommandPalette()
		return m, cmd

	case tea.MouseMsg:
		if m.cocreate != nil {
			var cmd tea.Cmd
			m.cocreate.promptVP, cmd = m.cocreate.promptVP.Update(msg)
			return m, cmd
		}
		// 弹窗打开时屏蔽鼠标，避免底层面板高亮乱跳
		if m.modelSwitch != nil || m.askState != nil {
			return m, nil
		}
		if pane, ok := m.paneAtMouse(msg.X, msg.Y); ok {
			m.hoverPane = pane
			m.hoverActive = true
			if msg.Action == tea.MouseActionPress {
				m.focusPane = pane
			}
		} else {
			m.hoverActive = false
		}
		var cmd tea.Cmd
		if m.focusPane == focusStream {
			m.streamVP, cmd = m.streamVP.Update(msg)
			if msg.Action == tea.MouseActionPress {
				m.streamScroll = m.streamVP.AtBottom()
			}
		} else if m.focusPane == focusDetail {
			m.detailVP, cmd = m.detailVP.Update(msg)
		} else {
			m.viewport, cmd = m.viewport.Update(msg)
			if msg.Action == tea.MouseActionPress {
				m.autoScroll = m.viewport.AtBottom()
			}
		}
		return m, cmd

	case eventMsg:
		ev := orchestrator.UIEvent(msg)
		m.events = append(m.events, ev)
		if len(m.events) > maxEvents {
			m.events = m.events[len(m.events)-maxEvents:]
		}
		m.refreshEventViewport()
		return m, listenEvents(m.runtime)

	case askUserMsg:
		m.askState = newAskUserState(askUserRequest(msg))
		m.textarea.Blur()
		m.events = append(m.events, orchestrator.UIEvent{
			Time: time.Now(), Category: "SYSTEM", Summary: "等待用户补充关键信息", Level: "info",
		})
		m.refreshEventViewport()
		return m, listenAskUser(m.askBridge)

	case snapshotMsg:
		m.snapshot = orchestrator.UISnapshot(msg)
		m.refreshDetailViewport()
		return m, tickSnapshot(m.runtime)

	case doneMsg:
		if msg.complete {
			m.abortPending = false
			m.mode = modeDone
			m.textarea.Placeholder = "创作已完成"
			m.textarea.Blur()
		} else {
			if m.abortPending {
				m.textarea.Placeholder = "创作已暂停，输入任意内容继续创作"
				m.abortPending = false
			} else {
				// 出错停止，保持 modeRunning，用户输入任意内容 Steer 即可恢复 agent 循环
				m.textarea.Placeholder = "运行中断，输入任意内容恢复创作"
			}
		}
		return m, tea.Batch(fetchSnapshot(m.runtime), listenDone(m.runtime))

	case abortResultMsg:
		if msg.stopped {
			m.abortPending = true
			m.textarea.Placeholder = "正在暂停创作..."
		}
		return m, nil

	case startResultMsg:
		if msg.err != nil {
			m.err = msg.err
			if m.mode != modeNew {
				m.events = append(m.events, orchestrator.UIEvent{
					Time: time.Now(), Category: "ERROR", Summary: msg.err.Error(), Level: "error",
				})
				m.refreshEventViewport()
			}
			if m.cocreate != nil {
				m.cocreate.awaiting = false
				m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
				return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus())
			}
			if m.mode == modeNew {
				m.textarea.Placeholder = placeholderForNewMode(m.startupMode)
				return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus())
			}
		} else if m.mode == modeNew {
			m.cocreate = nil
			m.mode = modeRunning
			m.textarea.SetWidth(m.currentInputWidth())
			m.textarea.Placeholder = "输入剧情干预，例如：把感情线提前到第4章"
			return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus())
		}
		return m, fetchSnapshot(m.runtime)

	case cocreateDeltaMsg:
		if m.cocreate == nil || msg.reqID != m.cocreate.reqID {
			return m, nil
		}
		m.cocreate.applyDelta(msg.text)
		return m, listenCoCreateDelta(m.cocreate)

	case cocreateDoneMsg:
		if m.cocreate == nil || msg.reqID != m.cocreate.reqID {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			m.cocreate.awaiting = false
			m.cocreate.streamReply = ""
			m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
			return m, m.textarea.Focus()
		}
		m.err = nil
		m.cocreate.apply(msg.reply)
		m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
		return m, m.textarea.Focus()

	case steerResultMsg:
		return m, tea.Batch(fetchSnapshot(m.runtime), listenDone(m.runtime))

	case spinnerTickMsg:
		m.spinnerIdx = (m.spinnerIdx + 1) % len(spinnerFrames)
		if m.snapshot.IsRunning {
			m.refreshEventViewport()
		}
		return m, tickSpinner()

	case streamDeltaMsg:
		if len(m.streamRounds) == 0 {
			m.streamRounds = append(m.streamRounds, "")
		}
		m.streamRounds[len(m.streamRounds)-1] += string(msg)
		m.streamVP.SetContent(renderStreamContent(m.streamRounds, m.streamVP.Width))
		if m.streamScroll {
			m.streamVP.GotoBottom()
		}
		return m, listenStream(m.runtime)

	case streamClearMsg:
		// 新一轮输出：按轮次分块显示，避免长文本和分隔线直接拼接导致错乱。
		if len(m.streamRounds) == 0 {
			m.streamRounds = append(m.streamRounds, "")
		} else if strings.TrimSpace(m.streamRounds[len(m.streamRounds)-1]) != "" {
			m.streamRounds = append(m.streamRounds, "")
		}
		m.streamRound = len(m.streamRounds)
		m.streamVP.SetContent(renderStreamContent(m.streamRounds, m.streamVP.Width))
		if m.streamScroll {
			m.streamVP.GotoBottom()
		}
		return m, listenStreamClear(m.runtime)

	case quitResetMsg:
		m.quitPending = false
		return m, nil
	}

	// 非键盘消息（光标闪烁等 textarea 内部消息）转发
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.updateCommandPalette()
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m *Model) paneAtMouse(x, y int) (focusPane, bool) {
	if m.width == 0 || m.height == 0 {
		return focusEvents, false
	}

	topH := lipgloss.Height(renderTopBar(m.snapshot, m.width, ""))
	inputH := lipgloss.Height(renderInputBox(m.textarea.View(), m.inputHints(), m.snapshot, m.runtime.Dir(), m.width))
	bodyH := m.height - topH - inputH
	if bodyH < 1 {
		return focusEvents, false
	}

	bodyStartY := topH
	bodyEndY := topH + bodyH
	if y < bodyStartY || y >= bodyEndY {
		return focusEvents, false
	}

	leftW := m.width * 25 / 100
	rightW := m.detailWidth()
	centerStartX := leftW
	rightStartX := m.width - rightW

	if x >= rightStartX {
		return focusDetail, true
	}
	if x < centerStartX {
		return focusEvents, true
	}

	eventH, _ := m.splitHeights(bodyH)
	if y-bodyStartY < eventH {
		return focusEvents, true
	}
	return focusStream, true
}

func (m *Model) paneHighlighted(pane focusPane) bool {
	if m.focusPane == pane {
		return true
	}
	return m.hoverActive && m.hoverPane == pane
}

// refreshEventViewport 重新渲染事件流内容并设置 viewport。
func (m *Model) refreshEventViewport() {
	centerW := m.eventFlowWidth()
	content := renderEventContent(m.events, centerW)
	if m.snapshot.IsRunning {
		content += renderSparkle(m.spinnerIdx)
	}
	m.viewport.SetContent(content)
	if m.autoScroll {
		m.viewport.GotoBottom()
	}
}

func (m *Model) refreshDetailViewport() {
	rightW := m.detailWidth()
	if rightW <= 4 {
		return
	}
	m.detailVP.SetContent(renderDetailContent(m.snapshot, rightW-4))
}

// updateViewportSize 根据当前窗口尺寸更新 viewport 大小。
func (m *Model) updateViewportSize() {
	centerW := m.eventFlowWidth()
	rightW := m.detailWidth()
	bodyH := m.bodyHeight()
	eventH, streamH := m.splitHeights(bodyH)
	m.viewport.Width = centerW - 2
	m.viewport.Height = eventH - 1 // -1 为 event panel header 行
	m.streamVP.Width = centerW - 2
	m.streamVP.Height = streamH - 1 // -1 为 stream panel header 行
	m.detailVP.Width = rightW - 2
	m.detailVP.Height = bodyH
}

// splitHeights 计算事件流和流式输出的高度分配。
func (m *Model) splitHeights(bodyH int) (eventH, streamH int) {
	eventH = bodyH * 40 / 100
	if eventH < 3 {
		eventH = 3
	}
	streamH = bodyH - eventH - 1 // -1 为分隔线
	if streamH < 3 {
		streamH = 3
	}
	return
}

func (m *Model) inputWidth() int {
	if m.width == 0 {
		return 60
	}
	return m.width - 6 // border + padding + 提示符 "❯ "
}

func (m *Model) currentInputWidth() int {
	if m.cocreate != nil {
		return coCreateInputWidth(m.width, m.height)
	}
	return m.inputWidth()
}

// inputHints 根据当前状态生成底部提示文本。
func (m *Model) inputHints() string {
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	if m.quitPending {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Bold(true).Render("Press Ctrl+C again to exit")
	}
	if m.cocreate != nil {
		switch {
		case m.cocreate.awaiting:
			return dimStyle.Render("等待 AI 回复 · Esc 退出共创")
		case m.cocreate.canStart():
			return dimStyle.Render("Enter 发送 · Ctrl+S 开始创作 · Esc 退出共创")
		default:
			return dimStyle.Render("Enter 发送 · Esc 退出共创")
		}
	}
	if m.mode == modeNew {
		if m.startupMode == startupModeQuick {
			return dimStyle.Render("Tab 切换启动模式 · 输入 / 搜索命令 · Enter 直接开始创作 · Esc 清空输入")
		}
		return dimStyle.Render("Tab 切换启动模式 · 输入 / 搜索命令 · Enter 开始共创对话 · Esc 清空输入")
	}
	return dimStyle.Render("输入 / 搜索命令 · 点击/Tab 切换面板 · ↑↓ 滚动 · End 跳底 · ^L 清屏 · Esc 暂停 · Enter 发送")
}

func (m *Model) eventFlowWidth() int {
	if m.width == 0 {
		return 80
	}
	leftW := m.width * 25 / 100
	rightW := m.detailWidth()
	return m.width - leftW - rightW
}

func (m *Model) detailWidth() int {
	if m.width == 0 {
		return 40
	}
	return m.width * 30 / 100
}

func (m *Model) bodyHeight() int {
	if m.height == 0 {
		return 20
	}
	topH := 1
	inputH := 6
	bodyH := m.height - topH - inputH
	if bodyH < 3 {
		bodyH = 3
	}
	return bodyH
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "加载中..."
	}
	if m.width < 100 {
		return lipgloss.NewStyle().
			Width(m.width).Height(m.height).
			AlignHorizontal(lipgloss.Center).
			AlignVertical(lipgloss.Center).
			Render("终端宽度不足，请至少扩展到 100 列")
	}
	if m.askState != nil {
		return renderAskUserModal(m.width, m.height, m.askState)
	}
	if m.cocreate != nil {
		return renderCoCreateModal(m.width, m.height, m.cocreate, errorText(m.err), m.textarea.View())
	}
	if m.help != nil {
		return renderHelpModal(m.width, m.height, m.help)
	}
	if m.report != nil {
		return renderReportModal(m.width, m.height, m.report)
	}

	spinnerFrame := ""
	if m.snapshot.IsRunning {
		spinnerFrame = spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
	}

	topBar := renderTopBar(m.snapshot, m.width, spinnerFrame)
	inputBox := renderInputBox(m.textarea.View(), m.inputHints(), m.snapshot, m.runtime.Dir(), m.width)
	if m.mode == modeNew && m.cocreate == nil {
		inputBox = renderStartupModeBar(m.width, m.startupMode) + "\n" + inputBox
	}

	topH := lipgloss.Height(topBar)
	inputH := lipgloss.Height(inputBox)
	bodyH := m.height - topH - inputH
	if bodyH < 3 {
		bodyH = 3
	}

	var body string
	if m.mode == modeNew && len(m.events) == 0 {
		errMsg := ""
		if m.err != nil {
			errMsg = m.err.Error()
		}
		body = renderWelcome(m.width, bodyH, errMsg, m.startupMode)
	} else {
		leftW := m.width * 25 / 100
		rightW := m.detailWidth()
		centerW := m.width - leftW - rightW
		eventH, streamH := m.splitHeights(bodyH)

		if m.viewport.Width != centerW-2 || m.viewport.Height != eventH-1 {
			m.viewport.Width = centerW - 2
			m.viewport.Height = eventH - 1 // -1 为 event panel header 行
		}
		if m.streamVP.Width != centerW-2 || m.streamVP.Height != streamH-1 {
			m.streamVP.Width = centerW - 2
			m.streamVP.Height = streamH - 1 // -1 为 stream panel header 行
		}

		eventFlow := renderEventFlowViewport(m.viewport, centerW, eventH, m.paneHighlighted(focusEvents))
		streamPanel := renderStreamPanel(m.streamVP, centerW, streamH, m.paneHighlighted(focusStream))
		center := lipgloss.JoinVertical(lipgloss.Left, eventFlow, streamPanel)

		left := renderStatePanel(m.snapshot, leftW, bodyH)
		right := renderDetailPanel(m.detailVP, rightW, bodyH, m.paneHighlighted(focusDetail))
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, center, right)
	}

	view := lipgloss.JoinVertical(lipgloss.Left, topBar, body, inputBox)

	// 弹窗覆盖叠加：浮在 body 底部上方，不影响布局
	if m.modelSwitch != nil {
		commandBar := renderModelSwitchBar(m.width, m.modelSwitch)
		view = overlayAboveInput(view, commandBar, inputH)
	} else if m.compActive {
		commandBar := renderCommandPalette(m.width, m.compItems, m.compIdx)
		view = overlayAboveInput(view, commandBar, inputH)
	}
	return view
}

// sendCoCreate 发起一轮共创请求，统一处理 reqID、textarea、placeholder。
func (m *Model) sendCoCreate() tea.Cmd {
	m.cocreateSeq++
	m.cocreate.reqID = m.cocreateSeq
	m.cocreate.awaiting = true
	m.cocreate.streamReply = ""
	m.textarea.SetWidth(m.currentInputWidth())
	m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
	m.textarea.Blur()
	return runCoCreate(m.runtime, m.cocreate)
}

func (m Model) handleCoCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.cocreate == nil {
		return m, nil
	}
	state := m.cocreate

	// 右侧指令面板滚动
	switch msg.Type {
	case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		state.promptVP, cmd = state.promptVP.Update(msg)
		return m, cmd
	case tea.KeyHome:
		state.promptVP.GotoTop()
		return m, nil
	case tea.KeyEnd:
		state.promptVP.GotoBottom()
		return m, nil
	case tea.KeyEsc:
		return m.exitCoCreate()
	}

	// 等待 AI 回复时只允许 Esc 退出（上面已处理）
	if state.awaiting {
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlS:
		if state.canStart() {
			state.awaiting = true
			m.textarea.Blur()
			return m, startRuntime(m.runtime, state.draftPrompt)
		}
		return m, nil
	case tea.KeyEnter:
		text := strings.TrimSpace(m.textarea.Value())
		if text == "" {
			return m, nil
		}
		m.err = nil
		state.appendUser(text)
		m.textarea.Reset()
		cmd := m.sendCoCreate()
		return m, cmd
	}

	// 常规输入转发给 textarea
	if msg.Type == tea.KeyRunes && (containsSGRFragment(string(msg.Runes)) || isCSILeak(msg.Runes)) {
		return m, nil
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

// exitCoCreate 退出共创模式，取消进行中的 LLM 请求，恢复输入框状态。
func (m Model) exitCoCreate() (tea.Model, tea.Cmd) {
	if m.cocreate.cancel != nil {
		m.cocreate.cancel()
	}
	initial := m.cocreate.initialInput()
	m.cocreate = nil
	m.textarea.SetWidth(m.currentInputWidth())
	m.textarea.SetValue(initial)
	m.textarea.Placeholder = placeholderForNewMode(m.startupMode)
	return m, m.textarea.Focus()
}

func (m Model) handleAskUserKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.askState == nil {
		return m, nil
	}
	state := m.askState
	q := state.currentQuestion()

	if state.typing {
		switch msg.Type {
		case tea.KeyEsc:
			state.cancelCurrentTyping()
			return m, nil
		case tea.KeyEnter:
			if state.finishCurrentAnswer() {
				state.submit()
				m.askState = nil
				if m.mode != modeDone {
					return m, m.textarea.Focus()
				}
			}
			return m, nil
		case tea.KeyBackspace, tea.KeyCtrlH:
			if state.input != "" {
				_, size := utf8.DecodeLastRuneInString(state.input)
				state.input = state.input[:len(state.input)-size]
			}
			return m, nil
		default:
			if msg.Type == tea.KeyRunes {
				state.input += string(msg.Runes)
			}
			return m, nil
		}
	}

	switch msg.Type {
	case tea.KeyEsc:
		// 关闭弹窗，返回空答案
		state.request.resultCh <- askUserResult{
			resp: &tools.AskUserResponse{
				Answers: make(map[string]string),
				Notes:   make(map[string]string),
			},
		}
		m.askState = nil
		if m.mode != modeDone {
			return m, m.textarea.Focus()
		}
		return m, nil
	case tea.KeyUp:
		state.moveCursor(-1)
	case tea.KeyDown:
		state.moveCursor(1)
	case tea.KeySpace:
		if q.MultiSelect {
			state.toggleSelection()
			if state.cursor == len(q.Options) && !state.selected[state.cursor] {
				state.input = ""
			}
		}
	case tea.KeyEnter:
		if q.MultiSelect {
			if state.cursor == len(q.Options) {
				state.toggleSelection()
				if state.selected[state.cursor] {
					state.typing = true
				}
				return m, nil
			}
			if len(state.selected) == 0 {
				state.toggleSelection()
			}
		}
		if state.finishCurrentAnswer() {
			state.submit()
			m.askState = nil
			if m.mode != modeDone {
				return m, m.textarea.Focus()
			}
		}
	}
	return m, nil
}

// overlayAboveInput 将 overlay 浮动叠加在 base 视图的底部（inputBox 上方），
// 不改变整体布局高度。仅覆盖 overlay 卡片自身宽度，右侧透出底层内容。
func overlayAboveInput(base, overlay string, inputLineCount int) string {
	baseLines := strings.Split(base, "\n")
	overLines := strings.Split(strings.TrimRight(overlay, "\n"), "\n")

	endY := len(baseLines) - inputLineCount
	startY := endY - len(overLines)
	if startY < 0 {
		startY = 0
	}

	for i, ol := range overLines {
		y := startY + i
		if y >= 0 && y < endY {
			olW := lipgloss.Width(ol)
			// 截掉基线左侧 olW 个可见字符，拼接 overlay + 剩余右侧内容
			right := ansi.TruncateLeft(baseLines[y], olW, "")
			baseLines[y] = ol + right
		}
	}
	return strings.Join(baseLines, "\n")
}

// isCSILeak 检测 KeyRunes 是否为 CSI 转义序列泄漏的残片。
// 终端发送方向键 \x1b[A 时，快速按键可能导致序列拆分：
// \x1b 被解析为 Escape，"[" 或 "[A" 作为 KeyRunes 泄漏到 textarea。
func isCSILeak(runes []rune) bool {
	if len(runes) == 0 || runes[0] != '[' {
		return false
	}
	for _, r := range runes[1:] {
		if (r >= '0' && r <= '9') || r == ';' ||
			(r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' {
			continue
		}
		return false
	}
	return true
}

// containsSGRFragment 检测文本是否包含 SGR 鼠标序列残片（"<数字;数字;" 模式）。
func containsSGRFragment(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		j := i + 1
		if j >= len(s) || s[j] < '0' || s[j] > '9' {
			continue
		}
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j < len(s) && s[j] == ';' {
			return true
		}
	}
	return false
}
