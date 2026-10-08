package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// proseSource 正文视图的内容来源，按校正链取最新一层：
// 候选稿（待确认）> 正在写的章的实时正文（易失）> 已入稿正文（权威）> 规划摘要。
type proseSource struct {
	overline, title, state string
	text                   string
	live                   bool // 正在流式接收：尾部跟随并显示光标
	truncated              bool
}

func (m model) proseSource() proseSource {
	snap := m.bench.snap
	rows := m.outlineRows()
	if cursor := m.bench.cursor; cursor >= 0 && cursor < len(rows) && rows[cursor].header() {
		return sectionSource(rows[cursor])
	}
	number := m.selectedChapterNumber()
	if number == 0 {
		return m.overviewSource()
	}
	overline := fmt.Sprintf("第 %d 章", number)
	if candidate, ok := m.candidateFor(number); ok {
		return proseSource{overline: overline, title: candidate.Title, state: "◇ 候选稿 · 等你确认 · 尚未入稿", text: chapterText(candidate)}
	}
	node, _ := m.outlineChapter(number)
	if node.State == workbench.ChapterInProgress {
		// 创作停下（暂停、出错）后草稿还在，但不再是直播：不跟随尾部、不显示光标。
		src := proseSource{overline: overline, title: node.Node.Title, state: "◌ 生成中的草稿 · 未入稿", live: m.bench.writing}
		if !src.live {
			src.state = "◌ 未完成的草稿 · 未入稿"
		}
		if block, ok := m.liveProse(number); ok {
			src.text, src.truncated = string(block.Text), block.Truncated
		} else if node.Detail != "" {
			src.state = "◌ " + node.Detail
		} else if src.live {
			src.state = "◌ 正在构思"
		}
		return src
	}
	if chapter, ok := chapterByNumber(snap.Manuscript, number); ok {
		return proseSource{overline: overline, title: chapter.Title, state: "✓ 已入稿", text: chapterText(chapter)}
	}
	state := "○ 已规划 · 尚无正文"
	if node.Proposed {
		state = "◇ 方案待你确认 · 尚未生效"
	}
	return proseSource{overline: overline, title: node.Node.Title, state: state, text: node.Node.Summary}
}

// sectionSource 选中卷/弧头行时显示这一部分的规划摘要与进度。
func sectionSource(row outlineRow) proseSource {
	text := row.node.Node.Summary
	if text == "" {
		text = "这一部分还没有规划摘要。选择其中的章节阅读正文。"
	}
	state := fmt.Sprintf("%d 章 · 已入稿 %d 章", row.chapters, row.confirmed)
	if row.node.Proposed {
		state = fmt.Sprintf("◇ 方案待你确认 · %d 章 · 尚未生效", row.chapters)
	}
	return proseSource{overline: "目录", title: row.node.Node.Title, state: state, text: text}
}

// overviewSource 还没有可选章节时（刚开始、大纲未出）显示作品总览与当下进展。
func (m model) overviewSource() proseSource {
	snap := m.bench.snap
	src := proseSource{overline: "作品", title: snap.Intent.Premise}
	if src.title == "" {
		src.title = "作品总览"
	}
	if !m.bench.writing {
		src.text = m.nextStepText()
		return src
	}
	src.live = true
	src.state = "◌ 创作进行中"
	src.text = snap.CurrentPhase
	if src.text == "" {
		src.text = "正在构思与规划，下方创作现场可以看到模型的每一步。"
	}
	return src
}

// nextStepText 总览页的下一步引导：键名属于界面键位表，随界面文案维护。
func (m model) nextStepText() string {
	switch m.bench.situation() {
	case situationDecidingProposal:
		return "有稿件等你确认：/review 查看全部内容，输入 y 回车通过，或写下修改意见后回车。"
	case situationDeciding:
		return "创作在等你的决定：" + m.bench.decision.reason
	case situationNoRun:
		return "输入 /continue 回车开始创作。"
	case situationFailed:
		return "输入 /diag 查看诊断，/continue 重试。"
	case situationCancelled:
		return "创作已取消。输入 /continue 从现有内容继续。"
	case situationCompleted:
		return "全书完成。输入 /continue 续写，由 AI 决定写多少；/goal 章数 可固定篇幅。"
	case situationPaused:
		return "创作已暂停。输入 /continue 回车继续。"
	default:
		return "从一个故事开始。输入 /continue 继续创作，或直接写下要求。"
	}
}

func (m model) candidateFor(number int) (domainmodel.ManuscriptChapter, bool) {
	for _, candidate := range m.bench.snap.Candidates {
		if candidate.Chapter.Number == number {
			return candidate.Chapter, true
		}
	}
	return domainmodel.ManuscriptChapter{}, false
}

// liveProse 取正在写的章的最新正文预览块。
func (m model) liveProse(number int) (activity.OutputBlock, bool) {
	feed, ok := m.activityFeed()
	if !ok {
		return activity.OutputBlock{}, false
	}
	for i := len(feed.Output) - 1; i >= 0; i-- {
		if block := feed.Output[i]; m.proseOf(block, number) {
			return block, true
		}
	}
	return activity.OutputBlock{}, false
}

// proseOf 报告输出块是不是第 number 章的正文预览：块自带章节归属时按章匹配，否则按当前章。
func (m model) proseOf(block activity.OutputBlock, number int) bool {
	return block.Kind == activity.Prose && number > 0 &&
		(block.Scope.ChapterNumber == number || block.Scope.ChapterNumber == 0 && number == m.currentChapter())
}

func chapterByNumber(chapters []domainmodel.ManuscriptChapter, number int) (domainmodel.ManuscriptChapter, bool) {
	for _, chapter := range chapters {
		if chapter.Number == number {
			return chapter, true
		}
	}
	return domainmodel.ManuscriptChapter{}, false
}

func chapterText(chapter domainmodel.ManuscriptChapter) string {
	paragraphs := make([]string, 0, len(chapter.Blocks))
	for _, block := range chapter.Blocks {
		paragraphs = append(paragraphs, block.Text)
	}
	return strings.Join(paragraphs, "\n\n")
}

const viewHeadRows = 1

// viewHead 正文/活动视图共用的单行头：上标、标题，右侧状态；放不下时截断标题。
func viewHead(overline, title, right string, width int) string {
	return alignRight(benchTheme.Muted.Render(overline)+"  "+benchTheme.Title.Render(title), right, width)
}

func proseStateStyle(state string) lipgloss.Style {
	switch {
	case strings.HasPrefix(state, "◇"):
		return benchTheme.Warning
	case strings.HasPrefix(state, "◌"):
		return benchTheme.Accent
	case strings.HasPrefix(state, "✓"):
		return lipgloss.NewStyle().Foreground(benchColors.Success)
	default:
		return benchTheme.Muted
	}
}

// proseBody 正文行；直播时末尾带光标。
func (m model) proseBody(src proseSource, width int) []string {
	var body []string
	if src.truncated {
		body = append(body, benchTheme.Warning.Render("… 前文已超出实时保留范围，完整正文以入稿为准"))
	}
	if src.text != "" {
		body = append(body, m.bench.proseCache.wrap(src.text, width)...)
	}
	if src.live {
		caret := benchTheme.Accent.Render("▍")
		if len(body) > 0 && lipgloss.Width(body[len(body)-1]) < width {
			body[len(body)-1] += caret
		} else {
			body = append(body, caret)
		}
	}
	return body
}

func (m model) proseOffset(total, capacity int, live bool) int {
	last := max(0, total-capacity)
	if live && !m.bench.proseHold {
		return last
	}
	return min(m.bench.proseOffset, last)
}

// proseView 正文页：章号、标题与状态一行，其下即正文，铺满主栏；直播跟随尾部，上滚后停在读者所在处。
func (m model) proseView(l benchLayout) []string {
	src := m.proseSource()
	lines := []string{viewHead(src.overline, src.title, proseStateStyle(src.state).Render(src.state), l.inner)}
	body := m.proseBody(src, l.inner)
	capacity := max(1, l.contentRows-viewHeadRows)
	offset := m.proseOffset(len(body), capacity, src.live)
	for _, line := range body[offset:min(len(body), offset+capacity)] {
		lines = append(lines, benchTheme.Text.Render(line))
	}
	return lines
}

func (m model) scrollProse(delta int) model {
	l := m.benchLayout()
	src := m.proseSource()
	total := len(m.proseBody(src, l.inner))
	capacity := max(1, l.contentRows-viewHeadRows)
	last := max(0, total-capacity)
	next := min(last, max(0, m.proseOffset(total, capacity, src.live)+delta))
	m.bench.proseOffset = next
	m.bench.proseHold = src.live && next < last
	return m
}

// openChapter 全屏阅读当前正文视图的内容。
func (m model) openChapter() (model, bool) {
	src := m.proseSource()
	if src.text == "" {
		return m, false
	}
	return m.openBody(src.overline + " · " + src.title + "\n" + src.state + "\n\n" + src.text), true
}
