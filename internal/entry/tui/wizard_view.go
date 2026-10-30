package tui

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/voocel/ainovel-cli/internal/app/binding"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// 模型设置几何：外框与首页相同，内容是居中的一栏，紧贴顶栏下方从上往下排（表单不随内容
// 多少上下浮动）。每一行的文字与热区在同一遍里生成。

const (
	wizardWidth = 76 // 内容栏最宽
	wizardLabel = 12 // 表单、角色与思考强度的标签列
)

type wizardFrame struct {
	text     string
	hits     []pageHit
	list     homeRect // 列表区：滚轮在这里移动选中行
	capacity int      // 列表一屏能放下的行数
}

// wizardSection 是一页内容之外的信息：底栏按键与右侧说明、列表在栏内的起始行与行数。
type wizardSection struct {
	hint, aside      string
	listTop, listLen int
	capacity         int
}

func (m model) viewWizard() string { return m.wizardFrame().text }

func (m model) wizardFrame() wizardFrame {
	w, h := max(1, m.width), max(1, m.height)
	width := min(wizardWidth, max(1, w-2*pageGutter-4))
	x := (w - width) / 2
	body := max(1, h-pageHeader-pageFooter)
	c := pageColumn{width: width}
	var section wizardSection
	switch m.wizard.step {
	case wizardOverview:
		section = m.overviewView(&c, body-1)
	case wizardProviders:
		section = m.providersView(&c)
	case wizardConnection:
		section = m.connectionView(&c)
	case wizardModels:
		section = m.modelsView(&c, body-1)
	default:
		section = m.roleView(&c, body-1)
	}
	top := pageHeader + 1
	frame := wizardFrame{capacity: section.capacity}
	frame.list = homeRect{x: x - pageGutter, y: top + section.listTop, width: width + 2*pageGutter, height: section.listLen}
	lines := pageTop("模型设置", "", w)
	pad := strings.Repeat(" ", x-pageGutter)
	for y := pageHeader; y < pageHeader+body; y++ {
		lines = append(lines, pad+lineAt(c.lines, y-top))
	}
	for _, hit := range c.hits {
		hit.x, hit.y = hit.x+x, hit.y+top
		if hit.y < pageHeader+body {
			frame.hits = append(frame.hits, hit)
		}
	}
	feedback := problemFeedback(m.wizard.err)
	if feedback == "" && m.wizard.notice != "" {
		style := benchTheme.Accent
		if m.wizard.confirm != (overviewItem{}) {
			style = benchTheme.Warning
		}
		feedback = style.Render(m.wizard.notice)
	}
	lines = append(lines, m.pageBottom(feedback, section.hint+" · Ctrl+C 退出", section.aside, w)...)
	frame.text = strings.Join(fitBlock(strings.Join(lines, "\n"), w, h), "\n")
	return frame
}

// --- 总览 ---

func (m model) overviewView(c *pageColumn, rows int) wizardSection {
	w := m.wizard
	items := m.overviewItems()
	c.add(sectionHead(benchTheme.Title.Render("当前"), "", c.width))
	c.add("")
	m.roleLine(c, 0, "")

	levels, at := m.thinkingLevels("")
	line := rowLabel(w.cursor == 1, "思考强度", wizardLabel)
	for i, level := range levels {
		text := " " + thinkingLabel(level) + " "
		style := benchTheme.Muted
		if i == at {
			style = benchTheme.Selected
		}
		c.hit(lipgloss.Width(line), lipgloss.Width(text), itemThinking, 1, i)
		line += style.Render(text) + " "
	}
	if at < 0 {
		line += benchTheme.Muted.Render(" 已设 " + thinkingLabel(m.api.Models.Config().Thinking) + "，这个模型不支持，按 auto 发")
	}
	c.mark(w.cursor == 1, line)
	c.add("")
	head := slices.IndexFunc(items, func(item overviewItem) bool { return item.kind == itemConnection || item.kind == itemAddConnection })
	for i := 2; i < head; i++ {
		m.roleLine(c, i, items[i].role)
	}
	c.add("")
	c.add("")
	c.add(sectionHead(benchTheme.Title.Render("连接与模型"), "", c.width))
	c.add("")

	// 列表装不下时开窗；末尾的配置位置占两行。
	listTop := len(c.lines)
	start, end := overviewWindow(items, head, max(head, w.cursor), max(1, rows-listTop-2))
	for i := start; i < end; i++ {
		if i > start && groupStart(items[i]) {
			c.add("")
		}
		c.item(m.overviewRow(items[i], c.width, i == w.cursor), items[i].kind, i)
	}
	listLen := len(c.lines) - listTop
	c.add("")
	c.add(benchTheme.Muted.Render("配置保存在 " + homePath(appconfig.ConfigPath(m.deps.ConfigDir))))
	return wizardSection{hint: m.overviewHint(items[w.cursor]), listTop: listTop, listLen: listLen, capacity: listLen}
}

// groupStart 报告这一行是否开始新的一组（连接或「添加连接」）：组与组之间空一行。
func groupStart(item overviewItem) bool {
	return item.kind == itemConnection || item.kind == itemAddConnection
}

// overviewWindow 是连接列表（items[head:]）在 room 行里的可见区间：组间空行也占行（窗口
// 首行之前的不算），从选中行向上下交替扩展，让它留在窗口中部。
func overviewWindow(items []overviewItem, head, cursor, room int) (int, int) {
	// height 是 items[i] 接在窗口里另一行之后占的行数：新的一组前面多一个空行。往上扩展时
	// 原来的首行有了上一行，它前面的空行这时才出现，所以代价是 height(start)。
	height := func(i int) int {
		if groupStart(items[i]) {
			return 2
		}
		return 1
	}
	start, end, used := cursor, cursor+1, 1
	for grew := true; grew; {
		grew = false
		if end < len(items) && used+height(end) <= room {
			used, end, grew = used+height(end), end+1, true
		}
		if start > head && used+height(start) <= room {
			used, start, grew = used+height(start), start-1, true
		}
	}
	return start, end
}

// roleLine 是「当前」里默认（role 为空）或一个角色的行：所用的模型，右侧是默认的上下文窗口
// 或角色自己的思考强度。整行可点。
func (m model) roleLine(c *pageColumn, index int, role string) {
	focused := m.wizard.cursor == index
	current := m.api.Models.Current(role)
	value := benchTheme.Text.Render(current.Connection + " / " + current.Model)
	right := ""
	switch {
	case role == "":
		value = benchTheme.Title.Render(current.Connection + " / " + current.Model)
		if current.ContextWindow > 0 {
			right = "上下文 " + formatWindow(current.ContextWindow)
		}
	case current.Inherited:
		value = benchTheme.Muted.Render("跟随默认")
	default:
		thinking := m.api.Models.Config().Roles[role].Thinking
		if thinking == "" {
			thinking = levelInherit
		}
		right = "思考 " + thinkingLabel(thinking)
	}
	c.hit(0, c.width, itemRole, index, -1)
	c.mark(focused, alignRight(rowLabel(focused, roleNames[role], wizardLabel)+value, benchTheme.Muted.Render(right), c.width))
}

func (m model) overviewHint(item overviewItem) string {
	var hint string
	switch item.kind {
	case itemRole:
		hint = "Enter 换模型"
		if _, ok := m.api.Models.Config().Roles[item.role]; ok {
			hint += " · ←→ 思考强度"
		}
	case itemThinking:
		hint = "←→ 思考强度"
	case itemConnection:
		hint = "Enter 编辑 · d 删除"
	case itemModel:
		hint = "Enter 换用 · d 删除"
	case itemAddModel:
		hint = "Enter 从服务商列出模型"
	default:
		hint = "Enter 添加连接"
	}
	hint += " · ↑↓ 选择"
	switch {
	case m.wizard.fromHome:
		hint += " · Esc 返回"
	case m.api.Models.Current("").Bound:
		hint += " · Esc 去首页"
	}
	return hint
}

func (m model) overviewRow(item overviewItem, width int, selected bool) string {
	config := m.api.Models.Config()
	switch item.kind {
	case itemConnection:
		left := []styled{{item.connection, benchTheme.Title}, {"   " + connectionLabel(item.connection, config.Providers[item.connection]), benchTheme.Muted}}
		return listRow(left, []styled{{"编辑 ›", benchTheme.Muted}}, width, selected)
	case itemModel:
		mark, tone := "  ○ ", benchTheme.Muted
		if item.connection == config.Provider && item.model == config.Model {
			mark, tone = "  ● ", benchTheme.Accent
		}
		var right []styled
		if window := m.api.Models.ContextWindow(item.connection, item.model); window > 0 {
			right = []styled{{formatWindow(window), benchTheme.Muted}}
		}
		return listRow([]styled{{mark, tone}, {item.model, benchTheme.Text}}, right, width, selected)
	case itemAddModel:
		return listRow([]styled{{"  + 添加模型", benchTheme.Accent}}, nil, width, selected)
	default:
		return listRow([]styled{{"+ 添加连接", benchTheme.Accent}}, nil, width, selected)
	}
}

// connectionLabel 说明一个连接连的是哪里：内置服务商写服务商名，中转写格式；有自定义地址时
// 带上主机名。
func connectionLabel(name string, pc appconfig.ProviderConfig) string {
	kind := pc.Type
	if kind == "" {
		kind = name
	}
	label := kind
	for _, preset := range wizardPresets {
		if preset.kind == kind {
			label = preset.label
		}
	}
	if renamable(kind) && (name != kind || pc.BaseURL != "") {
		for _, format := range wizardFormats {
			if format.kind == kind {
				label = format.label
			}
		}
	}
	if u, err := url.Parse(pc.BaseURL); err == nil && u.Host != "" {
		label += " · " + u.Host
	}
	return label
}

func homePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// --- 角色 ---

func (m model) roleView(c *pageColumn, rows int) wizardSection {
	w := m.wizard
	c.add(sectionHead(benchTheme.Title.Render("选择模型 · "+roleNames[w.role]), "", c.width))
	c.add("")
	choices := m.roleChoices()
	listTop := len(c.lines)
	capacity := max(1, rows-listTop-2)
	start, end := listWindow(len(choices), w.cursor, capacity)
	for i := start; i < end; i++ {
		c.item(m.choiceLine(choices[i], c.width, i == w.cursor), 0, i)
	}
	c.add("")
	c.add(benchTheme.Muted.Render("这里只有已保存的模型；要用别的，回总览给连接「+ 添加模型」"))
	return wizardSection{hint: "↑↓ 选择 · Enter 使用 · Esc 返回", listTop: listTop, listLen: end - start, capacity: capacity}
}

// choiceLine 是换模型时的一个选项：● 是正在用的；「跟随默认」右侧写默认此刻的模型。
func (m model) choiceLine(choice binding.Choice, width int, selected bool) string {
	mark, tone := "○ ", benchTheme.Muted
	if m.roleUses(m.wizard.role, choice) {
		mark, tone = "● ", benchTheme.Accent
	}
	if choice.Connection == "" {
		current := m.api.Models.Current("")
		return listRow([]styled{{mark, tone}, {"跟随默认", benchTheme.Text}}, []styled{{current.Connection + " / " + current.Model, benchTheme.Muted}}, width, selected)
	}
	var right []styled
	if window := m.api.Models.ContextWindow(choice.Connection, choice.Model); window > 0 {
		right = []styled{{formatWindow(window), benchTheme.Muted}}
	}
	left := []styled{{mark, tone}, {choice.Connection + " / ", benchTheme.Muted}, {choice.Model, benchTheme.Text}}
	return listRow(left, right, width, selected)
}

// --- 服务商 ---

func (m model) providersView(c *pageColumn) wizardSection {
	w := m.wizard
	saved := len(m.api.Models.Connections()) > 0
	if !saved {
		c.add(benchTheme.Text.Render("先连上一个模型服务，就能开始写。") + benchTheme.Muted.Render("密钥只保存在本机。"))
		c.add("")
		c.add("")
	}
	c.add(sectionHead(benchTheme.Title.Render("选择服务商"), "", c.width))
	c.add("")
	listTop := len(c.lines)
	for i, preset := range wizardPresets {
		left := []styled{{padLine(preset.label, 16), benchTheme.Text}, {preset.note, benchTheme.Muted}}
		c.item(listRow(left, nil, c.width, i == w.cursor), 0, i)
	}
	hint := "↑↓ 选择 · Enter 下一步"
	if saved {
		hint += " · Esc 返回"
	}
	return wizardSection{hint: hint, listTop: listTop, listLen: len(wizardPresets), capacity: len(wizardPresets)}
}

// --- 连接 ---

var formLabels = map[formRow]string{rowFormat: "格式", rowAddress: "地址", rowKey: "API Key", rowEndpoint: "接口", rowName: "名称"}

func (m model) connectionView(c *pageColumn) wizardSection {
	w := m.wizard
	name, _ := w.draft()
	title := map[wizardMode]string{addConnection: "添加连接 · ", editConnection: "编辑连接 · ", addModel: "添加模型 · "}[w.mode]
	if w.original == "" {
		title += wizardPresets[w.preset].label
	} else {
		title += name
	}
	c.add(sectionHead(benchTheme.Title.Render(title), "", c.width))
	c.add("")
	rows := w.formRows()
	for _, row := range rows[:len(rows)-1] {
		focused := w.row == row
		line := rowLabel(focused, formLabels[row], wizardLabel)
		switch row {
		case rowFormat, rowEndpoint:
			labels, selected := make([]string, 0, 3), 0
			if row == rowFormat {
				for _, format := range wizardFormats {
					labels = append(labels, format.label)
				}
				selected = w.format
			} else {
				for i, endpoint := range wizardEndpoints {
					labels = append(labels, endpoint.label)
					if endpoint.api == w.endpoint {
						selected = i
					}
				}
			}
			for i, label := range labels {
				text := benchTheme.Muted.Render("○ " + label)
				if i == selected {
					text = benchTheme.Accent.Render("● ") + benchTheme.Text.Bold(focused).Render(label)
				}
				c.hit(lipgloss.Width(line), lipgloss.Width(text), int(row), -1, i)
				line += text + strings.Repeat(" ", homeOptionGap)
			}
		default:
			input := w.inputs[map[formRow]int{rowAddress: inputAddress, rowKey: inputKey, rowName: inputName}[row]]
			line += inputView(input, c.width-lipgloss.Width(line))
		}
		c.hit(0, c.width, int(row), -1, -1)
		c.mark(focused, line)
		c.add("")
	}
	c.add(benchTheme.Muted.Render(m.formHelp()))
	c.add("")
	c.add("")
	button := "列出模型 ↵"
	switch {
	case w.listing:
		button = "正在连接服务商…"
	case w.mode == editConnection:
		button = "保存 ↵"
	}
	if w.row == rowSubmit {
		button = benchTheme.Selected.Render(" " + button + " ")
	} else {
		button = benchTheme.Accent.Bold(true).Render(button)
	}
	size := lipgloss.Width(button)
	c.hit(c.width-size, size, int(rowSubmit), -1, -1)
	c.add(padLeft(button, c.width))

	hint := "Tab 下一项 · Enter 列出模型 · Esc 返回"
	switch {
	case w.listing:
		hint = "正在连接服务商 · Esc 取消"
	case w.mode == editConnection:
		hint = "Tab 下一项 · Enter 验证并保存 · Esc 返回"
	}
	if w.row == rowFormat || w.row == rowEndpoint {
		hint = "←→ 选择 · " + hint
	}
	return wizardSection{hint: hint}
}

// formHelp 是焦点所在行的一句说明。
func (m model) formHelp() string {
	w := m.wizard
	switch w.row {
	case rowFormat:
		return "中转服务给的是哪种接口就选哪种，多数中转用 OpenAI 格式"
	case rowAddress:
		if w.preset == relayPreset {
			return "中转或自部署服务的接口地址"
		}
		return "用官方地址就留空"
	case rowKey:
		return "密钥只保存在本机的配置文件里"
	case rowEndpoint:
		return "不确定就用 Chat Completions；Responses 是 OpenAI 的新接口"
	case rowName:
		return "用来在多个连接之间区分它"
	}
	if w.mode == editConnection {
		return "回车先向服务商验证密钥，再保存"
	}
	return "回车向服务商列出这组密钥能用的模型，顺带验证密钥"
}

// --- 模型 ---

func (m model) modelsView(c *pageColumn, rows int) wizardSection {
	w := m.wizard
	name, _ := w.draft()
	count := ""
	if !w.listing && len(w.options) > 0 {
		count = benchTheme.Muted.Render(fmt.Sprintf("%d 个", len(w.options)))
	}
	c.add(sectionHead(benchTheme.Title.Render("选择模型 · "+name), count, c.width))
	c.add("")
	c.mark(true, inputView(w.filter, c.width-2))
	c.add("")
	switch {
	case w.listing:
		c.add(benchTheme.Muted.Render("正在向服务商获取模型列表…"))
		return wizardSection{hint: "正在获取 · Esc 取消"}
	case w.trying != "":
		c.add(benchTheme.Muted.Render("正在试用「" + w.trying + "」：发一句极短的对话，确认它能用…"))
		return wizardSection{hint: "正在试用 · Esc 取消"}
	}
	if w.unlisted != "" {
		c.add(benchTheme.Warning.Render("没能列出模型：") + benchTheme.Muted.Render(oneLine(w.unlisted)))
		c.add(benchTheme.Muted.Render("直接输入模型 ID，回车先试一句，能用就用上"))
		c.add("")
	}
	picks := w.pickerRows()
	if slices.ContainsFunc(picks, func(row pickerRow) bool {
		return row.option != nil && (row.option.ContextWindow > 0 || row.option.Input > 0)
	}) {
		c.add(padLeft(benchTheme.Muted.Render(pickerFacts("上下文", "$/百万 token 输入 / 输出")), c.width))
	}
	listTop := len(c.lines)
	capacity := max(1, rows-listTop)
	start, end := listWindow(len(picks), w.cursor, capacity)
	for i := start; i < end; i++ {
		c.item(w.pickerLine(picks[i], c.width, i == w.cursor), 0, i)
	}
	return wizardSection{hint: "输入筛选 · ↑↓ 选择 · Enter 使用 · Esc 返回", listTop: listTop, listLen: end - start, capacity: capacity}
}

func (w wizardState) pickerLine(row pickerRow, width int, selected bool) string {
	if row.option == nil {
		return listRow([]styled{{"↵ 使用「" + row.id + "」", benchTheme.Accent}, {"  先试一句确认能用", benchTheme.Muted}}, nil, width, selected)
	}
	mark := "  "
	if slices.Contains(w.models, row.id) {
		mark = "✓ "
	}
	left := []styled{{mark, benchTheme.Accent}, {row.id, benchTheme.Text}}
	if name := row.option.Name; name != "" && name != row.id {
		left = append(left, styled{"  " + name, benchTheme.Muted})
	}
	window, price := "", ""
	if row.option.ContextWindow > 0 {
		window = formatWindow(row.option.ContextWindow)
	}
	if row.option.Input > 0 || row.option.Output > 0 {
		price = fmt.Sprintf("$%.2f / $%.2f", row.option.Input, row.option.Output)
	}
	return listRow(left, []styled{{pickerFacts(window, price), benchTheme.Muted}}, width, selected)
}

// pickerFacts 是模型行右侧的两列：上下文窗口与标价，表头同一套宽度。
func pickerFacts(window, price string) string {
	return padLeft(window, 6) + padLeft(price, 26)
}
