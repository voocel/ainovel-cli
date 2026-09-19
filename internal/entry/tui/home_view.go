package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type homeHit struct {
	x, y, width, focus, row int
	open                    bool
}
type homeFrame struct {
	text     string
	hits     []homeHit
	capacity int
	libraryY int
}

func (m model) homeFrame() homeFrame {
	w, h := max(1, m.width), max(1, m.height)
	inner := min(96, w-4)
	x := (w - inner) / 2
	frame := homeFrame{}
	lines := []string{}
	add := func(text string) { lines = append(lines, strings.Repeat(" ", x)+fitLine(text, inner)) }
	left := benchTheme.Accent.Bold(true).Render("AINOVEL") + benchTheme.Muted.Render("  /  创作首页")
	right := homeControl("模型设置", m.home.focus == focusConfig)
	add(left + strings.Repeat(" ", max(1, inner-lipgloss.Width(left)-lipgloss.Width(right))) + right)
	frame.hits = append(frame.hits, homeHit{x: x + inner - lipgloss.Width(right), y: 0, width: lipgloss.Width(right), focus: focusConfig, row: -1})
	add(benchRule(inner))
	add("")
	add(benchTheme.Title.Render("开始一个新故事"))
	input := m.home.premise
	input.Width = max(1, inner-4)
	input.SetCursor(input.Position())
	input.Prompt = "› "
	frame.hits = append(frame.hits, homeHit{x: x, y: len(lines), width: inner, focus: focusPremise, row: -1})
	add(input.View())
	add("")
	if m.home.editingChapters {
		field := m.home.chapterInput
		field.Width = max(1, inner-20)
		field.SetCursor(field.Position())
		field.Prompt = "目标章数  "
		add(field.View())
	} else {
		line, hits := homeActions([]homeAction{{focusChapters, fmt.Sprintf("目标 %d 章", m.home.chapters)}, {focusApproval, approvalLabel(m.home.approval)}, {focusRefine, "更多设定"}}, x, len(lines), m.home.focus)
		primary := benchTheme.Accent.Bold(true).Render("开始创作 ↵")
		if m.home.focus == focusStart {
			primary = benchTheme.Selected.Render("开始创作 ↵")
		}
		size := lipgloss.Width(primary)
		hits = append(hits, homeHit{x: x + inner - size, y: len(lines), width: size, focus: focusStart, row: -1, open: true})
		frame.hits = append(frame.hits, hits...)
		add(line + strings.Repeat(" ", max(1, inner-lipgloss.Width(line)-size)) + primary)
	}
	add("")
	title := fmt.Sprintf("作品库 · %d 部", len(m.home.library))
	add(benchTheme.Title.Render(title))
	if m.home.searching {
		field := m.home.search
		field.Width = max(1, inner-4)
		field.SetCursor(field.Position())
		field.Prompt = "/ "
		add(field.View())
	} else {
		search := "/ 搜索"
		if m.home.search.Value() != "" {
			search = "/ " + truncate(m.home.search.Value(), max(4, inner-20))
		}
		line, hits := homeActions([]homeAction{{focusSearch, search}, {focusImport, "导入作品"}}, x, len(lines), m.home.focus)
		frame.hits = append(frame.hits, hits...)
		add(line)
	}
	frame.libraryY = len(lines)
	frame.capacity = max(1, h-3-len(lines))
	indices := m.libraryIndices()
	selected := 0
	for i, index := range indices {
		if index == m.home.cursor {
			selected = i
			break
		}
	}
	start := min(max(0, selected-frame.capacity/2), max(0, len(indices)-frame.capacity))
	end := min(len(indices), start+frame.capacity)
	if len(indices) == 0 {
		label := "写下一个故事想法，开始你的第一部作品。"
		if !m.home.loaded {
			label = "正在读取作品库…"
		} else if m.home.search.Value() != "" {
			label = "没有匹配的作品 · 搜索框留空显示全部"
		}
		add(benchTheme.Muted.Render(label))
	} else {
		for _, index := range indices[start:end] {
			entry := m.home.library[index]
			title := entry.premise
			if title == "" {
				title = entry.id
			}
			progress := fmt.Sprintf("%d/%d", entry.written, entry.target)
			state := entry.state
			right := progressBar(entry.written, entry.target, 6) + "  " + progress + "  " + state
			titleWidth := max(4, inner-lipgloss.Width(right)-5)
			label := truncate(title, titleWidth)
			label = label + strings.Repeat(" ", max(1, inner-3-lipgloss.Width(label)-lipgloss.Width(right))) + right
			if m.home.focus == focusLibrary && index == m.home.cursor {
				label = benchTheme.Selected.Render("▎ " + label)
			} else {
				label = "  " + label
			}
			frame.hits = append(frame.hits, homeHit{x: x, y: len(lines), width: inner, focus: focusLibrary, row: index, open: true})
			add(label)
		}
	}
	for len(lines) < h-3 {
		add("")
	}
	add(benchRule(inner))
	notice := m.home.notice
	if m.home.err != "" {
		add(benchTheme.Error.Render(m.home.err))
	} else if notice != "" {
		add(benchTheme.Accent.Render(notice))
	} else if len(indices) > 0 {
		add(benchTheme.Muted.Render(fmt.Sprintf("%d–%d / %d 部 · 点击作品打开", start+1, end, len(indices))))
	} else {
		add("")
	}
	hint := "Tab 切区 · Enter 开始 / 打开 · Esc 退出"
	if m.home.focus == focusLibrary {
		hint = "↑↓ 选择 · / 搜索 · Enter 打开 · d 删除 · Esc 退出"
	}
	if m.home.searching {
		hint = "输入筛选 · Enter 选择作品 · Esc 清除搜索"
	}
	if m.home.editingChapters {
		hint = "输入目标章数 · Enter 确认 · Esc 取消"
	}
	add(benchTheme.Muted.Render(hint))
	frame.text = strings.Join(fitBlock(strings.Join(lines, "\n"), w, h), "\n")
	return frame
}

func homeControl(text string, selected bool) string {
	if selected {
		return benchTheme.Selected.Render(text)
	}
	return benchTheme.Muted.Render(text)
}

type homeAction struct {
	focus int
	label string
}

// homeActions 渲染一行控件并给出热区：热区与文字同一遍生成，几何不会漂移。
func homeActions(actions []homeAction, x, y, focus int) (string, []homeHit) {
	var line string
	hits := make([]homeHit, 0, len(actions))
	for i, action := range actions {
		if i > 0 {
			line += "   "
		}
		hits = append(hits, homeHit{x: x + lipgloss.Width(line), y: y, width: lipgloss.Width(action.label), focus: action.focus, row: -1, open: true})
		line += homeControl(action.label, action.focus == focus)
	}
	return line, hits
}

func (m model) libraryIndices() []int {
	query := strings.ToLower(strings.TrimSpace(m.home.search.Value()))
	indices := make([]int, 0, len(m.home.library))
	for i, entry := range m.home.library {
		if query == "" || strings.Contains(strings.ToLower(entry.premise+" "+entry.id), query) {
			indices = append(indices, i)
		}
	}
	return indices
}

func (m model) beginLibrarySearch() (tea.Model, tea.Cmd) {
	m.home.searching = true
	m.home.focus = focusSearch
	m.home.premise.Blur()
	m.home.confirmDelete, m.home.notice = "", ""
	return m, m.home.search.Focus()
}

func (m model) handleHomeInline(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := &m.home
	if h.editingChapters {
		switch key.Type {
		case tea.KeyEsc:
			h.editingChapters = false
			h.chapterInput.Blur()
			h.err = ""
			return m, nil
		case tea.KeyEnter:
			n, err := strconv.Atoi(strings.TrimSpace(h.chapterInput.Value()))
			if err != nil || n < 1 {
				h.err = "请输入正整数章数"
				return m, nil
			}
			h.chapters = n
			h.editingChapters = false
			h.chapterInput.Blur()
			h.err = ""
			return m, nil
		}
		var cmd tea.Cmd
		h.chapterInput, cmd = h.chapterInput.Update(key)
		return m, cmd
	}
	switch key.Type {
	case tea.KeyEsc:
		h.search.SetValue("")
		h.searching = false
		h.search.Blur()
		h.focus = focusLibrary
		h.cursor = 0
		return m, nil
	case tea.KeyEnter:
		h.searching = false
		h.search.Blur()
		h.focus = focusLibrary
		return m, nil
	}
	var cmd tea.Cmd
	h.search, cmd = h.search.Update(key)
	indices := m.libraryIndices()
	if len(indices) > 0 {
		h.cursor = indices[0]
	}
	return m, cmd
}

func (m model) moveLibrary(key tea.KeyType) model {
	indices := m.libraryIndices()
	if len(indices) == 0 {
		return m
	}
	pos := 0
	for i, index := range indices {
		if index == m.home.cursor {
			pos = i
			break
		}
	}
	switch key {
	case tea.KeyUp:
		pos--
	case tea.KeyDown:
		pos++
	case tea.KeyHome:
		pos = 0
	case tea.KeyEnd:
		pos = len(indices) - 1
	case tea.KeyPgUp:
		pos -= m.homeFrame().capacity
	case tea.KeyPgDown:
		pos += m.homeFrame().capacity
	}
	m.home.cursor = indices[min(max(0, pos), len(indices)-1)]
	return m
}

func (m model) handleHomeMouse(mouse tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.home.mode != homeMain || m.home.searching || m.home.editingChapters {
		return m, nil
	}
	frame := m.homeFrame()
	if mouse.Button == tea.MouseButtonWheelUp || mouse.Button == tea.MouseButtonWheelDown {
		if mouse.Y >= frame.libraryY && mouse.Y < frame.libraryY+frame.capacity {
			m.home.premise.Blur()
			m.home.focus = focusLibrary
			key := tea.KeyDown
			if mouse.Button == tea.MouseButtonWheelUp {
				key = tea.KeyUp
			}
			return m.moveLibrary(key), nil
		}
		return m, nil
	}
	if mouse.Action != tea.MouseActionPress || mouse.Button != tea.MouseButtonLeft {
		return m, nil
	}
	for _, hit := range frame.hits {
		if mouse.Y != hit.y || mouse.X < hit.x || mouse.X >= hit.x+hit.width {
			continue
		}
		m.home.confirmDelete, m.home.notice = "", ""
		m.home.premise.Blur()
		m.home.focus = hit.focus
		if hit.row >= 0 {
			m.home.cursor = hit.row
		}
		if hit.focus == focusPremise {
			return m, m.home.premise.Focus()
		}
		if hit.open || hit.focus == focusConfig {
			return m.handleHomeKey(tea.KeyMsg{Type: tea.KeyEnter})
		}
		return m, nil
	}
	return m, nil
}
