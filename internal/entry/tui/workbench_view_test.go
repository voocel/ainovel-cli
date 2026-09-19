package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

func studioModel(t *testing.T, width, height int) model {
	t.Helper()
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.page, m.width, m.height = pageWorkbench, width, height
	m.bench = newWorkbenchState("letters", 1)
	m.bench.loaded, m.bench.writing = true, true
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "letters", Intent: domainmodel.Intent{Premise: "亡者来信", TargetChapters: 8},
		Run: &domainmodel.CreationRun{ID: "run", State: domainmodel.RunRunning},
	}
	for i, title := range []string{"无人签收", "雨夜来客", "回信", "门后的声音"} {
		state := workbench.ChapterConfirmed
		if i == 3 {
			state = workbench.ChapterInProgress
		}
		m.bench.snap.Outline = append(m.bench.snap.Outline, workbench.OutlineNode{Node: domainmodel.PlanNode{ID: fmt.Sprint(i), Kind: domainmodel.PlanChapter, Title: title}, Number: i + 1, State: state})
	}
	m.bench.cursor = 3
	at := time.Date(2026, 9, 17, 11, 29, 34, 0, time.Local)
	m.bench.activity = activity.Snapshot{Seq: 1, ProjectID: "letters", RunID: "run", OperationID: "writing", Thinking: true, ThinkingSeen: true,
		ThinkingNote: "保留上一章的雨声作为过渡。门后的人可以认出他的脚步，但暂时不解释身份。",
		Prose:        []byte("雨停了，屋檐却还在滴水。\n\n陈渡站在门前，把那封没有邮戳的信翻了过来。收件人的名字已经被雨水洇开，只有最后一个“渡”字，还像一枚钉子，牢牢留在纸上。\n\n他没有敲门。\n\n门后却传来一个声音：“你今天来晚了。”"),
		Entries: []activity.Entry{
			{Kind: activity.ToolStart, Tool: "authority_read", Done: true, At: at, DoneAt: at.Add(2 * time.Second)},
			{Kind: activity.ToolStart, Tool: "workspace_put_chapter", Bytes: 2048, At: at.Add(10 * time.Second)},
		},
	}
	m.bench.activity.Output = []activity.OutputBlock{
		{ID: 1, Version: 1, OperationID: "writing", TaskLabel: "第 4 章写作", Kind: activity.Thinking, At: at, Text: []byte(m.bench.activity.ThinkingNote)},
		{ID: 2, Version: 1, OperationID: "writing", TaskLabel: "第 4 章写作", Kind: activity.Prose, At: at, Text: append([]byte(nil), m.bench.activity.Prose...)},
	}
	return m
}

func clickBench(m model, x, y int) model {
	updated, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	return updated.(model)
}

func TestStudioClickAndKeyboardRespectInputFocus(t *testing.T) {
	m := studioModel(t, 150, 40)
	l := m.benchLayout()
	keyed, _ := submit(t, m, "/t")
	if !keyed.bench.reading || !strings.Contains(keyed.bench.body.View(), "保留上一章的雨声") {
		t.Fatal("/think must open the thinking text full screen")
	}
	clicked := clickBench(m, 5, l.outlineRowsY+1)
	if clicked.bench.cursor != 1 || !clicked.bench.pinned || clicked.bench.pane != benchPaneOutline {
		t.Fatalf("outline click: cursor=%d pinned=%v", clicked.bench.cursor, clicked.bench.pinned)
	}
	if clicked = clickBench(m, l.mainX+3, l.activityY+1); clicked.bench.cursor != m.bench.cursor || clicked.bench.pane != benchPaneFeed {
		t.Fatal("event clicks must focus events without changing the chapter")
	}
	// 字母只进输入框（写作中的 p 不是暂停）；点大纲仍选章，且不清空输入。
	typed, cmd := pressRune(t, m, 'p')
	if cmd != nil || typed.bench.input.Value() != "p" {
		t.Fatalf("letters must go to the input: cmd=%v value=%q", cmd, typed.bench.input.Value())
	}
	typed = clickBench(typed, 5, l.outlineRowsY+1)
	if typed.bench.cursor != 1 || typed.bench.input.Value() != "p" {
		t.Fatalf("click must select without clearing input: cursor=%d value=%q", typed.bench.cursor, typed.bench.input.Value())
	}
}

func TestStudioCommandPaletteAndEsc(t *testing.T) {
	m := studioModel(t, 150, 40)
	l := m.benchLayout()
	view := ansi.Strip(typeText(t, m, "/").View())
	for _, want := range []string{"命令 · 回车执行", "/pause", "暂停推进", "还有 11 个"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overlay missing %q:\n%s", want, view)
		}
	}
	// 浮层叠在中栏底部，几何不变：输入 /e 时只剩 export 一行，落在活动条最后一行。
	filtered := typeText(t, m, "/e")
	lines := strings.Split(ansi.Strip(filtered.View()), "\n")
	base := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) != 40 || !strings.Contains(lines[l.footerY-2], "命令") || !strings.Contains(lines[l.footerY-1], "/export <路径>") || strings.Contains(lines[l.footerY-1], "pause") {
		t.Fatalf("overlay not filtered or misplaced:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Join(lines[l.bodyY:l.footerY-2], "\n") != strings.Join(base[l.bodyY:l.footerY-2], "\n") {
		t.Fatal("overlay must only cover its own rows")
	}
	// 未知命令按章号或标题跳转；找不到时保留输入并报错。
	jumped, _ := submit(t, m, "/2")
	if jumped.bench.pane != benchPaneMain || jumped.selectedChapterNumber() != 2 || jumped.bench.input.Value() != "" {
		t.Fatalf("/2 must jump: pane=%v chapter=%d", jumped.bench.pane, jumped.selectedChapterNumber())
	}
	missed, _ := submit(t, m, "/没有这章")
	if !strings.Contains(missed.bench.err, "没有找到匹配的章节") || missed.bench.input.Value() != "/没有这章" {
		t.Fatalf("miss must keep input: err=%q value=%q", missed.bench.err, missed.bench.input.Value())
	}
	// Esc 先清空输入，再做分层返回。
	cleared, _ := press(t, missed, tea.KeyEsc)
	if cleared.bench.input.Value() != "" || cleared.page != pageWorkbench {
		t.Fatal("first Esc must only clear the input")
	}
}

// 底栏恒 4 行：决定卡、报错、通知、输入态都只改底栏，正文区一行不动，
// 大纲行的坐标因此不会随状态漂移。
func TestStudioFooterStateNeverMovesBody(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.writing = false
	l := m.benchLayout()
	if l.footerY != 40-benchFooterRows || lipgloss.Height(m.View()) != 40 {
		t.Fatalf("layout: footerY=%d", l.footerY)
	}
	// 概览卡的"阶段"随决定状态改文案，属内容变化；其余正文区一字不动。
	bodyOf := func(m model) string {
		lines := strings.Split(ansi.Strip(m.View()), "\n")[l.contentY+1 : l.footerY]
		for i := range lines {
			lines[i] = ansi.Cut(lines[i], l.mainX, l.inspectorX-1)
		}
		return strings.Join(lines, "\n")
	}
	base := bodyOf(m)
	decided, erred, noticed := m, m, m
	decided.bench.decision = &decisionState{reason: "第 4 章候选稿已就绪", hasProposal: true, stale: true}
	erred.bench.err = "导出失败"
	noticed.bench.notice = "已导出 3 章"
	prompted := typeText(t, m, "对第四章的要求")
	for name, variant := range map[string]model{"decision": decided, "error": erred, "notice": noticed, "prompt": prompted} {
		if bodyOf(variant) != base {
			t.Fatalf("%s changed the body", name)
		}
	}
	decided.switchContent(contentReview)
	view := ansi.Strip(decided.View())
	if !strings.Contains(view, "等你决定") || !strings.Contains(view, "不能直接通过") || !strings.Contains(view, "让它基于最新内容重写") || strings.Contains(view, "输入 y 通过") {
		t.Fatalf("stale decision footer:\n%s", view)
	}
}

// 五区几何与渲染同源：两条竖分隔线、左右栏的横分隔线和各区标题都落在布局给出的坐标上。
func TestStudioColumnsMatchLayout(t *testing.T) {
	for _, size := range [][2]int{{150, 40}, {180, 50}, {240, 60}} {
		m := studioModel(t, size[0], size[1])
		l := m.benchLayout()
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		for y := l.bodyY; y < l.footerY; y++ {
			if y == l.contentY-1 {
				if ansi.Cut(lines[y], l.leftWidth, l.leftWidth+1) != "├" || ansi.Cut(lines[y], l.inspectorX-1, l.inspectorX) != "┤" {
					t.Fatalf("%v row %d: divider junction misplaced", size, y)
				}
				continue
			}
			first := strings.Index(lines[y], "│")
			if first < 0 || lipgloss.Width(lines[y][:first]) != l.leftWidth || strings.Count(lines[y], "│") != 2 {
				t.Fatalf("%v row %d: separator misplaced: %q", size, y, lines[y])
			}
			if ansi.Cut(lines[y], l.inspectorX-1, l.inspectorX) != "│" {
				t.Fatalf("%v row %d: inspector separator misplaced", size, y)
			}
		}
		if !strings.HasPrefix(lines[l.bodyY], "概览 ─") || !strings.HasPrefix(lines[l.outlineTitleY], "▎大纲 ─") || !strings.HasPrefix(strings.TrimSpace(ansi.Cut(lines[l.detailY], l.inspectorX, l.width)), "本章 ─") || !strings.HasPrefix(strings.TrimSpace(ansi.Cut(lines[l.detailY+1], l.inspectorX, l.width)), "第 4 章") {
			t.Fatalf("%v left column misplaced:\n%s", size, strings.Join(lines[l.bodyY:l.footerY], "\n"))
		}
		main := func(y int) string { return strings.TrimSpace(ansi.Cut(lines[y], l.mainX, l.inspectorX-1)) }
		if !strings.HasPrefix(main(l.activityY), "执行事件 ─") || !strings.HasPrefix(main(l.activityY+1), "11:29:34 ✓ 查阅设定与前情") || !strings.HasSuffix(main(l.activityY+1), "2.0s") {
			t.Fatalf("%v activity strip misplaced: %q / %q", size, main(l.activityY), main(l.activityY+1))
		}
	}
}

func TestSmallTerminalAsksForFullscreen(t *testing.T) {
	m := studioModel(t, 100, 28)
	view := m.View()
	if !strings.Contains(view, "请将终端窗口最大化") || !strings.Contains(view, "150 × 40") || lipgloss.Height(view) != 28 {
		t.Fatalf("size gate:\n%s", view)
	}
	if clicked := clickBench(m, 5, 5); clicked.bench.cursor != m.bench.cursor {
		t.Fatal("mouse must be ignored below the size gate")
	}
	m, _ = press(t, m, tea.KeyEsc)
	if m.page != pageHome {
		t.Fatal("Esc must still leave the workbench")
	}
}

func TestStudioFramesFitDarkAndLightTerminals(t *testing.T) {
	renderer := lipgloss.DefaultRenderer()
	profile, dark := renderer.ColorProfile(), renderer.HasDarkBackground()
	t.Cleanup(func() { renderer.SetColorProfile(profile); renderer.SetHasDarkBackground(dark) })
	renderer.SetColorProfile(termenv.TrueColor)
	var variants []string
	for _, isDark := range []bool{false, true} {
		renderer.SetHasDarkBackground(isDark)
		for _, size := range [][2]int{{150, 40}, {180, 50}, {240, 60}} {
			m := studioModel(t, size[0], size[1])
			m.bench.writing = false
			m.bench.activity.ThinkingNote = strings.Repeat("很长的思考片段，含中文与🙂。", 80)
			m.bench.decision = &decisionState{reason: strings.Repeat("请检查人物动机", 30), hasProposal: true}
			m.switchContent(contentReview)
			view := m.View()
			if lipgloss.Height(view) != size[1] {
				t.Fatalf("%v dark=%v height=%d", size, isDark, lipgloss.Height(view))
			}
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > size[0] {
					t.Fatalf("%v overflow: %q", size, line)
				}
			}
			if !strings.Contains(ansi.Strip(view), "输入 y 通过") {
				t.Fatalf("decision clipped at %v", size)
			}
			if !strings.Contains(strings.Join(strings.Fields(ansi.Strip(view)), " "), "/? 更多") {
				t.Fatalf("footer clipped at %v", size)
			}
			m = typeText(t, m, strings.Repeat("输入中的中文", 30))
			if lipgloss.Width(m.View()) > size[0] || lipgloss.Height(m.View()) != size[1] {
				t.Fatalf("input frame overflow at %v", size)
			}
		}
		variants = append(variants, studioModel(t, 150, 40).View())
	}
	if variants[0] == variants[1] {
		t.Fatal("light and dark rendering used identical colors")
	}
}

func TestStudioLiveReadingFreezesUntilFollowResumes(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.activity.Output = []activity.OutputBlock{{ID: 1, Version: 1, Kind: activity.Prose, Text: []byte(strings.Repeat("雨停了，他站在门前。\n", 60))}}
	m.bench.pane = benchPaneMain
	m, _ = press(t, m, tea.KeyUp)
	if !m.bench.outputFrozen {
		t.Fatal("scrolling live prose did not stop following")
	}
	l := m.benchLayout()
	before := strings.Join(m.contentPanel(l.inner, l.proseRows), "\n")
	m.bench.activity.Output = []activity.OutputBlock{{ID: 1, Version: 2, Kind: activity.Prose, Text: append(append([]byte(nil), m.bench.activity.Output[0].Text...), []byte("新到达的正文，不应抢走焦点。")...)}}
	after := strings.Join(m.contentPanel(l.inner, l.proseRows), "\n")
	if before != after {
		t.Fatal("stream changed frozen reading content")
	}
	m, _ = submit(t, m, "/f")
	if m.bench.outputFrozen || !strings.Contains(m.View(), "新到达的正文") {
		t.Fatal("follow did not resume at latest prose")
	}
	m.bench.pane = benchPaneFeed
	cursor, offset := m.bench.cursor, m.bench.previewOffset
	m, _ = press(t, m, tea.KeyUp)
	if m.bench.feedOffset != 1 || m.bench.cursor != cursor || m.bench.previewOffset != offset {
		t.Fatal("activity scroll altered chapter reading")
	}
}

func TestStudioDetailSummaryAndFullReport(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.snap.Directives = []domainmodel.Directive{{Text: "保留雨声作为过渡"}}
	m.bench.snap.Manuscript = []domainmodel.ManuscriptChapter{{ID: "ch-4", Number: 4, Title: "门后的声音"}}
	m.bench.snap.Findings = []workbench.WorkbenchFinding{{ID: "r/0", ReviewFinding: domainmodel.ReviewFinding{ChapterID: "ch-4", Severity: domainmodel.FindingBlocking, Note: "门后的人身份前后矛盾"}}}
	view := ansi.Strip(m.View())
	for _, want := range []string{"第 4 章 · 进行中", "事实 0 · 发现 1", "! 门后的人身份前后矛盾"} {
		if !strings.Contains(view, want) {
			t.Fatalf("summary missing %q:\n%s", want, view)
		}
	}
	m, _ = submit(t, m, "/v")
	body := m.bench.body.View()
	if !m.bench.reading || !strings.Contains(body, "第 4 章详情") || !strings.Contains(body, "保留雨声作为过渡") {
		t.Fatalf("full report missing:\n%s", body)
	}
}

// 概览卡与状态行：阶段/字数/全书计数一眼可见，本轮 token 与费用随活动累计。
func TestOverviewCardAndStatusLine(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.config.Model = "deepseek-v4-flash"
	m.bench.snap.CurrentPhase = "正在写第 4 章"
	m.bench.snap.Directives = []domainmodel.Directive{{ID: "hook"}}
	m.bench.snap.Manuscript = []domainmodel.ManuscriptChapter{{Number: 1, Blocks: []domainmodel.ManuscriptBlock{{Text: strings.Repeat("字", 1234)}}}}
	m.bench.activity.Usage = activity.UsageTotals{Input: 233000, Output: 58000, CacheRead: 186400, Cost: 0.42}
	view := ansi.Strip(m.View())
	for _, want := range []string{"阶段    正在写第 4 章", "字数    1,234 · 已入稿 1 章", "要求 1 · 事实 0 · 待核验 0", "本轮 Token · 输入 233K · 输出 58K · 缓存命中 186K / 80.0% · $0.42"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	m.bench.activity.Usage = activity.UsageTotals{}
	if view = ansi.Strip(m.View()); !strings.Contains(view, "输入 0 · 输出 0 · 缓存命中 —") {
		t.Fatalf("status line without usage must not invent a cache ratio:\n%s", view)
	}
}
