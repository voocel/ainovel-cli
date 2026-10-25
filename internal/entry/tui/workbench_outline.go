package tui

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// outlineRow 是折叠过滤后的目录可见行：顶部的全书行、卷/弧头行（回车折叠/展开）、章行，
// 或末尾交代还没规划的后续的占位行。
type outlineRow struct {
	context     string
	node        workbench.OutlineNode
	endChapter  int // 占位行的末章；0 表示开放期，后续章数未定
	chapter     int // 章行与占位行的章节号
	level       int // 缩进级数：在卷下为 1，每级两列；弧与章同级，目录看起来只有卷与章两级
	placeholder bool
	collapsed   bool // 头行且已折叠
	empty       bool // 头行下面还没有节点（滚动规划尚未展开的故事弧）：不显示箭头、不可折叠
	chapters    int  // 子树章节数
	pending     int  // 子树待确认章数
	confirmed   int
	unresolved  int // 子树带未解决意见的章数（D94）
}

// bookRowID 是全书行的界面身份：刷新后按它找回光标，不是作品计划里的节点。
const bookRowID = "ui:book"

func (r outlineRow) isBook() bool    { return r.node.Node.ID == bookRowID }
func (r outlineRow) isChapter() bool { return r.node.Node.Kind == domainmodel.PlanChapter }
func (r outlineRow) header() bool {
	return r.node.Node.Kind == domainmodel.PlanVolume || r.node.Node.Kind == domainmodel.PlanArc
}
func (r outlineRow) foldable() bool { return r.header() && !r.empty }

// outlineDepth 卷→弧→章的层级深度；beat 是章内节拍，不入目录。
func outlineDepth(kind domainmodel.PlanNodeKind) (int, bool) {
	switch kind {
	case domainmodel.PlanVolume:
		return 0, true
	case domainmodel.PlanArc:
		return 1, true
	case domainmodel.PlanChapter:
		return 2, true
	default:
		return 0, false
	}
}

type outlineNodeStats struct{ children, chapters, pending, confirmed, unresolved int }

func outlineStats(outline []workbench.OutlineNode, unresolved map[int]bool) map[string]outlineNodeStats {
	stats := make(map[string]outlineNodeStats)
	var ancestors []string
	for _, entry := range outline {
		depth, ok := outlineDepth(entry.Node.Kind)
		if !ok {
			continue
		}
		if parent := min(depth, len(ancestors)); parent > 0 {
			s := stats[ancestors[parent-1]]
			s.children++
			stats[ancestors[parent-1]] = s
		}
		if entry.Node.Kind == domainmodel.PlanChapter {
			for _, id := range ancestors {
				s := stats[id]
				s.chapters++
				if entry.State == workbench.ChapterConfirmed {
					s.confirmed++
				}
				if entry.State == workbench.ChapterPending || entry.Proposed {
					s.pending++
				}
				if unresolved[entry.Number] {
					s.unresolved++
				}
				stats[id] = s
			}
			continue
		}
		ancestors = append(ancestors[:min(depth, len(ancestors))], entry.Node.ID)
	}
	return stats
}

func chapterCount(snap workbench.WorkbenchSnapshot) int {
	count := 0
	for _, entry := range snap.Outline {
		if entry.Node.Kind == domainmodel.PlanChapter {
			count++
		}
	}
	return count
}

const directoryGroupSize = 50

// directoryEntries 超过 50 章且没有卷/弧时按 50 章建纯展示分组，不改作品计划。
func (m model) directoryEntries() []workbench.OutlineNode {
	outline := m.bench.snap.Outline
	if chapterCount(m.bench.snap) <= directoryGroupSize {
		return outline
	}
	for _, entry := range outline {
		if entry.Node.Kind == domainmodel.PlanVolume || entry.Node.Kind == domainmodel.PlanArc {
			return outline
		}
	}
	var entries []workbench.OutlineNode
	group := -1
	for _, entry := range outline {
		if entry.Node.Kind != domainmodel.PlanChapter {
			continue
		}
		next := (entry.Number - 1) / directoryGroupSize
		if next != group {
			group = next
			start := group*directoryGroupSize + 1
			entries = append(entries, workbench.OutlineNode{Node: domainmodel.PlanNode{
				ID: fmt.Sprintf("ui:chapter-range:%d", start), Kind: domainmodel.PlanVolume,
				Title: fmt.Sprintf("第 %d–%d 章", start, start+directoryGroupSize-1),
			}})
		}
		entries = append(entries, entry)
	}
	return entries
}

// buildOutlineRows 先序快照按折叠状态过滤（折叠头行吞掉整棵子树）；有目录时顶部是全书行，
// 末尾交代还没规划的后续。
func (m model) buildOutlineRows() []outlineRow {
	bench := m.bench
	entries := m.directoryEntries()
	stats := outlineStats(entries, unresolvedChapters(bench.snap))
	var rows []outlineRow
	skipDepth := -1
	ancestors := make(map[int]string)
	for _, entry := range entries {
		depth, ok := outlineDepth(entry.Node.Kind)
		if !ok {
			continue
		}
		if skipDepth >= 0 {
			if depth > skipDepth {
				continue
			}
			skipDepth = -1
		}
		for d := range ancestors {
			if d >= depth {
				delete(ancestors, d)
			}
		}
		var context []string
		for d := 0; d < depth; d++ {
			if title := ancestors[d]; title != "" {
				context = append(context, title)
			}
		}
		row := outlineRow{node: entry, chapter: entry.Number, context: strings.Join(context, " / ")}
		if _, inVolume := ancestors[0]; inVolume && depth > 0 {
			row.level = 1
		}
		if depth < 2 {
			ancestors[depth] = entry.Node.Title
			s := stats[entry.Node.ID]
			row.chapters, row.pending, row.confirmed, row.unresolved, row.empty = s.chapters, s.pending, s.confirmed, s.unresolved, s.children == 0
		}
		if entry.Node.Kind != domainmodel.PlanChapter && bench.collapsed[entry.Node.ID] {
			row.collapsed = true
			skipDepth = depth
		}
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		rows = append([]outlineRow{{node: workbench.OutlineNode{Node: domainmodel.PlanNode{ID: bookRowID, Title: "全书"}}}}, rows...)
	}
	// 全书章数已知时末尾是待规划区间（还没有目录时也有）；开放期章数未定，有目录时交代后续
	// 随创作推进规划（D63/D73）。
	switch count, target := chapterCount(bench.snap), bench.snap.Length.Final; {
	case count < target:
		rows = append(rows, outlineRow{chapter: count + 1, endChapter: target, placeholder: true})
	case target == 0 && len(rows) > 0:
		rows = append(rows, outlineRow{chapter: count + 1, placeholder: true})
	}
	return rows
}

type outlineCache struct {
	first         *workbench.OutlineNode
	count, target int
	collapsed     map[string]bool
	rows          []outlineRow
}

func (m model) outlineRows() []outlineRow {
	cache := m.bench.rowsCache
	var first *workbench.OutlineNode
	if len(m.bench.snap.Outline) > 0 {
		first = &m.bench.snap.Outline[0]
	}
	target := m.bench.snap.Length.Final
	if cache.rows == nil || cache.first != first || cache.count != len(m.bench.snap.Outline) || cache.target != target || !maps.Equal(cache.collapsed, m.bench.collapsed) {
		*cache = outlineCache{first: first, count: len(m.bench.snap.Outline), target: target, collapsed: maps.Clone(m.bench.collapsed), rows: m.buildOutlineRows()}
	}
	return cache.rows
}

// nextSelectable 从 from 沿 delta 找下一个可选行；没有更多时原地不动。
func nextSelectable(rows []outlineRow, from, delta int) int {
	for next := from + delta; next >= 0 && next < len(rows); next += delta {
		if !rows[next].placeholder {
			return next
		}
	}
	return from
}

// anchorOutlineCursor 在新的可见行里找回选中行：先按节点 ID，再按章节号，最后回落到第一个章行。
func anchorOutlineCursor(rows []outlineRow, nodeID string, chapter int) int {
	if nodeID != "" {
		for index, row := range rows {
			if row.node.Node.ID == nodeID {
				return index
			}
		}
	}
	if chapter > 0 {
		for index, row := range rows {
			if row.isChapter() && row.chapter == chapter {
				return index
			}
		}
	}
	for index, row := range rows {
		if row.isChapter() {
			return index
		}
	}
	for index, row := range rows {
		if !row.placeholder {
			return index
		}
	}
	return 0
}

func (m model) selectedRowIdentity() (string, int) {
	rows := m.outlineRows()
	if m.bench.cursor < 0 || m.bench.cursor >= len(rows) {
		return "", 0
	}
	return rows[m.bench.cursor].node.Node.ID, rows[m.bench.cursor].chapter
}

// selectedChapterNumber 光标所在章行的章节号；头行/占位行返回 0。
func (m model) selectedChapterNumber() int {
	rows := m.outlineRows()
	if m.bench.cursor >= 0 && m.bench.cursor < len(rows) && rows[m.bench.cursor].isChapter() {
		return rows[m.bench.cursor].chapter
	}
	return 0
}

func (m model) outlineChapter(number int) (workbench.OutlineNode, bool) {
	for _, entry := range m.bench.snap.Outline {
		if entry.Node.Kind == domainmodel.PlanChapter && entry.Number == number {
			return entry, true
		}
	}
	return workbench.OutlineNode{}, false
}

// chapterTitle 是第 number 章的标题：取自章节计划，待确认的方案已投影在内。
func (m model) chapterTitle(number int) string {
	node, _ := m.outlineChapter(number)
	return node.Node.Title
}

func (m model) selectedChapterID() string {
	if chapter, ok := chapterByNumber(m.bench.snap.Manuscript, m.selectedChapterNumber()); ok {
		return chapter.ID
	}
	return ""
}

// planNumber 是章节计划在大纲里的章号；不是大纲里的章为 0。
func (m model) planNumber(id string) int {
	for _, entry := range m.bench.snap.Outline {
		if entry.Node.ID == id && entry.Node.Kind == domainmodel.PlanChapter {
			return entry.Number
		}
	}
	return 0
}

// currentChapter 卡在修订预算上 > 正在写 > 待确认 > 最近入稿的章节号。卡住的那一章选中
// 摆出来：正文就是它，写下的意见也落在它身上。
func (m model) currentChapter() int {
	if number := m.heldNumber(); number > 0 {
		return number
	}
	for _, state := range []workbench.ChapterState{workbench.ChapterInProgress, workbench.ChapterPending} {
		for _, entry := range m.bench.snap.Outline {
			if entry.Node.Kind == domainmodel.PlanChapter && entry.State == state {
				return entry.Number
			}
		}
	}
	last := 0
	for _, entry := range m.bench.snap.Outline {
		if entry.Node.Kind == domainmodel.PlanChapter && entry.State == workbench.ChapterConfirmed {
			last = max(last, entry.Number)
		}
	}
	return last
}

// selectOutline 手动选章：固定正文视图在该章，不被创作进度抢走。选中翻出来的那一行时目录
// 不动；选到窗口外（键盘走出去、按章号跳转）时窗口回到随选中行居中。
func (m *model) selectOutline(index int) {
	if start, end := m.bench.outlineWindow(len(m.outlineRows()), m.benchLayout().outlineRows); index < start || index >= end {
		m.bench.outlineScrolled = false
	}
	m.bench.cursor = index
	m.bench.pinned = true
	m.bench.proseOffset, m.bench.proseHold = 0, false
	m.bench.view = viewProse
}

// findChapter 在完整大纲（含折叠子树）里找章节号或标题；只展开命中的祖先。
func (m *model) findChapter(query string, next bool) bool {
	if query == "" {
		return false
	}
	number, numericErr := strconv.Atoi(query)
	outline := m.directoryEntries()
	start := 0
	if next {
		id, _ := m.selectedRowIdentity()
		for i, node := range outline {
			if node.Node.ID == id {
				start = i + 1
				break
			}
		}
	}
	for step := 0; step < len(outline); step++ {
		index := (start + step) % len(outline)
		node := outline[index]
		if node.Node.Kind != domainmodel.PlanChapter {
			continue
		}
		match := node.Number == number
		if numericErr != nil {
			match = strings.Contains(strings.ToLower(node.Node.Title), strings.ToLower(query))
		}
		if !match {
			continue
		}
		ancestors := map[int]string{}
		for _, ancestor := range outline[:index] {
			depth, ok := outlineDepth(ancestor.Node.Kind)
			if !ok {
				continue
			}
			for d := range ancestors {
				if d >= depth {
					delete(ancestors, d)
				}
			}
			if depth < 2 {
				ancestors[depth] = ancestor.Node.ID
			}
		}
		for _, id := range ancestors {
			delete(m.bench.collapsed, id)
		}
		for i, row := range m.outlineRows() {
			if row.node.Node.ID == node.Node.ID {
				m.selectOutline(i)
				break
			}
		}
		m.bench.search = query
		m.bench.pane = benchPaneMain
		return true
	}
	return false
}

// initializeDirectory 长目录首次打开：只展开当前章所在的卷/弧。
func (m *model) initializeDirectory() {
	if m.bench.directoryInitialized || chapterCount(m.bench.snap) <= directoryGroupSize {
		return
	}
	m.bench.directoryInitialized = true
	number := m.currentChapter()
	if m.bench.pinned && m.selectedChapterNumber() > 0 {
		number = m.selectedChapterNumber()
	}
	for _, entry := range m.directoryEntries() {
		if entry.Node.Kind == domainmodel.PlanVolume || entry.Node.Kind == domainmodel.PlanArc {
			m.bench.collapsed[entry.Node.ID] = true
		}
	}
	if number == 0 {
		for _, entry := range m.bench.snap.Outline {
			if entry.Node.Kind == domainmodel.PlanChapter {
				number = entry.Number
				break
			}
		}
	}
	m.revealCurrent(number)
}

// revealCurrent 定位到某章并展开祖先，但不改变视图、固定状态、搜索词与阅读位置。
func (m *model) revealCurrent(number int) bool {
	b := &m.bench
	view, pane, pinned, search := b.view, b.pane, b.pinned, b.search
	proseOffset, proseHold := b.proseOffset, b.proseHold
	if number == 0 || !m.findChapter(strconv.Itoa(number), false) {
		return false
	}
	b.view, b.pane, b.pinned, b.search = view, pane, pinned, search
	b.proseOffset, b.proseHold = proseOffset, proseHold
	return true
}

// toggleFold 折叠/展开一个卷/弧，并把偏好写进用户级 state.json；空列表也保存（明确全部展开）。
// 返回保存失败的提示（""=成功）：折叠已生效，丢的只是重启后的偏好，但必须让用户知道。
func (m *model) toggleFold(id string) string {
	bench := &m.bench
	bench.directoryInitialized = true
	if bench.collapsed[id] {
		delete(bench.collapsed, id)
	} else {
		bench.collapsed[id] = true
	}
	if err := appconfig.UpdateState(m.deps.ConfigDir, func(state *appconfig.State) {
		if state.Collapsed == nil {
			state.Collapsed = make(map[string][]string)
		}
		state.Collapsed[bench.projectID] = slices.Sorted(maps.Keys(bench.collapsed))
	}); err != nil {
		return "折叠已生效，但布局偏好未能保存：" + err.Error()
	}
	return ""
}

// clickOutlineRow 与键盘选章同一入口：章行选中并固定，头行折叠/展开（空头行只选中），占位行无动作。
func (m *model) clickOutlineRow(index int) {
	row := m.outlineRows()[index]
	switch {
	case row.placeholder:
	case row.header():
		m.bench.cursor, m.bench.pane, m.bench.pinned = index, benchPaneOutline, true
		if row.foldable() {
			m.bench.notice = m.toggleFold(row.node.Node.ID)
		}
	default:
		m.bench.pane = benchPaneOutline
		m.selectOutline(index)
	}
}

// navigateOutline 目录焦点下的 Home/End/PgUp/PgDn。
func (m model) navigateOutline(key string) model {
	rows := m.outlineRows()
	if len(rows) == 0 {
		return m
	}
	page := max(1, m.benchLayout().outlineRows)
	index := m.bench.cursor
	switch key {
	case "home":
		index = 0
	case "end":
		index = len(rows) - 1
	case "pgup":
		index = max(0, index-page)
	case "pgdown":
		index = min(len(rows)-1, index+page)
	}
	for index > 0 && rows[index].placeholder {
		index--
	}
	if !rows[index].placeholder {
		m.selectOutline(index)
	}
	return m
}

// outlineWindow 目录视口（视图与鼠标命中共用）：装不下时窗口随选中行居中，滚轮翻过就停在
// 翻到的地方。
func (b *workbenchState) outlineWindow(total, capacity int) (start, end int) {
	if total <= capacity {
		return 0, total
	}
	capacity = max(1, capacity)
	start = min(max(0, b.cursor), total-1) - capacity/2
	if b.outlineScrolled {
		start = b.outlineTop
	}
	start = min(max(0, start), total-capacity)
	return start, start + capacity
}

// scrollOutline 滚轮翻目录：只挪窗口，选中章与正文都不动。
func (m model) scrollOutline(delta int) model {
	total, capacity := len(m.outlineRows()), m.benchLayout().outlineRows
	if total <= capacity {
		return m
	}
	start, _ := m.bench.outlineWindow(total, capacity)
	m.bench.outlineTop = min(max(0, start+delta), total-max(1, capacity))
	m.bench.outlineScrolled = true
	return m
}

// outlineRowAt 把目录区坐标映射为行下标。
func (m model) outlineRowAt(l benchLayout, x, y int) (int, bool) {
	if x >= l.leftWidth || y < l.outlineY || y >= l.outlineY+l.outlineRows {
		return 0, false
	}
	rows := m.outlineRows()
	start, end := m.bench.outlineWindow(len(rows), l.outlineRows)
	if index := start + y - l.outlineY; index < end {
		return index, true
	}
	return 0, false
}

func paneTitle(text string, focused bool) string {
	if focused {
		return benchTheme.Focus.Render("▎" + text)
	}
	return benchTheme.Muted.Render(" " + text)
}

// directoryColumn 左栏：标题、目录窗口（上下截断指示）、作品信息。
func (m model) directoryColumn(l benchLayout) []string {
	width := l.leftWidth - 1
	rows := m.outlineRows()
	lines := []string{paneTitle("作品目录", m.bench.pane == benchPaneOutline)}
	start, end := m.bench.outlineWindow(len(rows), l.outlineRows)
	top := ""
	if start > 0 {
		top = benchTheme.Muted.Render(fmt.Sprintf("   ↑ 前面还有 %d 项", start))
	}
	lines = append(lines, top)
	if len(rows) == 0 {
		lines = append(lines, benchTheme.Muted.Render("   尚无目录 · 规划完成后出现"))
	}
	for index := start; index < end; index++ {
		lines = append(lines, m.outlineRowLine(rows[index], index == m.bench.cursor, width))
	}
	for len(lines) < 2+l.outlineRows {
		lines = append(lines, "")
	}
	bottom := ""
	if end < len(rows) {
		bottom = benchTheme.Muted.Render(fmt.Sprintf("   ↓ 后面还有 %d 项", len(rows)-end))
	}
	lines = append(lines, bottom, "")
	lines = append(lines, m.directoryNote()...)
	return fitBlock(strings.Join(lines, "\n"), l.leftWidth, l.bodyHeight)
}

// directoryNote 目录脚注：全书字数、生效中的要求数、推进方式（进度已在页眉）。
func (m model) directoryNote() []string {
	snap := m.bench.snap
	note := " " + groupDigits(snap.Words) + " 字"
	if len(snap.Directives) > 0 {
		note += fmt.Sprintf(" · %d 条要求", len(snap.Directives))
	}
	return []string{benchTheme.Muted.Render(note), benchTheme.Muted.Render(" " + workbench.ApprovalLabel(snap.Approval))}
}

// outlineRowLine 一行目录：一列选中标记，在卷下缩进两列，两列行标（折叠箭头或章节状态），
// 再接文字。卷顶格、标出序号；弧与章同级，用强调色区分；折叠的卷弧在右端淡色标出「已入稿/
// 章数」，名字先占位置；下面还没有节点的卷弧没有箭头，注明待展开，开放期末尾交代后续随创作
// 推进规划（滚动规划，D67/D73）。全书行与卷名对齐。
func (m model) outlineRowLine(row outlineRow, selected bool, width int) string {
	indent := strings.Repeat("  ", row.level)
	switch {
	case row.placeholder && row.endChapter == 0:
		return benchTheme.Muted.Render(" " + indent + "··· 后续随创作推进规划")
	case row.placeholder:
		label := fmt.Sprintf("%02d", row.chapter)
		if row.endChapter > row.chapter {
			label = fmt.Sprintf("%02d–%02d", row.chapter, row.endChapter)
		}
		return benchTheme.Muted.Render(" " + indent + "○ " + label + "  待规划")
	case row.isChapter():
		return m.outlineChapterLine(row.node, indent, selected, width)
	case row.isBook():
		if selected {
			return benchTheme.Selected.Render(fitLine("▎  全书", width))
		}
		return "   " + benchTheme.Title.Render("全书")
	}
	fold, style, title := "▾ ", benchTheme.Title, row.node.Node.Title
	if row.node.Volume > 0 {
		title = fmt.Sprintf("第 %d 卷 · %s", row.node.Volume, title)
	}
	if row.node.Node.Kind == domainmodel.PlanArc {
		style = benchTheme.Accent
	}
	label, pending, marked, progress := "", "", "", ""
	switch {
	case row.empty:
		fold, label = "  ", " · 待展开"
	case row.collapsed:
		fold = "▸ "
		if row.chapters > 0 {
			progress = fmt.Sprintf("%d/%d", row.confirmed, row.chapters)
		}
	}
	if row.node.Proposed || row.collapsed && row.pending > 0 {
		pending = " ◇"
	}
	// 折叠起来的部分里带未解决意见的章，章行看不到，在头行上点出数量（D94）。
	if row.collapsed && row.unresolved > 0 {
		marked = fmt.Sprintf(" !%d", row.unresolved)
	}
	room := width - 2 - lipgloss.Width(indent+fold+label+pending+marked)
	if progress != "" {
		room -= lipgloss.Width(progress) + 1
	}
	text := indent + fold + truncate(title, max(1, room))
	if selected {
		return benchTheme.Selected.Render(alignRight("▎"+text+label+pending+marked, progress, width))
	}
	return alignRight(" "+style.Render(text)+benchTheme.Muted.Render(label)+benchTheme.Warning.Render(pending+marked), benchTheme.Muted.Render(progress), width)
}

// unresolvedChapters 是带着尚未被接受的阻塞意见的章号（D94）：正在按意见重写，或自动修订
// 用尽后标记在案。目录、顶栏与右栏都按它标“!”。
func unresolvedChapters(snap workbench.WorkbenchSnapshot) map[int]bool {
	numbers := make(map[string]int, len(snap.Manuscript))
	for _, chapter := range snap.Manuscript {
		numbers[chapter.ID] = chapter.Number
	}
	unresolved := make(map[int]bool)
	for _, finding := range snap.Findings {
		if number, ok := numbers[finding.ChapterID]; ok && finding.Severity == domainmodel.FindingBlocking {
			unresolved[number] = true
		}
	}
	return unresolved
}

func (m model) outlineChapterLine(entry workbench.OutlineNode, indent string, selected bool, width int) string {
	symbol, style, titleStyle := "○", benchTheme.Muted, benchTheme.Muted
	switch {
	case entry.State == workbench.ChapterConfirmed && len(m.unresolvedFindings(entry.Number)) > 0:
		// 带着阻塞意见的章（D94）：正在按意见重写，或自动修订用尽后标记在这里接着写。
		symbol, style, titleStyle = "!", benchTheme.Warning, benchTheme.Text
	case entry.State == workbench.ChapterConfirmed:
		symbol, style, titleStyle = "✓", benchTheme.Success, benchTheme.Text
	case entry.State == workbench.ChapterPending || entry.Proposed:
		symbol, style, titleStyle = "◇", benchTheme.Warning, benchTheme.Text
	case entry.State == workbench.ChapterInProgress:
		symbol, style, titleStyle = "◉", benchTheme.Accent, benchTheme.Text
	}
	digits := max(2, len(strconv.Itoa(max(m.bench.snap.Length.Final, chapterCount(m.bench.snap)))))
	label := fmt.Sprintf("%0*d  %s", digits, entry.Number, truncate(entry.Node.Title, max(1, width-digits-5-len(indent))))
	if selected {
		return benchTheme.Selected.Render(fitLine("▎"+indent+symbol+" "+label, width))
	}
	return " " + indent + style.Render(symbol) + " " + titleStyle.Render(label)
}
