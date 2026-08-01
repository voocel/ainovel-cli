package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/rip"
)

// ripState 是 /rip 命令运行期间的模态状态。
//
// 与 importState 同形（同一套 reqID 过期判定、同一套 done/paused 二分）：
// 拆文管线也会在停靠点关闭事件通道而不发终态，缺 paused 分支面板就关不掉。
type ripState struct {
	reqID int
	// opts 是本次拆解的启动参数，供停靠点原地重入复用（y 放行）。
	// 重入必须带回同一个源路径/书名/库目录，否则会去开另一个拆文库。
	opts       rip.Options
	source     string // 源文件路径或书名（恢复时无路径）
	stage      rip.Stage
	current    int
	total      int
	startedAt  time.Time
	finishedAt time.Time
	history    []ripLine
	totalLines int // 累计日志行数（history 达上限后仍继续计数）
	err        error
	done       bool  // 终态（完成/出错）
	paused     bool  // 管线在 awaiting 处停下、通道已关闭：面板可关，非终态
	degraded   bool  // 有章节持久失败：产物不完整
	failed     []int // 失败章号
	frame      int
	cancel     context.CancelFunc
	viewport   viewport.Model
}

type ripLine struct {
	at      time.Time
	stage   rip.Stage
	current int
	total   int
	message string
	level   string
	key     string
	retryAt time.Time
	err     error

	rendered  string // 按宽度缓存；千章级书的逐章回显每 tick 全量重排会卡死面板
	renderedW int
}

// ripHistoryMax 与导入同一上限：全量转录始终在 logs/rip.log。
const ripHistoryMax = 1000

func newRipState(reqID int, source string, width, height int, cancel context.CancelFunc) *ripState {
	boxW, boxH := reportModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	vp := viewport.New(contentW, boxH-4)
	s := &ripState{
		reqID:     reqID,
		source:    source,
		startedAt: time.Now(),
		stage:     rip.StageIngesting,
		cancel:    cancel,
		viewport:  vp,
	}
	s.refresh(contentW)
	return s
}

func (s *ripState) appendEvent(ev rip.Event, contentW int) {
	s.stage = ev.Stage
	s.current = ev.Current
	s.total = ev.Total
	if ev.Err != nil {
		s.err = ev.Err
	}
	if ev.Degraded {
		s.degraded = true
		s.failed = append([]int(nil), ev.Failed...)
	}
	line := ripLine{
		at: ev.Time, stage: ev.Stage, current: ev.Current, total: ev.Total,
		message: ev.Message, level: ev.Level, key: ev.Key, retryAt: ev.RetryAt, err: ev.Err,
	}
	// 同 Key 且紧邻 → 原地更新（退避重试在一行跳动）；被其它行隔断则另起一行保持时间序。
	if ev.Key != "" && len(s.history) > 0 && s.history[len(s.history)-1].key == ev.Key {
		s.history[len(s.history)-1] = line
	} else {
		s.totalLines++
		s.history = append(s.history, line)
		if len(s.history) > ripHistoryMax {
			s.history = append(s.history[:0], s.history[len(s.history)-ripHistoryMax:]...)
		}
	}
	if ev.Stage == rip.StageDone || ev.Stage == rip.StageError {
		s.done = true
		s.finishedAt = ev.Time
	}
	s.refresh(contentW)
}

func (s *ripState) refresh(contentW int) {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	okStyle := lipgloss.NewStyle().Foreground(colorSuccess)
	warnStyle := lipgloss.NewStyle().Foreground(colorReview)
	errStyle := lipgloss.NewStyle().Foreground(colorError)
	stageStyle := lipgloss.NewStyle().Foreground(colorAccent2)

	var b strings.Builder
	b.WriteString(titleStyle.Render("拆解对标小说"))
	b.WriteString("\n\n")
	if s.source != "" {
		b.WriteString(dimStyle.Render("原文 "))
		b.WriteString(s.source)
		b.WriteString("\n")
	}
	b.WriteString(dimStyle.Render("开始 "))
	b.WriteString(formatReportTime(s.startedAt))
	if !s.finishedAt.IsZero() {
		b.WriteString(dimStyle.Render("  完成 "))
		b.WriteString(formatReportTime(s.finishedAt))
	}
	b.WriteString("\n\n")

	b.WriteString(mutedStyle.Render("阶段 "))
	b.WriteString(stageStyle.Render(string(s.stage)))
	if s.total > 0 {
		b.WriteString(mutedStyle.Render("  进度 "))
		b.WriteString(fmt.Sprintf("%d/%d", s.current, s.total))
	}
	b.WriteString("\n\n")

	b.WriteString(titleStyle.Render("流程日志"))
	b.WriteString(" ")
	if s.totalLines > len(s.history) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("(%d 条，仅显示最近 %d，全量见 logs/rip.log)", s.totalLines, len(s.history))))
	} else {
		b.WriteString(dimStyle.Render(fmt.Sprintf("(%d 条)", s.totalLines)))
	}
	b.WriteString("\n")
	now := time.Now()
	for i := range s.history {
		ln := &s.history[i]
		live := !ln.retryAt.IsZero() && now.Before(ln.retryAt.Add(2*time.Second))
		if ln.rendered == "" || ln.renderedW != contentW || live {
			ln.rendered = renderPipelineLine(pipelineLine{
				at: ln.at, stage: string(ln.stage), current: ln.current, total: ln.total,
				message: ln.message, level: ln.level, retryAt: ln.retryAt, err: ln.err,
				done: ln.stage == rip.StageDone,
			}, contentW, now)
			ln.renderedW = contentW
		}
		b.WriteString("\n")
		b.WriteString(ln.rendered)
	}

	running := !s.done && !s.paused
	if running {
		b.WriteString("\n\n")
		b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Bold(true).
			Render(streamCursorFrames[s.frame%len(streamCursorFrames)]))
	}

	b.WriteString("\n\n")
	switch {
	case s.err != nil:
		b.WriteString(errStyle.Render("拆解失败"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Esc 关闭面板"))
	case s.paused && s.stage == rip.StageAwaitingPreview:
		b.WriteString(okStyle.Render("黄金三章已就绪，等待你决定是否拆完全书"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("y 放行并逐章拆解全书；需调整切分可 Esc 后用 /rip --guide=<自然语言说明>；Esc 关闭面板"))
	case s.paused && s.stage == rip.StageAwaitingForm:
		b.WriteString(warnStyle.Render("字数介于长短篇之间，等待你裁定"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Esc 关闭后用 /rip --form=long 或 /rip --form=short 继续"))
	case s.paused:
		b.WriteString(okStyle.Render("拆解已暂停，等待你的操作"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("按上方提示继续；Esc 关闭面板"))
	case s.degraded:
		b.WriteString(warnStyle.Render(fmt.Sprintf("拆解完成，但有 %d 章失败，产物不完整", len(s.failed))))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("失败详情见拆文库 failures/；用 /rip --book=<书名> --retry-failed 重试失败章；Esc 关闭面板"))
	case s.done:
		b.WriteString(okStyle.Render("拆解完成，拆文库产物已就绪"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("拆文报告.md / 情节节点.md / 文风.md 等在拆文库目录内；Esc 关闭面板"))
	default:
		b.WriteString(dimStyle.Render("Esc 取消拆解"))
	}

	// 跟尾只在用户位于底部时生效：refresh 每 tick 都跑，无条件 GotoBottom 会把
	// 运行中向上翻阅的用户反复拽回底部。
	atBottom := s.viewport.AtBottom()
	s.viewport.SetContent(b.String())
	if running && atBottom {
		s.viewport.GotoBottom()
	}
}

func renderRipModal(width, height int, s *ripState, frame int) string {
	if s == nil {
		return ""
	}
	boxW, boxH := reportModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	running := !s.done && !s.paused
	if s.viewport.Width != contentW {
		s.viewport.Width = contentW
		s.refresh(contentW)
	}
	vpH := boxH - 4
	if running {
		vpH -= 2 // 顶部活动指示行 + 空行
	}
	if s.viewport.Height != vpH {
		s.viewport.Height = vpH
	}

	hint := "  ↑↓ 滚动 · Esc 取消/关闭"
	switch {
	case s.paused && s.stage == rip.StageAwaitingPreview:
		hint = "  ↑↓ 滚动 · y 放行全书 · Esc 关闭"
	case running:
		hint = "  ↑↓ 滚动 · Esc 取消"
	}

	body := strings.Split(s.viewport.View(), "\n")
	if running {
		// 运行中的活动指示挂在 viewport 外：viewport 内容只随事件刷新，长时模型调用
		// 期间面板纹丝不动会被误认为卡死。
		star := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).
			Render(streamCursorFrames[frame%len(streamCursorFrames)])
		status := lipgloss.NewStyle().Foreground(colorMuted).
			Render(fmt.Sprintf(" 进行中 · 已用时 %s", formatElapsed(time.Since(s.startedAt))))
		body = append([]string{star + status, ""}, body...)
	}
	modal := renderPaddedModalFrame(boxW, boxH, "对标小说拆解", hint, body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func (m Model) handleRipKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.ripper == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		// 仍在运行 → Esc 取消，交 runner 收尾；已终态或已在停靠点停下（通道关闭）→ 关面板。
		if !m.ripper.done && !m.ripper.paused && m.ripper.cancel != nil {
			m.ripper.cancel()
			return m, nil
		}
		m.ripper = nil
		return m, m.textarea.Focus()
	case tea.KeyUp:
		m.ripper.viewport.ScrollUp(1)
	case tea.KeyDown:
		m.ripper.viewport.ScrollDown(1)
	case tea.KeyPgUp:
		m.ripper.viewport.HalfPageUp()
	case tea.KeyPgDown:
		m.ripper.viewport.HalfPageDown()
	case tea.KeyRunes:
		// 预览停靠点按 y = 原地重跑并带 AcceptPreview，一次性放行全书拆解。
		if len(msg.Runes) == 1 && (msg.Runes[0] == 'y' || msg.Runes[0] == 'Y') &&
			m.ripper.paused && m.ripper.stage == rip.StageAwaitingPreview {
			return m.acceptRipPreview()
		}
	}
	return m, nil
}

// acceptRipPreview 把「看过黄金三章后放行」缩成一个按键：原地重跑拆解并带 AcceptPreview
// （恢复是无状态的，管线从缺失处继续）。与 --yes 的区别是「看过预览的显式裁定」——
// 带容错说明的边界表 --yes 不放行、y 放行；只随本次 Options 生效、不写 intent。
// 沿用旧面板的原文与流程日志，让黄金三章内容在全书拆解期间仍可回滚查看。
func (m Model) acceptRipPreview() (tea.Model, tea.Cmd) {
	prev := m.ripper
	m.ripSeq++
	// 沿用上次的定位参数（源路径/书名/库目录），只补 AcceptPreview；
	// Guidance 刻意不带——它会让边界表失配重切，而用户此刻要的是放行现有切分。
	opts := prev.opts
	opts.Guidance = ""
	opts.AcceptPreview = true
	state, listenCmd, err := startRipRun(m.runtime, m.ripSeq, opts, m.width, m.height)
	if err != nil {
		m.applyEvent(host.Event{
			Time: time.Now(), Category: "ERROR", Summary: "放行全书拆解失败：" + err.Error(), Level: "error",
		})
		return m, nil
	}
	state.history = append([]ripLine(nil), prev.history...)
	state.totalLines = prev.totalLines
	boxW, _ := reportModalSize(m.width, m.height)
	state.refresh(paddedModalContentWidth(boxW))
	m.ripper = state
	return m, listenCmd
}

// ripEventMsg 单次 rip.Event 投递。
type ripEventMsg struct {
	reqID int
	ev    rip.Event
	ch    <-chan rip.Event
}

// ripClosedMsg 事件通道关闭信号：无论停在终态还是停靠点，都靠它告知面板可关闭。
type ripClosedMsg struct {
	reqID int
}

// startRip 启动一次拆解：解析参数 → 创建 modal state → 监听事件流。
func startRip(rt *host.Host, reqID int, args []string, width, height int) (*ripState, tea.Cmd, error) {
	opts, err := parseRipArgs(args)
	if err != nil {
		return nil, nil, err
	}
	return startRipRun(rt, reqID, opts, width, height)
}

// startRipRun 以既定 Options 启动拆解（y 放行等内部重入不经参数解析）。
func startRipRun(rt *host.Host, reqID int, opts rip.Options, width, height int) (*ripState, tea.Cmd, error) {
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := rt.Deconstruct(ctx, opts)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	source := opts.SourcePath
	if source == "" {
		source = opts.BookName
	}
	state := newRipState(reqID, source, width, height, cancel)
	state.opts = opts
	return state, listenRipEvent(reqID, ch), nil
}

func listenRipEvent(reqID int, ch <-chan rip.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return ripClosedMsg{reqID: reqID}
		}
		return ripEventMsg{reqID: reqID, ev: ev, ch: ch}
	}
}

// parseRipArgs 解析 `/rip <原文路径> [--book=<书名>] [--lib=<拆文库目录>] [--form=long|short] [--yes] [--retry-failed] [--guide=<切分指导>]`。
// 无路径视为「从已有拆文库恢复」，此时须能定位到书（--book 或上次的推导名）。
// --guide 可含空格：从 --guide= 起其后全部内容并入指导文本，须置于最后。
func parseRipArgs(args []string) (rip.Options, error) {
	var opts rip.Options
	for i := range args {
		a := args[i]
		switch {
		case a == "--yes":
			opts.AutoConfirm = true
		case a == "--retry-failed":
			opts.RetryFailed = true
		case strings.HasPrefix(a, "--form="):
			v := strings.TrimPrefix(a, "--form=")
			if v != "long" && v != "short" {
				return rip.Options{}, fmt.Errorf("--form 只能是 long 或 short：%q", v)
			}
			opts.Form = v
		case strings.HasPrefix(a, "--book="):
			v := strings.TrimSpace(strings.TrimPrefix(a, "--book="))
			if v == "" {
				return rip.Options{}, fmt.Errorf("--book 需要书名")
			}
			opts.BookName = v
		case strings.HasPrefix(a, "--lib="):
			v := strings.TrimSpace(strings.TrimPrefix(a, "--lib="))
			if v == "" {
				return rip.Options{}, fmt.Errorf("--lib 需要拆文库根目录")
			}
			opts.LibraryDir = v
		case strings.HasPrefix(a, "--guide="):
			parts := append([]string{strings.TrimPrefix(a, "--guide=")}, args[i+1:]...)
			g := strings.TrimSpace(strings.Join(parts, " "))
			if g == "" {
				return rip.Options{}, fmt.Errorf("--guide 需要自然语言切分指导，例如 --guide=帖子体的分隔行也算章节起点")
			}
			opts.Guidance = g
			return opts, nil
		case strings.HasPrefix(a, "--"):
			return rip.Options{}, fmt.Errorf("未知选项 %q（支持：--book=<书名> / --lib=<拆文库目录> / --form=long|short / --yes / --retry-failed / --guide=<切分指导>）", a)
		default:
			if opts.SourcePath != "" {
				return rip.Options{}, fmt.Errorf("只接受一个原文路径：多了 %q", a)
			}
			opts.SourcePath = a
		}
	}
	if opts.SourcePath == "" && opts.BookName == "" {
		return rip.Options{}, fmt.Errorf("请给出原文路径，或用 --book=<书名> 从已有拆文库恢复")
	}
	return opts, nil
}
