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
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/tools"
	"github.com/voocel/ainovel-cli/internal/utils"
)

const maxEvents = 500

// maxStreamRounds 限制流式面板保留的轮次数。每个 LLM call Kết thúc触发一次 streamClear
// 开Mới轮，单章 writer 约 3~5 轮（agent header / 思考 / draft / commit），32 轮约等于
// 回看最近 6~10 章的流式输出。已 commit 的ChươngChính văn落盘在 store/drafts，超出即丢以免
// 每个 token delta 触发 O(全文) 重渲染。稳态内存上限约 512KB，远低于卡顿阈值。
const maxStreamRounds = 32

type focusPane int

const (
	focusEvents focusPane = iota
	focusStream
	focusDetail
	focusState // 左侧Trạng thái侧栏（可滚动）

	focusPaneCount // 焦点总数，Tab 轮转用
)

type appMode int

const (
	modeNew     appMode = iota // Chờ người dùngNhập小说需求
	modeRunning                // Đang创作（Bao gồm出错停止，Nhập可Phục hồi）
	modeDone                   // 创作Hoàn thành
)

// 顶栏 / 流式活动共用的 spinner 帧序列（bubbles.Spinner.MiniDot）。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// 事件流"Đang thực hiện"行专用的 spinner 帧序列（bubbles.Spinner.Dot）。
// 7 个点 + 1 个缺口沿 3×3 格子顺时针旋转，视觉上像完整的加载圆圈。
// 用独立帧索引 + 更快 tick，不影响顶栏和星星动画的节奏。
var toolSpinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// Model 是 TUI 的顶层Trạng thái。
type Model struct {
	runtime        *host.Host
	askBridge      *askUserBridge
	askState       *askUserState
	cocreate       *cocreateState
	help           *helpState
	modelSwitch    *modelSwitchState
	report         *reportState
	version        string
	importer       *importState
	importSeq      int
	simulator      *simulationState
	simSeq         int
	compItems      []commandPaletteItem
	compIdx        int
	compActive     bool
	snapshot       host.UISnapshot
	events         []host.Event
	eventIndex     map[string]int   // event.ID → m.events 下标；调用类事件到达时原地更Mới
	viewport       viewport.Model   // 事件流 viewport
	streamVP       viewport.Model   // 流式输出 viewport
	detailVP       viewport.Model   // 右侧详情 viewport
	stateVP        viewport.Model   // 左侧Trạng thái侧栏 viewport（可滚动）
	streamBuf      *strings.Builder // 流式文本累积缓冲
	streamRounds   []string
	textarea       textarea.Model
	width          int
	height         int
	autoScroll     bool
	streamScroll   bool      // 流式面板自动跟随
	streamDirty    bool      // streamRounds 有未Làm mới的 delta；由 streamFlushTick 60fps 合并
	lastKeyAt      time.Time // 上次非 Enter 按键时间；KeyEnter 节流防粘贴 \n 流误触发Nộp
	inputHistory   []string  // 已Nộp的NhậpLịch sử（去重：相邻不重复）
	historyIdx     int       // Hiện tại浏览索引；== len(inputHistory) 表示"未浏览，Đang sửaBản nháp"
	historyDraft   string    // 进入Lịch sử浏览前Lưu的Bản nháp，回到末端时Phục hồi
	focusPane      focusPane
	hoverPane      focusPane
	hoverActive    bool
	mode           appMode
	startupMode    startupMode
	cocreateSeq    int
	reportSeq      int
	err            error
	spinnerIdx     int
	toolSpinnerIdx int  // 事件流Đang thực hiện行的独立帧索引（150ms tick，不影响顶栏/星星）
	cursorIdx      int  // 流式光标帧索引（独立 tick）
	streamRound    int  // 流式输出轮次计数
	quitPending    bool // 双次 Ctrl+C ThoátXác nhận
	abortPending   bool // 等待 Done 回来的手动Tạm dừng
	mouseOff       bool // true 时已Tắt鼠标上报，让用户原生拖拽选中复制；再次切换Phục hồi
}

// NewModel Tạo TUI Model。
func NewModel(rt *host.Host, bridge *askUserBridge, version string) Model {
	ta := textarea.New()
	ta.Placeholder = placeholderForNewMode(startupModeQuick)
	ta.CharLimit = 2000
	ta.SetHeight(1)
	// MaxHeight=6 让超长Nhập按宽度自动 wrap 显示成多行（视觉上限 6 行）。
	ta.MaxHeight = 6
	ta.ShowLineNumbers = false
	ta.Focus()

	// Mặc định Enter 不换行（由 handleEnterKey Nộp）；
	// 主动换行重绑到 ctrl+j（unix \n）和 alt+enter（GUI 习惯）。
	// 终端协议层Không thể区分 Shift+Enter 与 Enter，所以Không hỗ trợ Shift+Enter。
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j", "alt+enter")

	vp := viewport.New(80, 20)
	vp.SetContent("")

	svp := viewport.New(80, 10)
	svp.SetContent("")

	dvp := viewport.New(40, 20)
	dvp.SetContent("")

	stvp := viewport.New(32, 20)
	stvp.SetContent("")

	return Model{
		runtime:      rt,
		askBridge:    bridge,
		version:      strings.TrimSpace(version),
		autoScroll:   true,
		streamScroll: true,
		mode:         modeNew,
		startupMode:  startupModeQuick,
		textarea:     ta,
		viewport:     vp,
		streamVP:     svp,
		detailVP:     dvp,
		stateVP:      stvp,
		streamBuf:    &strings.Builder{},
		eventIndex:   make(map[string]int),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		listenEvents(m.runtime),
		listenAskUser(m.askBridge),
		listenDone(m.runtime),
		listenStream(m.runtime),
		tickSnapshot(m.runtime),
		bootstrapRuntime(m.runtime),
		tickSpinner(),
		tickToolSpinner(),
		tickCursor(),
		tickStreamFlush(),
	)
}

func (m *Model) paneAtMouse(x, y int) (focusPane, bool) {
	if m.width == 0 || m.height == 0 {
		return focusEvents, false
	}

	topH, _, bodyH := m.layoutHeights()
	if bodyH < 1 {
		return focusEvents, false
	}

	bodyStartY := topH
	bodyEndY := topH + bodyH
	if y < bodyStartY || y >= bodyEndY {
		return focusEvents, false
	}

	leftW := m.sidebarWidth()
	rightW := m.detailWidth()
	centerStartX := leftW
	rightStartX := m.width - rightW

	if x >= rightStartX {
		return focusDetail, true
	}
	if x < centerStartX {
		return focusState, true
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

// hasRunningEvent Có czy không存在未Hoàn thành（spinner 仍在转）的调用类事件。
// toolSpinnerTick 用此判断Có czy không值得重渲：没有 running 事件时 spinner 帧不影响输出，
// 整个 refreshEventViewport 是确定的Không có效工作。
func (m *Model) hasRunningEvent() bool {
	for i := range m.events {
		if m.events[i].Running() {
			return true
		}
	}
	return false
}

// flushStreamIfDirty 将累积的 streamRounds 渲染到 viewport；mark 为已刷。
// Quay lạiCó czy không真正刷了，便于调用方决定要不要 GotoBottom。
func (m *Model) flushStreamIfDirty() bool {
	if !m.streamDirty {
		return false
	}
	m.refreshStreamViewport()
	m.streamDirty = false
	return true
}

// refreshEventViewport 重Mới渲染事件流内容并Thiết lập viewport。
func (m *Model) refreshEventViewport() {
	centerW := m.eventFlowWidth()
	content := renderEventContent(m.events, centerW, m.toolSpinnerIdx)
	if activity := renderEventActivity(m.snapshot, m.spinnerIdx, centerW); activity != "" {
		if strings.TrimSpace(content) != "" {
			content += "\n" + activity
		} else {
			content = activity
		}
	}
	m.viewport.SetContent(content)
	if m.autoScroll {
		m.viewport.GotoBottom()
	}
}

func (m *Model) refreshStreamViewport() {
	cursor := ""
	if m.snapshot.IsRunning {
		cursor = renderStreamCursor(m.cursorIdx)
	}
	m.streamVP.SetContent(renderStreamContent(m.streamRounds, m.streamVP.Width, cursor))
}

func (m *Model) refreshDetailViewport() {
	rightW := m.detailWidth()
	if rightW <= 4 {
		return
	}
	m.detailVP.SetContent(renderDetailContent(m.snapshot, rightW-4))
}

// refreshStateViewport 把左侧Trạng thái侧栏内容刷进 viewport。
// 侧栏内容纯由 snapshot 派生，故Chụp或尺寸变化时都要重刷。
func (m *Model) refreshStateViewport() {
	leftW := m.sidebarWidth()
	if leftW <= 4 {
		return
	}
	m.stateVP.SetContent(renderStateContent(m.snapshot, leftW-4))
}

// updateViewportSize 根据Hiện tạiCửa sổ尺寸更Mới viewport 大小。
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
	leftW := m.sidebarWidth()
	m.stateVP.Width = max(1, leftW-2)
	m.stateVP.Height = max(1, bodyH-2) // -2 为Trạng thái栏 Padding(1,1) 的上下留白
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

// refitTextareaHeight 按Hiện tại内容估算视觉行数，动态 SetHeight。
// 视觉行 = 逻辑行（\n 切分）每段按宽度 wrap 后的总和。配合 MaxHeight=6
// 实现"超长内容/主动换行自动多行展示，最多 6 行"。
func (m *Model) refitTextareaHeight() {
	w := m.textarea.Width()
	if w <= 0 {
		return
	}
	// 共创模式下 input 固定 1 行：textarea 多行内容会被 textarea 自身按光标
	// 滚动展示。否则 inputBox 高度跟着内容变，会让左栏 conversation 收缩、
	// input 在垂直方向漂移，破坏布局稳定性。
	if m.cocreate != nil {
		m.textarea.SetHeight(1)
		return
	}
	text := m.textarea.Value()
	if text == "" {
		m.textarea.SetHeight(1)
		return
	}
	// 扣 2 列冗余（textarea 内部 prompt symbol + cursor），偏多 1 行可接受。
	contentW := w - 2
	if contentW < 1 {
		contentW = 1
	}
	total := 0
	for line := range strings.SplitSeq(text, "\n") {
		lw := lipgloss.Width(line)
		if lw == 0 {
			total++
			continue
		}
		total += (lw + contentW - 1) / contentW
	}
	if total < 1 {
		total = 1
	}
	m.textarea.SetHeight(total) // SetHeight 内部按 MaxHeight clamp
}

// resizeTextarea 同步Thiết lập宽度与基于内容的高度。
// 替代散落各处的 SetWidth(currentInputWidth()) 调用，保证宽度变化时高度跟随。
func (m *Model) resizeTextarea() {
	m.textarea.SetWidth(m.currentInputWidth())
	m.refitTextareaHeight()
}

// maxInputHistory 限制Lịch sử长度，避免长会话内存增长。
const maxInputHistory = 200

// pushInputHistory 把Thành côngNộp的内容追加到Lịch sử，相邻去重。同步Đặt lại浏览索引。
func (m *Model) pushInputHistory(text string) {
	if text == "" {
		return
	}
	if n := len(m.inputHistory); n == 0 || m.inputHistory[n-1] != text {
		m.inputHistory = append(m.inputHistory, text)
		if len(m.inputHistory) > maxInputHistory {
			m.inputHistory = m.inputHistory[len(m.inputHistory)-maxInputHistory:]
		}
	}
	m.historyIdx = len(m.inputHistory)
	m.historyDraft = ""
}

// tryHistoryUp 向更早一条Lịch sử走；Quay lạiCó czy không处理了按键。
// 首次进入Lịch sử浏览时把Hiện tại textarea 内容存为 draft，回到末端时Phục hồi。
// 调用方需自行判断多行Cảnh下Có czy không应该绕开（让 textarea 处理光标行内移动）。
func (m *Model) tryHistoryUp() bool {
	if len(m.inputHistory) == 0 || m.historyIdx <= 0 {
		return false
	}
	if m.historyIdx == len(m.inputHistory) {
		m.historyDraft = m.textarea.Value()
	}
	m.historyIdx--
	m.textarea.SetValue(m.inputHistory[m.historyIdx])
	m.textarea.CursorEnd()
	m.refitTextareaHeight()
	return true
}

// tryHistoryDown 向更Mới一条Lịch sử走；走到末端Phục hồi draft。
func (m *Model) tryHistoryDown() bool {
	if m.historyIdx >= len(m.inputHistory) {
		return false
	}
	m.historyIdx++
	if m.historyIdx == len(m.inputHistory) {
		m.textarea.SetValue(m.historyDraft)
		m.historyDraft = ""
	} else {
		m.textarea.SetValue(m.inputHistory[m.historyIdx])
	}
	m.textarea.CursorEnd()
	m.refitTextareaHeight()
	return true
}

// textareaIsMultiline Hiện tại textarea 内容Có czy không含主动换行；用于决定 ↑↓ 是走Lịch sử还是行内移动。
func (m *Model) textareaIsMultiline() bool {
	return strings.Contains(m.textarea.Value(), "\n")
}

// inputHints 根据Hiện tạiTrạng thái生成底部提示文本。
// 末尾统一追加 copySuffix，让用户在任何非紧急Trạng thái都能看到选中复制方法；
// 鼠标已关时显示醒目红字提示，提醒再次按键Phục hồi鼠标交互。
func (m *Model) inputHints() string {
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	if m.quitPending {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Bold(true).Render("Press Ctrl+C again to exit")
	}
	// 欢迎页(modeNew)不开鼠标上报，终端原生拖拽即可复制，Không có需 Ctrl+R 提示；
	// 工作台才开上报，复制需 Ctrl+R 临时关闭。
	suffix := " · Ctrl+R 切到选中复制模式"
	if m.mode == modeNew {
		suffix = ""
	}
	if m.mouseOff && m.mode != modeNew {
		// 工作台手动切到选中复制：用强调色提示Hiện tại处于"自由拖拽选中"Trạng thái，按 Ctrl+R Phục hồi
		return lipgloss.NewStyle().Foreground(colorAccent).Bold(true).
			Render("✂ 选中复制模式：可拖拽选中文本复制 · Ctrl+R ThoátPhục hồi鼠标交互")
	}
	if m.cocreate != nil {
		scrollHint := " · Tab 滚动:对话"
		if m.cocreate.focusPrompt {
			scrollHint = " · Tab 滚动:创作指令"
		}
		switch {
		case m.cocreate.awaiting:
			return dimStyle.Render("等待 AI 回复 · Esc Thoát共创" + scrollHint + suffix)
		case m.cocreate.canStart():
			startLabel := "Ctrl+S Bắt đầu创作"
			if m.cocreate.stage {
				startLabel = "Ctrl+S Áp dụng并Tiếp tục"
			}
			return dimStyle.Render("Enter 发送 · " + startLabel + " · Esc Thoát共创" + scrollHint + suffix)
		default:
			return dimStyle.Render("Enter 发送 · Esc Thoát共创" + scrollHint + suffix)
		}
	}
	if m.mode == modeNew {
		if m.startupMode == startupModeQuick {
			return dimStyle.Render("Tab 切换启动模式 · Nhập / Tìm kiếm命令 · Enter 直接Bắt đầu创作 · Esc 清RỗngNhập" + suffix)
		}
		return dimStyle.Render("Tab 切换启动模式 · Nhập / Tìm kiếm命令 · Enter Bắt đầu共创对话 · Esc 清RỗngNhập" + suffix)
	}
	switch m.snapshot.RuntimeState {
	case "pausing":
		return dimStyle.Render("ĐangTạm dừng创作 · Vui lòng等待Hiện tại轮次Kết thúc" + suffix)
	case "paused":
		return dimStyle.Render("Nhập / Tìm kiếm命令 · Enter Tiếp tục创作 · Esc 清RỗngNhập" + suffix)
	}
	return dimStyle.Render("Nhập / Tìm kiếm命令 · 点击/Tab 切换面板 · ↑↓ 滚动 · End 跳底 · Ctrl+L 清屏 · Esc Tạm dừng · Enter 发送" + suffix)
}

func (m *Model) eventFlowWidth() int {
	if m.width == 0 {
		return 80
	}
	leftW := m.sidebarWidth()
	rightW := m.detailWidth()
	return m.width - leftW - rightW
}

func (m *Model) sidebarWidth() int {
	if m.width == 0 {
		return 32
	}
	return m.width * 23 / 100
}

func (m *Model) detailWidth() int {
	if m.width == 0 {
		return 40
	}
	return m.width * 27 / 100
}

func (m *Model) bodyHeight() int {
	_, _, bodyH := m.layoutHeights()
	return bodyH
}

func (m *Model) currentSpinnerFrame() string {
	if !m.snapshot.IsRunning {
		return ""
	}
	return spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
}

func (m *Model) outputDir() string {
	if m.runtime == nil {
		return ""
	}
	return m.runtime.Dir()
}

func defaultSteerPlaceholder() string {
	return "Nhập剧情干预，例如：把感情线提前到第4章"
}

func (m *Model) syncRuntimePlaceholder() {
	if m.mode != modeRunning || m.cocreate != nil {
		return
	}
	switch m.snapshot.RuntimeState {
	case "completed":
		m.textarea.Placeholder = "创作Đã hoàn thành"
	case "pausing":
		m.textarea.Placeholder = "ĐangTạm dừng创作..."
	case "paused":
		m.textarea.Placeholder = "创作已Tạm dừng，Nhập任意内容Tiếp tục创作"
	default:
		if !m.snapshot.IsRunning {
			m.textarea.Placeholder = "Đang chạy断，Nhập任意内容Phục hồi创作"
		} else {
			m.textarea.Placeholder = defaultSteerPlaceholder()
		}
	}
}

func (m *Model) renderBottomBar() string {
	inputBox := renderInputBox(
		m.textarea.View(),
		m.inputHints(),
		m.snapshot,
		m.outputDir(),
		m.width,
	)
	if m.mode != modeNew || m.cocreate != nil {
		return inputBox
	}
	return renderStartupModeBar(m.width, m.startupMode) + "\n" + inputBox
}

func (m *Model) layoutHeights() (topH, inputH, bodyH int) {
	if m.width == 0 || m.height == 0 {
		return 1, 4, 20
	}
	topH = lipgloss.Height(renderTopBar(m.snapshot, m.width, m.currentSpinnerFrame(), m.version))
	inputH = lipgloss.Height(m.renderBottomBar())
	bodyH = m.height - topH - inputH
	if bodyH < 3 {
		bodyH = 3
	}
	return
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
			Render("终端宽度不足，Vui lòng至少扩展到 100 列")
	}
	if m.askState != nil {
		return renderAskUserModal(m.width, m.height, m.askState)
	}
	if m.cocreate != nil {
		return renderCoCreateModal(m.width, m.height, m.cocreate, errorText(m.err), m.textarea.View(), m.spinnerIdx, m.quitPending)
	}
	if m.help != nil {
		return renderHelpModal(m.width, m.height, m.help)
	}
	if m.report != nil {
		return renderReportModal(m.width, m.height, m.report)
	}
	if m.importer != nil {
		return renderImportModal(m.width, m.height, m.importer)
	}
	if m.simulator != nil {
		return renderSimulationModal(m.width, m.height, m.simulator)
	}

	topBar := renderTopBar(m.snapshot, m.width, m.currentSpinnerFrame(), m.version)
	inputBox := m.renderBottomBar()
	_, inputH, bodyH := m.layoutHeights()

	var body string
	if m.mode == modeNew {
		errMsg := ""
		if m.err != nil {
			errMsg = m.err.Error()
		}
		body = renderWelcome(m.width, bodyH, errMsg, m.startupMode)
	} else {
		leftW := m.sidebarWidth()
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
		streamPanel := renderStreamPanel(m.streamVP, centerW, streamH, m.paneHighlighted(focusStream), m.snapshot.IsRunning, m.spinnerIdx)
		center := lipgloss.JoinVertical(lipgloss.Left, eventFlow, streamPanel)

		left := renderStatePanel(m.stateVP, leftW, bodyH, m.paneHighlighted(focusState))
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

// sendCoCreate 发起一轮共创Vui lòng求，统一处理 reqID、textarea、placeholder。
func (m *Model) sendCoCreate() tea.Cmd {
	m.cocreateSeq++
	m.cocreate.reqID = m.cocreateSeq
	m.cocreate.awaiting = true
	m.resizeTextarea()
	m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
	m.textarea.Blur()
	return runCoCreate(m.runtime, m.cocreate)
}

func (m Model) handleCoCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.cocreate == nil {
		return m, nil
	}
	state := m.cocreate

	// 键盘 ↑↓/PgUp/PgDn/Home/End 滚动；Tab 在左对话栏 ↔ 右创作指令栏间切换滚动焦点
	// （Mặc định左栏，用户回看主体）。欢迎页已关鼠标上报以保留原生复制，右栏溢出时靠 Tab
	// 切焦点后用键盘滚。左栏：上滚关 follow，滚到底重开 follow（流式跟随）。
	switch msg.Type {
	case tea.KeyTab:
		state.focusPrompt = !state.focusPrompt
		return m, nil
	case tea.KeyUp, tea.KeyPgUp:
		if state.focusPrompt {
			var cmd tea.Cmd
			state.promptVP, cmd = state.promptVP.Update(msg)
			return m, cmd
		}
		state.convFollow = false
		var cmd tea.Cmd
		state.convVP, cmd = state.convVP.Update(msg)
		return m, cmd
	case tea.KeyDown, tea.KeyPgDown:
		if state.focusPrompt {
			var cmd tea.Cmd
			state.promptVP, cmd = state.promptVP.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		state.convVP, cmd = state.convVP.Update(msg)
		if state.convVP.AtBottom() {
			state.convFollow = true
		}
		return m, cmd
	case tea.KeyHome:
		if state.focusPrompt {
			state.promptVP.GotoTop()
			return m, nil
		}
		state.convFollow = false
		state.convVP.GotoTop()
		return m, nil
	case tea.KeyEnd:
		if state.focusPrompt {
			state.promptVP.GotoBottom()
			return m, nil
		}
		state.convFollow = true
		state.convVP.GotoBottom()
		return m, nil
	case tea.KeyEsc:
		return m.exitCoCreate()
	}

	// 等待 AI 回复时Sửa类（字符Nhập/退格/光标/Ctrl+U/多行换行）放行——
	// 用户能在 AI 思考期间预Nhập下一句。Nộp类的屏蔽下沉到各 case 内部，
	// 让 Enter 节流先于 awaiting 屏蔽——这样粘贴的 \n 残片仍能补Rỗng格。

	switch msg.Type {
	case tea.KeyCtrlS:
		if state.awaiting {
			return m, nil
		}
		if !state.canStart() {
			return m, nil
		}
		// 阶段共创：把"后续方向 brief"注入并Phục hồi创作，回到运行台。
		if state.stage {
			draft := state.draftPrompt()
			m.cocreate = nil
			m.err = nil
			m.resizeTextarea()
			m.textarea.Placeholder = defaultSteerPlaceholder()
			return m, tea.Batch(resumeFromCoCreate(m.runtime, draft), m.textarea.Focus())
		}
		// 冷启动共创：用整理好的创作指令Bắt đầu创作。
		plan, err := state.buildPlan()
		if err != nil {
			m.err = err
			return m, nil
		}
		state.awaiting = true
		m.textarea.Blur()
		return m, startRuntime(m.runtime, plan)
	case tea.KeyEnter:
		// Alt+Enter → 主动换行，让 textarea.Update 接管（KeyMap.InsertNewline 已绑此键）
		if msg.Alt {
			break
		}
		// 与上一次字符按键间隔过短 → 视为粘贴流的 \n 残片：补Rỗng格代替Nộp。
		// 必须在 awaiting 屏蔽之前判断——否则 awaiting 期间粘贴 \n 残片会被屏蔽，
		// 导致 "abc\ndef" 被吞成 "abcdef"，与 base Đường dẫn语义不一致。
		if !m.lastKeyAt.IsZero() && time.Since(m.lastKeyAt) < 50*time.Millisecond {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
			m.refitTextareaHeight()
			return m, cmd
		}
		// 真正的Nộp意图：awaiting 期间屏蔽（不能并发发Vui lòng求）
		if state.awaiting {
			return m, nil
		}
		text := utils.CleanInputLine(m.textarea.Value())
		if text == "" {
			return m, nil
		}
		m.err = nil
		state.appendUser(text)
		m.textarea.Reset()
		m.refitTextareaHeight()
		cmd := m.sendCoCreate()
		return m, cmd
	case tea.KeyCtrlU:
		m.textarea.Reset()
		m.refitTextareaHeight()
		return m, nil
	}

	// 数字键 1/2/3 在 textarea 为Rỗng且有建议时 → 填入对应建议（不发送，可Sửa）。
	// 仅在RỗngNhập框时拦截，避免影响用户主动打数字。awaiting 时建议不展示，
	// 这里也Không có需额外判断（state.suggestions 为Rỗng即Bỏ qua）。
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && !state.awaiting {
		if r := msg.Runes[0]; r >= '1' && r <= '3' {
			if strings.TrimSpace(m.textarea.Value()) == "" {
				if sugs := state.suggestions(); int(r-'0') <= len(sugs) {
					m.textarea.SetValue(sugs[r-'1'])
					m.refitTextareaHeight()
					return m, nil
				}
			}
		}
	}

	// 常规Nhập转发给 textarea
	if msg.Type == tea.KeyRunes && (containsSGRFragment(string(msg.Runes)) || isCSILeak(msg.Runes)) {
		return m, nil
	}
	var ok bool
	if msg, ok = cleanHumanKeyRunes(msg); !ok {
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		m.lastKeyAt = time.Now()
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.refitTextareaHeight()
	return m, cmd
}

// exitCoCreate Thoát共创模式，HủyĐang thực hiện的 LLM Vui lòng求，Phục hồiNhập框Trạng thái。
func (m Model) exitCoCreate() (tea.Model, tea.Cmd) {
	if m.cocreate.cancel != nil {
		m.cocreate.cancel()
	}
	stage := m.cocreate.stage
	initial := m.cocreate.initialInput()
	m.cocreate = nil
	m.resizeTextarea()
	// 阶段共创Hủy：清占用标记、保持Tạm dừng，回到运行台Nhập态（不回填合成开场）。
	if stage {
		m.textarea.SetValue("")
		m.textarea.Placeholder = defaultSteerPlaceholder()
		return m, tea.Batch(cancelCoCreate(m.runtime), fetchSnapshot(m.runtime), m.textarea.Focus())
	}
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
				return m, m.textarea.Focus()
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
				state.input += utils.CleanInputRunes(msg.Runes)
			}
			return m, nil
		}
	}

	switch msg.Type {
	case tea.KeyEsc:
		// 关闭弹窗，Quay lạiRỗng答案
		state.request.resultCh <- askUserResult{
			resp: &tools.AskUserResponse{
				Answers: make(map[string]string),
				Notes:   make(map[string]string),
			},
		}
		m.askState = nil
		return m, m.textarea.Focus()
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
			return m, m.textarea.Focus()
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
			// 截掉基线左侧 olW 个可见字符，拼接 overlay + Còn lại右侧内容
			right := ansi.TruncateLeft(baseLines[y], olW, "")
			baseLines[y] = ol + right
		}
	}
	return strings.Join(baseLines, "\n")
}

// isCSILeak 检测 KeyRunes Có czy không为 CSI 转义序列泄漏的残片。
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

// containsSGRFragment 检测文本Có czy không包含 SGR 鼠标序列残片（"<数字;数字;" 模式）。
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

func cleanHumanKeyRunes(msg tea.KeyMsg) (tea.KeyMsg, bool) {
	if msg.Type != tea.KeyRunes {
		return msg, true
	}
	cleaned := utils.CleanInputRunes(msg.Runes)
	if cleaned == "" {
		return msg, false
	}
	msg.Runes = []rune(cleaned)
	return msg, true
}
