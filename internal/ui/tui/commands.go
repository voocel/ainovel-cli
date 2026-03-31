package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
)

type slashCommandSpec struct {
	Name        string
	Usage       string
	Description string
	AutoExecute bool
	Run         func(m Model, args []string) (tea.Model, tea.Cmd)
}

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

func commandSpecs() []slashCommandSpec {
	return []slashCommandSpec{
		{
			Name:        "help",
			Usage:       "/help",
			Description: "显示可用命令及其用法",
			AutoExecute: true,
			Run: func(m Model, _ []string) (tea.Model, tea.Cmd) {
				m.help = newHelpState(m.width, m.height)
				m.textarea.Blur()
				return m, nil
			},
		},
		{
			Name:        "model",
			Usage:       "/model [role]",
			Description: "切换默认模型或指定角色模型",
			AutoExecute: true,
			Run: func(m Model, args []string) (tea.Model, tea.Cmd) {
				roleHint := ""
				if len(args) > 0 {
					roleHint = args[0]
					if normalizeRoleKey(roleHint) == "" {
						m.events = append(m.events, orchestrator.UIEvent{
							Time: time.Now(), Category: "ERROR", Summary: "未知角色：" + roleHint, Level: "error",
						})
						m.refreshEventViewport()
						return m, nil
					}
				}
				m.modelSwitch = newModelSwitchState(m.runtime, roleHint)
				m.textarea.Blur()
				return m, nil
			},
		},
		{
			Name:        "report",
			Usage:       "/report",
			Description: "分析当前小说 output 产物并显示诊断报告",
			AutoExecute: true,
			Run: func(m Model, _ []string) (tea.Model, tea.Cmd) {
				m.report = newReportState(m.runtime.Dir(), m.width, m.height)
				m.textarea.Blur()
				return m, nil
			},
		},
	}
}

func findCommandSpec(name string) (slashCommandSpec, bool) {
	for _, spec := range commandSpecs() {
		if spec.Name == strings.ToLower(strings.TrimSpace(name)) {
			return spec, true
		}
	}
	return slashCommandSpec{}, false
}

func (m Model) handleSlashCommand(cmd slashCommand) (tea.Model, tea.Cmd) {
	spec, ok := findCommandSpec(cmd.name)
	if !ok {
		m.events = append(m.events, orchestrator.UIEvent{
			Time: time.Now(), Category: "ERROR", Summary: "未知命令：/" + cmd.name, Level: "error",
		})
		m.refreshEventViewport()
		return m, nil
	}
	return spec.Run(m, cmd.args)
}
