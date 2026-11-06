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
)

// 作品行的定宽列（书名占余下的宽度）：进度条、章数、状态、时间。
const (
	libraryBar   = 10
	libraryCount = 11
	libraryState = 10
	libraryWhen  = 14 // 2025年12月31日
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
	picker   homeRect // 展开的选项浮框；没展开时为空
}

// target 是指针下的控件：浮框盖住的地方只认框里的选项。
func (f homeFrame) target(x, y int) (pageHit, bool) {
	for _, hit := range f.hits {
		if y != hit.y || x < hit.x || x >= hit.x+hit.width || f.picker.contains(x, y) && hit.option < 0 {
			continue
		}
		return hit, true
	}
	return pageHit{}, false
}

// homeLayout 是内容栏的水平几何：x 是内容左缘，两侧各留 pageGutter 列。
type homeLayout struct{ x, width int }

func layoutHome(width int) homeLayout {
	inner := min(homeColumnMax, max(1, width-2*pageGutter-4))
	return homeLayout{x: (max(1, width) - inner) / 2, width: inner}
}

// premiseWidth 是故事输入框的文字宽度：框线与框内留白左右各占两格。
func premiseWidth(width int) int { return max(1, layoutHome(width).width-4) }

func (m model) homeFrame() homeFrame {
	w, h := max(1, m.width), max(1, m.height)
	l := layoutHome(w)
	body := max(1, h-pageHeader-pageFooter)
	var frame homeFrame

	config := m.home.control("模型设置", focusConfig)
	binding := benchTheme.Muted.Render(truncate(m.bindingLabel(), w/2))
	lines := pageTop("", binding+"   "+config, w)
	configWidth := lipgloss.Width(config)
	frame.hits = append(frame.hits, pageHit{x: w - 1 - configWidth, y: 0, width: configWidth, focus: focusConfig, row: -1, option: -1})

	c := pageColumn{width: l.width}
	picker := m.composerSection(&c)
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
	// 选项浮框盖在下面的内容上，只合成被盖住的几行；左右各垫一格空白，压断下面的分隔线，
	// 免得与框线连成一片。
	if picker.box != "" {
		box := lipgloss.NewStyle().Margin(0, 1).Render(picker.box)
		x, y := l.x+picker.x-1, top+picker.y
		boxWidth, boxHeight := lipgloss.Size(box)
		frame.picker = homeRect{x: x, y: y, width: boxWidth, height: boxHeight}
		covered := lipgloss.NewLayer(strings.Join(lines[y:y+boxHeight], "\n"))
		copy(lines[y:], fitBlock(lipgloss.NewCompositor(covered, lipgloss.NewLayer(box).X(x).Z(1)).Render(), w, boxHeight))
		options := make([]pageHit, picker.options)
		for i := range options {
			options[i] = pageHit{x: x + 2, y: y + i, width: boxWidth - 4, focus: m.home.focus, row: -1, option: i}
		}
		frame.hits = append(options, frame.hits...)
	}
	lines = append(lines, m.pageBottom(m.homeFeedback(), m.homeHint(), m.libraryPosition(capacity), !m.home.searching, w)...)
	frame.text = strings.Join(fitBlock(strings.Join(lines, "\n"), w, h), "\n")
	return frame
}

func lineAt(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	return lines[i]
}

// homePicker 是展开的选项浮框：没有上框线，左框线紧挨焦点所在选项的左侧，挂在新故事框的
// 下边线上（接口画成 ┬；第一个选项的浮框与新故事框左框线对齐，接成 ├），像从选项垂下来。
// x、y 是它在内容栏里的左上角；没展开时 box 为空。
type homePicker struct {
	x, y    int
	box     string
	options int
}

// composerSection 新故事是一个整体的输入框：标题写在上框线里，框里是故事输入，最下一行是
// 篇幅、推进、设定三个选项与开始按钮；框下一行说清当前选择意味着什么。焦点在框里任何一处，
// 框线都用强调色。返回展开的选项浮框。
func (m model) composerSection(c *pageColumn) homePicker {
	home := m.home
	frame := benchTheme.Border
	if home.composing() {
		frame = benchTheme.Accent
	}
	inner := c.width - 2
	title := " " + benchTheme.Title.Render("新故事") + " "
	c.hit(0, c.width, focusPremise, -1, -1)
	c.add(frame.Render("╭─") + title + frame.Render(strings.Repeat("─", max(0, inner-1-lipgloss.Width(title)))+"╮"))

	// 框里铺输入底色，故事左右各留一格。
	surface := lipgloss.NewStyle().Background(benchColors.InputBackground)
	for _, row := range premiseView(home.premise, surface) {
		c.hit(0, c.width, focusPremise, -1, -1)
		c.add(frame.Render("│") + surface.Render(" ") + row + surface.Render(strings.Repeat(" ", max(0, inner-1-lipgloss.Width(row)))) + frame.Render("│"))
	}

	// 选项各自两侧留一格，聚焦时底色铺满这一段；开始按钮靠右。行里其余地方点了回到故事输入。
	var picker homePicker
	x, row := 1, ""
	for i, chip := range home.chips() {
		if i > 0 {
			row += surface.Render("  ")
			x += 2
		}
		text := home.chipView(chip, surface)
		if chip.focus == home.focus && home.picker {
			picker.x = x - 1
			picker.box, picker.options = home.pickerView()
		}
		c.hit(x, lipgloss.Width(text), chip.focus, -1, -1)
		row += text
		x += lipgloss.Width(text)
	}
	start := home.startButton(surface)
	startX := c.width - 2 - lipgloss.Width(start)
	c.hit(startX, lipgloss.Width(start), focusStart, -1, -1)
	c.hit(0, c.width, focusPremise, -1, -1)
	c.add(frame.Render("│") + row + surface.Render(strings.Repeat(" ", max(0, startX-x))) + start + surface.Render(" ") + frame.Render("│"))
	bottom := "╰" + strings.Repeat("─", inner) + "╯"
	if picker.box != "" {
		edges := []rune(bottom)
		edges[picker.x] = '┬'
		if picker.x == 0 {
			edges[0] = '├'
		}
		edges[picker.x+lipgloss.Width(picker.box)-1] = '┬'
		bottom = string(edges)
	}
	c.hit(0, c.width, focusPremise, -1, -1)
	c.add(frame.Render(bottom))

	picker.y = len(c.lines)
	c.add("  " + home.composerNote())
	return picker
}

// composing 是焦点在新故事框里：故事输入或底行的选项。
func (h homeState) composing() bool {
	switch h.focus {
	case focusPremise, focusLength, focusApproval, focusSettings:
		return true
	}
	return false
}

// composerChip 是输入框底行的一个选项：标签、当前值，与提示它点开是浮框（▾）还是一页（›）。
type composerChip struct {
	focus               int
	label, value, glyph string
}

func (h homeState) chips() []composerChip {
	length, _ := h.choices(focusLength)
	settings := "可选"
	if filled := h.filledSettings(); len(filled) > 0 {
		settings = fmt.Sprintf("已填 %d 项", len(filled))
	}
	return []composerChip{
		{focusLength, "篇幅", length[h.choice(focusLength)], "▾"},
		{focusApproval, "推进", workbench.ApprovalLabel(h.approval), "▾"},
		{focusSettings, "设定", settings, "›"},
	}
}

// chipView 画一个选项：聚焦时铺选中底色，指针悬停时整段换强调色，平时标签淡、值亮。
func (h homeState) chipView(chip composerChip, surface lipgloss.Style) string {
	switch {
	case chip.focus == h.focus:
		return benchTheme.Selected.Render(" " + chip.label + " " + chip.value + " " + chip.glyph + " ")
	case h.hovered(chip.focus, -1, -1):
		return benchTheme.Accent.Inherit(surface).Render(" " + chip.label + " " + chip.value + " " + chip.glyph + " ")
	}
	muted := benchTheme.Muted.Inherit(surface)
	return muted.Render(" "+chip.label+" ") + benchTheme.Text.Inherit(surface).Render(chip.value) + muted.Render(" "+chip.glyph+" ")
}

// startButton 是开始创作：写了故事就是强调色的主按钮，还空着时淡在输入面里；悬停时提亮。
func (h homeState) startButton(surface lipgloss.Style) string {
	const text = " 开始创作 ↵ "
	ready, hovered := strings.TrimSpace(h.premise.Value()) != "", h.hovered(focusStart, -1, -1)
	switch {
	case ready && hovered:
		return benchTheme.Primary.Background(benchColors.Glow).Render(text)
	case ready:
		return benchTheme.Primary.Render(text)
	case hovered:
		return benchTheme.Button.Render(text)
	}
	return benchTheme.Muted.Inherit(surface).Render(text)
}

// composerNote 是框下一行：焦点在某个选项上时说清它的当前选择，否则概括篇幅与推进。
// 浮框展开时它被盖住，说明都在框里，这一行留空，免得露出半句。
func (h homeState) composerNote() string {
	if h.picker {
		return ""
	}
	switch h.focus {
	case focusLength, focusApproval:
		_, notes := h.choices(h.focus)
		return benchTheme.Text.Render(notes[h.choice(h.focus)])
	case focusSettings:
		return benchTheme.Text.Render(settingsNote(h.filledSettings()))
	}
	_, length := h.choices(focusLength)
	_, approval := h.choices(focusApproval)
	return benchTheme.Muted.Render(length[h.choice(focusLength)] + " · " + approval[h.choice(focusApproval)])
}

// choices 是篇幅或推进的全部选项与各自的说明（§5.4）。
func (h homeState) choices(focus int) (labels, notes []string) {
	if focus == focusLength {
		fixed, note := "固定章数", "敲数字填章数，全书写满这么多章收官"
		switch {
		case h.chapters > 0:
			fixed, note = fmt.Sprintf("固定 %d 章", h.chapters), fmt.Sprintf("全书写满 %d 章收官，AI 只定终局方向", h.chapters)
		case h.fixed:
			fixed = "固定 __ 章"
		}
		return []string{"AI 决定", fixed}, []string{"篇幅与终局由 AI 按故事容量决定，写到合适处收官", note}
	}
	for _, policy := range approvalOrder {
		labels = append(labels, workbench.ApprovalLabel(policy))
		notes = append(notes, approvalNote(policy))
	}
	return labels, notes
}

// choice 是篇幅或推进当前选中的下标。
func (h homeState) choice(focus int) int {
	if focus == focusLength {
		if h.fixed {
			return 1
		}
		return 0
	}
	return slices.Index(approvalOrder, h.approval)
}

func (h *homeState) choose(focus, option int) {
	switch focus {
	case focusLength:
		h.fixed = option == 1
	case focusApproval:
		h.approval = approvalOrder[option]
	}
}

// step 把焦点所在选项的选择前后挪一格，到头为止。
func (h *homeState) step(delta int) {
	labels, _ := h.choices(h.focus)
	h.choose(h.focus, min(max(0, h.choice(h.focus)+delta), len(labels)-1))
}

// pickerView 是焦点所在选项的浮框：全部选项与各自的说明，● 是当前选择；返回框与选项数。
// 浮框只在焦点在新故事框里时展开，框线与新故事框同用强调色。
func (h homeState) pickerView() (string, int) {
	labels, notes := h.choices(h.focus)
	labelWidth, noteWidth := 0, 0
	for i := range labels {
		labelWidth, noteWidth = max(labelWidth, lipgloss.Width(labels[i])), max(noteWidth, lipgloss.Width(notes[i]))
	}
	rows := make([]string, len(labels))
	for i := range labels {
		mark, label := styled{"○ ", benchTheme.Muted}, styled{padLine(labels[i], labelWidth) + "    ", benchTheme.Text}
		if i == h.choice(h.focus) {
			mark.text, mark.style, label.style = "● ", benchTheme.Accent, benchTheme.Text.Bold(true)
		}
		look := rowPlain
		if h.hovered(h.focus, -1, i) {
			look = rowHovered
		}
		rows[i] = listRow([]styled{mark, label, {notes[i], benchTheme.Muted}}, nil, 2+labelWidth+4+noteWidth, look)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), false, true, true).BorderForeground(benchColors.Accent)
	return box.Render(strings.Join(rows, "\n")), len(rows)
}

func (h homeState) filledSettings() []string {
	var filled []string
	for i, input := range h.settings {
		if strings.TrimSpace(input.Value()) != "" {
			filled = append(filled, settingFields[i].label)
		}
	}
	return filled
}

// settingsNote 说明更多设定：这些是用户的原话，原样作为创作意图保留（D70）。
func settingsNote(filled []string) string {
	if len(filled) > 0 {
		return "已填 " + strings.Join(filled, "、") + "。你的原话原样保留，AI 不会改写"
	}
	labels := make([]string, len(settingFields))
	for i, field := range settingFields {
		labels[i] = field.label
	}
	return "可选：" + strings.Join(labels, "、") + "。你的原话原样保留，AI 不会改写"
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

// librarySection 作品库：标题行（搜索与导入；没有作品时不搜索）、作品表或空状态。rows 是内容栏可用的总行数；
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
		importing := home.control("导入作品", focusImport)
		controls := importing
		if len(home.library) > 0 {
			search = home.control(search, focusSearch)
			controls = search + "   " + importing
			c.hit(c.width-lipgloss.Width(controls), lipgloss.Width(search), focusSearch, -1, -1)
		}
		c.hit(c.width-lipgloss.Width(importing), lipgloss.Width(importing), focusImport, -1, -1)
		c.add(sectionHead(title, controls, c.width))
	}
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
			look := rowPlain
			switch {
			case home.focus == focusLibrary && index == home.cursor:
				look = rowSelected
			case home.hovered(focusLibrary, index, -1):
				look = rowHovered
			}
			c.item(libraryRow(home.library[index], c.width, look, now), focusLibrary, index)
		}
	}
	return top, capacity
}

// libraryRow 是一部作品的一行：书名、进度条（全书章数已知时）、章数、状态与相对时间，
// 各列上下对齐。
func libraryRow(entry libraryEntry, width int, look rowLook, now time.Time) string {
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
	}, nil, width, look)
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
	switch {
	case m.home.searching:
		return "输入筛选 · Enter 确定 · Esc 清除"
	case m.home.picker && m.home.focus == focusLength:
		return "↑↓ 选择 · 敲数字固定章数 · Enter 确定"
	case m.home.picker:
		return "↑↓ 选择 · Enter 确定"
	}
	hint := map[int]string{
		focusPremise:  "Enter 开始创作 · Tab 下一项",
		focusLength:   "←→ 切换 · ↓ 展开 · 敲数字固定章数 · Enter 开始创作 · Tab 下一项",
		focusApproval: "←→ 切换 · ↓ 展开 · Enter 开始创作 · Tab 下一项",
		focusSettings: "Enter 填写更多设定 · Tab 下一项",
		focusLibrary:  "↑↓ 选择 · Enter 或双击打开 · / 搜索 · d 删除",
		focusSearch:   "Enter 搜索作品 · Tab 下一项",
		focusImport:   "Enter 导入作品文件 · Tab 下一项",
		focusConfig:   "Enter 设置模型连接 · Tab 下一项",
	}[m.home.focus]
	return hint + " · Ctrl+C 退出"
}

// control 是首页的文字入口（模型设置、搜索、导入）：平时是链接样式，悬停换强调色，
// 聚焦时铺选中底色。
func (h homeState) control(text string, focus int) string {
	switch {
	case h.focus == focus:
		return benchTheme.Selected.Render(text)
	case h.hovered(focus, -1, -1):
		return benchTheme.Link.Foreground(benchColors.Accent).Render(text)
	}
	return benchTheme.Link.Render(text)
}

// hovered 是指针此刻停在这个控件上（row、option 不适用时为 -1）。
func (h homeState) hovered(focus, row, option int) bool {
	return h.hover.width > 0 && h.hover.focus == focus && h.hover.row == row && h.hover.option == option
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

// handleHomeMouse 指针移动只更新悬停；滚轮翻作品库；点击：选项浮框里的一项直接选中并收起，
// 篇幅、推进点一下展开或收起浮框，作品单击选中双击打开，其余控件与回车同一入口。浮框外的
// 点击先收起浮框。
func (m model) handleHomeMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.home.mode != homeMain || m.home.searching {
		return m, nil
	}
	mouse := msg.Mouse()
	frame := m.homeFrame()
	hit, ok := frame.target(mouse.X, mouse.Y)
	switch msg.(type) {
	case tea.MouseMotionMsg:
		m.home.hover = hit
		return m, nil
	case tea.MouseWheelMsg:
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
	if !ok {
		m.home.picker = m.home.picker && frame.picker.contains(mouse.X, mouse.Y)
		return m, nil
	}
	m.home.confirmDelete, m.home.notice = "", ""
	switch {
	case hit.option >= 0:
		m.home.choose(hit.focus, hit.option)
		m.home.picker = false
		return m, nil
	case hit.focus == focusLength || hit.focus == focusApproval:
		open := !m.home.picker || m.home.focus != hit.focus
		m.home.setFocus(hit.focus)
		m.home.picker = open
		return m, nil
	case hit.row >= 0:
		m.home.setFocus(focusLibrary)
		m.home.cursor = hit.row
		now := time.Now()
		if last.again(hit.row, now) {
			return m.activate(focusLibrary)
		}
		m.home.clicked = listClick{row: hit.row, at: now}
		return m, nil
	case hit.focus == focusPremise:
		return m, m.home.setFocus(focusPremise)
	}
	m.home.setFocus(hit.focus)
	return m.activate(hit.focus)
}
