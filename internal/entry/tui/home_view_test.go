package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

func homeFixture(t *testing.T, width, height, count int) model {
	t.Helper()
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = updated.(model)
	m.home.loaded = true
	for i := 0; i < count; i++ {
		m.home.library = append(m.home.library, libraryEntry{
			id: fmt.Sprintf("book-%03d", i), premise: fmt.Sprintf("雨夜来信 %03d", i), written: 128, target: 500,
			state: domainmodel.RunWaitingUser, updated: time.Now().Add(-3 * time.Hour),
		})
	}
	return m
}

func clickHome(t *testing.T, m model, focus int) (model, tea.Cmd) {
	t.Helper()
	return clickHit(t, m, func(hit pageHit) bool { return hit.focus == focus })
}

func clickOption(t *testing.T, m model, focus, option int) model {
	t.Helper()
	m, _ = clickHit(t, m, func(hit pageHit) bool { return hit.focus == focus && hit.option == option })
	return m
}

func clickHit(t *testing.T, m model, match func(pageHit) bool) (model, tea.Cmd) {
	t.Helper()
	for _, hit := range m.homeFrame().hits {
		if match(hit) {
			next, cmd := m.Update(tea.MouseClickMsg{X: hit.x, Y: hit.y, Button: tea.MouseLeft})
			return next.(model), cmd
		}
	}
	t.Fatal("control not visible")
	return m, nil
}

func TestHomeFramesFitSizesThemesAndStates(t *testing.T) {
	dark := darkBackground
	t.Cleanup(func() { darkBackground = dark })
	states := map[string]func(m model) model{
		"composer": func(m model) model { return m },
		"library": func(m model) model {
			m.home.setFocus(focusLibrary)
			m.home.cursor = 99
			return m
		},
		"search": func(m model) model {
			next, _ := m.beginLibrarySearch()
			return typeText(t, next.(model), "09")
		},
		"fixed": func(m model) model {
			m.home.premise.SetValue(strings.Repeat("写一个关于旧信与雨夜的故事", 30))
			m.home.fixed, m.home.chapters = true, 1000
			m.home.setFocus(focusLength)
			return m
		},
		"empty": func(m model) model {
			m.home.library = nil
			return m
		},
	}
	for _, isDark := range []bool{false, true} {
		darkBackground = isDark
		for _, size := range [][2]int{{120, 36}, {150, 40}, {240, 60}} {
			for name, state := range states {
				m := state(homeFixture(t, size[0], size[1], 100))
				frame := m.homeFrame()
				if lipgloss.Width(frame.text) > size[0] || lipgloss.Height(frame.text) != size[1] {
					t.Fatalf("frame overflow %v %s", size, name)
				}
				if !strings.Contains(ansi.Strip(frame.text), "Enter") {
					t.Fatalf("footer missing at %v %s", size, name)
				}
				for _, hit := range frame.hits {
					if hit.y < 0 || hit.y >= size[1]-pageFooter || hit.x < 0 || hit.x+hit.width > size[0] {
						t.Fatalf("invalid hit %v %s %+v", size, name, hit)
					}
				}
			}
		}
	}
}

// 首页把选择全部摊开并各附一句说明：键盘左右切换、鼠标直接点选，在篇幅行敲数字即固定篇幅。
func TestHomeChoicesAreVisibleExplainedAndSelectable(t *testing.T) {
	m := homeFixture(t, 150, 40, 0)
	requireContains(t, ansi.Strip(m.screen()),
		"● AI 决定", "○ 固定章数", "● 自动推进", "○ 里程碑确认", "○ 逐章确认",
		"写到合适处收官", "只在越界时停下来问你", "AI 不会改写", "开始创作 ↵",
	)
	m = tabTo(t, m, focusApproval)
	m, _ = press(t, m, tea.KeyRight)
	requireContains(t, ansi.Strip(m.screen()), "● 里程碑确认", "蓝图和每个新故事弧先给你过目")
	m = clickOption(t, m, focusApproval, 2)
	if m.home.approval != domainmodel.ApprovalManual || m.home.focus != focusApproval {
		t.Fatalf("click did not choose manual: %v", m.home.approval)
	}
	requireContains(t, ansi.Strip(m.screen()), "● 逐章确认", "蓝图和每一章都等你确认")

	m = tabTo(t, m, focusLength)
	m, _ = pressRune(t, m, '4')
	m, _ = pressRune(t, m, '0')
	if !m.home.fixed || m.home.chapters != 40 {
		t.Fatalf("digits must fix the length: fixed=%v chapters=%d", m.home.fixed, m.home.chapters)
	}
	requireContains(t, ansi.Strip(m.screen()), "● 固定 40 章", "全书写满 40 章收官")
	m, _ = press(t, m, tea.KeyBackspace)
	m, _ = press(t, m, tea.KeyLeft)
	if m.home.fixed || m.home.chapters != 4 {
		t.Fatalf("left must hand the length back to AI: fixed=%v chapters=%d", m.home.fixed, m.home.chapters)
	}

	// 固定篇幅却没填章数、或没写故事，都不开始，并把焦点送到要补的地方。
	m = clickOption(t, m, focusLength, 1)
	m.home.chapters = 0
	m, _ = press(t, m, tea.KeyEnter)
	if m.page != pageHome || m.home.focus != focusPremise || !strings.Contains(m.home.err, "先写下") {
		t.Fatalf("empty story must not start: page=%v err=%q", m.page, m.home.err)
	}
	m = typeText(t, m, "一个失忆的邮差")
	m, _ = press(t, m, tea.KeyEnter)
	if m.page != pageHome || m.home.focus != focusLength || !strings.Contains(m.home.err, "章数") {
		t.Fatalf("fixed length without chapters must not start: page=%v err=%q", m.page, m.home.err)
	}
	if m.home.intent("一个失忆的邮差") != nil {
		t.Fatal("no extra settings means the quick path, not a full intent")
	}
}

// 作品库每部作品一行，各列对齐：书名、进度（全书章数已知时有进度条）、章数、状态与相对时间。
// 运行标着进行中却没有任务在写，是上次没停就退出了，标「待续写」而不是「创作中」。
// 空作品库讲清楚接下来会发生什么。
func TestHomeLibraryRowsAndEmptyState(t *testing.T) {
	m := homeFixture(t, 150, 40, 0)
	requireContains(t, ansi.Strip(m.screen()), "还没有作品", "AI 先规划蓝图", "关键处等你确认")
	now := time.Now()
	m.home.library = []libraryEntry{
		{id: "b1", premise: "亡者来信", written: 12, target: 40, state: domainmodel.RunWaitingUser, updated: now.Add(-3 * time.Hour)},
		{id: "b2", premise: "修仙界最后一个炼丹师", written: 3, state: domainmodel.RunPaused, updated: now.Add(-5 * 24 * time.Hour)},
		{id: "b3", premise: "还没开始的书"},
		{id: "b4", premise: "上次没停就退出", written: 82, state: domainmodel.RunRunning},
		{id: "b5", premise: "别处正在写", written: 7, state: domainmodel.RunRunning, writing: true},
	}
	view := ansi.Strip(m.screen())
	requireContains(t, view,
		"作品库  5 部", "亡者来信", "12/40 章", "◇ 等你决定", "3 小时前",
		"3 章", "‖ 已暂停", "5 天前", "○ 未开始", "◌ 待续写", "◉ 创作中",
	)
	var columns []int
	for _, line := range strings.Split(view, "\n") {
		if at := strings.Index(line, " 章 "); at >= 0 {
			columns = append(columns, lipgloss.Width(line[:at]))
		}
	}
	if len(columns) != 4 || columns[0] != columns[1] || columns[1] != columns[2] || columns[2] != columns[3] {
		t.Fatalf("chapter counts must line up: %v", columns)
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.Local)
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, ""},
		{now.Add(-20 * time.Second), "刚刚"},
		{now.Add(-5 * time.Minute), "5 分钟前"},
		{now.Add(-3 * time.Hour), "3 小时前"},
		{now.Add(-30 * time.Hour), "昨天"},
		{now.Add(-4 * 24 * time.Hour), "4 天前"},
		{time.Date(2026, 9, 3, 9, 0, 0, 0, time.Local), "9月3日"},
		{time.Date(2025, 12, 31, 9, 0, 0, 0, time.Local), "2025年12月31日"},
	} {
		if got := relativeTime(c.at, now); got != c.want {
			t.Fatalf("relativeTime(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestHomeSearchAndMouseSelectCorrectBook(t *testing.T) {
	m := homeFixture(t, 150, 40, 100)
	m, _ = clickHome(t, m, focusSearch)
	m = typeText(t, m, "099")
	if len(m.libraryIndices()) != 1 || m.home.cursor != 99 {
		t.Fatal("search did not select filtered result")
	}
	m, _ = clickHome(t, m, focusStart)
	if !m.home.searching || m.page != pageHome {
		t.Fatal("click stole focus from search input")
	}
	m, _ = press(t, m, tea.KeyEnter)
	m, _ = clickHome(t, m, focusLibrary)
	if m.page != pageHome || m.home.focus != focusLibrary || m.home.cursor != 99 {
		t.Fatal("single click must only select the book")
	}
	m, cmd := clickHome(t, m, focusLibrary)
	if m.page != pageWorkbench || m.bench.projectID != "book-099" || cmd == nil {
		t.Fatal("double click opened wrong book")
	}
}

// 双击只认同一部作品、且在时限内：慢两下、点两部不同的作品都只是选中。
func TestHomeDoubleClickNeedsSameBookInTime(t *testing.T) {
	m := homeFixture(t, 150, 40, 3)
	row := func(index int) func(pageHit) bool {
		return func(hit pageHit) bool { return hit.row == index }
	}
	m, _ = clickHit(t, m, row(0))
	m, _ = clickHit(t, m, row(1))
	if m.page != pageHome || m.home.cursor != 1 {
		t.Fatal("clicks on two books must only move the selection")
	}
	m.home.clicked.at = time.Now().Add(-2 * doubleClick)
	m, _ = clickHit(t, m, row(1))
	if m.page != pageHome {
		t.Fatal("a slow second click must not open the book")
	}
	m, _ = clickHit(t, m, row(1))
	if m.page != pageWorkbench || m.bench.projectID != "book-001" {
		t.Fatal("double click did not open the selected book")
	}
}

// 占位按阅读折行（行首不落标点），每一行都铺满输入面底色；有字后不再出现占位。
func TestHomePremisePlaceholderFillsSurface(t *testing.T) {
	m := homeFixture(t, 213, 54, 0)
	surface := lipgloss.NewStyle().Background(benchColors.InputBackground)
	rows := premiseView(m.home.premise, surface)
	if len(rows) != premiseRows {
		t.Fatalf("rows = %d, want %d", len(rows), premiseRows)
	}
	for i, row := range rows {
		if got := lipgloss.Width(row); got != m.home.premise.Width() {
			t.Fatalf("row %d width = %d, want %d", i, got, m.home.premise.Width())
		}
		if strings.Contains(row, "\x1b[m ") {
			t.Fatalf("row %d has cells without the surface background: %q", i, row)
		}
	}
	if second := strings.TrimSpace(ansi.Strip(rows[1])); strings.HasPrefix(second, "，") {
		t.Fatalf("placeholder line starts with punctuation: %q", second)
	}
	m = typeText(t, m, "雨夜")
	if strings.Contains(ansi.Strip(strings.Join(premiseView(m.home.premise, surface), "\n")), "邮差") {
		t.Fatal("placeholder still shown after typing")
	}
}

func TestHomePagingWheelAndLateLoadKeepUserIntent(t *testing.T) {
	m := homeFixture(t, 150, 40, 100)
	frame := m.homeFrame()
	next, _ := m.Update(tea.MouseWheelMsg{X: frame.list.x + 2, Y: frame.list.y, Button: tea.MouseWheelDown})
	m = next.(model)
	if m.home.focus != focusLibrary || m.home.cursor != 1 {
		t.Fatalf("wheel over the library must select the next book: focus=%d cursor=%d", m.home.focus, m.home.cursor)
	}
	m, _ = press(t, m, tea.KeyPgDown)
	if m.home.cursor < 3 {
		t.Fatal("page navigation did not advance")
	}
	m, _ = press(t, m, tea.KeyEnd)
	if m.home.cursor != 99 {
		t.Fatal("End did not reach last book")
	}
	m.home.loaded = false
	m.home.lastOpened = "book-050"
	m.home.setFocus(focusPremise)
	m.home.premise.SetValue("正在输入新故事")
	next, _ = m.Update(libraryLoadedMsg{entries: m.home.library})
	m = next.(model)
	if m.home.focus != focusPremise || m.home.premise.Value() != "正在输入新故事" {
		t.Fatal("late load stole story input")
	}
}

// 故事超出输入框时随光标滚动（Bubble Tea 每条消息后都会渲染一次，这里照样先渲染再续写）。
func TestHomeLongStoryInputKeepsCursorEndVisible(t *testing.T) {
	m := homeFixture(t, 150, 40, 0)
	m = typeText(t, m, "BEGINONLY"+strings.Repeat("雨夜来信", 60))
	m.screen()
	m = typeText(t, m, "TAIL")
	view := ansi.Strip(m.screen())
	if !strings.Contains(view, "TAIL") || strings.Contains(view, "BEGINONLY") {
		t.Fatal("input did not scroll to the cursor")
	}
}

// 首页故事输入是多行输入框：真实光标落在已输入文字之后（中文按两列算）。
func TestHomePremiseCursorFollowsCaret(t *testing.T) {
	m := homeFixture(t, 150, 40, 0)
	m = typeText(t, m, "雨夜来信")
	view := m.View()
	if view.Cursor == nil {
		t.Fatal("the story input must place the terminal cursor")
	}
	row := strings.Split(ansi.Strip(view.Content), "\n")[view.Cursor.Y]
	prefix, _, ok := strings.Cut(row, "雨夜来信")
	if !ok || view.Cursor.X != lipgloss.Width(prefix+"雨夜来信") {
		t.Fatalf("cursor %+v must sit right after the typed text on %q", view.Cursor, row)
	}
}
