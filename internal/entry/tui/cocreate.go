package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
)

type startupMode int

const (
	startupModeQuick startupMode = iota
	startupModeCoCreate
)

func (m startupMode) label() string {
	switch m {
	case startupModeCoCreate:
		return "共创规划"
	default:
		return "快速开始"
	}
}

func (m startupMode) subtitle() string {
	switch m {
	case startupModeCoCreate:
		return "先与 AI 对话澄清，再开始创作"
	default:
		return "一句话直接开始写"
	}
}

func placeholderForNewMode(mode startupMode) string {
	switch mode {
	case startupModeCoCreate:
		return "先输入你的核心想法，Enter 开始与 AI 共创"
	default:
		return "输入一句小说需求，Enter 直接开始创作"
	}
}

func placeholderForCoCreate(state *cocreateState) string {
	if state == nil {
		return placeholderForNewMode(startupModeCoCreate)
	}
	switch {
	case state.awaiting:
		return "AI 正在整理你的要求..."
	case state.canStart():
		return "继续补充，或按 Ctrl+S 开始创作"
	default:
		return "继续补充你的要求，Enter 发送给 AI"
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

type cocreateState struct {
	session  *startup.CoCreateSession
	awaiting bool
	reqID    int
	cancel   context.CancelFunc // 取消当前 LLM 请求
	deltaCh  chan string
	doneCh   chan cocreateDoneMsg
	promptVP viewport.Model
}

func newCoCreateState(initial string) *cocreateState {
	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 3
	return &cocreateState{
		session:  startup.NewCoCreateSession(strings.TrimSpace(initial)),
		awaiting: true,
		promptVP: vp,
	}
}

func (s *cocreateState) appendUser(text string) {
	s.session.AppendUser(text)
}

func (s *cocreateState) apply(reply orchestrator.CoCreateReply) {
	s.awaiting = false
	s.session.ApplyReply(reply)
}

func (s *cocreateState) applyDelta(text string) {
	s.session.ApplyDelta(text)
}

func (s *cocreateState) canStart() bool {
	return s.session.CanStart()
}

func (s *cocreateState) initialInput() string {
	return s.session.InitialInput()
}

func (s *cocreateState) streamReply() string {
	return s.session.StreamReply()
}

func (s *cocreateState) draftPrompt() string {
	return s.session.DraftPrompt()
}

func (s *cocreateState) ready() bool {
	return s.session.Ready()
}

func (s *cocreateState) buildPlan() (startup.Plan, error) {
	return s.session.BuildPlan()
}

func renderStartupModeBar(width int, mode startupMode) string {
	quick := renderStartupModePill(mode == startupModeQuick, "快速开始")
	cocreate := renderStartupModePill(mode == startupModeCoCreate, "共创规划")
	title := lipgloss.NewStyle().
		Foreground(colorAccent).
		Bold(true).
		Render("启动模式")
	divider := lipgloss.NewStyle().
		Foreground(colorDim).
		Render("·")
	line := title + " " + divider + " " + quick + "  " + cocreate
	return lipgloss.NewStyle().
		Width(width).
		Padding(0, 1).
		Render(line)
}

func renderStartupModePill(active bool, label string) string {
	style := lipgloss.NewStyle().
		Padding(0, 1).
		Foreground(colorText)
	if active {
		style = style.Foreground(lipgloss.Color("#1c1a14")).Background(colorAccent).Bold(true)
	} else {
		style = style.Foreground(colorMuted)
	}
	return style.Render(label)
}

func renderCoCreateBody(width, height int, state *cocreateState, errMsg string) string {
	if state == nil {
		return ""
	}
	leftW := width * 58 / 100
	if leftW < 42 {
		leftW = width / 2
	}
	rightW := width - leftW
	if rightW < 28 {
		rightW = 28
		leftW = width - rightW
	}

	left := renderCoCreateConversationPanel(leftW, height, state, errMsg)
	right := renderCoCreatePromptPanel(rightW, height, state)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func coCreateModalSize(width, height int) (boxW, boxH int) {
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 24
	}
	boxW = minInt(maxInt(width*76/100, 88), width-4)
	boxH = minInt(maxInt(height*72/100, 22), height-4)
	if boxW < 64 {
		boxW = maxInt(width-2, 42)
	}
	if boxH < 14 {
		boxH = maxInt(height-2, 12)
	}
	return boxW, boxH
}

func coCreateInputWidth(width, height int) int {
	boxW, _ := coCreateModalSize(width, height)
	inputBoxW := boxW - 6
	inputW := inputBoxW - 4
	if inputW < 20 {
		inputW = 20
	}
	return inputW
}

func renderCoCreateModal(width, height int, state *cocreateState, errMsg, inputView string) string {
	if state == nil {
		return ""
	}

	boxW, boxH := coCreateModalSize(width, height)

	var b strings.Builder
	title := lipgloss.NewStyle().Foreground(colorMuted).Bold(true).Render("共创规划")
	subtitle := lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render("先把需求聊清楚，再开始创作")
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(subtitle)
	b.WriteString("\n\n")

	bodyH := boxH - 10
	if bodyH < 8 {
		bodyH = 8
	}
	b.WriteString(renderCoCreateBody(boxW-6, bodyH, state, errMsg))
	b.WriteString("\n\n")

	inputBox := lipgloss.NewStyle().
		Width(boxW-6).
		Border(baseBorder).
		BorderForeground(colorDim).
		Padding(0, 1).
		Render(inputView)
	b.WriteString(inputBox)
	b.WriteString("\n")

	hint := "Enter 发送 · Esc 退出共创"
	switch {
	case state.awaiting:
		hint = "等待 AI 回复 · ↑↓ / PgUpPgDn 滚动右侧 · Esc 退出共创"
	case state.canStart():
		hint = "Enter 继续补充 · Ctrl+S 开始创作 · ↑↓ / PgUpPgDn 滚动右侧 · Esc 退出共创"
	default:
		hint = "Enter 发送 · ↑↓ / PgUpPgDn 滚动右侧 · Esc 退出共创"
	}
	b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render(hint))

	box := lipgloss.NewStyle().
		Width(boxW).
		Height(boxH).
		Border(baseBorder).
		BorderForeground(colorAccent).
		Padding(1, 2).
		Render(b.String())

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func renderCoCreateConversationPanel(width, height int, state *cocreateState, errMsg string) string {
	var lines []string
	for _, item := range state.session.History() {
		role := "你"
		roleStyle := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
		if item.Role == "assistant" {
			role = "AI"
			roleStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
		}
		lines = append(lines, roleStyle.Render(role))
		for _, line := range wrapStreamText(strings.TrimSpace(item.Content), max(12, width-6)) {
			lines = append(lines, "  "+line)
		}
		lines = append(lines, "")
	}
	if state.awaiting {
		if state.streamReply() != "" {
			lines = append(lines, lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("AI"))
			for _, line := range wrapStreamText(state.streamReply(), max(12, width-6)) {
				lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render(line))
			}
			lines = append(lines, "")
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render("AI 正在整理你的要求..."))
	}
	if errMsg != "" {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Foreground(colorError).Render("! "+errMsg))
	}

	contentH := max(4, height-2)
	if len(lines) > contentH {
		lines = lines[len(lines)-contentH:]
	}
	content := strings.Join(lines, "\n")

	style := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(baseBorder, false, true, false, false).
		BorderForeground(colorDim).
		Padding(0, 1)
	return style.Render(panelTitleStyle.Render(":: 共创对话") + "\n" + content)
}

func renderCoCreatePromptPanel(width, height int, state *cocreateState) string {
	status := lipgloss.NewStyle().Foreground(colorDim).Render("继续对话中")
	if state.ready() {
		status = lipgloss.NewStyle().Foreground(colorAccent).Render("已可开始创作")
	}
	if state.awaiting {
		status = lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render("AI 整理中")
	}

	text := strings.TrimSpace(state.draftPrompt())
	if text == "" {
		text = "AI 会在这里持续整理出一段可直接进入创作的最终指令。"
		text = lipgloss.NewStyle().Foreground(colorDim).Italic(true).Render(text)
	} else {
		text = renderMarkdownPreview(text, max(12, width-6))
	}

	vpHeight := height - 5
	if vpHeight < 3 {
		vpHeight = 3
	}
	vpWidth := width - 2
	if vpWidth < 8 {
		vpWidth = 8
	}
	if state.promptVP.Width != vpWidth || state.promptVP.Height != vpHeight {
		state.promptVP.Width = vpWidth
		state.promptVP.Height = vpHeight
	}
	state.promptVP.MouseWheelEnabled = true
	state.promptVP.SetContent(text)

	hint := ""
	if state.promptVP.TotalLineCount() > state.promptVP.VisibleLineCount() {
		switch {
		case state.promptVP.AtTop():
			hint = "↓ 下方还有内容，可滚轮或 PgDn 查看"
		case state.promptVP.AtBottom():
			hint = "↑ 上方还有内容，可滚轮或 PgUp 查看"
		default:
			hint = "↑↓ 可继续滚动查看"
		}
	}

	style := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(baseBorder, false, false, false, false).
		BorderForeground(colorDim).
		Padding(0, 1)

	body := panelTitleStyle.Render(":: 当前创作指令") + "\n" + status + "\n\n" + state.promptVP.View()
	if hint != "" {
		body += "\n\n" + lipgloss.NewStyle().
			Width(vpWidth).
			AlignHorizontal(lipgloss.Center).
			Foreground(colorDim).
			Italic(true).
			Render(hint)
	}
	return style.Render(body)
}

func renderMarkdownPreview(text string, width int) string {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}

	h1Style := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	h2Style := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	h3Style := lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
	bulletStyle := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	codeStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)

	var out []string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			out = append(out, "")
			continue
		}

		switch {
		case strings.HasPrefix(line, "# "):
			title := strings.TrimSpace(strings.TrimPrefix(line, "# "))
			out = append(out, h1Style.Render(title))
		case strings.HasPrefix(line, "## "):
			title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			out = append(out, h2Style.Render(title))
		case strings.HasPrefix(line, "### "):
			title := strings.TrimSpace(strings.TrimPrefix(line, "### "))
			out = append(out, h3Style.Render(title))
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
			body := strings.TrimSpace(line[2:])
			wrapped := wrapStreamText(body, max(8, width-4))
			for i, item := range wrapped {
				if i == 0 {
					out = append(out, bulletStyle.Render("• ")+cardContentStyle.Render(item))
				} else {
					out = append(out, "  "+cardContentStyle.Render(item))
				}
			}
		case isOrderedMarkdownItem(line):
			prefix, body := splitOrderedMarkdownItem(line)
			wrapped := wrapStreamText(body, max(8, width-len(prefix)-2))
			for i, item := range wrapped {
				if i == 0 {
					out = append(out, bulletStyle.Render(prefix+" ")+cardContentStyle.Render(item))
				} else {
					out = append(out, strings.Repeat(" ", len(prefix)+1)+cardContentStyle.Render(item))
				}
			}
		case strings.HasPrefix(line, "> "):
			body := strings.TrimSpace(strings.TrimPrefix(line, "> "))
			for _, item := range wrapStreamText(body, max(8, width-4)) {
				out = append(out, codeStyle.Render("│ "+item))
			}
		default:
			for _, item := range wrapStreamText(line, width) {
				out = append(out, cardContentStyle.Render(item))
			}
		}
	}
	return strings.Join(out, "\n")
}

func isOrderedMarkdownItem(line string) bool {
	if len(line) < 3 {
		return false
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

func splitOrderedMarkdownItem(line string) (prefix, body string) {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) {
		return "", strings.TrimSpace(line)
	}
	return line[:i+1], strings.TrimSpace(line[i+2:])
}
