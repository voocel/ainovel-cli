package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
)

// thinkingLabel 是思考强度档位的中文名；空档位是自动（沿用模型默认）。
func thinkingLabel(level agentcore.ThinkingLevel) string {
	switch level {
	case "":
		return "自动"
	case levelInherit:
		return "跟随默认"
	case agentcore.ThinkingOff:
		return "关闭"
	case agentcore.ThinkingMinimal:
		return "最少"
	case agentcore.ThinkingLow:
		return "低"
	case agentcore.ThinkingMedium:
		return "中"
	case agentcore.ThinkingHigh:
		return "高"
	case agentcore.ThinkingXHigh:
		return "极高"
	case agentcore.ThinkingMax:
		return "最高"
	}
	return string(level)
}

// bindingLabel 是页脚与右栏的当前绑定摘要：模型 · 思考强度（非自动时）· 角色覆盖数。
func (m model) bindingLabel() string {
	current := m.api.Models.Current("")
	parts := []string{current.Model}
	if current.Thinking != "" {
		parts = append(parts, "思考 "+thinkingLabel(current.Thinking))
	}
	if roles := len(m.api.Models.Config().Roles); roles > 0 {
		parts = append(parts, fmt.Sprintf("角色 %d", roles))
	}
	return strings.Join(parts, " · ")
}

// 模型面板（老版本 /model 的形态）：角色 / 连接 / 模型 / 思考强度四行，←→ 切选项、
// Tab/↑↓ 换行、Enter 应用、Esc 取消。覆盖在内容区底部，与命令浮层同一位置。
const (
	modelPanelRows = 6 // 标题线、四个字段、提示或错误
	// levelInherit 是角色专用的档位：不设自己的强度，跟随默认绑定。
	levelInherit agentcore.ThinkingLevel = "inherit"
	// followDefault 是角色专用的连接选项：清除覆盖，跟随默认绑定。
	followDefault = ""
)

type modelPanelField int

const (
	panelRole modelPanelField = iota
	panelConnection
	panelModel
	panelThinking
	panelFieldCount
)

type modelPanel struct {
	focus       modelPanelField
	roles       []string // "" 是默认
	connections []string // 角色时首项是 followDefault
	models      []string
	levels      []agentcore.ThinkingLevel // 角色时首项是 levelInherit
	roleIdx     int
	connIdx     int
	modelIdx    int
	levelIdx    int
	// initialLevel 是打开或换角色时的档位：没动过不回写——存储的意图可能高于当前
	// 模型能力、面板呈现不出来，不能因"没动"误抹成自动。
	initialLevel int
	message      string
}

var roleLabels = map[string]string{"": "默认", "architect": "策划 architect", "writer": "作者 writer", "editor": "编辑 editor"}

func (p *modelPanel) role() string       { return p.roles[p.roleIdx] }
func (p *modelPanel) connection() string { return pick(p.connections, p.connIdx) }
func (p *modelPanel) model() string      { return pick(p.models, p.modelIdx) }
func (p *modelPanel) level() agentcore.ThinkingLevel {
	if p.levelIdx < len(p.levels) {
		return p.levels[p.levelIdx]
	}
	return ""
}

func pick(items []string, index int) string {
	if index >= 0 && index < len(items) {
		return items[index]
	}
	return ""
}

func indexOf[T comparable](items []T, want T) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return 0
}

// openModelPanel 打开面板；role 预选角色（architect / writer / editor），空是默认。
func (m model) openModelPanel(role string) (tea.Model, tea.Cmd) {
	panel := &modelPanel{roles: m.api.Models.Roles()}
	if role != "" && indexOf(panel.roles, role) == 0 {
		m.bench.err = "用法：/model [architect|writer|editor]；不带角色调整默认模型"
		return m, nil
	}
	panel.roleIdx = indexOf(panel.roles, role)
	m.syncModelPanel(panel)
	m.bench.modelPanel = panel
	return m, nil
}

// syncModelPanel 按当前角色的生效绑定重置连接、模型与档位。
func (m model) syncModelPanel(p *modelPanel) {
	role := p.role()
	current := m.api.Models.Current(role)
	p.connections = m.api.Models.Connections()
	if role != "" {
		p.connections = append([]string{followDefault}, p.connections...)
	}
	p.connIdx = indexOf(p.connections, current.Connection)
	if role != "" && current.Inherited {
		p.connIdx = 0
	}
	m.syncModelPanelModels(p, current.Model)
	raw := m.api.Models.Config().Roles[role].Thinking
	switch {
	case role != "" && raw == "":
		p.levelIdx = 0
	default:
		p.levelIdx = indexOf(p.levels, current.Thinking)
	}
	p.initialLevel = p.levelIdx
	p.message = ""
}

// syncModelPanelModels 换连接后重列模型（优先落在 preferred），并按所选模型重列档位。
func (m model) syncModelPanelModels(p *modelPanel, preferred string) {
	p.models = nil
	if p.connection() != followDefault {
		p.models = m.api.Models.Models(p.connection())
	}
	p.modelIdx = indexOf(p.models, preferred)
	m.syncModelPanelLevels(p)
}

func (m model) syncModelPanelLevels(p *modelPanel) {
	p.levels = []agentcore.ThinkingLevel{""}
	if p.connection() != followDefault && p.model() != "" {
		p.levels = m.api.Models.Levels(p.connection(), p.model())
	}
	if p.role() != "" {
		p.levels = append([]agentcore.ThinkingLevel{levelInherit}, p.levels...)
	}
	p.levelIdx = min(p.levelIdx, len(p.levels)-1)
}

func (m model) handleModelPanelKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.bench.modelPanel
	switch key.Type {
	case tea.KeyEsc:
		m.bench.modelPanel = nil
	case tea.KeyTab, tea.KeyDown:
		p.focus = (p.focus + 1) % panelFieldCount
	case tea.KeyShiftTab, tea.KeyUp:
		p.focus = (p.focus + panelFieldCount - 1) % panelFieldCount
	case tea.KeyLeft:
		m.cycleModelPanel(p, -1)
	case tea.KeyRight:
		m.cycleModelPanel(p, 1)
	case tea.KeyEnter:
		return m.applyModelPanel(p), nil
	}
	return m, nil
}

func (m model) cycleModelPanel(p *modelPanel, delta int) {
	step := func(index, size int) int {
		if size == 0 {
			return 0
		}
		return (index + delta + size) % size
	}
	p.message = ""
	switch p.focus {
	case panelRole:
		p.roleIdx = step(p.roleIdx, len(p.roles))
		m.syncModelPanel(p)
	case panelConnection:
		p.connIdx = step(p.connIdx, len(p.connections))
		m.syncModelPanelModels(p, "")
	case panelModel:
		p.modelIdx = step(p.modelIdx, len(p.models))
		m.syncModelPanelLevels(p)
	case panelThinking:
		p.levelIdx = step(p.levelIdx, len(p.levels))
	}
}

// applyModelPanel 先切模型再（仅当用户动过）设思考强度；成功关面板并在底栏说明生效时机。
func (m model) applyModelPanel(p *modelPanel) model {
	role := p.role()
	switch {
	case role != "" && p.connection() == followDefault:
		if err := m.api.Models.Use(role, "", ""); err != nil {
			p.message = err.Error()
			return m
		}
	case p.model() == "":
		p.message = "这个连接还没有保存模型；先在作品首页「模型设置」里添加"
		return m
	default:
		if err := m.api.Models.Use(role, p.connection(), p.model()); err != nil {
			p.message = err.Error()
			return m
		}
		if p.levelIdx != p.initialLevel {
			if err := m.api.Models.SetThinking(role, string(p.level())); err != nil {
				p.message = err.Error()
				return m
			}
		}
	}
	current := m.api.Models.Current(role)
	when := "下个任务生效"
	if m.bench.writing {
		when = "当前任务写完后生效"
	}
	target := current.Connection + "/" + current.Model
	if current.Inherited {
		target = "跟随默认（" + target + "）"
	}
	m.bench.notice = fmt.Sprintf("已切换：%s → %s · 思考 %s · %s", roleLabels[role], target, thinkingLabel(current.Thinking), when)
	m.bench.modelPanel = nil
	return m
}

// modelPanelLines 渲染面板：标题线、四个字段（聚焦行用 ‹ › 标出）、提示或错误。
func (m model) modelPanelLines(width int) []string {
	p := m.bench.modelPanel
	lines := []string{sectionTitle(benchTheme.Muted.Render("模型 · ←→ 切换 · Tab 换行 · Enter 应用 · Esc 取消"), width)}
	values := [panelFieldCount]string{roleLabels[p.role()], p.connection(), p.model(), thinkingLabel(p.level())}
	if p.role() != "" && p.connection() == followDefault {
		values[panelConnection], values[panelModel] = "跟随默认", "跟随默认"
	}
	if values[panelModel] == "" {
		values[panelModel] = "（无已保存模型）"
	}
	for field, label := range [panelFieldCount]string{"角色", "连接", "模型", "思考强度"} {
		label += strings.Repeat(" ", max(0, 10-lipgloss.Width(label)))
		name := benchTheme.Muted.Render(label)
		value := benchTheme.Text.Render("  " + values[field])
		if modelPanelField(field) == p.focus {
			name = benchTheme.Accent.Render(label)
			value = benchTheme.Selected.Render("‹ " + values[field] + " ›")
		}
		lines = append(lines, fitLine(name+value, width))
	}
	hint := benchTheme.Muted.Render(fitLine("切换只影响之后开始的任务；正在写的这一步用原来的模型", width))
	if p.message != "" {
		hint = benchTheme.Error.Render(fitLine("! "+p.message, width))
	}
	return append(lines, hint)
}

// modelPanelFieldAt 把整屏坐标映射到面板字段行（鼠标点行聚焦）。
func (m model) modelPanelFieldAt(l benchLayout, x, y int) (modelPanelField, bool) {
	if m.bench.modelPanel == nil || x < l.mainX || l.inRail(x) {
		return 0, false
	}
	end := l.bodyY + benchTabRows + l.contentRows
	start := max(l.bodyY+benchTabRows, end-modelPanelRows)
	field := y - start - 1
	if field < 0 || field >= int(panelFieldCount) {
		return 0, false
	}
	return modelPanelField(field), true
}
