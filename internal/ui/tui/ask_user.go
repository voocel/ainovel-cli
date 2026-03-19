package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/tools"
)

type askUserRequest struct {
	questions []tools.Question
	resultCh  chan askUserResult
}

type askUserResult struct {
	resp *tools.AskUserResponse
	err  error
}

type askUserBridge struct {
	requests chan askUserRequest
}

func newAskUserBridge() *askUserBridge {
	return &askUserBridge{
		requests: make(chan askUserRequest),
	}
}

func (b *askUserBridge) handler(ctx context.Context, questions []tools.Question) (*tools.AskUserResponse, error) {
	req := askUserRequest{
		questions: questions,
		resultCh:  make(chan askUserResult, 1),
	}
	select {
	case b.requests <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case result := <-req.resultCh:
		return result.resp, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type askUserState struct {
	request  askUserRequest
	index    int
	cursor   int
	typing   bool
	input    string
	selected map[int]bool
	answers  map[string]string
	notes    map[string]string
}

func newAskUserState(req askUserRequest) *askUserState {
	return &askUserState{
		request:  req,
		selected: make(map[int]bool),
		answers:  make(map[string]string),
		notes:    make(map[string]string),
	}
}

func (s *askUserState) currentQuestion() tools.Question {
	return s.request.questions[s.index]
}

func (s *askUserState) optionCount() int {
	return len(s.currentQuestion().Options) + 1
}

func (s *askUserState) choiceLabel(idx int) string {
	q := s.currentQuestion()
	if idx < len(q.Options) {
		return q.Options[idx].Label
	}
	return "自由输入"
}

func (s *askUserState) choiceDescription(idx int) string {
	q := s.currentQuestion()
	if idx < len(q.Options) {
		return q.Options[idx].Description
	}
	return "以上都不合适，自己补充"
}

func (s *askUserState) moveCursor(delta int) {
	total := s.optionCount()
	if total == 0 {
		s.cursor = 0
		return
	}
	s.cursor = (s.cursor + delta + total) % total
}

func (s *askUserState) toggleSelection() {
	if s.selected[s.cursor] {
		delete(s.selected, s.cursor)
		return
	}
	s.selected[s.cursor] = true
}

func (s *askUserState) finishCurrentAnswer() bool {
	q := s.currentQuestion()
	if s.typing {
		text := strings.TrimSpace(s.input)
		if text == "" {
			return false
		}
		s.answers[q.Question] = text
		s.notes[q.Question] = text
		return s.advance()
	}

	if q.MultiSelect {
		var values []string
		var custom string
		for idx := 0; idx < s.optionCount(); idx++ {
			if !s.selected[idx] {
				continue
			}
			if idx < len(q.Options) {
				values = append(values, q.Options[idx].Label)
				continue
			}
			custom = strings.TrimSpace(s.input)
		}
		if custom != "" {
			values = append(values, custom)
			s.notes[q.Question] = custom
		}
		if len(values) == 0 {
			return false
		}
		s.answers[q.Question] = strings.Join(values, "、")
		return s.advance()
	}

	if s.cursor >= len(q.Options) {
		s.typing = true
		s.input = ""
		return false
	}
	s.answers[q.Question] = q.Options[s.cursor].Label
	return s.advance()
}

func (s *askUserState) advance() bool {
	s.index++
	if s.index >= len(s.request.questions) {
		return true
	}
	s.cursor = 0
	s.typing = false
	s.input = ""
	s.selected = make(map[int]bool)
	return false
}

func (s *askUserState) submit() {
	s.request.resultCh <- askUserResult{
		resp: &tools.AskUserResponse{
			Answers: s.answers,
			Notes:   s.notes,
		},
	}
}

func (s *askUserState) cancelCurrentTyping() {
	s.typing = false
	s.input = ""
}

func renderAskUserModal(width, height int, state *askUserState) string {
	if state == nil {
		return ""
	}
	q := state.currentQuestion()
	boxW := minInt(maxInt(width*60/100, 52), width-4)
	boxH := minInt(maxInt(height*60/100, 16), height-4)
	if boxW < 40 {
		boxW = maxInt(width-2, 20)
	}
	if boxH < 10 {
		boxH = maxInt(height-2, 8)
	}

	var b strings.Builder
	title := fmt.Sprintf("需要补充信息 %d/%d", state.index+1, len(state.request.questions))
	b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(title))
	b.WriteString("\n\n")
	if q.Header != "" {
		b.WriteString(highlightValueStyle.Render(q.Header))
		b.WriteString("\n")
	}
	b.WriteString(cardContentStyle.Render(q.Question))
	b.WriteString("\n\n")

	for idx := 0; idx < state.optionCount(); idx++ {
		prefix := "  "
		if state.cursor == idx {
			prefix = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("› ")
		}
		label := state.choiceLabel(idx)
		if q.MultiSelect {
			marker := "[ ]"
			if state.selected[idx] {
				marker = "[x]"
			}
			label = marker + " " + label
		}
		b.WriteString(prefix + cardContentStyle.Render(label))
		b.WriteString("\n")
		b.WriteString("  " + lipgloss.NewStyle().Foreground(colorDim).Render(state.choiceDescription(idx)))
		b.WriteString("\n")
	}

	if state.typing || (q.MultiSelect && state.selected[len(q.Options)]) {
		b.WriteString("\n")
		b.WriteString(panelTitleStyle.Render("补充内容"))
		b.WriteString("\n")
		content := state.input
		if content == "" {
			content = "请输入..."
		}
		style := lipgloss.NewStyle().
			Width(boxW-8).
			Border(baseBorder).
			BorderForeground(colorDim).
			Padding(0, 1)
		b.WriteString(style.Render(content))
		b.WriteString("\n")
	}

	hint := "↑↓ 选择 · Enter 确认 · Esc 关闭"
	if q.MultiSelect {
		hint = "↑↓ 选择 · Space 勾选 · Enter 提交 · Esc 关闭"
	}
	if state.typing {
		hint = "输入补充内容 · Enter 确认 · Esc 返回选项"
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render(hint))

	box := lipgloss.NewStyle().
		Width(boxW).
		Height(boxH).
		Border(baseBorder).
		BorderForeground(colorAccent).
		Padding(1, 2).
		Render(b.String())

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
