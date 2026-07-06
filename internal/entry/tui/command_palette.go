package tui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/skills"
)

// 条目类型。
//   - kindCommand：内置 / 命令（help / diag / model / ...）
//   - kindSkill：跨书 skill 库条目
//   - kindSkillEntry：命令列表里的 "Skill ▸" 子菜单入口，不直接补全
const (
	kindCommand    = "command"
	kindSkill      = "skill"
	kindSkillEntry = "skill-entry"
)

// paletteLevel 标识菜单层级。
//   - paletteLevelCommands：一级，显示"命令 + Skill 入口"混合列表
//   - paletteLevelSkills：选中 Skill 入口后展开的子菜单，只显示 skill 库内容
type paletteLevel int

const (
	paletteLevelCommands paletteLevel = iota
	paletteLevelSkills
)

type commandPaletteItem struct {
	Name        string
	Aliases     []string
	Usage       string
	Description string
	AutoExecute bool
	Kind        string // 缺省 kindCommand
	Category    string // 仅 skill 用：genres / structures / ...
}

func builtinCommandItems() []commandPaletteItem {
	return commandRegistryInstance().PaletteItems()
}

// skillPaletteItems 从 store 抽出当前所有 skill 元数据并转为菜单条目。
// store 为 nil（库未启用）时返回 nil —— 子菜单为空，但入口仍可见。
func skillPaletteItems(store *skills.Store) []commandPaletteItem {
	if store == nil {
		return nil
	}
	metas := store.List("")
	items := make([]commandPaletteItem, 0, len(metas))
	for _, m := range metas {
		desc := m.Description
		if desc == "" {
			desc = "（无描述）"
		}
		items = append(items, commandPaletteItem{
			Name:        m.Name,
			Description: desc,
			Usage:       "/" + m.Name + " [补充要求]",
			Kind:        kindSkill,
			Category:    m.Category,
		})
	}
	return items
}

// commandsWithSkillEntry 在内置命令后追加 "Skill ▸" 子菜单入口。
// 入口始终存在（即使 skill 库为空），描述里给出数量提示让用户判断是否值得展开。
// 这样 skill 路径可发现，又不污染命令列表的高频入口（help/diag/...）。
func commandsWithSkillEntry(store *skills.Store) []commandPaletteItem {
	items := append([]commandPaletteItem(nil), builtinCommandItems()...)

	skillCount := 0
	if store != nil {
		skillCount = len(store.List(""))
	}
	desc := "本地题材 / 结构 / 风格 skill 库"
	if skillCount == 0 {
		desc = "（skill 库为空，用 'ainovel skill add' 添加）"
	} else {
		desc = desc + "（" + strconv.Itoa(skillCount) + " 项）"
	}

	items = append(items, commandPaletteItem{
		Name:        "Skill",
		Description: desc,
		Usage:       "→ 展开 skill 库",
		Kind:        kindSkillEntry,
	})
	return items
}

func scoreCommandItem(item commandPaletteItem, query string) int {
	if query == "" {
		base := 100
		switch item.Kind {
		case kindSkill:
			base = 60
		case kindSkillEntry:
			// Skill 入口在空查询时排末尾：它是个"补充路径"，不希望抢占 help/diag 等高频命令的视线。
			// 用户主动输入 "skill" 时下面精确匹配分支会让它浮顶。
			base = 40
		}
		return base
	}

	name := strings.ToLower(item.Name)
	desc := strings.ToLower(item.Description)
	usage := strings.ToLower(item.Usage)
	aliases := strings.ToLower(strings.Join(item.Aliases, " "))
	category := strings.ToLower(item.Category)

	switch {
	case name == query:
		return 1200
	case slices.ContainsFunc(item.Aliases, func(alias string) bool { return strings.EqualFold(alias, query) }):
		return 1100
	case strings.HasPrefix(name, query):
		return 900 - min(len(name)-len(query), 40)
	case aliases != "" && strings.Contains(aliases, query):
		return 700
	case strings.Contains(name, query):
		return 650
	case strings.Contains(desc, query):
		return 420
	case category != "" && strings.Contains(category, query):
		return 380
	case strings.Contains(usage, query):
		return 360
	default:
		return 0
	}
}

// sortAndFilter 共用排序+过滤。query 非空时丢弃零分条目。
func sortAndFilter(items []commandPaletteItem, query string) []commandPaletteItem {
	slices.SortStableFunc(items, func(a, b commandPaletteItem) int {
		scoreA := scoreCommandItem(a, query)
		scoreB := scoreCommandItem(b, query)
		if scoreA != scoreB {
			return scoreB - scoreA
		}
		return strings.Compare(a.Name, b.Name)
	})
	if query == "" {
		return items
	}
	out := items[:0]
	for _, it := range items {
		if scoreCommandItem(it, query) > 0 {
			out = append(out, it)
		}
	}
	return out
}

// commandCompletions 一级菜单用：返回"内置命令 + Skill 入口"，按 query 过滤。
// Skill 入口在 query 命中 "skill" 时排前，否则随 base 分排在末尾（保持命令优先）。
func commandCompletions(prefix string, store *skills.Store) []commandPaletteItem {
	query := strings.TrimSpace(strings.ToLower(prefix))
	items := commandsWithSkillEntry(store)
	return sortAndFilter(items, query)
}

// skillCompletions 二级子菜单用：只返回 skill 库条目。
func skillCompletions(prefix string, store *skills.Store) []commandPaletteItem {
	query := strings.TrimSpace(strings.ToLower(prefix))
	items := skillPaletteItems(store)
	return sortAndFilter(items, query)
}

func (m *Model) clearCommandPalette() {
	m.compItems = nil
	m.compIdx = 0
	m.compActive = false
	m.compLevel = paletteLevelCommands
}

// enterSkillSubMenu 从命令列表进入 Skill 子菜单：清空 textarea 到 "/" 重新过滤。
// 用 Esc 返回时调 exitSkillSubMenu。
func (m *Model) enterSkillSubMenu() {
	m.compLevel = paletteLevelSkills
	m.compIdx = 0
	m.textarea.Reset()
	m.textarea.SetValue("/")
	m.textarea.CursorEnd()
	m.refreshCommandPaletteItems()
}

// exitSkillSubMenu 从 Skill 子菜单返回命令列表：恢复 textarea 到 "/" 并重建一级列表。
func (m *Model) exitSkillSubMenu() {
	m.compLevel = paletteLevelCommands
	m.compIdx = 0
	m.textarea.Reset()
	m.textarea.SetValue("/")
	m.textarea.CursorEnd()
	m.refreshCommandPaletteItems()
}

// refreshCommandPaletteItems 按 compLevel 重新计算 compItems。
// 抽出来避免 update / enter / exit 路径重复构造。
func (m *Model) refreshCommandPaletteItems() {
	var store *skills.Store
	if m.runtime != nil {
		store = m.runtime.SkillStore()
	}

	text := strings.TrimSpace(m.textarea.Value())
	query := strings.TrimPrefix(text, "/")

	switch m.compLevel {
	case paletteLevelSkills:
		m.compItems = skillCompletions(query, store)
	default:
		m.compItems = commandCompletions(query, store)
	}

	m.compActive = len(m.compItems) > 0
	if !m.compActive {
		m.compIdx = 0
		return
	}
	if m.compIdx >= len(m.compItems) {
		m.compIdx = max(0, len(m.compItems)-1)
	}
}

func (m *Model) updateCommandPalette() {
	text := strings.TrimSpace(m.textarea.Value())
	if !strings.HasPrefix(text, "/") {
		m.clearCommandPalette()
		return
	}
	if strings.ContainsAny(text, " \t") {
		m.clearCommandPalette()
		return
	}
	m.refreshCommandPaletteItems()
}

func (m *Model) selectedCommandItem() (commandPaletteItem, bool) {
	if !m.compActive || m.compIdx < 0 || m.compIdx >= len(m.compItems) {
		return commandPaletteItem{}, false
	}
	return m.compItems[m.compIdx], true
}

// acceptCommandCompletion 接受当前选中条目。
//   - 一级 + Skill 入口：进入子菜单（不写入 textarea）
//   - 一级 + 普通命令 / 二级 + skill：补全到 "/<name> " 让用户附加消息
//
// 返回 (item, true) 表示已补全（调用方可继续 AutoExecute 流程）；
// 返回 (item, false) 表示"已切换层级，textarea 已重置，等待下一轮按键"。
func (m *Model) acceptCommandCompletion() (commandPaletteItem, bool) {
	item, ok := m.selectedCommandItem()
	if !ok {
		return commandPaletteItem{}, false
	}

	// Skill 子菜单入口：进入子菜单
	if m.compLevel == paletteLevelCommands && item.Kind == kindSkillEntry {
		m.enterSkillSubMenu()
		return item, false
	}

	// 普通条目：补全到 /<name> 让用户继续
	m.textarea.Reset()
	m.textarea.SetValue("/" + item.Name + " ")
	m.textarea.CursorEnd()
	m.compItems = nil
	m.compIdx = 0
	m.compActive = false
	return item, true
}

func renderCommandPalette(width int, level paletteLevel, items []commandPaletteItem, cursor int) string {
	if len(items) == 0 || width <= 0 {
		return ""
	}

	boxW := width - 2
	if boxW > 84 {
		boxW = 84
	}
	if boxW < 48 {
		boxW = 48
	}
	contentW := paddedModalContentWidth(boxW)
	if contentW < 20 {
		contentW = 20
	}
	start, end := commandPaletteWindow(len(items), cursor, 5)
	visible := items[start:end]
	remaining := len(items) - end

	// 配色：command 主色（金）；skill 次色（青绿）；Skill 入口用次色但加 ▸ 强调
	nameStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	skillNameStyle := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	entryNameStyle := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(bodyTextColor)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	selectedNameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#1c1a14")).Background(colorAccent).Bold(true)
	selectedDescStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	title := "命令"
	hint := "↑↓ 选择 · Enter/Tab 接受 · → 展开 Skill · Esc 关闭"
	if level == paletteLevelSkills {
		title = "Skill 库"
		hint = "↑↓ 选择 · Enter/Tab 接受 · ←/Esc 返回上级"
	}

	var body []string
	selectedIdx := cursor - start
	for i, item := range visible {
		prefix := "  "
		nameRenderer := nameStyle
		descRenderer := descStyle
		displayName := item.Name
		switch item.Kind {
		case kindSkill:
			nameRenderer = skillNameStyle
			cat := strings.TrimSpace(item.Category)
			if cat == "" {
				cat = "skill"
			}
			displayName = "[" + cat + "] " + item.Name
		case kindSkillEntry:
			nameRenderer = entryNameStyle
			displayName = item.Name + " ▸"
		}
		if i == selectedIdx {
			prefix = "› "
			nameRenderer = selectedNameStyle
			descRenderer = selectedDescStyle
		}

		// 动态算 desc 可用宽度：contentW - prefix(2) - displayName 宽 - gap(1)
		// name 超长先截断 name；desc 至少留 8 列。
		nameAvail := contentW - 2 - 1 - 8
		if nameAvail < 8 {
			nameAvail = 8
		}
		if lipgloss.Width(displayName) > nameAvail {
			displayName = truncateWidth(displayName, nameAvail)
		}
		descAvail := contentW - 2 - lipgloss.Width(displayName) - 1
		if descAvail < 8 {
			descAvail = 8
		}
		desc := truncateWidth(item.Description, descAvail)

		nameView := nameRenderer.Render(displayName)
		descText := descRenderer.Render(desc)
		gap := contentW - 2 - lipgloss.Width(displayName) - lipgloss.Width(desc)
		if gap < 1 {
			gap = 1
		}
		body = append(body, prefix+nameView+strings.Repeat(" ", gap)+descText)
	}

	if selectedIdx < 0 || selectedIdx >= len(visible) {
		selectedIdx = 0
	}
	usage := "Usage: " + visible[selectedIdx].Usage
	if remaining > 0 {
		usage = usage + " · 还有 " + strconv.Itoa(remaining) + " 项"
	}
	usageLine := mutedStyle.Render(truncateWidth(usage, contentW))
	body = append(body, usageLine+strings.Repeat(" ", max(0, contentW-lipgloss.Width(usageLine))))
	hintLine := mutedStyle.Render(hint)
	body = append(body, hintLine+strings.Repeat(" ", max(0, contentW-lipgloss.Width(hintLine))))

	return renderPaddedModalFrame(boxW, len(body)+2, title, "", body)
}

func commandPaletteWindow(total, cursor, limit int) (start, end int) {
	if total <= limit {
		return 0, total
	}
	start = max(cursor-limit/2, 0)
	end = min(start+limit, total)
	if end-start < limit {
		start = max(end-limit, 0)
	}
	return start, end
}
