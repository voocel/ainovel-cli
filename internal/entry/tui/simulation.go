package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Accelerator-mzq/ainovel-cli/internal/host"
	"github.com/Accelerator-mzq/ainovel-cli/internal/host/sim"
)

type simulationState struct {
	reqID      int
	title      string
	source     string
	stage      sim.Stage
	current    int
	total      int
	startedAt  time.Time
	finishedAt time.Time
	history    []simulationLine
	err        error
	done       bool
	cancel     context.CancelFunc
	viewport   viewport.Model
}

type simulationLine struct {
	at      time.Time
	stage   sim.Stage
	current int
	total   int
	message string
	err     error
}

type simEventMsg struct {
	reqID int
	ev    sim.Event
	ch    <-chan sim.Event
}

func (m simEventMsg) terminal() bool {
	return m.ev.Stage == sim.StageDone || m.ev.Stage == sim.StageError
}

func newSimulationState(reqID int, title, source string, width, height int, cancel context.CancelFunc) *simulationState {
	boxW, boxH := reportModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	vp := viewport.New(contentW, boxH-4)
	s := &simulationState{
		reqID:     reqID,
		title:     title,
		source:    source,
		stage:     sim.StageScan,
		startedAt: time.Now(),
		cancel:    cancel,
		viewport:  vp,
	}
	s.refresh(contentW)
	return s
}

func (s *simulationState) appendEvent(ev sim.Event, contentW int) {
	s.stage = ev.Stage
	s.current = ev.Current
	s.total = ev.Total
	if ev.Err != nil {
		s.err = ev.Err
	}
	s.history = append(s.history, simulationLine{
		at: ev.Time, stage: ev.Stage, current: ev.Current, total: ev.Total,
		message: ev.Message, err: ev.Err,
	})
	if ev.Stage == sim.StageDone || ev.Stage == sim.StageError {
		s.done = true
		s.finishedAt = ev.Time
	}
	s.refresh(contentW)
}

func (s *simulationState) refresh(contentW int) {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	okStyle := lipgloss.NewStyle().Foreground(colorSuccess)
	errStyle := lipgloss.NewStyle().Foreground(colorError)
	stageStyle := lipgloss.NewStyle().Foreground(colorAccent2)

	var b strings.Builder
	b.WriteString(titleStyle.Render(s.title))
	b.WriteString("\n\n")
	if s.source != "" {
		b.WriteString(dimStyle.Render("来源 "))
		b.WriteString(s.source)
		b.WriteString("\n")
	}
	b.WriteString(dimStyle.Render("开始 "))
	b.WriteString(formatReportTime(s.startedAt))
	if !s.finishedAt.IsZero() {
		b.WriteString(dimStyle.Render("  完成 "))
		b.WriteString(formatReportTime(s.finishedAt))
	}
	b.WriteString("\n\n")

	b.WriteString(mutedStyle.Render("阶段 "))
	b.WriteString(stageStyle.Render(string(s.stage)))
	if s.total > 0 {
		b.WriteString(mutedStyle.Render("  进度 "))
		b.WriteString(fmt.Sprintf("%d/%d", s.current, s.total))
	}
	b.WriteString("\n\n")

	b.WriteString(titleStyle.Render("流程日志"))
	b.WriteString(" ")
	b.WriteString(dimStyle.Render(fmt.Sprintf("(%d 条)", len(s.history))))
	b.WriteString("\n")
	for _, ln := range s.history {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(ln.at.Format("15:04:05")))
		b.WriteString(" ")
		b.WriteString(stageStyle.Render(string(ln.stage)))
		if ln.total > 0 && ln.current > 0 {
			b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d/%d", ln.current, ln.total)))
		}
		b.WriteString(" ")
		if ln.err != nil {
			b.WriteString(errStyle.Render(ln.message + " - " + ln.err.Error()))
		} else {
			b.WriteString(wrapText(ln.message, contentW))
		}
	}

	b.WriteString("\n\n")
	switch {
	case !s.done:
		b.WriteString(dimStyle.Render("Esc 取消"))
	case s.err != nil:
		b.WriteString(errStyle.Render("仿写画像处理失败"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Esc 关闭面板"))
	default:
		b.WriteString(okStyle.Render("仿写画像已就绪，后续 Agent 会从 novel_context 读取"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Esc 关闭面板"))
	}

	s.viewport.SetContent(b.String())
	if !s.done {
		s.viewport.GotoBottom()
	}
}

func renderSimulationModal(width, height int, s *simulationState) string {
	if s == nil {
		return ""
	}
	boxW, boxH := reportModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	if s.viewport.Width != contentW {
		s.viewport.Width = contentW
		s.refresh(contentW)
	}
	if s.viewport.Height != boxH-4 {
		s.viewport.Height = boxH - 4
	}
	hint := "  ↑↓ 滚动 · Esc 取消/关闭"
	modal := renderPaddedModalFrame(boxW, boxH, "仿写画像", hint, strings.Split(s.viewport.View(), "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func (m Model) handleSimulationKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.simulator == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		if !m.simulator.done && m.simulator.cancel != nil {
			m.simulator.cancel()
			return m, nil
		}
		m.simulator = nil
		return m, m.textarea.Focus()
	case tea.KeyUp:
		m.simulator.viewport.ScrollUp(1)
	case tea.KeyDown:
		m.simulator.viewport.ScrollDown(1)
	case tea.KeyPgUp:
		m.simulator.viewport.HalfPageUp()
	case tea.KeyPgDown:
		m.simulator.viewport.HalfPageDown()
	}
	return m, nil
}

func startSimulate(rt *host.Host, reqID int, args []string, width, height int) (*simulationState, tea.Cmd, error) {
	if len(args) > 0 {
		return nil, nil, fmt.Errorf("用法：/simulate")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := rt.Simulate(ctx)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	state := newSimulationState(reqID, "生成仿写画像", "./simulate", width, height, cancel)
	return state, listenSimulationEvent(reqID, ch), nil
}

func startImportSimulation(rt *host.Host, reqID int, args []string, width, height int) (*simulationState, tea.Cmd, error) {
	if len(args) != 1 {
		return nil, nil, fmt.Errorf("用法：/importsim <profile.json>")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := rt.ImportSimulationProfile(ctx, args[0])
	if err != nil {
		cancel()
		return nil, nil, err
	}
	state := newSimulationState(reqID, "导入仿写画像", args[0], width, height, cancel)
	return state, listenSimulationEvent(reqID, ch), nil
}

func listenSimulationEvent(reqID int, ch <-chan sim.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return simEventMsg{reqID: reqID, ev: ev, ch: ch}
	}
}
