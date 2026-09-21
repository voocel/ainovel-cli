package tui

import (
	"fmt"
	"strconv"

	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

const directoryGroupSize = 50

// Range groups are presentation-only: never added to the project plan.
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

// Revealing a running chapter must not change the output tab or an input draft.
func (m *model) revealCurrent(number int) bool {
	b := &m.bench
	content, pane, pinned, search := b.content, b.pane, b.pinned, b.search
	previewOffset, outputOffset, outputFrozen, outputHeld := b.previewOffset, b.outputOffset, b.outputFrozen, b.outputHeld
	if number == 0 || !m.findChapter(strconv.Itoa(number), false) {
		return false
	}
	b.content, b.pane, b.pinned, b.search = content, pane, pinned, search
	b.previewOffset, b.outputOffset, b.outputFrozen, b.outputHeld = previewOffset, outputOffset, outputFrozen, outputHeld
	return true
}

func (m *model) returnToCurrentChapter() {
	if !m.revealCurrent(m.currentChapter()) {
		m.bench.notice = "暂无正在写作、待确认或已入稿的章节"
		return
	}
	m.bench.pane = benchPaneOutline
	m.bench.pinned = false
}

const directorySearchLabel = "/ 跳章或搜索"
const directoryCurrentLabel = "◎ 当前"
