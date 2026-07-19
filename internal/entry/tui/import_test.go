package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voocel/ainovel-cli/internal/host/imp"
)

// TestImportHistoryCoalescesRetryLines 守护重试行原地更新：同 Key 连续事件只占一行
// （"第 N 次"在一行跳动），被普通进度行隔断后另起一行，保持时间序。
func TestImportHistoryCoalescesRetryLines(t *testing.T) {
	s := newImportState(1, "book.txt", 100, 40, nil)
	base := len(s.history)
	retry := func(msg string) imp.Event {
		return imp.Event{Time: time.Now(), Stage: imp.StageSegmenting, Message: msg, Level: "warn", Key: "retry:segmenting"}
	}
	s.appendEvent(retry("1s 后重试（第 1 次）"), 80)
	s.appendEvent(retry("2s 后重试（第 2 次）"), 80)
	s.appendEvent(retry("4s 后重试（第 3 次）"), 80)
	if got := len(s.history) - base; got != 1 {
		t.Fatalf("同 Key 连续重试应合并为 1 行，得 %d", got)
	}
	if last := s.history[len(s.history)-1]; last.message != "4s 后重试（第 3 次）" {
		t.Fatalf("合并行应更新为最新消息，得 %q", last.message)
	}
	// 普通进度行隔断后，新重试另起一行。
	s.appendEvent(imp.Event{Time: time.Now(), Stage: imp.StageAnalyzing, Message: "分析第 1 章起的连续批次..."}, 80)
	s.appendEvent(retry("1s 后重试（第 1 次）"), 80)
	if got := len(s.history) - base; got != 3 {
		t.Fatalf("隔断后重试应另起一行，共 3 行，得 %d", got)
	}
}

// TestRenderImportLineWrapsWithoutClipping 守护错误详情完整可见：正文按扣除前缀后的
// 剩余宽度换行、续行对齐，任何一行都不得超出 contentW——viewport 对超宽行是硬裁，
// 错误里的 HTTP 状态/provider/模型正是排查依据，截掉等于白报错。
func TestRenderImportLineWrapsWithoutClipping(t *testing.T) {
	ln := importLine{
		at:      time.Now(),
		stage:   imp.StageSegmenting,
		message: "切分区间 L1..L171",
		err: errors.New("imp: 模型调用失败（请求参数非法，HTTP 400，openrouter，deepseek/deepseek-chat）：" +
			"Provider returned error: invalid request payload with a very long gateway message tail"),
	}
	const contentW = 80
	out := renderImportLine(ln, contentW, time.Now())
	// 换行可能在任意字符处断开，去掉空白后比对，只验证内容一个字不丢。
	norm := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' {
				return -1
			}
			return r
		}, s)
	}
	for _, want := range []string{"HTTP 400", "openrouter", "gateway message tail"} {
		if !strings.Contains(norm(out), norm(want)) {
			t.Fatalf("行内容缺少 %q：%q", want, out)
		}
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > contentW {
			t.Fatalf("第 %d 行宽 %d 超出 %d，会被 viewport 裁掉：%q", i, w, contentW, line)
		}
	}
	// 窄终端：前缀（时间戳+图标+长阶段名）可占掉大半行宽，正文须另起行而非按下限硬凑超宽。
	ln.stage = imp.StageAwaitingConfirmation
	const narrowW = 40
	for i, line := range strings.Split(renderImportLine(ln, narrowW, time.Now()), "\n") {
		if w := lipgloss.Width(line); w > narrowW {
			t.Fatalf("窄终端第 %d 行宽 %d 超出 %d：%q", i, w, narrowW, line)
		}
	}
}

// TestRenderImportLineMultilineBlock 守护多行块消息（切分确认预览）的排版：续行整体
// 浅缩进（2 列），不得按前缀宽对齐——40+ 列前缀会把整块章节列表挤到面板右半，左半全空。
func TestRenderImportLineMultilineBlock(t *testing.T) {
	ln := importLine{
		at:      time.Now(),
		stage:   imp.StageAwaitingConfirmation,
		current: 157, total: 157,
		message: "已切分 157 章，请核对：\n  第1章 引子\n  第2章 我故意的\n",
	}
	const contentW = 100
	out := strings.Split(renderImportLine(ln, contentW, time.Now()), "\n")
	if len(out) != 3 {
		t.Fatalf("应为前缀行 + 2 个正文行，得 %d 行：%q", len(out), out)
	}
	for i, line := range out[1:] {
		if w := lipgloss.Width(line); w > contentW {
			t.Fatalf("第 %d 行超宽 %d：%q", i+1, w, line)
		}
		if strings.HasPrefix(line, strings.Repeat(" ", 20)) {
			t.Fatalf("多行块续行不应按前缀宽对齐：%q", line)
		}
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("多行块续行应浅缩进 2 列：%q", line)
		}
	}
}

// TestWrapTextResetsAtNewlines 守护多行消息换行：'\n' 处必须重置行宽计数，否则只要
// 任一行触发换行，其后每行都会被误判超宽插入伪换行+缩进，整份确认预览被打散。
func TestWrapTextResetsAtNewlines(t *testing.T) {
	in := strings.Repeat("宽", 30) + "\n短行一\n短行二"
	out := wrapText(in, 20)
	for i, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > 20 {
			t.Fatalf("第 %d 行宽 %d 超出 20：%q", i, w, l)
		}
	}
	if !strings.Contains(out, "\n短行一\n短行二") {
		t.Fatalf("原有短行不得被打散：%q", out)
	}
}

// TestImportEscResumeGate 守护导入面板 Esc 的落点：从欢迎页发起的导入成功收尾后，
// 关面板必须补跑一次恢复（bootstrap 的 Resume 只在启动时跑），否则用户被留在没有
// 续写入口的欢迎页；出错终态与工作台场景只关面板；运行中 Esc 仍是取消而非关闭。
func TestImportEscResumeGate(t *testing.T) {
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	// tea.Batch 执行后返回 BatchMsg（子命令不被执行），以此区分"焦点+恢复"与纯焦点。
	isBatch := func(cmd tea.Cmd) bool {
		_, ok := cmd().(tea.BatchMsg)
		return ok
	}
	newM := func(mode appMode, st *importState) Model {
		return Model{mode: mode, importer: st, textarea: textarea.New()}
	}

	m := newM(modeNew, &importState{done: true, stage: imp.StageDone})
	next, cmd := m.handleImportKey(esc)
	if next.(Model).importer != nil {
		t.Fatal("终态 Esc 应关闭面板")
	}
	if !isBatch(cmd) {
		t.Fatal("欢迎页导入成功关面板应附带恢复命令")
	}

	m = newM(modeNew, &importState{done: true, stage: imp.StageError, err: errors.New("boom")})
	if _, cmd := m.handleImportKey(esc); isBatch(cmd) {
		t.Fatal("出错终态不应触发恢复（书可能根本没导入成功）")
	}

	m = newM(modeRunning, &importState{done: true, stage: imp.StageDone})
	if _, cmd := m.handleImportKey(esc); isBatch(cmd) {
		t.Fatal("工作台自有门禁，不应重复触发恢复")
	}

	canceled := false
	m = newM(modeNew, &importState{cancel: func() { canceled = true }})
	next, _ = m.handleImportKey(esc)
	if !canceled || next.(Model).importer == nil {
		t.Fatal("运行中 Esc 应取消导入且保留面板等 runner 收尾")
	}
}

// TestRetryCountdown 守护倒计时渲染契约（事件面板与导入面板共用）：
// 未设截止或已到点返回空（请求已在途）；剩余时间向上取整到秒，逐秒递减且不出现 0s。
func TestRetryCountdown(t *testing.T) {
	now := time.Now()
	if got := retryCountdown(time.Time{}, now); got != "" {
		t.Fatalf("零值截止应返回空，得 %q", got)
	}
	if got := retryCountdown(now.Add(-time.Second), now); got != "" {
		t.Fatalf("已到点应返回空，得 %q", got)
	}
	if got := retryCountdown(now.Add(7500*time.Millisecond), now); got != "8s 后重试" {
		t.Fatalf("7.5s 应上取整为 8s，得 %q", got)
	}
	if got := retryCountdown(now.Add(300*time.Millisecond), now); got != "1s 后重试" {
		t.Fatalf("不足 1s 应显示 1s，得 %q", got)
	}
}

// TestParseImportArgsGuide 守护 --guide 解析：自然语言指导可含空格（其后 token 全部并入），
// 可与其它选项组合（置于最后），空内容报错。
func TestParseImportArgsGuide(t *testing.T) {
	opts, err := parseImportArgs([]string{"--guide=幕间·X", "也是", "独立章节"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Guidance != "幕间·X 也是 独立章节" {
		t.Fatalf("含空格指导应整体并入，得 %q", opts.Guidance)
	}
	opts, err = parseImportArgs([]string{"book.txt", "--yes", "--guide=序章并入第一章"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.AutoConfirm || opts.SourcePath != "book.txt" || opts.Guidance != "序章并入第一章" {
		t.Fatalf("与其它选项组合解析不符：%+v", opts)
	}
	if _, err := parseImportArgs([]string{"--guide="}); err == nil {
		t.Fatal("空 --guide 应报错")
	}
}
