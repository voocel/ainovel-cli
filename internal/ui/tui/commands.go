package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
)

type slashCommand struct {
	name string
	args []string
}

func parseSlashCommand(text string) (slashCommand, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return slashCommand{}, false
	}
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return slashCommand{}, false
	}
	return slashCommand{name: strings.ToLower(fields[0]), args: fields[1:]}, true
}

type modelSwitchFocus int

const (
	modelFocusRole modelSwitchFocus = iota
	modelFocusProvider
	modelFocusModel
)

type modelRoleOption struct {
	Key   string
	Label string
}

var modelRoleOptions = []modelRoleOption{
	{Key: "default", Label: "默认"},
	{Key: "coordinator", Label: "Coordinator"},
	{Key: "architect", Label: "Architect"},
	{Key: "writer", Label: "Writer"},
	{Key: "editor", Label: "Editor"},
}

type modelSwitchState struct {
	focus       modelSwitchFocus
	roleIdx     int
	providerIdx int
	modelIdx    int
	providers   []string
	models      []string
	message     string
}

func newModelSwitchState(rt *orchestrator.Runtime, roleHint string) *modelSwitchState {
	state := &modelSwitchState{
		providers: rt.ConfiguredProviders(),
	}
	if len(state.providers) == 0 {
		state.message = "当前没有可用 provider"
	}

	roleHint = normalizeRoleKey(roleHint)
	for i, opt := range modelRoleOptions {
		if opt.Key == roleHint {
			state.roleIdx = i
			break
		}
	}
	state.syncSelection(rt)
	return state
}

func normalizeRoleKey(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "default":
		return "default"
	case "coordinator", "architect", "writer", "editor":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

func (s *modelSwitchState) role() string {
	return modelRoleOptions[s.roleIdx].Key
}

func (s *modelSwitchState) roleLabel() string {
	return modelRoleOptions[s.roleIdx].Label
}

func (s *modelSwitchState) provider() string {
	if len(s.providers) == 0 || s.providerIdx < 0 || s.providerIdx >= len(s.providers) {
		return ""
	}
	return s.providers[s.providerIdx]
}

func (s *modelSwitchState) model() string {
	if len(s.models) == 0 || s.modelIdx < 0 || s.modelIdx >= len(s.models) {
		return ""
	}
	return s.models[s.modelIdx]
}

func (s *modelSwitchState) moveFocus(delta int) {
	total := 3
	s.focus = modelSwitchFocus((int(s.focus) + delta + total) % total)
}

func (s *modelSwitchState) cycle(delta int, rt *orchestrator.Runtime) {
	switch s.focus {
	case modelFocusRole:
		total := len(modelRoleOptions)
		s.roleIdx = (s.roleIdx + delta + total) % total
		s.syncSelection(rt)
	case modelFocusProvider:
		if len(s.providers) == 0 {
			return
		}
		total := len(s.providers)
		s.providerIdx = (s.providerIdx + delta + total) % total
		s.syncModels(rt, "")
	case modelFocusModel:
		if len(s.models) == 0 {
			return
		}
		total := len(s.models)
		s.modelIdx = (s.modelIdx + delta + total) % total
	}
}

func (s *modelSwitchState) syncSelection(rt *orchestrator.Runtime) {
	provider, model, _ := rt.CurrentModelSelection(s.role())
	if len(s.providers) > 0 {
		s.providerIdx = 0
		for i, candidate := range s.providers {
			if candidate == provider {
				s.providerIdx = i
				break
			}
		}
	}
	s.syncModels(rt, model)
	s.message = ""
}

func (s *modelSwitchState) syncModels(rt *orchestrator.Runtime, preferred string) {
	s.models = rt.ConfiguredModels(s.provider())
	s.modelIdx = 0
	if len(s.models) == 0 {
		return
	}
	preferred = strings.TrimSpace(preferred)
	for i, model := range s.models {
		if model == preferred {
			s.modelIdx = i
			return
		}
	}
}

func (s *modelSwitchState) apply(rt *orchestrator.Runtime) error {
	if len(s.providers) == 0 {
		return fmt.Errorf("当前没有可用 provider")
	}
	if len(s.models) == 0 {
		return fmt.Errorf("provider %q 没有已配置模型", s.provider())
	}
	return rt.SwitchModel(s.role(), s.provider(), s.model())
}

func renderModelSwitchBar(width int, state *modelSwitchState) string {
	if state == nil || width <= 0 {
		return ""
	}

	title := lipgloss.NewStyle().
		Foreground(colorMuted).
		Bold(true).
		Render("/model 切换模型")

	row1 := renderModelField("角色", state.roleLabel(), state.focus == modelFocusRole)
	row2 := renderModelField("Provider", state.provider(), state.focus == modelFocusProvider)
	row3 := renderModelField("模型", state.model(), state.focus == modelFocusModel)
	hint := lipgloss.NewStyle().
		Foreground(colorDim).
		Italic(true).
		Render("Tab 切字段   ←→ 切选项   Enter 应用   Esc 取消")
	lines := []string{
		row1,
		row2,
		row3,
		hint,
	}
	if state.message != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(colorError).Italic(true).Render(truncate(state.message, width-8)))
	}

	content := strings.Join(lines, "\n")
	boxW := lipgloss.Width(content) + 8
	maxW := width - 2
	if maxW > 68 {
		maxW = 68
	}
	if boxW > maxW {
		boxW = maxW
	}
	if boxW < 56 {
		boxW = 56
	}

	innerW := boxW - 2
	if innerW < 16 {
		innerW = 16
	}
	sepW := innerW - lipgloss.Width(title) - 3
	if sepW < 0 {
		sepW = 0
	}
	lineStyle := lipgloss.NewStyle().Foreground(colorDim)
	topBorder := lineStyle.Render("┌─ ") + title + lineStyle.Render(" "+strings.Repeat("─", sepW)+"┐")
	bottomBorder := lineStyle.Render("└" + strings.Repeat("─", innerW) + "┘")

	body := make([]string, 0, len(lines))
	for _, line := range lines {
		padding := innerW - lipgloss.Width(line)
		if padding < 0 {
			padding = 0
		}
		body = append(body, lineStyle.Render("│")+line+strings.Repeat(" ", padding)+lineStyle.Render("│"))
	}

	return strings.Join(append(append([]string{topBorder}, body...), bottomBorder), "\n")
}

func renderModelField(label, value string, focused bool) string {
	if strings.TrimSpace(value) == "" {
		value = "未设置"
	}
	labelText := lipgloss.NewStyle().
		Foreground(colorMuted).
		Width(12).
		Render(label + ":")
	style := lipgloss.NewStyle().
		Foreground(colorText).
		Padding(0, 1)
	if focused {
		style = style.Foreground(colorAccent).Bold(true).Underline(true)
	}
	return labelText + style.Render("["+value+"]")
}
