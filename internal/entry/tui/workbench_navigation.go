package tui

import (
	"maps"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

type benchRefresh struct {
	busy, pending bool
	reader        *workbench.Reader
}

// Mutations can request a refresh while polling is in flight. Coalesce them into
// one follow-up, so their result is observed without overlapping queries.
func (m model) updateWorkbench(message tea.Msg) (tea.Model, tea.Cmd) {
	refreshed, ok := message.(benchRefreshedMsg)
	if !ok || refreshed.gen != m.bench.gen {
		return m.applyWorkbench(message)
	}
	gate := m.bench.refresh
	pending := gate.pending
	gate.busy, gate.pending = false, false
	updated, cmd := m.applyWorkbench(message)
	next := updated.(model)
	if pending && next.page == pageWorkbench {
		cmd = tea.Batch(cmd, next.refreshBenchCmd())
	}
	return next, cmd
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
	target := m.currentTarget()
	if cache == nil {
		return m.buildOutlineRows()
	}
	if cache.rows == nil || cache.first != first || cache.count != len(m.bench.snap.Outline) || cache.target != target || !maps.Equal(cache.collapsed, m.bench.collapsed) {
		*cache = outlineCache{first: first, count: len(m.bench.snap.Outline), target: target, collapsed: maps.Clone(m.bench.collapsed), rows: m.buildOutlineRows()}
	}
	return cache.rows
}

type wrappedText struct {
	text  string
	width int
	lines []string
}

func (c *wrappedText) wrap(text string, width int) []string {
	if c == nil {
		return readingLines(text, width)
	}
	if c.lines == nil || c.text != text || c.width != width {
		c.text, c.width, c.lines = text, width, readingLines(text, width)
	}
	return c.lines
}

func (m *model) selectOutline(index int) {
	m.bench.cursor = index
	m.bench.pinned = true
	m.bench.liveHeld = false
	m.bench.previewOffset, m.bench.detailOffset = 0, 0
}

// Search the complete outline, including collapsed descendants. Only ancestors
// of the match are expanded; unrelated fold preferences remain intact.
func (m *model) findChapter(query string, next bool) bool {
	if query == "" {
		return false
	}
	number, numericErr := strconv.Atoi(query)
	outline := m.bench.snap.Outline
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
		m.bench.view, m.bench.pane = 0, benchPaneMain
		m.bench.tab = benchTabProse
		return true
	}
	return false
}

func (m model) navigateOutline(key tea.KeyType) model {
	rows := m.outlineRows()
	if len(rows) == 0 {
		return m
	}
	// Page keys scroll the focused content; Home/End locate outline boundaries.
	if (key == tea.KeyPgUp || key == tea.KeyPgDown) && ((m.multiPane() && m.bench.pane == benchPaneMain) || (!m.multiPane() && m.bench.view == 0)) {
		delta := max(1, m.workbenchFrame().proseHeight-3)
		if key == tea.KeyPgUp {
			delta = -delta
		}
		return m.scrollBenchPane(benchPaneMain, delta).(model)
	}
	index := m.bench.cursor
	switch key {
	case tea.KeyHome:
		index = 0
	case tea.KeyEnd:
		index = len(rows) - 1
	case tea.KeyPgUp:
		index = max(0, index-max(1, m.workbenchFrame().bodyHeight-3))
	case tea.KeyPgDown:
		index = min(len(rows)-1, index+max(1, m.workbenchFrame().bodyHeight-3))
	}
	for index > 0 && rows[index].placeholder {
		index--
	}
	if !rows[index].placeholder {
		m.selectOutline(index)
	}
	return m
}
