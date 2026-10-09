package tui

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
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
	rail := func(m model) string {
		l := m.benchLayout()
		var cells []string
		for _, row := range strings.Split(frame(m), "\n")[l.bodyY:l.footerY] {
			cells = append(cells, ansi.Cut(row, l.railX, l.railX+l.railWidth))
		}
		return strings.Join(cells, "\n")
	}
	// 上块是正在写的章的依据：规划、覆盖它的要求（没审过是一个点）；下块创作团队钉在底部：
	// 这本书上的花费与用时、每个角色的模型、用时、用量、缓存命中与花费，最后是合计。
	view := rail(wide)
	requireContains(t, view,
		"写作依据 ─", "第 4 章 · 门后的声音", "◉ 写作中", "规划", "陈渡循着回信找到老宅",
		"要求", "3 条", "· 禁止 主角死亡", "· 用声音与一个具体动作承接悬念 · 第 4 章",
		"创作团队 ─", "$0.42 · 4m12s", "✓ 策划 deepseek-v4-pro", "4m00s", "↑41K ↓9.0K · 缓存 12%", "$0.31",
		"◉ 作者 deepseek-v4-flash", "↑192K ↓49K · 缓存 95%", "$0.11", "○ 编辑 deepseek-v4-flash", "还没出场",
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
	if view := rail(crew); strings.Contains(view, "$") || !strings.Contains(view, "! 编辑 glm-5") || !strings.Contains(view, "3m00s") ||
		!strings.Contains(view, "↑50K ↓8.0K · 缓存 50%") || strings.Count(view, "还没出场") != 2 {
		t.Fatalf("crew must come from the persisted run usage and hide unknown prices:\n%s", view)
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
	// 选中审过的章：要求带审阅结论，审阅意见与本章设定各成一段，计数标出要紧的。
	reviewed := clickBench(wide, 6, l.outlineY+3)
	requireContains(t, rail(reviewed),
		"第 3 章 · 回信", "✓ ", " 字", "违反 1 · 2 条", "✓ 禁止 主角死亡", "! 保持悬疑氛围",
		"审阅意见", "阻塞 1 · 2 条", "! 回信背面的笔迹提前揭示了门后人的身份", "· 雨声意象连续三章出现",
		"本章设定", "待核验 1 · 2 条", "事件 陈渡：在口袋里摸到一把陌生的钥匙", "伏笔 回信：背面是陈渡本人的笔迹",
	)
	// 卷头行：这一部分的进度（点击头行会折叠，这里直接移光标）。
	section := wide
	section.bench.cursor = 0
	requireContains(t, rail(section), "第一卷 · 未寄出的信", "3/4 章")
	// 最窄的右栏（170 列，32 列宽）每一行都放得下，不出现截断省略号。
	narrowRail := studioModel(t, 170, 45)
	nl := narrowRail.benchLayout()
	if nl.railWidth != railMinWidth || studioModel(t, 169, 45).benchLayout().railWidth != 0 {
		t.Fatalf("rail must appear at exactly %d columns with the minimum width: %+v", railThreshold, nl)
	}
	if view := rail(narrowRail); strings.Contains(view, "…") {
		t.Fatalf("rail truncated at 32 columns:\n%s", view)
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
	requireContains(t, frame(done), "✓ 已完成", "AI 创作现场 · 已完成", "全书完成 · /continue 续写", "/continue 续写（AI 决定篇幅）")
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
	m, _ = press(t, m, tea.KeyF2)
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
		workbench.OutlineNode{Node: domainmodel.PlanNode{ID: "plan-chapter-5", Kind: domainmodel.PlanChapter, ParentID: "plan-arc-2", Title: "旧友", Summary: "苏晚认出陈渡"},
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
		"y 通过 · 写下修改意见后回车重新规划（意见留作后续章节的要求） · /review 查看全部", "门后 ◇", "◇ 05  旧友",
	)
	m.bench.cursor = 6 // 行 5 是方案里的新故事弧，行 6 是它的第 5 章
	requireContains(t, frame(m), "◇ 方案待你确认 · 尚未生效", "苏晚认出陈渡")
	if scope, target := m.directiveScope(); scope != "chapter_range:5-5" || target != "第 5 章" || m.directiveScopeLabel(scope) != "第 5 章" {
		t.Fatalf("proposed chapter scope = %q %q", scope, target)
	}
	m.bench.cursor = 5
	if scope, _ := m.directiveScope(); scope != "from_chapter:4" {
		t.Fatalf("proposed arc must not be referenced before it exists: %q", scope)
	}
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

func TestDecisionCardGuardsApprovalAndFeedback(t *testing.T) {
	m := withCandidate(studioModel(t, 150, 40), false)
	view := frame(m)
	requireContains(t, view,
		"◇ 等你决定", "◇ 04  门后的声音", "◇ 候选稿 · 等你确认 · 尚未入稿",
		"◇ 第 4 章 · 门后的声音 · 等待你确认", "新稿 ", "说明：第四章初稿", "y 通过 · 写下修改意见后回车重写 · /review 查看全部",
		"写下修改意见后回车，或输入 y 通过", "◇ 修改意见 · 第 4 章 · 门后的声音", "/review 查看全部内容",
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
	// 创作停下后末行是当下处境，报错步就在它上面一行。
	if _, cmd := decided.Update(tea.MouseMsg{X: l.mainX + 4, Y: l.sceneY + benchSceneRows - 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}); cmd == nil {
		t.Fatal("clicking the failed step must open diagnostics")
	}
	// 等待计时在标题线上，不占正文行；点击标题线、处境行与其余行都不能越界或误命中。
	decided.bench.activity.Waiting = true
	for y := l.sceneY; y < l.sceneY+benchSceneRows; y++ {
		_, cmd := decided.Update(tea.MouseMsg{X: l.mainX + 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
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
	if _, cmd := decided.Update(tea.MouseMsg{X: l.mainX + 4, Y: l.sceneY + benchSceneRows - 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}); cmd == nil {
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
