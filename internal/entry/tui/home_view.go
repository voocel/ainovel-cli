package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

// 首页几何：外框与工作台相同（顶栏品牌与模型、底栏反馈与按键）；主体是居中的一栏，
// 上面写新故事，下面是作品库。每一行的文字与热区在同一遍里生成，渲染与鼠标命中不会漂移。

const (
	premiseRows   = 3   // 故事输入框的可见行数
	homeColumnMax = 104 // 内容栏最宽：再宽的终端也不把作品行拉成长条
	homeLabel     = 8   // 选项行的标签列
	homeOptionGap = 4   // 同一行选项之间的间隔
)

// 作品行的定宽列（书名占余下的宽度）：进度条、章数、状态、时间。
const (
	libraryBar   = 10
	libraryCount = 11
	libraryState = 10
	libraryWhen  = 14 // 2025年12月31日
)

// settingsSummary 是更多设定未填时的摘要；它是选项行里最长的一段，说明列排在它后面。
var (
	settingsSummary = func() string {
		labels := make([]string, len(settingFields))
		for i, field := range settingFields {
			labels[i] = field.label
		}
		return strings.Join(labels, " · ")
	}()
	homeNoteX = homeLabel + lipgloss.Width(settingsSummary) + homeOptionGap
)

type homeRect struct{ x, y, width, height int }

func (r homeRect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.width && y >= r.y && y < r.y+r.height
}

type homeFrame struct {
	text     string
	hits     []pageHit
	capacity int      // 作品库一屏能放下的作品数
	list     homeRect // 作品库列表区：滚轮在这里翻动
}

// homeLayout 是内容栏的水平几何：x 是内容左缘，两侧各留 pageGutter 列。
type homeLayout struct{ x, width int }

func layoutHome(width int) homeLayout {
	inner := min(homeColumnMax, max(1, width-2*pageGutter-4))
	return homeLayout{x: (max(1, width) - inner) / 2, width: inner}
}

// premiseWidth 是故事输入框的文字宽度：输入面左右各留两格。
func premiseWidth(width int) int { return max(1, layoutHome(width).width-4) }

func (m model) homeFrame() homeFrame {
	w, h := max(1, m.width), max(1, m.height)
	l := layoutHome(w)
	body := max(1, h-pageHeader-pageFooter)
	var frame homeFrame

	config := homeControl("模型设置", m.home.focus == focusConfig)
	binding := benchTheme.Muted.Render(truncate(m.bindingLabel(), w/2))
	lines := pageTop("", binding+"   "+config, w)
	configWidth := lipgloss.Width(config)
	frame.hits = append(frame.hits, pageHit{x: w - 1 - configWidth, y: 0, width: configWidth, focus: focusConfig, row: -1, option: -1})

	c := pageColumn{width: l.width}
	m.composerSection(&c)
	c.add("")
	c.add("")
	listTop, capacity := m.librarySection(&c, body-2)
	// 内容不满一屏时整体落在上三分之一处；作品铺满时上下各留一行。
	top := pageHeader + max(1, (body-len(c.lines))/3)
	frame.capacity = capacity
	frame.list = homeRect{x: l.x - pageGutter, y: top + listTop, width: l.width + 2*pageGutter, height: capacity}

	pad := strings.Repeat(" ", l.x-pageGutter)
	for y := pageHeader; y < pageHeader+body; y++ {
		lines = append(lines, pad+lineAt(c.lines, y-top))
	}
	for _, hit := range c.hits {
		hit.x, hit.y = hit.x+l.x, hit.y+top
		if hit.y < pageHeader+body {
			frame.hits = append(frame.hits, hit)
		}
	}
	lines = append(lines, m.pageBottom(m.homeFeedback(), m.homeHint(), m.libraryPosition(capacity), w)...)
	frame.text = strings.Join(fitBlock(strings.Join(lines, "\n"), w, h), "\n")
	return frame
}

func lineAt(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	return lines[i]
}

// composerSection 新故事：故事输入，篇幅、推进与更多设定三行选项，开始按钮。选项全部摊开，
// 每行右侧的说明列写清所选项意味着什么。
func (m model) composerSection(c *pageColumn) {
	home := m.home
	c.add(sectionHead(benchTheme.Title.Render("新故事"), "", c.width))
	c.add("")

	// 输入框是一块铺底色的面，上下各留一行、左右各留两格。
	surface := lipgloss.NewStyle().Background(benchColors.InputBackground)
	block := append([]string{""}, premiseView(home.premise, surface)...)
	for _, line := range append(block, "") {
		c.hit(0, c.width, focusPremise, -1, -1)
		c.mark(home.focus == focusPremise, surface.Render("  ")+line+surface.Render(strings.Repeat(" ", max(0, c.width-2-lipgloss.Width(line)))))
	}
	c.add("")

	fixed, note, selected := "固定章数", "AI 按故事容量定篇幅与终局，写到合适处收官", 0
	switch {
	case home.fixed && home.chapters > 0:
		fixed, note, selected = fmt.Sprintf("固定 %d 章", home.chapters), fmt.Sprintf("全书写满 %d 章收官，AI 只定终局方向", home.chapters), 1
	case home.fixed:
		fixed, note, selected = "固定 __ 章", "直接输入章数：全书写满这么多章收官", 1
	}
	c.options(home.focus == focusLength, "篇幅", focusLength, []string{"AI 决定", fixed}, selected, note)

	labels := make([]string, len(approvalOrder))
	for i, policy := range approvalOrder {
		labels[i] = workbench.ApprovalLabel(policy)
	}
	c.options(home.focus == focusApproval, "推进", focusApproval, labels, slices.Index(approvalOrder, home.approval), approvalNote(home.approval))

	var filled []string
	for i, input := range home.settings {
		if strings.TrimSpace(input.Value()) != "" {
			filled = append(filled, settingFields[i].label)
		}
	}
	summary := benchTheme.Muted.Render(settingsSummary)
	if len(filled) > 0 {
		summary = benchTheme.Text.Render("已填 " + strings.Join(filled, "、"))
	}
	c.hit(0, c.width, focusSettings, -1, -1)
	c.mark(home.focus == focusSettings, noted(rowLabel(home.focus == focusSettings, "设定", homeLabel)+summary, "可选 · 你的原话原样保留，AI 不会改写"))
	c.add("")

	start := benchTheme.Accent.Bold(true).Render("开始创作 ↵")
	if home.focus == focusStart {
		start = benchTheme.Selected.Render(" 开始创作 ↵ ")
	}
	size := lipgloss.Width(start)
	c.hit(c.width-size, size, focusStart, -1, -1)
	c.add(padLeft(start, c.width))
}

// rowLabel 是选项与表单行的标签列：焦点所在行用强调色。
func rowLabel(focused bool, label string, width int) string {
	style := benchTheme.Muted
	if focused {
		style = benchTheme.Accent.Bold(true)
	}
	return padLine(style.Render(label), width)
}

// noted 把说明放进说明列，三行选项的说明上下对齐。
func noted(text, note string) string {
	return padLine(text, homeNoteX) + benchTheme.Muted.Render(note)
}

// options 渲染一行单选（● 选中、○ 未选）与所选项的说明；每个选项可直接点选。
func (c *pageColumn) options(focused bool, label string, focus int, options []string, selected int, note string) {
	line := rowLabel(focused, label, homeLabel)
	c.hit(0, homeLabel, focus, -1, -1)
	for i, option := range options {
		if i > 0 {
			line += strings.Repeat(" ", homeOptionGap)
		}
		text := benchTheme.Muted.Render("○ " + option)
		if i == selected {
			text = benchTheme.Accent.Render("● ") + benchTheme.Text.Bold(focused).Render(option)
		}
		c.hit(lipgloss.Width(line), lipgloss.Width(text), focus, -1, i)
		line += text
	}
	c.mark(focused, noted(line, note))
}

// premiseView 是故事输入的各行。bubbles textarea 的占位只有首行用占位色，续行与空行不铺底，
// 折行也不避开行首标点：占位按阅读折行排，首行交给 textarea（光标停在首字上闪烁），其余行自己画。
func premiseView(input textarea.Model, surface lipgloss.Style) []string {
	var placeholder []string
	if input.Value() == "" {
		placeholder = readingLines(premisePlaceholder, input.Width())
		input.Placeholder = placeholder[0]
	}
	rows := areaView(input)
	if placeholder != nil {
		faint := surface.Foreground(benchColors.Faint)
		for i := 1; i < len(rows); i++ {
			rows[i] = faint.Render(padLine(lineAt(placeholder, i), input.Width()))
		}
	}
	return rows
}

// approvalNote 说清每种推进方式会在哪里停下来等你（§5.4）。
func approvalNote(policy domainmodel.ApprovalPolicy) string {
	switch policy {
	case domainmodel.ApprovalMilestone:
		return "蓝图和每个新故事弧先给你过目，章节自动写"
	case domainmodel.ApprovalManual:
		return "蓝图和每一章都等你确认后才入稿"
	default:
		return "一路写下去，只在越界时停下来问你"
	}
}

func (h *homeState) choose(focus, option int) {
	switch focus {
	case focusLength:
		h.fixed = option == 1
	case focusApproval:
		h.approval = approvalOrder[option]
	}
}

// librarySection 作品库：标题行（搜索与导入）、作品表或空状态。rows 是内容栏可用的总行数；
// 返回列表起始行与一屏容量。
func (m model) librarySection(c *pageColumn, rows int) (int, int) {
	home := m.home
	title := benchTheme.Title.Render("作品库")
	if count := len(home.library); count > 0 {
		title += benchTheme.Muted.Render(fmt.Sprintf("  %d 部", count))
	}
	if home.searching {
		field := home.search
		field.Prompt = "/ "
		c.add(sectionHead(title, inputView(field, min(28, c.width/2)+2), c.width))
	} else {
		search := "/ 搜索"
		if query := home.search.Value(); query != "" {
			search = "/ " + truncate(query, 16)
		}
		search, importing := homeControl(search, home.focus == focusSearch), homeControl("导入作品", home.focus == focusImport)
		controls := search + "   " + importing
		c.hit(c.width-lipgloss.Width(controls), lipgloss.Width(search), focusSearch, -1, -1)
		c.hit(c.width-lipgloss.Width(importing), lipgloss.Width(importing), focusImport, -1, -1)
		c.add(sectionHead(title, controls, c.width))
	}
	c.add("")
	top := len(c.lines)
	capacity := max(1, rows-top)

	indices := m.libraryIndices()
	switch {
	case !home.loaded:
		c.add(benchTheme.Muted.Render("正在读取作品库…"))
	case len(indices) == 0 && home.search.Value() != "":
		c.add(benchTheme.Muted.Render("没有匹配「" + home.search.Value() + "」的作品"))
		c.add("")
		c.add(benchTheme.Muted.Render("Esc 清除搜索，显示全部作品"))
	case len(indices) == 0:
		c.add(benchTheme.Text.Render("还没有作品。") + benchTheme.Muted.Render("在上面写下一个故事想法，回车就开始："))
		c.add("")
		for i, step := range []string{
			"AI 先规划蓝图：卷、故事弧、章节与终局",
			"逐章写作；写完一段自动审阅，按意见重写",
			"途中随时提要求、改方向，关键处等你确认",
		} {
			c.add(benchTheme.Accent.Render(fmt.Sprintf("%d  ", i+1)) + benchTheme.Text.Render(step))
		}
	default:
		start, end := libraryWindow(indices, home.cursor, capacity)
		now := time.Now()
		for _, index := range indices[start:end] {
			selected := home.focus == focusLibrary && index == home.cursor
			c.item(libraryRow(home.library[index], c.width, selected, now), focusLibrary, index)
		}
	}
	return top, capacity
}

// libraryRow 是一部作品的一行：书名、进度条（全书章数已知时）、章数、状态与相对时间，
// 各列上下对齐。
func libraryRow(entry libraryEntry, width int, selected bool, now time.Time) string {
	done, rest, count := "", strings.Repeat(" ", libraryBar), ""
	switch {
	case entry.target > 0:
		filled := min(libraryBar, max(0, entry.written)*libraryBar/entry.target)
		done, rest = strings.Repeat("▰", filled), strings.Repeat("▱", libraryBar-filled)
		count = fmt.Sprintf("%d/%d 章", entry.written, entry.target)
	case entry.written > 0:
		count = fmt.Sprintf("%d 章", entry.written)
	}
	state, tone := entryState(entry)
	title := width - (3 + libraryBar + 1 + libraryCount + 4 + libraryState + 3 + libraryWhen)
	return listRow([]styled{
		{padLine(entry.title(), title) + "   ", benchTheme.Text},
		{done, benchTheme.Focus},
		{rest, benchTheme.Muted},
		{" " + padLeft(count, libraryCount), benchTheme.Muted},
		{"    " + padLine(state, libraryState), tone},
		{"   " + relativeTime(entry.updated, now), benchTheme.Muted},
	}, nil, width, selected)
}

// libraryWindow 是作品列表的可见区间：装不下时让选中作品留在窗口中部。
func libraryWindow(indices []int, cursor, capacity int) (int, int) {
	return listWindow(len(indices), max(0, slices.Index(indices, cursor)), capacity)
}

func (e libraryEntry) title() string {
	if e.premise == "" {
		return e.id
	}
	return oneLine(e.premise)
}

// entryState 是作品最近一轮创作的状态：带符号的短语与它的颜色，符号与工作台同一套。
// 运行标着进行中却没有任务在写，是上次没停就退出了：打开作品即接着写。
func entryState(entry libraryEntry) (string, lipgloss.Style) {
	switch entry.state {
	case "":
		return "○ 未开始", benchTheme.Muted
	case domainmodel.RunWaitingUser:
		return "◇ " + runStateLabel(entry.state), benchTheme.Warning
	case domainmodel.RunRunning:
		if entry.writing {
			return "◉ " + runStateLabel(entry.state), benchTheme.Focus
		}
		return "◌ 待续写", benchTheme.Accent
	case domainmodel.RunCompleted:
		return "✓ " + runStateLabel(entry.state), benchTheme.Success
	case domainmodel.RunFailed:
		return "! " + runStateLabel(entry.state), benchTheme.Error
	case domainmodel.RunPaused:
		return "‖ " + runStateLabel(entry.state), benchTheme.Muted
	default:
		return "· " + runStateLabel(entry.state), benchTheme.Muted
	}
}

// relativeTime 用人说话的方式写时间：刚刚、3 小时前、昨天、9月3日。
func relativeTime(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	at, now = at.Local(), now.Local()
	switch d := now.Sub(at); {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 48*time.Hour:
		return "昨天"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	case at.Year() == now.Year():
		return at.Format("1月2日")
	default:
		return at.Format("2006年1月2日")
	}
}

// homeFeedback 是嵌在底栏分隔线里的反馈：错误优先于提示。
func (m model) homeFeedback() string {
	switch {
	case m.home.err != "":
		return problemFeedback(m.home.err)
	case m.home.notice != "":
		return benchTheme.Accent.Render(m.home.notice)
	}
	return ""
}

// libraryPosition 作品库装不下时在底栏右侧标出可见区间。
func (m model) libraryPosition(capacity int) string {
	indices := m.libraryIndices()
	if len(indices) <= capacity {
		return ""
	}
	start, end := libraryWindow(indices, m.home.cursor, capacity)
	return benchTheme.Muted.Render(fmt.Sprintf("作品 %d–%d / %d", start+1, end, len(indices)))
}

// homeHint 底栏按键：只列当前焦点能做的事。
func (m model) homeHint() string {
	if m.home.searching {
		return "输入筛选 · Enter 确定 · Esc 清除"
	}
	hint := map[int]string{
		focusPremise:  "Enter 开始创作 · Tab 下一项",
		focusLength:   "←→ 切换 · 直接输入数字固定章数 · Enter 开始创作 · Tab 下一项",
		focusApproval: "←→ 切换 · Enter 开始创作 · Tab 下一项",
		focusSettings: "Enter 填写更多设定 · Tab 下一项",
		focusStart:    "Enter 开始创作 · Tab 下一项",
		focusLibrary:  "↑↓ 选择 · Enter 或双击打开 · / 搜索 · d 删除",
		focusSearch:   "Enter 搜索作品 · Tab 下一项",
		focusImport:   "Enter 导入作品文件 · Tab 下一项",
		focusConfig:   "Enter 设置模型连接 · Tab 下一项",
	}[m.home.focus]
	return hint + " · Ctrl+C 退出"
}

func homeControl(text string, selected bool) string {
	if selected {
		return benchTheme.Selected.Render(text)
	}
	return benchTheme.Muted.Render(text)
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
	m.home.setFocus(focusSearch)
	m.home.searching = true
	m.home.confirmDelete, m.home.notice = "", ""
	return m, m.home.search.Focus()
}

// handleSearchKey 搜索框：输入即筛选并选中第一部；Enter 确定，Esc 清空并回到作品库。
func (m model) handleSearchKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	h := &m.home
	switch key.String() {
	case "esc", "enter":
		if key.String() == "esc" {
			h.search.SetValue("")
		}
		h.searching = false
		h.search.Blur()
		h.focus = focusLibrary
		if indices := m.libraryIndices(); len(indices) > 0 && key.String() == "esc" {
			h.cursor = indices[0]
		}
		return m, nil
	}
	var cmd tea.Cmd
	h.search, cmd = h.search.Update(key)
	if indices := m.libraryIndices(); len(indices) > 0 {
		h.cursor = indices[0]
	}
	return m, cmd
}

func (m model) moveLibrary(key string) model {
	indices := m.libraryIndices()
	if len(indices) == 0 {
		return m
	}
	pos := max(0, slices.Index(indices, m.home.cursor))
	switch key {
	case "up":
		pos--
	case "down":
		pos++
	case "home":
		pos = 0
	case "end":
		pos = len(indices) - 1
	case "pgup":
		pos -= m.homeFrame().capacity
	case "pgdown":
		pos += m.homeFrame().capacity
	}
	m.home.cursor = indices[min(max(0, pos), len(indices)-1)]
	return m
}

// handleHomeMouse 滚轮翻作品库；点击：故事框聚焦、选项直接选中、作品单击选中双击打开，
// 其余控件与回车同一入口。
func (m model) handleHomeMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.home.mode != homeMain || m.home.searching {
		return m, nil
	}
	mouse := msg.Mouse()
	frame := m.homeFrame()
	if mouse.Button == tea.MouseWheelUp || mouse.Button == tea.MouseWheelDown {
		if !frame.list.contains(mouse.X, mouse.Y) {
			return m, nil
		}
		m.home.setFocus(focusLibrary)
		key := "down"
		if mouse.Button == tea.MouseWheelUp {
			key = "up"
		}
		return m.moveLibrary(key), nil
	}
	if !leftClick(msg) {
		return m, nil
	}
	last := m.home.clicked
	m.home.clicked = listClick{}
	for _, hit := range frame.hits {
		if mouse.Y != hit.y || mouse.X < hit.x || mouse.X >= hit.x+hit.width {
			continue
		}
		m.home.confirmDelete, m.home.notice = "", ""
		cmd := m.home.setFocus(hit.focus)
		switch {
		case hit.option >= 0:
			m.home.choose(hit.focus, hit.option)
		case hit.row >= 0:
			m.home.cursor = hit.row
			now := time.Now()
			if last.again(hit.row, now) {
				return m.activate(focusLibrary)
			}
			m.home.clicked = listClick{row: hit.row, at: now}
		case hit.focus != focusPremise && hit.focus != focusLength && hit.focus != focusApproval:
			return m.activate(hit.focus)
		}
		return m, cmd
	}
	return m, nil
}
