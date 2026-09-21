package tui

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

type benchContent int

const (
	contentOutput benchContent = iota
	contentManuscript
	contentReview
)

const contentTabWidth = 16

var contentLabels = [...]string{"F1 实时输出", "F2 正文", "F3 审阅"}

func (m model) composingReview() bool {
	if m.bench.inputScope != "" {
		return m.bench.inputDecision != ""
	}
	return m.bench.content == contentReview
}

func (m *model) switchContent(content benchContent) {
	m.bench.content, m.bench.pane = content, benchPaneMain
	if content == contentManuscript {
		m.bench.pinned = true
	}
	// Switching a view does not change either the selected chapter or a held stream.
}

func (m model) contentTabs(width int) string {
	var tabs []string
	for i, label := range contentLabels {
		if benchContent(i) == contentOutput && m.bench.writing {
			label += " ●"
		}
		if benchContent(i) == contentReview && m.bench.decision != nil {
			label += " ◇"
		}
		text := " " + label
		text = fitBlock(text, contentTabWidth, 1)[0]
		if m.bench.content == benchContent(i) {
			text = benchTheme.Selected.Render(text)
		} else {
			text = benchTheme.Muted.Render(text)
		}
		tabs = append(tabs, text)
	}
	return fitLine(strings.Join(tabs, ""), width)
}

type outputWrappedBlock struct {
	version uint64
	width   int
	lines   []string
}
type outputLayoutCache struct{ blocks map[uint64]outputWrappedBlock }

func (m model) outputBlocks() ([]activity.OutputBlock, uint64) {
	if m.bench.outputFrozen {
		return m.bench.outputHeld, m.bench.outputDropped
	}
	feed, ok := m.activityFeed()
	if !ok {
		return nil, 0
	}
	return feed.Output, feed.OutputDropped
}

func (m model) outputLines(width int) []string {
	blocks, dropped := m.outputBlocks()
	if len(blocks) == 0 {
		return []string{
			benchTheme.Title.Render("等待创作输出"), "",
			"模型提供的思考、说明和正文预览会分别显示在这里。",
			"已入稿正文请查看 F2；待确认稿件请查看 F3。",
			"离开后重新打开不会回放已丢失的直播内容。",
		}
	}
	var lines []string
	if dropped > 0 {
		lines = append(lines, benchTheme.Warning.Render(fmt.Sprintf("↑ %d 个早期输出块已超出实时保留范围", dropped)), "")
	}
	cache := m.bench.outputCache
	nextCache := make(map[uint64]outputWrappedBlock, len(blocks))
	for _, block := range blocks {
		var entry outputWrappedBlock
		if cache != nil {
			entry = cache.blocks[block.ID]
		}
		if entry.lines == nil || entry.version != block.Version || entry.width != width {
			kind, style := "说明", benchTheme.Text
			switch block.Kind {
			case activity.Thinking:
				kind, style = "思考", benchTheme.Muted
			case activity.Prose:
				kind = "正文预览 · 未入稿 · 最终以确认稿为准"
			}
			task := block.TaskLabel
			if task == "" {
				task = "创作任务"
			}
			if len(block.Scope.ChapterIDs) > 1 {
				task += fmt.Sprintf(" · 跨章任务（%d 章）", len(block.Scope.ChapterIDs))
			} else if block.Scope.ChapterNumber > 0 {
				task += fmt.Sprintf(" · 第 %d 章", block.Scope.ChapterNumber)
			}
			header := task + " · " + kind
			if !block.At.IsZero() {
				header = block.At.Local().Format("15:04:05") + "  " + header
			}
			part := []string{benchTheme.Accent.Bold(true).Render(fitLine(header, width))}
			if block.Truncated {
				part = append(part, benchTheme.Warning.Render("… 本段前文已截断，仅保留最近输出"))
			}
			for _, line := range readingLines(string(block.Text), max(1, width)) {
				part = append(part, style.Render(line))
			}
			part = append(part, "")
			entry = outputWrappedBlock{version: block.Version, width: width, lines: part}
		}
		lines = append(lines, entry.lines...)
		nextCache[block.ID] = entry
	}
	if cache != nil {
		cache.blocks = nextCache
	}
	return lines
}

func (m model) contentPanel(width, height int) []string {
	switch m.bench.content {
	case contentManuscript:
		return m.prosePanel(width, height)
	case contentReview:
		text, err := m.reviewContent()
		if err != nil {
			text = "无法展示审阅内容：" + err.Error()
		}
		lines := readingLines(text, width)
		offset := min(m.bench.reviewOffset, max(0, len(lines)-height))
		return fitBlock(strings.Join(lines[offset:min(len(lines), offset+height)], "\n"), width, height)
	default:
		lines := m.outputLines(width)
		status := "实时跟随 · 上滚暂停 · /follow 回到最新"
		if m.bench.outputFrozen {
			status = "已暂停跟随 · 新输出继续接收 · /follow 回到最新"
		}
		blocks, dropped := m.outputBlocks()
		truncated := dropped > 0
		for _, block := range blocks {
			truncated = truncated || block.Truncated
		}
		if truncated {
			status = "前文已截断 · " + status
		}
		capacity := max(1, height-1)
		offset := max(0, len(lines)-capacity)
		if m.bench.outputFrozen {
			offset = min(m.bench.outputOffset, offset)
		}
		body := []string{benchTheme.Muted.Render(fitLine(status, width))}
		body = append(body, lines[offset:min(len(lines), offset+capacity)]...)
		return fitBlock(strings.Join(body, "\n"), width, height)
	}
}

func (m model) scrollContent(delta int) model {
	l := m.benchLayout()
	switch m.bench.content {
	case contentManuscript:
		return m.scrollProse(delta).(model)
	case contentReview:
		text, err := m.reviewContent()
		if err != nil {
			m.bench.err = err.Error()
			return m
		}
		last := max(0, len(readingLines(text, l.inner))-l.proseRows)
		m.bench.reviewOffset = min(last, max(0, m.bench.reviewOffset+delta))
	default:
		last := max(0, len(m.outputLines(l.inner))-max(1, l.proseRows-1))
		if !m.bench.outputFrozen {
			if delta >= 0 || last == 0 {
				return m
			}
			m.bench.outputHeld, m.bench.outputDropped = m.outputBlocks()
			m.bench.outputFrozen, m.bench.outputOffset = true, last
		}
		m.bench.outputOffset = min(last, max(0, m.bench.outputOffset+delta))
		if delta > 0 && m.bench.outputOffset == last {
			m.bench.outputFrozen, m.bench.outputHeld = false, nil
		}
	}
	return m
}

func (m model) thinkingContent() string {
	feed, ok := m.activityFeed()
	if !ok {
		return ""
	}
	var parts []string
	for _, block := range feed.Output {
		if block.Kind != activity.Thinking {
			continue
		}
		label := block.TaskLabel
		if label == "" {
			label = "创作任务"
		}
		if block.Truncated {
			label += " · 前文已截断"
		}
		parts = append(parts, label+"\n"+string(block.Text))
	}
	if len(parts) == 0 {
		return ""
	}
	return "思考原文 · 模型提供的实时内容\n\n" + strings.Join(parts, "\n\n")
}
