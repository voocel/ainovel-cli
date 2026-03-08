package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/app"
)

const maxEvents = 500

type appMode int

const (
	modeNew     appMode = iota // 等待用户输入小说需求
	modeRunning                // 正在创作
	modeDone                   // 创作完成
)

// spinner 帧序列
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Model 是 TUI 的顶层状态。
type Model struct {
	runtime      *app.Runtime
	snapshot     app.UISnapshot
	events       []app.UIEvent
	viewport     viewport.Model
	textarea     textarea.Model
	width        int
	height       int
	autoScroll   bool
	mode         appMode
	err          error
	spinnerIdx   int
}

// NewModel 创建 TUI Model。
func NewModel(rt *app.Runtime) Model {
	ta := textarea.New()
	ta.Placeholder = "输入小说需求，例如：写一部12章都市悬疑小说"
	ta.CharLimit = 500
	ta.MaxHeight = 3
	ta.ShowLineNumbers = false
	ta.Focus()

	// Enter 不换行（由 Update 处理提交）
	ta.KeyMap.InsertNewline.SetEnabled(false)

	vp := viewport.New(80, 20)
	vp.SetContent("")

	return Model{
		runtime:    rt,
		autoScroll: true,
		mode:       modeNew,
		textarea:   ta,
		viewport:   vp,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		listenEvents(m.runtime),
		listenDone(m.runtime),
		tickSnapshot(m.runtime),
		checkResume(m.runtime),
		tickSpinner(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.textarea.SetWidth(m.width - 4)
		m.updateViewportSize()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEscape:
			m.textarea.Reset()
			return m, nil
		case tea.KeyCtrlL:
			m.events = nil
			m.viewport.SetContent("")
			m.viewport.GotoTop()
			return m, nil
		case tea.KeyEnter:
			text := strings.TrimSpace(m.textarea.Value())
			if text == "" {
				return m, nil
			}
			m.textarea.Reset()
			switch m.mode {
			case modeNew:
				m.mode = modeRunning
				m.textarea.Placeholder = "输入剧情干预，例如：把感情线提前到第4章"
				return m, startRuntime(m.runtime, text)
			case modeRunning:
				return m, steerRuntime(m.runtime, text)
			}
			return m, nil
		case tea.KeyUp, tea.KeyPgUp:
			m.autoScroll = false
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		case tea.KeyDown, tea.KeyPgDown:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			// 滚动到底部时恢复自动跟随
			if m.viewport.AtBottom() {
				m.autoScroll = true
			}
			return m, cmd
		case tea.KeyEnd:
			m.autoScroll = true
			m.viewport.GotoBottom()
			return m, nil
		}

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		if msg.Action == tea.MouseActionPress {
			m.autoScroll = false
			if m.viewport.AtBottom() {
				m.autoScroll = true
			}
		}
		return m, cmd

	case eventMsg:
		ev := app.UIEvent(msg)
		m.events = append(m.events, ev)
		if len(m.events) > maxEvents {
			m.events = m.events[len(m.events)-maxEvents:]
		}
		m.refreshEventViewport()
		return m, listenEvents(m.runtime)

	case snapshotMsg:
		m.snapshot = app.UISnapshot(msg)
		return m, tickSnapshot(m.runtime)

	case doneMsg:
		m.mode = modeDone
		m.textarea.Placeholder = "创作已完成"
		m.textarea.Blur()
		return m, fetchSnapshot(m.runtime)

	case startResultMsg:
		if msg.err != nil {
			m.err = msg.err
			m.mode = modeNew
			m.textarea.Placeholder = "输入小说需求，例如：写一部12章都市悬疑小说"
			m.events = append(m.events, app.UIEvent{
				Time: time.Now(), Category: "ERROR", Summary: msg.err.Error(), Level: "error",
			})
			m.refreshEventViewport()
		} else if m.mode == modeNew {
			m.mode = modeRunning
			m.textarea.Placeholder = "输入剧情干预，例如：把感情线提前到第4章"
		}
		return m, fetchSnapshot(m.runtime)

	case steerResultMsg:
		return m, fetchSnapshot(m.runtime)

	case spinnerTickMsg:
		m.spinnerIdx = (m.spinnerIdx + 1) % len(spinnerFrames)
		return m, tickSpinner()
	}

	// 更新 textarea 组件
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// refreshEventViewport 重新渲染事件流内容并设置 viewport。
func (m *Model) refreshEventViewport() {
	centerW := m.eventFlowWidth()
	content := renderEventContent(m.events, centerW)
	m.viewport.SetContent(content)
	if m.autoScroll {
		m.viewport.GotoBottom()
	}
}

// updateViewportSize 根据当前窗口尺寸更新 viewport 大小。
func (m *Model) updateViewportSize() {
	centerW := m.eventFlowWidth()
	bodyH := m.bodyHeight()
	m.viewport.Width = centerW - 2
	m.viewport.Height = bodyH
}

func (m *Model) eventFlowWidth() int {
	if m.width == 0 {
		return 80
	}
	leftW := m.width * 25 / 100
	rightW := m.width * 30 / 100
	return m.width - leftW - rightW
}

func (m *Model) bodyHeight() int {
	if m.height == 0 {
		return 20
	}
	topH := 1
	inputH := 3
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

	spinnerFrame := ""
	if m.snapshot.IsRunning {
		spinnerFrame = spinnerFrames[m.spinnerIdx]
	}

	topBar := renderTopBar(m.snapshot, m.width, spinnerFrame)
	inputBox := renderInputBox(m.textarea.View(), m.width)

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
		body = renderWelcome(m.width, bodyH, errMsg)
	} else {
		leftW := m.width * 25 / 100
		rightW := m.width * 30 / 100
		centerW := m.width - leftW - rightW

		if m.viewport.Width != centerW-2 || m.viewport.Height != bodyH {
			m.viewport.Width = centerW - 2
			m.viewport.Height = bodyH
		}

		left := renderStatePanel(m.snapshot, leftW, bodyH)
		center := renderEventFlowViewport(m.viewport, centerW, bodyH)
		right := renderDetailPanel(m.snapshot, rightW, bodyH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, center, right)
	}

	return lipgloss.JoinVertical(lipgloss.Left, topBar, body, inputBox)
}
