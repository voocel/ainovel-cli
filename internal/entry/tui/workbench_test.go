package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/app/binding"
	"github.com/voocel/ainovel-cli/internal/app/chief"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// withCandidate 让第 4 章进入等你决定：候选稿 + 决定卡，创作已停。
func withCandidate(m model, stale bool) model {
	m.bench.writing = false
	m.bench.snap.Run.State = domainmodel.RunWaitingUser
	m.bench.snap.CurrentPhase = ""
	m.bench.snap.Outline[4].State = workbench.ChapterPending
	chapter := domainmodel.ManuscriptChapter{ID: "ch-4", PlanNodeID: "c4", Number: 4,
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

func frame(m model) string { return ansi.Strip(m.screen()) }

// requireContains 按屏幕上看到的文字比对：样式的转义序列会把相邻两段不同样式的字隔开。
func requireContains(t *testing.T, view string, wants ...string) {
	t.Helper()
	view = ansi.Strip(view)
	for _, want := range wants {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestWorkbenchFramesFitSizesThemesAndStates(t *testing.T) {
	dark := darkBackground
	t.Cleanup(func() { darkBackground = dark })
	for _, isDark := range []bool{false, true} {
		darkBackground = isDark
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
				view := m.screen()
				if lipgloss.Height(view) != size[1] {
					t.Fatalf("%s %v: height %d", name, size, lipgloss.Height(view))
				}
				for i, line := range strings.Split(view, "\n") {
					if w := lipgloss.Width(line); w != size[0] {
						t.Fatalf("%s %v: line %d width %d:\n%s", name, size, i, w, ansi.Strip(line))
					}
				}
				requireContains(t, ansi.Strip(view), "AINOVEL", "作品目录", "正文", "活动", "Esc ")
			}
		}
	}
}

// 正文页与活动、现场条同宽：任何宽度下都从主栏留白处起笔，不在主栏内居中。
func TestProsePageFillsMainColumn(t *testing.T) {
	for _, width := range []int{150, 170, 240} {
		m := studioModel(t, width, 60)
		l := m.benchLayout()
		rows := strings.Split(frame(m), "\n")
		head, first := rows[l.contentY], rows[l.contentY+1]
		if at := strings.Index(head, "第 4 章"); at < 0 || lipgloss.Width(head[:at]) != l.mainX+benchPad || !strings.Contains(head, "门后的声音") {
			t.Fatalf("%d columns: chapter number and title must share one head row at the main column origin:\n%s", width, head)
		}
		if !strings.Contains(first, "雨停了") {
			t.Fatalf("%d columns: prose must start right below the head row:\n%s", width, first)
		}
	}
}

// railView 是右栏这一列的纯文字。
func railView(m model) string {
	l := m.benchLayout()
	var cells []string
	for _, row := range strings.Split(frame(m), "\n")[l.bodyY:l.footerY] {
		cells = append(cells, ansi.Cut(row, l.railX, l.railX+l.railWidth))
	}
	return strings.Join(cells, "\n")
}

// 全书行在目录顶部：右栏是全书的依据，人物按涉及章数排、放不下的指向 /view；/view 列出全部
// 人物与别名。
func TestBookRowShowsTheWholeBookWithItsCast(t *testing.T) {
	m := studioModel(t, 200, 50)
	m.bench.snap.Cast = nil
	for i := 1; i <= 12; i++ {
		m.bench.snap.Cast = append(m.bench.snap.Cast, workbench.CastMember{Name: fmt.Sprintf("人物%02d", i), Chapters: 20 - i, Latest: 4})
	}
	m.bench.snap.Cast[0].Aliases = []string{"邮差"}
	m.bench.snap.Threads = []workbench.Thread{
		{Name: "姐姐", Question: "姐姐为何失踪", Advanced: 2, Quiet: 1, Next: 5, Answer: 8},
		{Name: "回信", Question: "谁在替亡者回信", Advanced: 3},
	}
	m.bench.cursor = 0
	requireContains(t, railView(m), "全书", "长线", "2 条", "姐姐", "停了 1 章", "姐姐为何失踪", "回信", "第 3 章推进",
		"人物", "12 位", "人物01", "19 章", "人物10", "… 另 2 位 · /view 查看全部")
	if view := railView(m); strings.Contains(view, "人物11") {
		t.Fatalf("the rail lists the leading members only:\n%s", view)
	}
	report := ansi.Strip(m.detailReport())
	requireContains(t, report, "全书详情", "长线 · 2 条", "姐姐：姐姐为何失踪 · 最近第 2 章推进 · 停了 1 章 · 下次第 5 章 · 第 8 章作答",
		"回信：谁在替亡者回信 · 最近第 3 章推进 · 作答章未定", "人物 · 12 位", "人物01（又名 邮差） · 19 章 · 最近第 4 章", "人物12")
}

func TestRailShowsTheBriefOfWhatTheMainViewShows(t *testing.T) {
	narrow := studioModel(t, 150, 40)
	if l := narrow.benchLayout(); l.railWidth != 0 || strings.Contains(frame(narrow), "写作依据") {
		t.Fatalf("150 columns must stay two-column: %+v", l)
	}
	wide := studioModel(t, 200, 50)
	l := wide.benchLayout()
	if l.railWidth != 42 || l.inner != mainTargetWidth || l.railX+l.railWidth != 200 {
		t.Fatalf("200 columns must give the rail the slack beyond the main target width: %+v", l)
	}
	rail := railView
	// 上块是正在写的章的依据：规划、覆盖它的要求（没审过是一个点），分区名后横线补满、
	// 分区里的行左侧一道竖线；下块创作团队钉在底部：这本书上的花费与用时、每个角色的模型、
	// 用时、用量、缓存命中与花费，最后是合计。
	view := rail(wide)
	requireContains(t, view,
		"写作依据 ─", "第 4 章 · 门后的声音", "◉ 写作中", "规划 ─", "│ 陈渡循着回信找到老宅", "转折 ─", "│ 门后的人认得陈渡",
		"长线 ─", "│ · 姐姐", "│ · 回信 · 本章作答",
		"要求 ─", "3 条", "│ · 禁止 主角死亡", "│ · 用声音与一个具体动作承接悬念 · 第 4",
		"创作团队 ─", "$0.42 · 4m12s", "✓ Architect deepseek-v4-pro", "4m00s", "↑41K ↓9.0K · 缓存 12%", "$0.31",
		"◉ Writer deepseek-v4-flash", "↑192K ↓49K · 缓存 95%", "$0.11", "○ Editor deepseek-v4-flash", "还没出场",
		"合计 ↑233K ↓58K · 缓存 80%",
	)
	if cells := strings.Split(view, "\n"); !strings.Contains(cells[len(cells)-1], "合计") {
		t.Fatalf("the crew card must sit at the bottom of the rail:\n%s", view)
	}
	// 创作团队来自落盘的执行记录：实时活动没了（重启、换窗口）照样完整；状态取角色最近一项
	// 任务（失败标出来），模型是它最近实际用的；没有定价的模型（中转、自部署）不显示 $0。
	crew := wide
	crew.bench.activity = activity.Snapshot{}
	crew.bench.snap.Usage = workbench.TeamUsage{
		Total: domainmodel.Usage{Input: 50000, Output: 8000, CacheRead: 25000}, Worked: 3 * time.Minute,
		Roles: map[string]workbench.RoleUsage{"editor": {
			Usage: domainmodel.Usage{Input: 50000, Output: 8000, CacheRead: 25000}, Worked: 3 * time.Minute,
			Model: "glm-5", State: domainmodel.OperationFailed,
		}},
	}
	if view := rail(crew); strings.Contains(view, "$") || !strings.Contains(view, "! Editor glm-5") || !strings.Contains(view, "3m00s") ||
		!strings.Contains(view, "↑50K ↓8.0K · 缓存 50%") || strings.Count(view, "还没出场") != 3 {
		t.Fatalf("crew must come from the persisted usage and hide unknown prices:\n%s", view)
	}
	for _, gone := range []string{"代理", "工具调用", "条消息", "本轮运行"} {
		if strings.Contains(view, gone) {
			t.Fatalf("rail still shows run telemetry %q:\n%s", gone, view)
		}
	}
	rows := strings.Split(frame(wide), "\n")
	if footer := rows[len(rows)-1]; strings.Contains(footer, "↑233K") || !strings.Contains(footer, "deepseek-v4-flash") {
		t.Fatalf("footer must not repeat the rail's totals:\n%s", footer)
	}
	// 两块都来自落盘数据：没有实时活动（重启、换窗口）时照样完整；这本书还没有执行记录时
	// 创作团队不出现。
	idle := wide
	idle.bench.activity = activity.Snapshot{}
	if view := rail(idle); !strings.Contains(view, "陈渡循着回信找到老宅") || !strings.Contains(view, "合计 ↑233K") {
		t.Fatalf("idle rail must keep the brief and the crew card:\n%s", view)
	}
	idle.bench.snap.Usage = workbench.TeamUsage{}
	if view := rail(idle); strings.Contains(view, "创作团队") {
		t.Fatalf("a run without any attempt has no crew card:\n%s", view)
	}
	// 选中审过的章：要求带审阅结论，审阅意见与本章设定各成一段，计数标出要紧的；带未解决
	// 意见的章，标题旁与目录同样标“!”（D94）。
	reviewed := clickBench(wide, 6, l.outlineY+4)
	requireContains(t, rail(reviewed),
		"第 3 章 · 回信", "! 未解决 1 条", "违反 1 · 2 条", "✓ 禁止 主角死亡", "! 保持悬疑氛围",
		"审阅意见", "未解决 1 · 2 条", "! 回信背面的笔迹提前揭示了门后人的身份", "· 雨声意象连续三章出现",
		"本章设定", "待核验 1 · 2 条", "│ 事件 陈渡：在口袋里摸到一把陌生的钥", "│ 伏笔 回信：背面是陈渡本人的笔迹",
	)
	// 返工要求（D78）：这一章写在要求之前，标出要按它重写。
	reworking := reviewed
	reworking.bench.snap.Briefs = maps.Clone(reviewed.bench.snap.Briefs)
	third := reworking.bench.snap.Briefs[3]
	third.Requirements = append(slices.Clone(third.Requirements), workbench.BriefRequirement{ID: "directive:rework", Text: "紧凑点", Scope: "第 3 章", Rework: true})
	reworking.bench.snap.Briefs[3] = third
	requireContains(t, rail(reworking), "↻ 紧凑点 · 第 3 章 · 待按它重写")
	// 卷头行：这一部分的进度（点击头行会折叠，这里直接移光标）。
	section := wide
	section.bench.cursor = 1
	requireContains(t, rail(section), "第一卷 · 未寄出的信", "3/4 章")
	// 最窄的右栏（170 列，32 列宽）依据每一行都放得下，不出现截断省略号；团队里放不下时
	// 只截短模型名，角色名与用时完整。
	narrowRail := studioModel(t, 170, 45)
	nl := narrowRail.benchLayout()
	if nl.railWidth != railMinWidth || studioModel(t, 169, 45).benchLayout().railWidth != 0 {
		t.Fatalf("rail must appear at exactly %d columns with the minimum width: %+v", railThreshold, nl)
	}
	brief, team, _ := strings.Cut(rail(narrowRail), "创作团队")
	if strings.Contains(brief, "…") {
		t.Fatalf("brief truncated at 32 columns:\n%s", brief)
	}
	requireContains(t, team, "✓ Architect deepseek-v4", "4m00s", "◉ Writer deepseek-v4-flash", "12s")
	// 右栏只读：滚轮不滚正文，点击不命中任何区域。
	wide.bench.snap.Manuscript[1].Blocks[0].Text = strings.Repeat(wide.bench.snap.Manuscript[1].Blocks[0].Text, 6)
	wide = clickBench(wide, 6, l.outlineY+3)
	wide = wheelBench(wide, l.railX+3, l.contentY+3, false)
	if wide.bench.proseOffset != 0 || wide.selectedChapterNumber() != 2 {
		t.Fatal("wheel over the rail must not scroll the prose")
	}
	wide = clickBench(wide, l.railX+3, l.footerY-2)
	if wide.bench.reading || wide.selectedChapterNumber() != 2 {
		t.Fatal("click on the rail must not hit the decision card or scene strip")
	}
}

func TestProseViewStreamsLiveChapterAndSceneShowsThinking(t *testing.T) {
	m := studioModel(t, 150, 40)
	view := frame(m)
	requireContains(t, view,
		"◉ 正在落笔第 4 章", "已入稿 3 / 8 章",
		"实时预览 · 最终以入稿版本为准", "◌ 生成中的草稿 · 未入稿", "门后却传来一个声音：“你今天来晚了。”▍",
		"AI 创作现场 · 第 4 章写作", "✓ 查阅设定与前情", "落笔章节工作稿 · 已接收 2.0K", "思考  保留上一章的雨声作为过渡",
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
	requireContains(t, frame(m), "◌ 正在构思", "思考  ")
}

// 现场条与活动视图同一排版锚点：逐字追加时前几行纹丝不动、末行只在尾部生长，满行后整体上推一行。
func TestSceneTailStreamsWithoutReflowing(t *testing.T) {
	m := studioModel(t, 150, 40)
	l := m.benchLayout()
	block := &m.bench.activity.Output[0]
	tail := func() []string { // 思考块各行栏目名之后的文字
		var lines []string
		for _, row := range m.sceneRows(m.bench.activity, l.inner, sceneBodyRows) {
			if row.body != "" {
				lines = append(lines, ansi.Strip(row.body))
			}
		}
		return lines
	}
	block.Text = []byte("先把雨声接过来。\n\n" + strings.Repeat("门后的人认出了他的脚步。", 40))
	before := tail()
	block.Version++
	block.Text = append(block.Text, "雨"...)
	after := tail()
	if len(before) != len(after) || !strings.HasPrefix(after[len(after)-1], before[len(before)-1]) {
		t.Fatalf("appending a rune reflowed the tail:\n%q\n%q", before, after)
	}
	for i := range before[:len(before)-1] {
		if before[i] != after[i] {
			t.Fatalf("appending a rune moved line %d:\n%q\n%q", i, before, after)
		}
	}
	for i := 0; i < 80 && tail()[0] == after[0]; i++ {
		block.Version++
		block.Text = append(block.Text, "雨"...)
	}
	pushed := tail()
	if pushed[0] != after[1] || !strings.HasPrefix(pushed[len(pushed)-2], after[len(after)-1]) {
		t.Fatalf("a full line must push the tail up by exactly one line:\n%q\n%q", after, pushed)
	}
	// 段落之间的空行不占行：新段落从新行开始。
	block.Version++
	block.Text = append(block.Text, "\n\n门开了。"...)
	for _, line := range tail() {
		if line == "" {
			t.Fatalf("blank paragraph gap leaked into the tail: %q", tail())
		}
	}
}

// 现场条是创作时间线的尾巴：从底部长起，进行中的步骤钉在最后；标题线说「此刻」——创作中
// 行首转着动画、等待计时在行尾，停下后动画消失。
func TestSceneStripReflectsWaitingIdleAndLeavesWithActivityView(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.activity.Waiting, m.bench.activity.WaitingSince = true, time.Now().Add(-7*time.Second)
	m.bench.activity.Entries[1].Done = true
	m.bench.activity.Entries[1].DoneAt = m.bench.activity.Entries[1].At.Add(3 * time.Second)
	requireContains(t, frame(m), "✓ 落笔章节工作稿", "等待模型回应 · 7 秒")
	l := m.benchLayout()
	rows := strings.Split(frame(m), "\n")
	scene := rows[l.sceneY : l.sceneY+benchSceneRows]
	main := func(row string) string { return strings.TrimSpace(ansi.Cut(row, l.mainX, l.mainX+l.mainWidth)) }
	if cell := main(scene[0]); cell != "" {
		t.Fatalf("scene strip must start with a blank row, got %q", cell)
	}
	pulse := starFrames[m.bench.pulse.frame/starHold%len(starFrames)] + " AI 创作现场 · 第 4 章写作"
	if !strings.Contains(scene[1], pulse) || !strings.HasSuffix(main(scene[1]), "等待模型回应 · 7 秒") ||
		!strings.Contains(scene[benchSceneRows-1], "✓ 落笔章节工作稿") || !strings.Contains(scene[benchSceneRows-2], "✓ 查阅设定与前情") {
		t.Fatalf("scene rows out of order:\n%s", strings.Join(scene, "\n"))
	}
	// 思考在步骤之上，栏目名只在块首；内容不满时上方留空。
	first := -1
	for i, row := range scene[2:] {
		if strings.Contains(row, "思考  ") {
			first = i
		}
	}
	if first < 0 || main(scene[2]) != "" {
		t.Fatalf("scene must grow from the bottom with thinking above the steps:\n%s", strings.Join(scene, "\n"))
	}

	done := studioModel(t, 150, 40)
	done.bench.writing = false
	done.bench.snap.Run.State = domainmodel.RunCompleted
	done.bench.activity = activity.Snapshot{}
	requireContains(t, frame(done), "✓ 已完成", "AI 创作现场 · 已完成", "全书完成 · 想续写就跟主编说", "Enter 阅读选中章")
	if title := main(strings.Split(frame(done), "\n")[l.sceneY+1]); !strings.HasPrefix(title, "AI 创作现场 · 已完成") {
		t.Fatalf("the star must go out once creation stops: %q", title)
	}

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
	m, _ = press(t, m, tea.KeyF3)
	view := frame(m)
	requireContains(t, view, "创作过程 · 跟随最新", "第 4 章写作", "✓ 查阅设定与前情", "第 4 章写作 · 思考", "第 4 章写作 · 正文预览 · 未入稿", "雨停了，屋檐却还在滴水。")
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

// 规划方案等你确认时：卡上说清规模、罗盘与怎么调整；方案按生效后的样子投在目录里（◇），
// 选中即可读本章规划；/review 用故事语言写出全部内容，不露 JSON 与文档 ID。
func TestBlueprintProposalReadsAsStoryAndPreviewsInOutline(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.writing = false
	m.bench.snap.Run.State = domainmodel.RunWaitingUser
	m.bench.snap.CurrentPhase = ""
	m.bench.snap.Outline[4].State = workbench.ChapterPlanned
	m.bench.snap.Outline = append(m.bench.snap.Outline,
		workbench.OutlineNode{Node: domainmodel.PlanNode{ID: "plan-arc-2", Kind: domainmodel.PlanArc, ParentID: "v1", Title: "门后"}, Proposed: true},
		workbench.OutlineNode{Node: domainmodel.PlanNode{ID: "plan-chapter-5", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "plan-arc-2", Title: "旧友", Summary: "苏晚认出陈渡"},
			Number: 5, State: workbench.ChapterPlanned, Proposed: true, Detail: "方案待你确认"},
	)
	patch := domainmodel.Patch{Document: domainmodel.DocumentRef{Kind: domainmodel.DocumentPlan, ID: "plan-chapter-5"}, Operation: domainmodel.PatchPut, Content: []byte(`{}`)}
	m.bench.decision = &decisionState{
		reason: "后续章节的蓝图已拟好，等你确认后继续", hasProposal: true, continueAfter: true,
		proposal: domainmodel.Proposal{ID: "blueprint", Patches: []domainmodel.Patch{patch}},
		view: workbench.ProposalView{
			Summary: []string{"新增 1 个故事弧 · 1 章", "新增人物地点 1 个", "设定 1 条"},
			Outline: []narrative.VolumeView{{Volume: 1, Title: "第一卷 · 未寄出的信", Arcs: []narrative.ArcView{{
				Arc: 2, Title: "门后", Summary: "门后的人现身",
				Chapters: []narrative.ChapterPlanView{{Chapter: 5, Title: "旧友", Summary: "苏晚认出陈渡"}},
			}}}},
			Entities: []narrative.EntityView{{Name: "苏晚", Kind: domainmodel.EntityCharacter, Aliases: []string{"晚晚"}}},
			Facts:    []workbench.Fact{{Kind: domainmodel.CanonState, Subject: "苏晚", Text: "陈渡的旧友"}},
			Compass:  &domainmodel.Compass{Ending: "真相大白"},
		},
	}
	requireContains(t, frame(m),
		"◇ 创作方案 · 等待你确认", "新增 1 个故事弧 · 1 章 · 新增人物地点 1 个 · 设定 1 条 · 终局：真相大白",
		"y 通过 · 不满意跟主编说理由，重新规划（意见留作后续章节的要求）", "门后 ◇", "◇ 05  旧友",
	)
	m.bench.cursor = 7 // 行 6 是方案里的新故事弧，行 7 是它的第 5 章
	requireContains(t, frame(m), "◇ 方案待你确认 · 尚未生效", "苏晚认出陈渡")
	review, err := m.reviewContent()
	if err != nil {
		t.Fatal(err)
	}
	review = ansi.Strip(review)
	requireContains(t, review,
		"第 1 卷 · 第一卷 · 未寄出的信", "  第 2 个故事弧 · 门后", "    第 5 章 · 旧友", "      苏晚认出陈渡",
		"苏晚（人物） 又名 晚晚", "· 状态 苏晚：陈渡的旧友", "故事罗盘\n终局：真相大白", "重新规划（意见留作后续章节的要求）",
	)
	for _, leak := range []string{"{", "plan-", "原始内容"} {
		if strings.Contains(review, leak) {
			t.Fatalf("review leaks %q:\n%s", leak, review)
		}
	}
}

func TestDecisionCardGuardsApproval(t *testing.T) {
	m := withCandidate(studioModel(t, 150, 40), false)
	m.bench.writing = false
	view := frame(m)
	requireContains(t, view,
		"◇ 等你决定", "◇ 04  门后的声音", "◇ 候选稿 · 等你确认 · 尚未入稿",
		"◇ 第 4 章 · 门后的声音 · 等待你确认", "新稿 ", "说明：第四章初稿", "y 通过 · 不满意跟主编说理由，按意见重写",
		"y 通过，或跟主编说哪里不满意", "Enter 查看全部内容",
	)
	if l := m.benchLayout(); l.cardY < 0 || l.sceneY < 0 || l.cardY != l.sceneY+benchSceneRows {
		t.Fatalf("decision card must sit between the scene strip and the footer: %+v", l)
	}
	// 通过只认 y；其余的话交给主编，由它按原话退稿（D74）。
	if typed, cmd := submit(t, m, "结尾再收一点"); cmd == nil || typed.bench.view != viewTalk || typed.bench.decision == nil {
		t.Fatal("feedback must go to the chief")
	}
	// 过时的稿件不能通过，回车按现在的内容重写（§5.5 第 4 条）。
	stale := withCandidate(studioModel(t, 150, 40), true)
	stale.bench.writing = false
	requireContains(t, frame(stale), "书又有了新变化", "回车按现在的内容重写")
	stale, cmd := submit(t, stale, "y")
	if cmd != nil || !strings.Contains(stale.bench.notice, "不能直接通过") {
		t.Fatalf("stale proposal must not be approved: notice=%q", stale.bench.notice)
	}
	if _, cmd := submit(t, stale, ""); cmd == nil {
		t.Fatal("enter on a stale draft must redo it")
	}
}

// 主编提出的修改等你确认（D77）：确认卡压过稿件的决定卡；y 确认，回车看全部；有出入时
// 输入序号选处置，y 不作数；序号之外的话照常交给主编。
func TestChangeCardConfirmsOrChoosesAResolution(t *testing.T) {
	pending := func(change workbench.PendingChange) model {
		m := studioModel(t, 150, 40)
		m.bench.writing = false
		m.bench.snap.Run.State = domainmodel.RunWaitingUser
		change.Proposal = domainmodel.Proposal{ID: "change-1", Reason: "你想要更长的标题"}
		m.bench.snap.Change = &change
		m.bench.snap.Decision = &workbench.PendingDecision{Reason: "主编提出的修改等你确认，确认或撤回后接着写"}
		updated, _ := m.Update(benchRefreshedMsg{gen: m.bench.gen, snap: m.bench.snap})
		return updated.(model)
	}
	plain := pending(workbench.PendingChange{View: workbench.ProposalView{Changes: []string{"第 2 章《回信》改名《没有寄出的回信》"}}})
	requireContains(t, frame(plain), "◇ 主编提出的修改 · 等你确认", "你想要更长的标题",
		"第 2 章《回信》改名《没有寄出的回信》", "y 确认 · 不要了跟主编说 · 回车查看全部", "Enter 查看修改")
	if opened, _ := press(t, plain, tea.KeyEnter); !opened.bench.reading || !strings.Contains(opened.bench.bodyText, "改名《没有寄出的回信》") {
		t.Fatal("Enter must open the whole change")
	}
	if confirmed, cmd := submit(t, plain, "y"); cmd == nil || confirmed.bench.notice != "" {
		t.Fatalf("y must confirm the change: notice=%q", confirmed.bench.notice)
	}

	conflicted := pending(workbench.PendingChange{
		View:     workbench.ProposalView{Changes: []string{"设定 · 「陈渡」从未去过北方"}},
		Findings: []string{"第 3 章写了陈渡在北方送信"},
		Options: []workbench.ChangeOption{
			{Strategy: domainmodel.ResolutionRewriteAffected, Chapters: []int{3}, Explanation: "按新设定重写第 3 章"},
			{Strategy: domainmodel.ResolutionReinterpretFuture, Explanation: "保留第 3 章，后文解释"},
			{Strategy: domainmodel.ResolutionAbandon, Explanation: "不改了"},
		},
	})
	requireContains(t, frame(conflicted), "与已写章节有出入：第 3 章写了陈渡在北方送信",
		"输入序号选怎么处理：1 重写第 3 章 · 2 只改设定，后文按新的写 · 3 不改了")
	if refused, cmd := submit(t, conflicted, "y"); cmd != nil || !strings.Contains(refused.bench.notice, "输入序号") {
		t.Fatalf("y must not confirm a conflicted change: notice=%q", refused.bench.notice)
	}
	if chosen, cmd := submit(t, conflicted, "2"); cmd == nil || chosen.bench.view == viewTalk || chosen.bench.input.Value() != "" {
		t.Fatal("a number must choose the resolution, not go to the chief")
	}
	if talked, cmd := submit(t, conflicted, "4"); cmd == nil || talked.bench.view != viewTalk {
		t.Fatal("other words go to the chief")
	}

	stale := pending(workbench.PendingChange{View: workbench.ProposalView{Changes: []string{"第 2 章《回信》改名《信》"}}, Stale: true})
	requireContains(t, frame(stale), "提出之后书又有了新变化，不能照它确认", "跟主编说，让它按最新内容重提")
	if refused, cmd := submit(t, stale, "y"); cmd != nil || !strings.Contains(refused.bench.notice, "不能照它确认") {
		t.Fatalf("a stale change must not be confirmed: notice=%q", refused.bench.notice)
	}
}

// 对话视图是一条时间线（D77）：生产里程碑按时间插在对话之间，自成一段。
func TestTalkInterleavesMilestonesByTime(t *testing.T) {
	m := studioModel(t, 150, 40)
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	m.bench.thread = chief.Thread{Entries: []chief.Entry{
		{Kind: chief.EntryUser, Text: "第二章多点雨声", At: base},
		{Kind: chief.EntryChief, Text: "记下了。", At: base.Add(time.Second)},
		{Kind: chief.EntryUser, Text: "写得怎么样了", At: base.Add(time.Hour)},
	}}
	m.bench.snap.Milestones = []workbench.Milestone{
		{At: base.Add(10 * time.Minute), Tone: workbench.MilestoneDone, Text: "第 2 章《回信》入稿"},
		{At: base.Add(20 * time.Minute), Tone: workbench.MilestoneWait, Text: "审阅第 1–2 章：未通过 · 1 处未解决"},
		{At: base.Add(2 * time.Hour), Tone: workbench.MilestoneDone, Text: "第 3 章《门后》入稿"},
	}
	m.switchView(viewTalk)
	lines := m.talkLines(80)
	var said []string
	for _, line := range lines {
		said = append(said, strings.TrimSpace(ansi.Strip(line)))
	}
	want := []string{"› 第二章多点雨声", "记下了。", "", "◆ 第 2 章《回信》入稿", "◆ 审阅第 1–2 章：未通过 · 1 处未解决", "", "› 写得怎么样了", "", "◆ 第 3 章《门后》入稿"}
	if strings.Join(said, "\n") != strings.Join(want, "\n") {
		t.Fatalf("talk = %q, want %q", said, want)
	}
}

func TestCommandPaletteFiltersJumpsAndRejectsUnknown(t *testing.T) {
	m := studioModel(t, 150, 40)
	closed := m.benchLayout()
	m = typeText(t, m, "/")
	requireContains(t, frame(m), "命令 · 回车执行，支持唯一前缀", "/export <路径>", "/model [角色]", "/help")
	// 面板是浮在底栏分隔线正上方的方框：内容对齐输入的文字，其他区域几何不变。
	l := m.benchLayout()
	rows := strings.Split(frame(m), "\n")
	if !strings.HasPrefix(rows[l.panel.Min.Y], " ╭─") || !strings.HasPrefix(rows[l.panel.Min.Y+1], " │ 命令 · ") ||
		!strings.HasPrefix(rows[l.footerY-2], " │ /help") || !strings.HasPrefix(rows[l.footerY-1], " ╰─") || !strings.HasPrefix(rows[l.footerY+1], " › /") {
		t.Fatalf("palette must float right above the input:\n%s", strings.Join(rows[l.panel.Min.Y:], "\n"))
	}
	closed.panel = l.panel
	if l != closed {
		t.Fatalf("the palette must not move other regions: closed=%+v open=%+v", closed, l)
	}
	m = typeText(t, m, "m")
	view := frame(m)
	requireContains(t, view, "/model")
	if strings.Contains(view, "/export") {
		t.Fatal("palette must filter by prefix")
	}
	m, _ = press(t, m, tea.KeyEsc)
	m, _ = submit(t, m, "/2")
	if m.selectedChapterNumber() != 2 || !m.bench.pinned {
		t.Fatalf("/2 must pin chapter 2: number=%d pinned=%v", m.selectedChapterNumber(), m.bench.pinned)
	}
	requireContains(t, frame(m), "第 2 章", "✓ 已入稿", "来客没有撑伞")
	m, _ = submit(t, m, "/门后")
	if m.selectedChapterNumber() != 4 || m.bench.notice != "/next 下一个匹配" {
		t.Fatalf("title search failed: number=%d notice=%q", m.selectedChapterNumber(), m.bench.notice)
	}
	m, _ = submit(t, m, "/xyz")
	if !strings.Contains(m.bench.err, "没有找到匹配的章节") || m.bench.input.Value() != "/xyz" {
		t.Fatalf("unknown command must explain and keep the input: err=%q", m.bench.err)
	}
	m, _ = press(t, m, tea.KeyEsc)
	m, _ = submit(t, m, "/help")
	if !m.bench.reading || !strings.Contains(m.bench.bodyText, "创作控制台") {
		t.Fatal("/help must open the help page")
	}
}

func TestEscapeLayersInputFollowPauseThenHome(t *testing.T) {
	m := studioModel(t, 150, 40)
	m = typeText(t, m, "多一点雨声")
	m, _ = press(t, m, tea.KeyEsc)
	if m.bench.input.Value() != "" {
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
	m, cmd := press(t, m, tea.KeyEsc)
	if m.page != pageWorkbench || cmd == nil {
		t.Fatal("Esc while writing must pause the creation before leaving")
	}
	m.bench.writing = false // 暂停打断了驱动
	closed := false
	m.bench.activityOff = func() { closed = true }
	m, cmd = press(t, m, tea.KeyEsc)
	if m.page != pageHome || cmd == nil || !closed || m.home.lastOpened != "letters" || len(m.bench.snap.Manuscript) != 0 {
		t.Fatal("Esc from a following workbench must go home and release the book")
	}
}

// 对话视图里回到最新不离开对话：目录回到当前章，下一次 Esc 才暂停。
func TestEscapeInTalkReturnsToLatestThenPauses(t *testing.T) {
	m := studioModel(t, 150, 40)
	m, _ = press(t, m, tea.KeyTab)
	m, _ = press(t, m, tea.KeyUp)
	m, _ = press(t, m, tea.KeyF1)
	if !m.bench.pinned || m.bench.view != viewTalk {
		t.Fatalf("talk view with a pinned chapter: pinned=%v view=%v", m.bench.pinned, m.bench.view)
	}
	m, cmd := press(t, m, tea.KeyEsc)
	if cmd != nil || m.bench.pinned || m.bench.view != viewTalk || m.selectedChapterNumber() != 4 {
		t.Fatalf("Esc must return to the current chapter and stay in talk: pinned=%v view=%v", m.bench.pinned, m.bench.view)
	}
	if _, cmd = press(t, m, tea.KeyEsc); cmd == nil {
		t.Fatal("the next Esc must pause the creation")
	}
}

// 创作停着时回车就接着写；完本不算停着（续写要跟主编说），回车照常全屏阅读。打开作品时，
// 上次没停就退出的创作自动接着写，暂停过的保持暂停。
func TestStoppedCreationResumesOnEnterAndOnOpen(t *testing.T) {
	stopped := func(state domainmodel.CreationRunState) model {
		m := studioModel(t, 150, 40)
		m.bench.writing = false
		m.bench.snap.Run.State = state
		return m
	}
	m, cmd := press(t, stopped(domainmodel.RunPaused), tea.KeyEnter)
	if cmd == nil || !m.bench.writing {
		t.Fatal("Enter on a paused book must continue creating")
	}
	m, _ = press(t, stopped(domainmodel.RunCompleted), tea.KeyEnter)
	if m.bench.writing || !m.bench.reading {
		t.Fatal("Enter on a finished book must read, not extend it")
	}
	for state, resume := range map[domainmodel.CreationRunState]bool{domainmodel.RunRunning: true, domainmodel.RunPaused: false} {
		m := stopped(state)
		m.bench.resumeOnLoad = true
		updated, _ := m.Update(benchRefreshedMsg{gen: m.bench.gen, snap: m.bench.snap})
		if m = updated.(model); m.bench.writing != resume || m.bench.resumeOnLoad {
			t.Fatalf("opening a %s book: writing=%v, want %v", state, m.bench.writing, resume)
		}
	}
}

// 某章自动修订多次仍没做到用户的要求（D94）：决定卡点名那一章与它的阻塞意见并选中它；
// 回车阅读这一章（照原样续跑还会停在这里），y 接受现状后接着写。
func TestHeldChapterDecisionAcceptsOrReads(t *testing.T) {
	held := func() model {
		m := studioModel(t, 150, 40)
		m.bench.writing = false
		m.bench.snap.Run.State = domainmodel.RunWaitingUser
		m.bench.snap.Decision = &workbench.PendingDecision{Reason: "第 3 章《回信》自动修订了 2 次仍没做到你的要求，等你决定：接受现状，或跟主编说怎么改", Held: "ch-3"}
		updated, _ := m.Update(benchRefreshedMsg{gen: m.bench.gen, snap: m.bench.snap})
		return updated.(model)
	}
	m := held()
	view := frame(m)
	requireContains(t, view, "第 3 章《回信》自动修订了 2 次", "未解决：回信背面的笔迹提前揭示了门后人的身份",
		"y 接受现状 · 回车阅读这一章 · 或跟主编说怎么改", "Enter 阅读这一章")
	if m.selectedChapterNumber() != 3 || strings.Contains(view, "（另有") {
		t.Fatalf("the held chapter must be selected and only its blocking findings summarized: %d", m.selectedChapterNumber())
	}
	if read, _ := press(t, m, tea.KeyEnter); read.bench.writing || !read.bench.reading || !strings.Contains(read.bench.bodyText, "第 3 章") {
		t.Fatal("Enter must open the held chapter instead of resuming into the same stop")
	}
	if accepted, cmd := submit(t, held(), "y"); cmd == nil || accepted.bench.decision != nil || accepted.bench.err != "" {
		t.Fatalf("y must accept the held chapter as it is: err=%q", accepted.bench.err)
	}
}

// 带着未解决意见的章（D94）：目录标“!”，顶栏计数，卷折叠后在头行点出数量；选中按 y 接受
// 现状，标记随之消失，正在按意见重写的章等它写完。
func TestUnresolvedChaptersAreMarkedAndAcceptable(t *testing.T) {
	m := studioModel(t, 150, 40)
	requireContains(t, frame(m), "✓ 02  雨夜来客", "! 03  回信", "! 1 章未解决")
	m.bench.collapsed["v1"] = true
	requireContains(t, frame(m), "▸ 第一卷", "!1  3/4")
	delete(m.bench.collapsed, "v1")

	m.bench.cursor = 4 // 第 3 章
	if m.selectedChapterNumber() != 3 || !strings.Contains(frame(m), "y 接受这一章的现状") {
		t.Fatalf("selecting a marked chapter must offer to accept it:\n%s", frame(m))
	}
	if accepted, cmd := submit(t, m, "y"); cmd == nil || accepted.bench.notice != "" {
		t.Fatalf("y must accept the selected marked chapter: notice=%q", accepted.bench.notice)
	}
	rewriting := m
	rewriting.bench.snap.Outline = slices.Clone(m.bench.snap.Outline)
	for i := range rewriting.bench.snap.Outline {
		if rewriting.bench.snap.Outline[i].Number == 3 {
			rewriting.bench.snap.Outline[i].State = workbench.ChapterInProgress
		}
	}
	if waited, cmd := submit(t, rewriting, "y"); cmd != nil || waited.bench.notice != "这一章正在按意见重写，写完再说" {
		t.Fatalf("a chapter being rewritten is not accepted mid-way: notice=%q", waited.bench.notice)
	}
	m.bench.cursor = 3 // 第 2 章没有未解决意见
	if none, cmd := submit(t, m, "y"); cmd != nil || none.bench.notice != "现在没有等你决定的稿件" {
		t.Fatalf("y on a settled chapter decides nothing: notice=%q", none.bench.notice)
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
	m.bench.cursor = 1
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
	m = clickBench(m, 6, l.outlineY+3)
	if m.selectedChapterNumber() != 2 || m.bench.pane != benchPaneOutline || !m.bench.pinned {
		t.Fatalf("click must select the chapter row: number=%d", m.selectedChapterNumber())
	}
	m = wheelBench(m, l.mainX+10, l.contentY+2, false)
	if m.selectedChapterNumber() != 2 || m.bench.proseOffset != 1 {
		t.Fatalf("wheel over prose must scroll prose only: number=%d offset=%d", m.selectedChapterNumber(), m.bench.proseOffset)
	}
	tabs := m.benchTabs(l)
	m = clickBench(m, tabs[2].x0, l.tabsY)
	if m.bench.view != viewActivity || m.bench.pane != benchPaneMain {
		t.Fatal("clicking a tab must switch the view")
	}
	m = clickBench(m, tabs[1].x0, l.tabsY)
	if m.bench.view != viewProse {
		t.Fatal("clicking the prose tab must switch back")
	}
	// 创作中的主要操作是「Esc 暂停」：点它直接暂停，不往输入框里填东西。
	if clicked, cmd := m.Update(tea.MouseClickMsg{X: 3, Y: l.footerY + 2, Button: tea.MouseLeft}); cmd == nil || clicked.(model).bench.input.Value() != "" {
		t.Fatal("clicking 「Esc 暂停」 must pause")
	}

	decided := withCandidate(m, false)
	l = decided.benchLayout()
	decided = clickBench(decided, l.mainX+4, l.cardY+1)
	if decided.selectedChapterNumber() != 4 || !strings.Contains(frame(decided), "◇ 候选稿 · 等你确认") {
		t.Fatal("clicking the decision card must reveal the candidate")
	}
	decided.bench.activity.Entries[1].Err = "写入失败"
	decided.bench.activity.Entries[1].OperationID = "writing"
	// 创作停下后末行是当下处境，报错步就在它上面一行。
	if _, cmd := decided.Update(tea.MouseClickMsg{X: l.mainX + 4, Y: l.sceneY + benchSceneRows - 2, Button: tea.MouseLeft}); cmd == nil {
		t.Fatal("clicking the failed step must open diagnostics")
	}
	// 等待计时在标题线上，不占正文行；点击标题线、处境行与其余行都不能越界或误命中。
	decided.bench.activity.Waiting = true
	for y := l.sceneY; y < l.sceneY+benchSceneRows; y++ {
		_, cmd := decided.Update(tea.MouseClickMsg{X: l.mainX + 4, Y: y, Button: tea.MouseLeft})
		if hit := cmd != nil; hit != (y == l.sceneY+benchSceneRows-2) {
			t.Fatalf("scene row %d hit=%v while waiting", y-l.sceneY, hit)
		}
	}
	// 任务失败的旁白同样可以下钻诊断。
	decided.bench.activity.Waiting = false
	decided.bench.activity.Entries[1].Err = ""
	decided.bench.activity.Entries = append(decided.bench.activity.Entries, activity.Entry{
		ID: 9, OperationID: "writing", Kind: activity.Notice, Tone: activity.ToneFail, Text: "第 4 章《门后的声音》这次没能写完", Done: true,
		At: decided.bench.activity.Entries[1].At.Add(time.Minute),
	})
	if _, cmd := decided.Update(tea.MouseClickMsg{X: l.mainX + 4, Y: l.sceneY + benchSceneRows - 2, Button: tea.MouseLeft}); cmd == nil {
		t.Fatal("clicking a failed task notice must open diagnostics")
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
		flat.bench.snap.Outline = append(flat.bench.snap.Outline, workbench.OutlineNode{Node: domainmodel.PlanNode{ID: fmt.Sprint(i), Kind: domainmodel.PlanChapter, Turn: "转折", Title: fmt.Sprintf("雨夜来信 %d", i)}, Number: i, State: workbench.ChapterConfirmed})
	}
	flat.bench.rowsCache = &outlineCache{}
	requireContains(t, frame(flat), "第 1–50 章", "后面还有")
}

func TestTooSmallTerminalAsksToMaximize(t *testing.T) {
	m := studioModel(t, 100, 30)
	requireContains(t, frame(m), "请将终端窗口最大化", "当前 100 × 30 · 需要至少 120 × 36")
}

func TestFooterShowsUsageContextAndPrimaryAction(t *testing.T) {
	m := studioModel(t, 150, 40)
	requireContains(t, frame(m), "deepseek/deepseek-v4-flash · 思考 auto · ↑233K ↓58K · 缓存 80% · $0.42", "Esc 暂停")
	// 模型清单查得到时附上下文窗口；最窄时提示完整，模型摘要与用量截掉尾部。
	list := `{"deepseek-v4-flash": {"mode": "chat", "litellm_provider": "deepseek", "max_input_tokens": 1000000}}`
	if err := os.WriteFile(filepath.Join(m.deps.ConfigDir, "models.json"), []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	m.api.Models = binding.New(m.deps.ConfigDir, &testExecutor{})
	if err := m.api.Models.Apply(testConfig()); err != nil {
		t.Fatal(err)
	}
	requireContains(t, frame(m), "deepseek/deepseek-v4-flash · 思考 auto · 上下文 1M · ↑233K ↓58K")
	narrow := m
	narrow.width = 120
	requireContains(t, frame(narrow), "Esc 暂停", "deepseek/deepseek-v4-flash · 思考 auto · 上下文 1M")
	m.bench.snap.Usage = workbench.TeamUsage{}
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
	requireContains(t, view, "已暂停  ·  已入稿", "Enter 继续创作", "已暂停 · 回车继续", "回车继续创作，或跟主编说说想法", "◌ 未完成的草稿 · 未入稿")
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
	m.bench.writing = false // 创作进行中 Esc 先暂停，停下后才离开
	oldCache := m.bench.rowsCache
	_ = m.screen()
	closed := false
	m.bench.activityOff = func() { closed = true }
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	m.bench = testBench(m, "long")
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
	_ = m.screen()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.screen()
	}
}

func BenchmarkLongWorkbench(b *testing.B) {
	for _, count := range []int{50, 500, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			m := longWorkbench(b, count)
			m.findChapter(fmt.Sprint(count), false)
			_ = m.screen()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.screen()
			}
		})
	}
}

func BenchmarkLongWorkbenchStreaming(b *testing.B) {
	m := longWorkbench(b, 500)
	m.findChapter("500", false)
	m.bench.snap.Outline[len(m.bench.snap.Outline)-1].State = workbench.ChapterInProgress
	m.bench.rowsCache = &outlineCache{}
	prefix := string(m.bench.activity.Output[1].Text)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.bench.activity.Output[1].Text = []byte(prefix + fmt.Sprint(i))
		m.bench.activity.Output[1].Version = uint64(i + 2)
		_ = m.screen()
	}
}

// 顶栏取正在执行的任务：审阅、重写已入稿章节时不能停在大纲推出的章号上。
func TestStateBadgeFollowsRunningTask(t *testing.T) {
	m := studioModel(t, 150, 40)
	m.bench.snap.CurrentPhase = "正在审阅第 1–3 章"
	if header := strings.Split(frame(m), "\n")[0]; !strings.Contains(header, "◉ 正在审阅第 1–3 章") || strings.Contains(header, "第 4 章") {
		t.Fatalf("badge must name the running task:\n%s", header)
	}
}

// 规划时现场逐条看到大纲与设定：块首被顶出时栏目名粘在可见的第一行，正在生成的提交钉在最后。
func TestSceneStreamsStructuredItemsWithStickySection(t *testing.T) {
	m := planningModel(t, 150, 40)
	l := m.benchLayout()
	scene := strings.Split(frame(m), "\n")[l.sceneY+2 : l.sceneY+benchSceneRows]
	if !strings.Contains(scene[0], "大纲  ") {
		t.Fatalf("the outline block was cut at the top but lost its section:\n%s", strings.Join(scene, "\n"))
	}
	requireContains(t, strings.Join(scene, "\n"), "设定  陈渡（人物） 又名 邮差", "      「陈渡」state.job：镇上最后一位邮差")
	if last := scene[len(scene)-1]; !strings.Contains(last, "提交候选稿 · 已接收 18.2K") {
		t.Fatalf("the submission in progress must stay on the last row: %q", last)
	}
	m.switchView(viewActivity)
	requireContains(t, frame(m), "全书规划 · 大纲", "全书规划 · 设定", "✓ 查阅设定与前情 · 「陈渡」", "第 2 章 · 雨夜来客 —— 不留水迹的来客问有没有他的信")
}

func TestUserActionsEchoOnTheRunTimeline(t *testing.T) {
	m := studioModel(t, 150, 40)
	updated, _ := m.Update(runControlMsg{gen: m.bench.gen, next: "refresh", echo: "你暂停了创作"})
	m = updated.(model)
	updated, _ = m.Update(decisionDoneMsg{gen: m.bench.gen, err: errors.New("state conflict"), echo: "你通过了第 4 章 · 雨夜"})
	m = updated.(model)
	scene := frame(m)
	requireContains(t, scene, "› 你暂停了创作")
	if strings.Contains(scene, "你通过了") {
		t.Fatal("a failed action must not echo")
	}
	// 回响按时间并入时间线：在它之后发生的步骤排在它下面。
	m.bench.activity.Entries = append(m.bench.activity.Entries, activity.Entry{ID: 50, Kind: activity.Notice, Tone: activity.ToneInfo, Text: "已暂停", Done: true, At: time.Now().Add(time.Second)})
	if view := frame(m); strings.Index(view, "› 你暂停了创作") > strings.Index(view, "· 已暂停") {
		t.Fatalf("echo must sort by time:\n%s", view)
	}
	// 没有直播内容时（重开或刚续跑）回响仍在；换一轮就不再属于它。
	m.bench.activity = activity.Snapshot{}
	requireContains(t, frame(m), "› 你暂停了创作")
	m.bench.snap.Run = &domainmodel.CreationRun{ID: "next", State: domainmodel.RunRunning}
	if strings.Contains(frame(m), "你暂停了创作") {
		t.Fatal("echoes belong to the run they were made in")
	}
	m.bench.addEcho("你把修订预算调到 3 次")
	if len(m.bench.echoes) != 1 || m.bench.echoRun != "next" {
		t.Fatalf("a new run starts a fresh echo list: %#v", m.bench.echoes)
	}
}

// 上下文窗口按厂商的写法：整千按 1000 进位，2 的幂按 1024 进位。
func TestFormatWindowFollowsHowVendorsQuoteIt(t *testing.T) {
	for n, want := range map[int]string{
		1000000: "1M", 1048576: "1M", 2000000: "2M", 1500000: "1.5M",
		200000: "200K", 256000: "256K", 131072: "128K", 163840: "160K", 32768: "32K",
	} {
		if got := formatWindow(n); got != want {
			t.Errorf("formatWindow(%d) = %q, want %q", n, got, want)
		}
	}
}

// 长对话里主编回话时每帧的排版：定下的条目沿用排好的行。
func BenchmarkLongTalk(b *testing.B) {
	m := studioModel(&testing.T{}, 200, 50)
	for i := 0; i < 300; i++ {
		m.bench.thread.Entries = append(m.bench.thread.Entries,
			chief.Entry{Kind: chief.EntryUser, Text: fmt.Sprintf("第%d章结尾留个钩子，主角不要赢得太轻松", i)},
			chief.Entry{Kind: chief.EntryAction, Text: "记下要求 · 第 2 章 ·「结尾留个钩子」", Done: true, Change: true},
			chief.Entry{Kind: chief.EntryChief, Text: strings.Repeat("记下了，第二章结尾会留钩子。", 8)},
		)
	}
	m.switchView(viewTalk)
	m.bench.thread.Busy, m.bench.thread.Reply = true, "正在回"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.screen()
	}
}

// 真实光标跟着插入点：输入法把拼写中的文字画在终端光标处，光标留在右下角会把整屏顶上去。
func TestRealCursorFollowsInputCaret(t *testing.T) {
	m := studioModel(t, 150, 40)
	m = typeText(t, m, "雨夜来信")
	m, _ = press(t, m, tea.KeyLeft)
	view, l := m.View(), m.benchLayout()
	if view.Cursor == nil || view.Cursor.Y != l.footerY+1 || view.Cursor.X != 1+lipgloss.Width("› 雨夜来") {
		t.Fatalf("cursor = %+v, want right after 雨夜来 on input row %d", view.Cursor, l.footerY+1)
	}
	if strings.Contains(view.Content, caretMark) {
		t.Fatal("the caret mark must not reach the terminal")
	}
	m = typeText(t, m, "/")
	if view := m.View(); view.Cursor == nil || view.Cursor.Y != l.footerY+1 {
		t.Fatalf("the floating palette must keep the cursor on the input: %+v", view.Cursor)
	}
	m.bench.reading = true
	if m.View().Cursor != nil {
		t.Fatal("without a focused input the cursor stays hidden")
	}
}

// 单行输入按显示宽度横向滚动：插入点之前放不下时向左滚，插入点始终落在宽度之内。
func TestInputViewScrollsByDisplayWidth(t *testing.T) {
	input := newInput("")
	input.Focus()
	input.SetValue(strings.Repeat("长", 30))
	line, cursor := placeCaret(inputView(input, 21))
	if lipgloss.Width(line) > 21 || cursor.X != 20 {
		t.Fatalf("end of a long line: width=%d cursor=%d %q", lipgloss.Width(line), cursor.X, ansi.Strip(line))
	}
	input.SetCursor(5)
	line, cursor = placeCaret(inputView(input, 21))
	if lipgloss.Width(line) > 21 || cursor.X != 2+10 || !strings.HasSuffix(ansi.Strip(line), "…") {
		t.Fatalf("caret in the middle: width=%d cursor=%d %q", lipgloss.Width(line), cursor.X, ansi.Strip(line))
	}
	input.EchoMode, input.EchoCharacter = textinput.EchoPassword, '•'
	if line, _ := placeCaret(inputView(input, 80)); strings.Contains(line, "长") {
		t.Fatalf("password input must stay masked: %q", ansi.Strip(line))
	}
	input.Blur()
	if _, cursor := placeCaret(inputView(input, 80)); cursor != nil {
		t.Fatal("a blurred input places no caret")
	}
}
