package tui

import (
	"context"
	"encoding/json"
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

// withCandidate 让第 4 章进入等你决定：候选稿 + 决定卡，创作已停。
func withCandidate(m model, stale bool) model {
	m.bench.writing = false
	m.bench.snap.Run.State = domainmodel.RunWaitingUser
	m.bench.snap.CurrentPhase = ""
	m.bench.snap.Outline[4].State = workbench.ChapterPending
	chapter := domainmodel.ManuscriptChapter{ID: "ch-4", PlanNodeID: "c4", Number: 4, Title: "门后的声音",
		Blocks: []domainmodel.ManuscriptBlock{{Text: strings.Repeat("雨停了，屋檐却还在滴水。\n\n他没有敲门。\n\n", 4)}}}
	payload, _ := json.Marshal(chapter)
	m.bench.snap.Candidates = []workbench.ChapterCandidate{{OperationID: "writing", BaseRevision: 3, Chapter: chapter}}
	m.bench.decision = &decisionState{reason: "第 4 章候选稿已就绪，等待你确认", hasProposal: true, stale: stale,
		proposal: domainmodel.Proposal{ID: "candidate-4", Reason: "第四章初稿", Patches: []domainmodel.Patch{{
			Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentManuscript, ID: "ch-4"}, Operation: domainmodel.PatchPut, Content: payload,
		}}}}
	m.bench.activity.Entries[1].Done = true
	m.bench.activity.Tasks[0].Done = true
	return m
}

func frame(m model) string { return ansi.Strip(m.View()) }

func requireContains(t *testing.T, view string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestWorkbenchFramesFitSizesThemesAndStates(t *testing.T) {
	renderer := lipgloss.DefaultRenderer()
	profile, dark := renderer.ColorProfile(), renderer.HasDarkBackground()
	t.Cleanup(func() { renderer.SetColorProfile(profile); renderer.SetHasDarkBackground(dark) })
	renderer.SetColorProfile(termenv.TrueColor)
	for _, isDark := range []bool{false, true} {
		renderer.SetHasDarkBackground(isDark)
		for _, size := range [][2]int{{120, 36}, {150, 40}, {200, 50}} {
			states := map[string]model{"writing": studioModel(t, size[0], size[1])}
			states["decision"] = withCandidate(studioModel(t, size[0], size[1]), false)
			activityView := studioModel(t, size[0], size[1])
			activityView.switchView(viewActivity)
			states["activity"] = activityView
			overlay := studioModel(t, size[0], size[1])
			overlay.bench.input.SetValue("/")
			states["overlay"] = overlay
			panel, _ := studioModel(t, size[0], size[1]).openModelPanel("writer")
			states["panel"] = panel.(model)
			for name, m := range states {
				view := m.View()
				if lipgloss.Height(view) != size[1] {
					t.Fatalf("%s %v: height %d", name, size, lipgloss.Height(view))
				}
				for i, line := range strings.Split(view, "\n") {
					if w := lipgloss.Width(line); w != size[0] {
						t.Fatalf("%s %v: line %d width %d:\n%s", name, size, i, w, ansi.Strip(line))
					}
				}
				requireContains(t, ansi.Strip(view), "AINOVEL", "作品目录", "正文", "活动", "Esc 返回")
			}
		}
	}
}

func TestProsePageFillsNarrowAndCentersWide(t *testing.T) {
	narrow := studioModel(t, 150, 40)
	if l := narrow.benchLayout(); l.proseX != 0 || l.proseWidth != l.inner {
		t.Fatalf("150 columns must give the prose the whole main column: %+v", l)
	}
	// 右栏吸收余量后正文仍铺满；余量超过右栏上限时，正文页才在中栏内居中。
	wide := studioModel(t, 240, 60)
	l := wide.benchLayout()
	if l.railWidth != railMaxWidth || l.proseWidth != proseMaxWidth || l.proseX == 0 || l.inner-l.proseX-l.proseWidth < l.proseX {
		t.Fatalf("240 columns must center a capped prose page beside a max-width rail: %+v", l)
	}
	title := strings.Split(frame(wide), "\n")[l.contentY+1]
	if at := strings.Index(title, "门后的声音"); at < 0 || lipgloss.Width(title[:at]) != l.mainX+benchPad+l.proseX {
		t.Fatalf("title must sit at the prose page origin:\n%s", title)
	}
}

func TestRunRailShowsLiveModelsAgentsAndTotalsOnWideTerminals(t *testing.T) {
	narrow := studioModel(t, 150, 40)
	if l := narrow.benchLayout(); l.railWidth != 0 || strings.Contains(frame(narrow), "本轮运行") {
		t.Fatalf("150 columns must stay two-column: %+v", l)
	}
	wide := studioModel(t, 200, 50)
	l := wide.benchLayout()
	if l.railWidth != 42 || l.inner != proseMaxWidth || l.proseX != 0 || l.railX+l.railWidth != 200 {
		t.Fatalf("200 columns must give the rail the slack beyond the prose page: %+v", l)
	}
	view := frame(wide)
	requireContains(t, view,
		"本轮运行", "模型", "2 个 · 切换 1 次",
		"● deepseek-v4-flash", "当前", "↑192K ↓49K · 缓存 95%", "$0.11 · 7 条消息 · ",
		"· deepseek-v4-pro", "deepseek", "↑41K ↓9.0K · 缓存 12%", "$0.31 · 3 条消息 · ",
		"代理", "第 3 轮", "◉ 作者 · 第 4 章", "工具调用 10 · 重试 1", "· 本任务 $0.11",
		"✓ 策划 · 故事蓝图", "4m00s · $0.31",
		"本轮合计", "2 个任务", "↑233K ↓58K · 缓存 80%", "$0.42 · 已用 4m", "工具调用 13 · 重试 1",
		"/view 本章详情 · /diag 诊断",
	)
	// 最窄的右栏（180 列，32 列宽）每一行都放得下，不出现截断省略号。
	narrowRail := studioModel(t, 180, 45)
	nl := narrowRail.benchLayout()
	for i, row := range strings.Split(frame(narrowRail), "\n")[nl.bodyY:nl.footerY] {
		if cell := ansi.Cut(row, nl.railX, nl.railX+nl.railWidth); strings.Contains(cell, "…") {
			t.Fatalf("rail row %d truncated at 32 columns: %q", i, cell)
		}
	}
	rows := strings.Split(view, "\n")
	if footer := rows[len(rows)-1]; strings.Contains(footer, "↑233K") || !strings.Contains(footer, "deepseek-v4-flash") {
		t.Fatalf("footer must not repeat the rail's totals:\n%s", footer)
	}
	// 右栏只读：滚轮不滚正文，点击不命中任何区域。
	wide.bench.snap.Manuscript[1].Blocks[0].Text = strings.Repeat(wide.bench.snap.Manuscript[1].Blocks[0].Text, 6)
	wide = clickBench(wide, 6, l.outlineY+2)
	wide = wheelBench(wide, l.railX+3, l.contentY+3, false)
	if wide.bench.proseOffset != 0 || wide.selectedChapterNumber() != 2 {
		t.Fatal("wheel over the rail must not scroll the prose")
	}
	wide = clickBench(wide, l.railX+3, l.footerY-2)
	if wide.bench.reading || wide.selectedChapterNumber() != 2 {
		t.Fatal("click on the rail must not hit the decision card or scene strip")
	}
	// 没有活动时右栏说明处境，不留空白。
	wide.bench.activity = activity.Snapshot{}
	requireContains(t, frame(wide), "本轮运行", "准备中")
}

func TestProseViewStreamsLiveChapterAndSceneShowsThinking(t *testing.T) {
	m := studioModel(t, 150, 40)
	view := frame(m)
	requireContains(t, view,
		"◉ 正在创作 · 第 4 章", "已入稿 3 / 8 章",
		"实时预览 · 最终以入稿版本为准", "◌ 生成中的草稿 · 未入稿", "门后却传来一个声音：“你今天来晚了。”▍",
		"AI 创作现场 · 第 4 章写作", "✓ 查阅设定与前情", "落笔章节工作稿 · 已接收 2.0K", "思考 ▏ 保留上一章的雨声作为过渡",
		"681 字 · 2 条要求", "自动推进",
	)
	if strings.Count(view, "门后却传来一个声音") != 1 {
		t.Fatal("scene strip must not repeat the prose that is already streaming above")
	}
	// 新增量到达：光标跟着尾部走；上滚后停在读者所在处，Esc 恢复跟随。
	m.bench.activity.Output[1].Text = append(m.bench.activity.Output[1].Text, []byte(strings.Repeat("\n\n他数着屋檐的滴水声。", 30))...)
	m.bench.activity.Output[1].Version++
	if view = frame(m); !strings.Contains(view, "他数着屋檐的滴水声。▍") || strings.Contains(view, "雨停了，屋檐却还在滴水。") {
		t.Fatalf("live prose must follow the tail:\n%s", view)
	}
	m, _ = press(t, m, tea.KeyUp)
	if !m.bench.proseHold {
		t.Fatal("scrolling up during streaming must hold the tail")
	}
	held := frame(m)
	m.bench.activity.Output[1].Text = append(m.bench.activity.Output[1].Text, []byte("\n\n门开了一条缝。")...)
	if frame(m) != held {
		t.Fatal("new prose moved the held view")
	}
	m, _ = press(t, m, tea.KeyEsc)
	if m.bench.proseHold || !strings.Contains(frame(m), "门开了一条缝。▍") {
		t.Fatal("Esc must resume following the tail")
	}
	// 还没收到正文时只显示构思状态与光标，现场条看思考流。
	m.bench.activity.Output = m.bench.activity.Output[:1]
	requireContains(t, frame(m), "◌ 正在构思", "思考 ▏")
}

// 现场条尾部与活动视图同一排版锚点：逐字追加时前几行纹丝不动、末行只在尾部生长，满行后整体上推一行。
func TestSceneTailStreamsWithoutReflowing(t *testing.T) {
	m := studioModel(t, 150, 40)
	l := m.benchLayout()
	block := &m.bench.activity.Output[0]
	tail := func() []string { // 每行竖线之后的文字
		lines := m.outputTail(m.bench.activity, l.inner)
		for i := range lines {
			_, lines[i], _ = strings.Cut(ansi.Strip(lines[i]), "▏ ")
		}
		return lines
	}
	block.Text = []byte("先把雨声接过来。\n\n" + strings.Repeat("门后的人认出了他的脚步。", 40))
	before := tail()
	block.Text = append(block.Text, "雨"...)
	after := tail()
	if before[0] != after[0] || before[1] != after[1] || !strings.HasPrefix(after[2], before[2]) {
		t.Fatalf("appending a rune reflowed the tail:\n%q\n%q", before, after)
	}
	for i := 0; i < 80 && tail()[0] == after[0]; i++ {
		block.Text = append(block.Text, "雨"...)
	}
	pushed := tail()
	if pushed[0] != after[1] || !strings.HasPrefix(pushed[1], after[2]) {
		t.Fatalf("a full line must push the tail up by exactly one line:\n%q\n%q", after, pushed)
	}
	// 段落之间的空行不占尾部：新段落从新行开始，三行仍全是文字。
	block.Text = append(block.Text, "\n\n门开了。"...)
	for _, line := range tail() {
		if line == "" {
			t.Fatalf("blank paragraph gap leaked into the tail: %q", tail())
		}
	}
}

func TestSceneStripReflectsWaitingIdleAndLeavesWithActivityView(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.activity.Waiting, m.bench.activity.WaitingSince = true, time.Now().Add(-7*time.Second)
	m.bench.activity.Entries[1].Done = true
	requireContains(t, frame(m), "✓ 落笔章节工作稿", "等待模型回应 · 7 秒")
	// 现场条与正文之间隔一行空白，思考尾部占三行，最后一行是当前步／等待计时。
	l := m.benchLayout()
	rows := strings.Split(frame(m), "\n")
	if cell := ansi.Cut(rows[l.sceneY], l.mainX, l.railX); strings.TrimSpace(cell) != "" {
		t.Fatalf("scene strip must start with a blank row, got %q", cell)
	}
	if !strings.Contains(rows[l.sceneY+1], "AI 创作现场 · 第 4 章写作") || !strings.Contains(rows[l.sceneY+2], "思考 ▏") ||
		!strings.Contains(rows[l.sceneY+4], "▏") || !strings.Contains(rows[l.sceneY+benchSceneRows-1], "等待模型回应") {
		t.Fatalf("scene rows out of order:\n%s", strings.Join(rows[l.sceneY:l.sceneY+benchSceneRows], "\n"))
	}

	done := studioModel(t, 150, 40)
	done.bench.writing = false
	done.bench.snap.Run.State = domainmodel.RunCompleted
	done.bench.activity = activity.Snapshot{}
	requireContains(t, frame(done), "✓ 已完成", "AI 创作现场 · 已完成", "全书完成 · /goal 提高目标续写", "/goal 提高目标续写")

	prose := studioModel(t, 150, 40).benchLayout()
	activityLayout := studioModel(t, 150, 40)
	activityLayout.switchView(viewActivity)
	if l := activityLayout.benchLayout(); prose.sceneY < 0 || l.sceneY >= 0 || l.contentRows != prose.contentRows+benchSceneRows {
		t.Fatalf("scene strip must exist only in the prose view: %+v vs %+v", prose, l)
	}
	if strings.Contains(frame(activityLayout), "AI 创作现场") {
		t.Fatal("activity view still renders the scene strip")
	}
}

func TestActivityTimelineMergesByTimeAndFreezesWhileHeld(t *testing.T) {
	m := studioModel(t, 150, 40)
	m, _ = press(t, m, tea.KeyF2)
	view := frame(m)
	requireContains(t, view, "本轮创作过程 · 跟随最新", "本轮创作", "第 4 章写作", "✓ 查阅设定与前情", "第 4 章写作 · 思考", "第 4 章写作 · 正文预览 · 未入稿", "雨停了，屋檐却还在滴水。")
	if strings.Index(view, "思考") > strings.Index(view, "正文预览") {
		t.Fatal("timeline must order blocks by time")
	}
	at := time.Now()
	for i := 0; i < 60; i++ {
		m.bench.activity.Entries = append(m.bench.activity.Entries, activity.Entry{ID: uint64(10 + i), Kind: activity.ToolStart, Tool: "workspace_read", Done: true, At: at.Add(time.Duration(i) * time.Second), DoneAt: at.Add(time.Duration(i) * time.Second)})
	}
	m = pressTimes(t, m, tea.KeyUp, 5)
	if m.bench.activityHeld == nil {
		t.Fatal("scrolling up must hold the timeline")
	}
	held := frame(m)
	requireContains(t, held, "已暂停跟随")
	// 缓冲翻转也不动读者所在处：持有的是当时的快照副本。
	m.bench.activity.Entries = append(m.bench.activity.Entries[30:], activity.Entry{ID: 99, Kind: activity.ToolStart, Tool: "proposal_submit", At: at.Add(time.Minute)})
	m.bench.activity.OutputDropped = 1
	if frame(m) != held {
		t.Fatal("buffer rotation moved the held timeline")
	}
	m = pressTimes(t, m, tea.KeyDown, 5)
	if m.bench.activityHeld != nil {
		t.Fatal("scrolling to the bottom must resume following")
	}
	requireContains(t, frame(m), "跟随最新", "提交候选稿")
	m, _ = press(t, m, tea.KeyHome)
	if m.bench.activityHeld == nil || m.bench.activityOffset != 0 {
		t.Fatal("Home must hold the timeline at the top")
	}
	requireContains(t, frame(m), "早期输出块已超出实时保留范围")
	m, _ = press(t, m, tea.KeyEsc)
	if m.bench.view != viewProse {
		t.Fatal("Esc from the activity view must return to prose")
	}
}

func TestDecisionCardGuardsApprovalAndFeedback(t *testing.T) {
	m := withCandidate(studioModel(t, 150, 40), false)
	view := frame(m)
	requireContains(t, view,
		"◇ 等你决定", "◇ 04  门后的声音", "◇ 候选稿 · 等你确认 · 尚未入稿",
		"◇ 第 4 章 · 门后的声音 · 等待你确认", "新稿 ", "说明：第四章初稿", "y 通过 · 写下修改意见后回车 · /review 查看完整变更",
		"写下修改意见后回车，或输入 y 通过", "◇ 修改意见 · 第 4 章 · 门后的声音", "/review 查看完整变更",
	)
	if l := m.benchLayout(); l.cardY < 0 || l.sceneY < 0 || l.cardY != l.sceneY+benchSceneRows {
		t.Fatalf("decision card must sit between the scene strip and the footer: %+v", l)
	}
	// 稿件在打字期间被替换：y 与修改意见都拒绝，提示先清空。
	m = typeText(t, m, "y")
	m.bench.decision.proposal.ID = "candidate-4b"
	m, cmd := press(t, m, tea.KeyEnter)
	if cmd != nil || !strings.Contains(m.bench.err, "待审稿件已变化") || m.bench.input.Value() != "y" {
		t.Fatalf("changed proposal must refuse approval: err=%q", m.bench.err)
	}
	m, _ = press(t, m, tea.KeyEsc)
	m = typeText(t, m, "结尾再收一点")
	m.bench.decision.proposal.ID = "candidate-4c"
	m, cmd = press(t, m, tea.KeyEnter)
	if cmd != nil || !strings.Contains(m.bench.err, "/note") {
		t.Fatalf("changed proposal must refuse feedback: err=%q", m.bench.err)
	}
	// 过期稿件只能重写。
	stale := withCandidate(studioModel(t, 150, 40), true)
	requireContains(t, frame(stale), "书又有了新变化", "写下修改意见后回车，让它基于最新内容重写")
	stale = typeText(t, stale, "y")
	stale, cmd = press(t, stale, tea.KeyEnter)
	if cmd != nil || !strings.Contains(stale.bench.notice, "不能直接通过") {
		t.Fatalf("stale proposal must not be approved: notice=%q", stale.bench.notice)
	}
	// 创作已恢复时输入框是要求而不是修改意见。
	m, _ = press(t, m, tea.KeyEsc)
	m.bench.writing = true
	requireContains(t, frame(m), "给第 4 章提一个要求")
}

func TestCommandPaletteFiltersJumpsAndRejectsUnknown(t *testing.T) {
	m := studioModel(t, 150, 40)
	m = typeText(t, m, "/")
	requireContains(t, frame(m), "命令 · 回车执行，支持唯一前缀", "/current", "/note /n <要求>", "继续输入首字母筛选")
	m = typeText(t, m, "a")
	view := frame(m)
	requireContains(t, view, "/activity", "/accept")
	if strings.Contains(view, "/current") {
		t.Fatal("palette must filter by prefix")
	}
	m, _ = press(t, m, tea.KeyEnter)
	if !strings.Contains(m.bench.err, "/activity 或 /accept") || m.bench.input.Value() != "/a" {
		t.Fatalf("ambiguous prefix must keep the input: err=%q", m.bench.err)
	}
	// 首字母撞车的常用动作走单字母别名：/c 是 continue（创作中被屏蔽即证明没落到 current）、/p 暂停、/n 要求。
	m, _ = press(t, m, tea.KeyEsc)
	m = typeText(t, m, "/c")
	requireContains(t, frame(m), "/continue /c", "/current")
	m, _ = press(t, m, tea.KeyEsc)
	if m, _ = submit(t, m, "/c"); !strings.Contains(m.bench.notice, "创作进行中") {
		t.Fatalf("/c must resolve to /continue: notice=%q", m.bench.notice)
	}
	if _, cmd := submit(t, m, "/p"); cmd == nil {
		t.Fatal("/p must resolve to /pause and issue the pause command")
	}
	if _, cmd := submit(t, m, "/n 结尾留一个未拆的信封"); cmd == nil {
		t.Fatal("/n must resolve to /note and submit the directive")
	}
	m, _ = submit(t, m, "/2")
	if m.selectedChapterNumber() != 2 || !m.bench.pinned {
		t.Fatalf("/2 must pin chapter 2: number=%d pinned=%v", m.selectedChapterNumber(), m.bench.pinned)
	}
	requireContains(t, frame(m), "第 2 章", "✓ 已入稿", "来客没有撑伞", "给第 2 章提一个要求")
	m, _ = submit(t, m, "/门后")
	if m.selectedChapterNumber() != 4 || m.bench.notice != "/next 下一个匹配" {
		t.Fatalf("title search failed: number=%d notice=%q", m.selectedChapterNumber(), m.bench.notice)
	}
	m, _ = submit(t, m, "/xyz")
	if !strings.Contains(m.bench.err, "没有找到匹配的章节") || m.bench.input.Value() != "/xyz" {
		t.Fatalf("unknown command must explain and keep the input: err=%q", m.bench.err)
	}
	m, _ = press(t, m, tea.KeyEsc)
	m, _ = submit(t, m, "/continue")
	if !strings.Contains(m.bench.notice, "创作进行中") {
		t.Fatalf("idle-only command must be blocked while writing: notice=%q", m.bench.notice)
	}
	m, _ = submit(t, m, "/help")
	if !m.bench.reading || !strings.Contains(m.bench.bodyText, "创作控制台") {
		t.Fatal("/help must open the help page")
	}
}

func TestEscapeLayersInputFollowThenHome(t *testing.T) {
	m := studioModel(t, 150, 40)
	m = typeText(t, m, "多一点雨声")
	if m.bench.inputLabel != "第 4 章" {
		t.Fatalf("first keystroke must lock the scope: %q", m.bench.inputLabel)
	}
	m, _ = press(t, m, tea.KeyEsc)
	if m.bench.input.Value() != "" || m.bench.inputScope != "" {
		t.Fatal("Esc must clear the input first")
	}
	m, _ = press(t, m, tea.KeyTab)
	m, _ = press(t, m, tea.KeyUp)
	if !m.bench.pinned || m.selectedChapterNumber() != 3 || m.bench.pane != benchPaneOutline {
		t.Fatalf("outline focus must move and pin: pinned=%v number=%d", m.bench.pinned, m.selectedChapterNumber())
	}
	requireContains(t, frame(m), "正在写第 4 章 · Esc 回到当前章", "✓ 已入稿")
	m, _ = press(t, m, tea.KeyEsc)
	if m.bench.pinned || m.selectedChapterNumber() != 4 || m.bench.pane != benchPaneMain || m.page != pageWorkbench {
		t.Fatalf("Esc must return to the current chapter: pinned=%v number=%d", m.bench.pinned, m.selectedChapterNumber())
	}
	closed := false
	m.bench.activityOff = func() { closed = true }
	m, cmd := press(t, m, tea.KeyEsc)
	if m.page != pageHome || cmd == nil || !closed || m.home.lastOpened != "letters" || len(m.bench.snap.Manuscript) != 0 {
		t.Fatal("Esc from a following workbench must go home and release the book")
	}
}

func TestFocusAndEnterFollowThePane(t *testing.T) {
	m := studioModel(t, 150, 40)
	if m.bench.pane != benchPaneMain {
		t.Fatal("main pane must have focus by default")
	}
	m, _ = press(t, m, tea.KeyEnter)
	if !m.bench.reading || !strings.Contains(m.bench.bodyText, "第 4 章 · 门后的声音") {
		t.Fatal("empty Enter on the main pane must open the chapter")
	}
	m, _ = press(t, m, tea.KeyEsc)
	m, _ = press(t, m, tea.KeyTab)
	m.bench.cursor = 0
	m, _ = press(t, m, tea.KeyEnter)
	if !m.bench.collapsed["v1"] || !strings.Contains(frame(m), "▸ 第一卷") {
		t.Fatal("empty Enter on a header row must fold it")
	}
	requireContains(t, frame(m), "目录", "第一卷 · 未寄出的信", "4 章 · 已入稿 3 章")
	m, _ = press(t, m, tea.KeyEnter)
	if m.bench.collapsed["v1"] {
		t.Fatal("Enter again must unfold")
	}
}

func TestMouseHitsUseTheSharedLayout(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.snap.Manuscript[1].Blocks[0].Text = strings.Repeat(m.bench.snap.Manuscript[1].Blocks[0].Text, 4)
	l := m.benchLayout()
	m = clickBench(m, 6, l.outlineY+2)
	if m.selectedChapterNumber() != 2 || m.bench.pane != benchPaneOutline || !m.bench.pinned {
		t.Fatalf("click must select the chapter row: number=%d", m.selectedChapterNumber())
	}
	m = wheelBench(m, l.mainX+10, l.contentY+2, false)
	if m.selectedChapterNumber() != 2 || m.bench.proseOffset != 1 {
		t.Fatalf("wheel over prose must scroll prose only: number=%d offset=%d", m.selectedChapterNumber(), m.bench.proseOffset)
	}
	tabs := m.benchTabs(l)
	m = clickBench(m, tabs[1].x0, l.tabsY)
	if m.bench.view != viewActivity || m.bench.pane != benchPaneMain {
		t.Fatal("clicking a tab must switch the view")
	}
	m = clickBench(m, tabs[0].x0, l.tabsY)
	if m.bench.view != viewProse {
		t.Fatal("clicking the prose tab must switch back")
	}
	m = clickBench(m, 3, l.footerY+2)
	if m.bench.input.Value() != "/pause" {
		t.Fatalf("clicking the primary action must fill the command: %q", m.bench.input.Value())
	}
	m.bench.input.SetValue("")

	decided := withCandidate(m, false)
	l = decided.benchLayout()
	decided = clickBench(decided, l.mainX+4, l.cardY+1)
	if decided.selectedChapterNumber() != 4 || !strings.Contains(frame(decided), "◇ 候选稿 · 等你确认") {
		t.Fatal("clicking the decision card must reveal the candidate")
	}
	decided.bench.activity.Entries[1].Err = "写入失败"
	decided.bench.activity.Entries[1].OperationID = "writing"
	if _, cmd := decided.Update(tea.MouseMsg{X: l.mainX + 4, Y: l.sceneY + benchSceneRows - 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}); cmd == nil {
		t.Fatal("clicking the failed current step must open diagnostics")
	}
	// 等待模型时末行是等待计时，报错步上移一行；点击等待行与其余行都不能越界或误命中。
	decided.bench.activity.Waiting = true
	for y := l.sceneY; y < l.sceneY+benchSceneRows; y++ {
		_, cmd := decided.Update(tea.MouseMsg{X: l.mainX + 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if hit := cmd != nil; hit != (y == l.sceneY+benchSceneRows-2) {
			t.Fatalf("scene row %d hit=%v while waiting", y-l.sceneY, hit)
		}
	}
}

func TestLongOutlineGroupsAndTruncatesGracefully(t *testing.T) {
	m := longWorkbench(t, 120)
	m.findChapter("120", false)
	view := frame(m)
	requireContains(t, view, "↑ 前面还有", "第3卷", "✓ 120  雨夜来信 120", "已入稿 120 / 620 章", "360,000 字")
	if strings.Contains(view, "AI 创作现场 ·") {
		t.Fatal("no running task must leave the scene title bare")
	}
	flat := longWorkbench(t, 120)
	flat.bench.snap.Outline = flat.bench.snap.Outline[:0:0]
	for i := 1; i <= 120; i++ {
		flat.bench.snap.Outline = append(flat.bench.snap.Outline, workbench.OutlineNode{Node: domainmodel.PlanNode{ID: fmt.Sprint(i), Kind: domainmodel.PlanChapter, Title: fmt.Sprintf("雨夜来信 %d", i)}, Number: i, State: workbench.ChapterConfirmed})
	}
	flat.bench.rowsCache = nil
	requireContains(t, frame(flat), "第 1–50 章", "后面还有")
}

func TestTooSmallTerminalAsksToMaximize(t *testing.T) {
	m := studioModel(t, 100, 30)
	requireContains(t, frame(m), "请将终端窗口最大化", "当前 100 × 30 · 需要至少 120 × 36")
}

func TestFooterShowsUsageContextAndPrimaryAction(t *testing.T) {
	m := studioModel(t, 150, 40)
	requireContains(t, frame(m), "deepseek-v4-flash · ↑233K ↓58K · 缓存 80% · $0.42", "/pause 暂停推进", "要求 · 第 4 章")
	m.bench.activity.Usage = activity.UsageTotals{}
	if view := frame(m); strings.Contains(view, "↑233K") || !strings.Contains(view, "deepseek-v4-flash") {
		t.Fatal("no usage yet must show only the model")
	}
	m.bench.err = "没有找到匹配的章节"
	requireContains(t, frame(m), "没有找到匹配的章节")
	paused := studioModel(t, 150, 40)
	paused.bench.writing = false
	paused.bench.snap.Run.State = domainmodel.RunPaused
	paused.bench.activity = activity.Snapshot{}
	view := frame(paused)
	requireContains(t, view, "已暂停  ·  已入稿", "/continue 继续创作", "已暂停 · /continue 继续", "◌ 未完成的草稿 · 未入稿")
	if strings.Contains(view, "▍") || strings.Contains(view, "实时预览") {
		t.Fatal("a paused draft must not look like a live stream")
	}
	// 中途取消、没等到收尾的工具调用：创作停下后不再转圈。
	stopped := studioModel(t, 150, 40)
	stopped.bench.writing = false
	stopped.bench.snap.Run.State = domainmodel.RunCancelled
	view = frame(stopped)
	requireContains(t, view, "◦ 落笔章节工作稿 · 未收尾")
	if strings.Contains(view, "⠋ 落笔") {
		t.Fatal("an unfinished call must not spin after the run stopped")
	}
}

func TestLeavingWorkbenchReleasesBookCaches(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.page = pageWorkbench
	m.bench = longWorkbench(t, 500).bench
	oldCache := m.bench.rowsCache
	_ = m.View()
	closed := false
	m.bench.activityOff = func() { closed = true }
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if !closed || m.page != pageHome || len(m.bench.snap.Manuscript) != 0 || m.bench.rowsCache == oldCache || m.bench.refresh.reader != nil {
		t.Fatal("leaving retained the workbench cache or subscription")
	}
	if m.home.lastOpened != "long" {
		t.Fatal("leaving lost library selection")
	}
}

func TestRefreshCoalescesWhileQueryIsInFlight(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.page = pageWorkbench
	m.bench = newWorkbenchState("long", 1)
	if m.refreshBenchCmd() == nil {
		t.Fatal("first refresh missing")
	}
	for i := 0; i < 10; i++ {
		if m.refreshBenchCmd() != nil {
			t.Fatal("overlapping refresh scheduled")
		}
	}
	updated, cmd := m.Update(benchRefreshedMsg{gen: 1, snap: workbench.WorkbenchSnapshot{ProjectID: "long"}})
	m = updated.(model)
	if cmd == nil || !m.bench.refresh.busy || m.bench.refresh.pending {
		t.Fatal("coalesced refresh did not schedule exactly one follow-up")
	}
	updated, cmd = m.Update(benchRefreshedMsg{gen: 1, snap: workbench.WorkbenchSnapshot{ProjectID: "long"}})
	m = updated.(model)
	if cmd != nil || m.bench.refresh.busy {
		t.Fatal("idle refresh loop did not settle")
	}
}

func BenchmarkWorkbenchView(b *testing.B) {
	m := studioModel(&testing.T{}, 150, 40)
	_ = m.View()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

func BenchmarkLongWorkbench(b *testing.B) {
	for _, count := range []int{50, 500, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			m := longWorkbench(b, count)
			m.findChapter(fmt.Sprint(count), false)
			_ = m.View()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
	}
}

func BenchmarkLongWorkbenchStreaming(b *testing.B) {
	m := longWorkbench(b, 500)
	m.findChapter("500", false)
	m.bench.snap.Outline[len(m.bench.snap.Outline)-1].State = workbench.ChapterInProgress
	m.bench.rowsCache = nil
	prefix := string(m.bench.activity.Output[1].Text)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.bench.activity.Output[1].Text = []byte(prefix + fmt.Sprint(i))
		m.bench.activity.Output[1].Version = uint64(i + 2)
		_ = m.View()
	}
}
