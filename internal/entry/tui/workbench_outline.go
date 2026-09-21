package tui

import (
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

// outlineRow 是折叠过滤后的目录可见行：卷/弧头行（回车折叠/展开）、章行或未规划占位行。
type outlineRow struct {
	context     string
	node        workbench.OutlineNode
	endChapter  int
	chapter     int // 章行与占位行的章节号
	placeholder bool
	collapsed   bool // 头行且已折叠
	chapters    int  // 子树章节数
	pending     int  // 子树待确认章数
	confirmed   int
}

func (r outlineRow) isChapter() bool { return r.node.Node.Kind == domainmodel.PlanChapter }
func (r outlineRow) header() bool {
	return r.node.Node.Kind == domainmodel.PlanVolume || r.node.Node.Kind == domainmodel.PlanArc
}

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

type outlineNodeStats struct{ chapters, pending, confirmed int }

func outlineStats(outline []workbench.OutlineNode) map[string]outlineNodeStats {
	stats := make(map[string]outlineNodeStats)
	var ancestors []string
	for _, entry := range outline {
		depth, ok := outlineDepth(entry.Node.Kind)
		if !ok {
			continue
		}
		if entry.Node.Kind == domainmodel.PlanChapter {
			for _, id := range ancestors {
				s := stats[id]
				s.chapters++
				if entry.State == workbench.ChapterConfirmed {
					s.confirmed++
				}
				if entry.State == workbench.ChapterPending {
					s.pending++
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

// buildOutlineRows 先序快照按折叠状态过滤（折叠头行吞掉整棵子树），末尾补未规划占位。
func (m model) buildOutlineRows() []outlineRow {
	bench := m.bench
	entries := m.directoryEntries()
	stats := outlineStats(entries)
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
		if depth < 2 {
			ancestors[depth] = entry.Node.Title
			s := stats[entry.Node.ID]
			row.chapters, row.pending, row.confirmed = s.chapters, s.pending, s.confirmed
		}
		if entry.Node.Kind != domainmodel.PlanChapter && bench.collapsed[entry.Node.ID] {
			row.collapsed = true
			skipDepth = depth
		}
		rows = append(rows, row)
	}
	target := m.bench.snap.TargetChapters
	if number := chapterCount(bench.snap) + 1; number <= target {
		rows = append(rows, outlineRow{chapter: number, endChapter: target, placeholder: true})
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
	target := m.bench.snap.TargetChapters
	if cache == nil {
		return m.buildOutlineRows()
	}
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

func (m model) selectedChapterID() string {
	if chapter, ok := chapterByNumber(m.bench.snap.Manuscript, m.selectedChapterNumber()); ok {
		return chapter.ID
	}
	return ""
}

// currentChapter 正在写 > 待确认 > 最近入稿的章节号。
func (m model) currentChapter() int {
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

// selectOutline 手动选章：固定正文视图在该章，不被创作进度抢走。
func (m *model) selectOutline(index int) {
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

// returnToCurrentChapter 回到创作进度所在章并恢复跟随。
func (m *model) returnToCurrentChapter() {
	if !m.revealCurrent(m.currentChapter()) {
		m.bench.notice = "暂无正在写作、待确认或已入稿的章节"
		return
	}
	m.bench.pinned = false
	m.bench.proseOffset, m.bench.proseHold = 0, false
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
	state, err := appconfig.LoadState(m.deps.ConfigDir)
	if err != nil {
		// 读不出来就不回写：用空状态读改写会抹掉其他作品的偏好。
		return "折叠已生效，但偏好文件异常未保存：" + err.Error()
	}
	if state.Collapsed == nil {
		state.Collapsed = make(map[string][]string)
	}
	ids := make([]string, 0, len(bench.collapsed))
	for nodeID := range bench.collapsed {
		ids = append(ids, nodeID)
	}
	sort.Strings(ids)
	state.Collapsed[bench.projectID] = ids
	if err := appconfig.SaveState(m.deps.ConfigDir, state); err != nil {
		return "折叠已生效，但布局偏好保存失败：" + err.Error()
	}
	return ""
}

// clickOutlineRow 与键盘选章同一入口：章行选中并固定，头行折叠/展开，占位行无动作。
func (m *model) clickOutlineRow(index int) {
	row := m.outlineRows()[index]
	switch {
	case row.placeholder:
	case row.header():
		m.bench.cursor, m.bench.pane, m.bench.pinned = index, benchPaneOutline, true
		m.bench.notice = m.toggleFold(row.node.Node.ID)
	default:
		m.bench.pane = benchPaneOutline
		m.selectOutline(index)
	}
}

// navigateOutline 目录焦点下的 Home/End/PgUp/PgDn。
func (m model) navigateOutline(key tea.KeyType) model {
	rows := m.outlineRows()
	if len(rows) == 0 {
		return m
	}
	page := max(1, m.benchLayout().outlineRows)
	index := m.bench.cursor
	switch key {
	case tea.KeyHome:
		index = 0
	case tea.KeyEnd:
		index = len(rows) - 1
	case tea.KeyPgUp:
		index = max(0, index-page)
	case tea.KeyPgDown:
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

// outlineWindow 目录视口（视图与鼠标命中共用）：装不下时窗口随选中行居中。
func outlineWindow(total, capacity, selected int) (start, end int) {
	if total <= capacity {
		return 0, total
	}
	capacity = max(1, capacity)
	selected = min(max(0, selected), total-1)
	start = min(max(0, selected-capacity/2), total-capacity)
	return start, start + capacity
}

// outlineRowAt 把目录区坐标映射为行下标。
func (m model) outlineRowAt(l benchLayout, x, y int) (int, bool) {
	if x >= l.leftWidth || y < l.outlineY || y >= l.outlineY+l.outlineRows {
		return 0, false
	}
	rows := m.outlineRows()
	start, end := outlineWindow(len(rows), l.outlineRows, m.bench.cursor)
	if index := start + y - l.outlineY; index < end {
		return index, true
	}
	return 0, false
}

func paneTitle(text string, focused bool) string {
	if focused {
		return styleFocus.Render("▎" + text)
	}
	return benchTheme.Muted.Render(" " + text)
}

// directoryColumn 左栏：标题、目录窗口（上下截断指示）、作品信息。
func (m model) directoryColumn(l benchLayout) []string {
	width := l.leftWidth - 1
	rows := m.outlineRows()
	lines := []string{paneTitle("作品目录", m.bench.pane == benchPaneOutline)}
	start, end := outlineWindow(len(rows), l.outlineRows, m.bench.cursor)
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
	note := " " + groupDigits(m.wordCount()) + " 字"
	if active := len(domainmodel.ActiveDirectives(snap.Directives)); active > 0 {
		note += fmt.Sprintf(" · %d 条要求", active)
	}
	return []string{benchTheme.Muted.Render(note), benchTheme.Muted.Render(" " + approvalLabel(snap.Approval))}
}

func (m model) outlineRowLine(row outlineRow, selected bool, width int) string {
	switch {
	case row.placeholder:
		if row.endChapter > row.chapter {
			return benchTheme.Muted.Render(fmt.Sprintf("   ○ %02d–%02d  待规划", row.chapter, row.endChapter))
		}
		return benchTheme.Muted.Render(fmt.Sprintf("   ○ %02d  待规划", row.chapter))
	case row.isChapter():
		return m.outlineChapterLine(row.node, selected, width)
	default:
		fold, style, indent := "▾ ", benchTheme.Title, ""
		if row.collapsed {
			fold = "▸ "
		}
		if row.node.Node.Kind == domainmodel.PlanArc {
			style, indent = styleSubtitle, " "
		}
		summary, pending := "", ""
		if row.collapsed {
			summary = fmt.Sprintf(" · %d/%d 章", row.confirmed, row.chapters)
			if row.pending > 0 {
				pending = " ◇"
			}
		}
		text := indent + fold + truncate(row.node.Node.Title, max(1, width-3-lipgloss.Width(indent+fold+summary+pending)))
		if selected {
			return benchTheme.Selected.Render(fitLine("▎ "+text+summary+pending, width))
		}
		return "  " + style.Render(text) + benchTheme.Muted.Render(summary) + benchTheme.Warning.Render(pending)
	}
}

func (m model) outlineChapterLine(entry workbench.OutlineNode, selected bool, width int) string {
	symbol, style, titleStyle := "○", benchTheme.Muted, benchTheme.Muted
	switch entry.State {
	case workbench.ChapterConfirmed:
		symbol, style, titleStyle = "✓", styleNotice, benchTheme.Text
	case workbench.ChapterPending:
		symbol, style, titleStyle = "◇", benchTheme.Warning, benchTheme.Text
	case workbench.ChapterInProgress:
		symbol, style, titleStyle = "◉", benchTheme.Accent, benchTheme.Text
	}
	digits := max(2, len(strconv.Itoa(m.bench.snap.TargetChapters)))
	label := fmt.Sprintf("%0*d  %s", digits, entry.Number, truncate(entry.Node.Title, max(1, width-digits-7)))
	if selected {
		return benchTheme.Selected.Render(fitLine("▎ "+symbol+" "+label, width))
	}
	return "   " + style.Render(symbol) + " " + titleStyle.Render(label)
}
