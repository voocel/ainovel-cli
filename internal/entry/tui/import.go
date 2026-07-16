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
	"github.com/voocel/ainovel-cli/internal/host/imp"
)

// importState 是 /import 命令运行期间的模态状态。
//
// 模态在导入开始时创建，跟随事件流推进；完成或出错后保留在屏上等用户 Esc 关闭。
// Esc 在运行中触发取消（ctx.Cancel），交由 runner 在下一事件点收尾。
type importState struct {
	reqID      int
	source     string
	stage      imp.Stage
	current    int
	total      int
	startedAt  time.Time
	finishedAt time.Time
	history    []importLine
	totalLines int // 累计日志行数（history 达到 importHistoryMax 后仍继续计数）
	err        error
	done       bool // 终态（完成/出错）
	paused     bool // 管线在 awaiting 处停下、事件通道已关闭：面板可关闭，非终态
	frame      int  // cursor tick（120ms，与流式光标同速）同步的动画帧：尾随星标与倒计时靠它逐 tick 重算
	cancel     context.CancelFunc
	viewport   viewport.Model
}

type importLine struct {
	at      time.Time
	stage   imp.Stage
	current int
	total   int
	message string
	level   string    // "warn" 重试/退避警示
	key     string    // 非空时同 key 连续行原地更新（对齐事件面板 ID 机制）
	retryAt time.Time // 非零 = 下次重试截止时刻，渲染时算剩余秒数形成倒计时
	err     error

	rendered  string // 按 renderedW 缓存的渲染结果；历史可达千行级，逐 tick 全量重排会卡死面板
	renderedW int
}

// importHistoryMax 是面板内存中保留的日志行上限：千章级书逐章回显 + 逐章发布会
// 无上限增长，既耗内存又拖慢重渲染。日志文件（logs/import.log）始终保有全量转录。
const importHistoryMax = 1000

func newImportState(reqID int, source string, width, height int, cancel context.CancelFunc) *importState {
	boxW, boxH := reportModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	vp := viewport.New(contentW, boxH-4)
	s := &importState{
		reqID:     reqID,
		source:    source,
		startedAt: time.Now(),
		stage:     imp.StageIngesting,
		cancel:    cancel,
		viewport:  vp,
	}
	s.refresh(contentW)
	return s
}

func (s *importState) appendEvent(ev imp.Event, contentW int) {
	s.stage = ev.Stage
	s.current = ev.Current
	s.total = ev.Total
	if ev.Err != nil {
		s.err = ev.Err
	}
	line := importLine{
		at: ev.Time, stage: ev.Stage, current: ev.Current, total: ev.Total,
		message: ev.Message, level: ev.Level, key: ev.Key, retryAt: ev.RetryAt, err: ev.Err,
	}
	// 同 Key 且紧邻 → 原地更新（7 次退避在一行跳动）；被其它进度行隔断则另起一行，保持时间序。
	if ev.Key != "" && len(s.history) > 0 && s.history[len(s.history)-1].key == ev.Key {
		s.history[len(s.history)-1] = line
	} else {
		s.totalLines++
		s.history = append(s.history, line)
		if len(s.history) > importHistoryMax {
			s.history = append(s.history[:0], s.history[len(s.history)-importHistoryMax:]...)
		}
	}
	if ev.Stage == imp.StageDone || ev.Stage == imp.StageError {
		s.done = true
		s.finishedAt = ev.Time
	}
	s.refresh(contentW)
}

func (s *importState) refresh(contentW int) {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	okStyle := lipgloss.NewStyle().Foreground(colorSuccess)
	errStyle := lipgloss.NewStyle().Foreground(colorError)
	stageStyle := lipgloss.NewStyle().Foreground(colorAccent2)

	var b strings.Builder
	b.WriteString(titleStyle.Render("导入外部小说"))
	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("源文件 "))
	b.WriteString(s.source)
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("开始 "))
	b.WriteString(formatReportTime(s.startedAt))
	if !s.finishedAt.IsZero() {
		b.WriteString(dimStyle.Render("  完成 "))
		b.WriteString(formatReportTime(s.finishedAt))
	}
	b.WriteString("\n\n")

	// 当前阶段行
	b.WriteString(mutedStyle.Render("阶段 "))
	b.WriteString(stageStyle.Render(string(s.stage)))
	if s.total > 0 {
		b.WriteString(mutedStyle.Render("  进度 "))
		if s.current > 0 {
			b.WriteString(fmt.Sprintf("%d/%d", s.current, s.total))
		} else {
			b.WriteString(fmt.Sprintf("0/%d", s.total))
		}
	}
	b.WriteString("\n\n")

	// 历史日志。每行一个语义图标列（对齐事件面板形态）：
	// ✗ 红=失败 · ↻ 橙=退避重试/校验重问（同键原地跳动） · ✓ 绿=完成 · · 灰=普通进度。
	b.WriteString(titleStyle.Render("流程日志"))
	b.WriteString(" ")
	if s.totalLines > len(s.history) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("(%d 条，仅显示最近 %d，全量见 logs/import.log)", s.totalLines, len(s.history))))
	} else {
		b.WriteString(dimStyle.Render(fmt.Sprintf("(%d 条)", s.totalLines)))
	}
	b.WriteString("\n")
	now := time.Now()
	for i := range s.history {
		ln := &s.history[i]
		// 已定稿行按宽度缓存渲染结果：refresh 每 120ms tick 都跑，千行级历史全量
		// 重排（wrapText+逐行套色）是平方级开销，publish 阶段会肉眼可见卡顿。
		// 只有倒计时仍活跃的行需要逐 tick 重算（到点后多算 2s 以清掉徽标）。
		live := !ln.retryAt.IsZero() && now.Before(ln.retryAt.Add(2*time.Second))
		if ln.rendered == "" || ln.renderedW != contentW || live {
			ln.rendered = renderImportLine(*ln, contentW, now)
			ln.renderedW = contentW
		}
		b.WriteString("\n")
		b.WriteString(ln.rendered)
	}

	running := !s.done && !s.paused
	if running {
		// 尾随光标：流式面板同款单星跟在最后一条日志下方，cursor tick 驱动逐帧跳动，
		// 与顶部"进行中"指示行呼应——日志尾部有它，退避等待期也一眼可见管线还活着。
		b.WriteString("\n\n")
		b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Bold(true).
			Render(streamCursorFrames[s.frame%len(streamCursorFrames)]))
	}

	// 收尾提示
	b.WriteString("\n\n")
	switch {
	case s.err != nil:
		b.WriteString(errStyle.Render("导入失败"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Esc 关闭面板"))
	case s.paused && s.stage == imp.StageAwaitingConfirmation:
		b.WriteString(okStyle.Render("切分完成，等待你核对"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("y 确认切分并继续；需调整切分可 Esc 后用 /import --guide=<自然语言说明>；Esc 关闭面板"))
	case s.paused:
		// 管线在等待裁定处停下，通道已关闭：按面板内提示操作后 Esc 关闭。
		b.WriteString(okStyle.Render("导入已暂停，等待你的操作"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("按上方提示继续（如 /import --story=open|closed）；Esc 关闭面板"))
	case s.done:
		b.WriteString(okStyle.Render("导入完成，Foundation 与章节已就绪"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Esc 关闭面板；如需续写请在主界面按正常门禁继续"))
	default:
		b.WriteString(dimStyle.Render("Esc 取消导入"))
	}

	// 跟尾只在用户位于底部时生效：refresh 现在每 tick 都跑（动画/倒计时），
	// 无条件 GotoBottom 会把运行中向上翻阅的用户每 350ms 拽回底部。
	atBottom := s.viewport.AtBottom()
	s.viewport.SetContent(b.String())
	if running && atBottom {
		s.viewport.GotoBottom()
	}
}

// renderImportLine 渲染一条流程日志行：时间戳 + 语义图标列 + 阶段（+进度）+ 正文。
// 正文按扣除前缀后的剩余宽度换行，续行对齐正文起点；超宽只换行绝不裁剪——
// viewport 对超宽行是硬裁，错误里的 HTTP 状态/provider/模型正是排查依据，截掉等于白报错。
func renderImportLine(ln importLine, contentW int, now time.Time) string {
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	okStyle := lipgloss.NewStyle().Foreground(colorSuccess)
	errStyle := lipgloss.NewStyle().Foreground(colorError)
	warnStyle := lipgloss.NewStyle().Foreground(colorReview)
	stageStyle := lipgloss.NewStyle().Foreground(colorAccent2)

	var p strings.Builder
	p.WriteString(dimStyle.Render(ln.at.Format("15:04:05")))
	p.WriteString(" ")
	switch {
	case ln.err != nil:
		p.WriteString(errStyle.Bold(true).Render("✗"))
	case ln.level == "warn":
		p.WriteString(warnStyle.Bold(true).Render("↻"))
	case ln.stage == imp.StageDone:
		p.WriteString(okStyle.Bold(true).Render("✓"))
	default:
		p.WriteString(dimStyle.Render("·"))
	}
	p.WriteString(" ")
	p.WriteString(stageStyle.Render(string(ln.stage)))
	if ln.total > 0 && ln.current > 0 {
		p.WriteString(mutedStyle.Render(fmt.Sprintf(" %d/%d", ln.current, ln.total)))
	}
	p.WriteString(" ")
	prefix := p.String()

	var text string
	style := lipgloss.NewStyle()
	switch {
	case ln.err != nil:
		text = ln.message + " — " + ln.err.Error()
		style = errStyle
	case ln.level == "warn":
		text = ln.message
		if cd := retryCountdown(ln.retryAt, now); cd != "" {
			text += " · " + cd
		}
		style = warnStyle
	default:
		text = ln.message
	}
	// 逐行套色后自行拼接：lipgloss 对多行字符串会把每行补齐到块内最宽行，
	// 前缀只在首行，整块渲染会让首行超出 contentW 被 viewport 裁掉。
	prefixW := lipgloss.Width(prefix)
	wrapW := contentW - prefixW
	if wrapW < 20 {
		// 窄终端下前缀（时间戳+图标+长阶段名+进度）已占掉大半行宽：正文另起行浅缩进，
		// 换行宽度始终受 contentW 约束——按 20 列下限硬凑会让首行超宽被 viewport 裁掉，
		// 恰好裁掉错误尾部的 HTTP 状态/provider 等排查依据。
		var out strings.Builder
		out.WriteString(prefix)
		for _, l := range strings.Split(wrapText(text, max(10, contentW-4)), "\n") {
			out.WriteString("\n    ")
			out.WriteString(style.Render(l))
		}
		return out.String()
	}
	// 多行块消息（如切分确认预览）：首行跟在前缀后，其余行整体浅缩进——若按前缀宽
	// 对齐续行，40+ 列的前缀会把整块内容挤到面板右半，左半全空。
	head, body := text, ""
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		head, body = text[:i], strings.TrimRight(text[i+1:], "\n")
	}
	lines := strings.Split(wrapText(head, wrapW), "\n")
	var out strings.Builder
	out.WriteString(prefix)
	out.WriteString(style.Render(lines[0]))
	pad := strings.Repeat(" ", prefixW)
	for _, l := range lines[1:] {
		out.WriteString("\n")
		out.WriteString(pad)
		out.WriteString(style.Render(l))
	}
	if body != "" {
		for _, l := range strings.Split(wrapText(body, contentW-2), "\n") {
			out.WriteString("\n  ")
			out.WriteString(style.Render(l))
		}
	}
	return out.String()
}

func renderImportModal(width, height int, s *importState, frame int) string {
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
	case s.paused && s.stage == imp.StageAwaitingConfirmation:
		hint = "  ↑↓ 滚动 · y 确认切分 · Esc 关闭"
	case running:
		hint = "  ↑↓ 滚动 · Esc 取消"
	}

	body := strings.Split(s.viewport.View(), "\n")
	if running {
		// 运行中的活动指示：单颗流式面板同款星星 + 已用时。刻意用慢速 spinner 帧（350ms）——
		// 与日志尾部 120ms 的快速尾随光标拉开节奏，顶部是常驻状态行，太快反而扎眼。
		// 挂在 viewport 外的固定行——viewport 内容只随事件刷新，动画放里面不会动；
		// 没有它，长时模型调用/退避重试期间面板纹丝不动，用户会误以为卡死。
		star := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).
			Render(streamCursorFrames[frame%len(streamCursorFrames)])
		status := lipgloss.NewStyle().Foreground(colorMuted).
			Render(fmt.Sprintf(" 进行中 · 已用时 %s", formatElapsed(time.Since(s.startedAt))))
		body = append([]string{star + status, ""}, body...)
	}
	modal := renderPaddedModalFrame(boxW, boxH, "外部小说导入", hint, body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

// formatElapsed 渲染 mm:ss 已用时（超过 1 小时进位到 h:mm:ss）。
func formatElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

func (m Model) handleImportKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.importer == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		// 仍在运行（未终态、未暂停）→ Esc 取消，交 runner 收尾；已终态或已在 awaiting 处停下
		// （通道关闭）→ Esc 关闭面板。缺少 paused 分支会让 awaiting 停机后面板无法关闭（卡死）。
		if !m.importer.done && !m.importer.paused && m.importer.cancel != nil {
			m.importer.cancel()
			return m, nil
		}
		m.importer = nil
		return m, m.textarea.Focus()
	case tea.KeyUp:
		m.importer.viewport.ScrollUp(1)
	case tea.KeyDown:
		m.importer.viewport.ScrollDown(1)
	case tea.KeyPgUp:
		m.importer.viewport.HalfPageUp()
	case tea.KeyPgDown:
		m.importer.viewport.HalfPageDown()
	case tea.KeyRunes:
		// 切分确认暂停处按 y = 原地重跑 /import --yes（无路径恢复），一次性放行当前切分。
		if len(msg.Runes) == 1 && (msg.Runes[0] == 'y' || msg.Runes[0] == 'Y') &&
			m.importer.paused && m.importer.stage == imp.StageAwaitingConfirmation {
			return m.confirmImportSegmentation()
		}
	}
	return m, nil
}

// confirmImportSegmentation 把"看过预览后放行"缩成一个按键：原地重跑导入并带上
// AcceptSegmentation（恢复是无状态的，管线从 confirmation 缺失处继续）。它与 --yes 的
// 区别是"看过预览的显式裁定"——带容错说明（Notes）的切分 --yes 不放行、y 放行；
// 只随本次 Options 生效、不写 intent.json，之后 --guide 重切出的新切分仍会停下核对。
// 沿用旧面板的源文件名与流程日志，让章节预览在继续分析时仍可回滚查看。
func (m Model) confirmImportSegmentation() (tea.Model, tea.Cmd) {
	prev := m.importer
	m.importSeq++
	state, listenCmd, err := startImportRun(m.runtime, m.importSeq, imp.Options{AcceptSegmentation: true}, m.width, m.height)
	if err != nil {
		m.applyEvent(host.Event{
			Time: time.Now(), Category: "ERROR", Summary: "确认切分失败：" + err.Error(), Level: "error",
		})
		return m, nil
	}
	state.source = prev.source
	state.history = append([]importLine(nil), prev.history...)
	state.totalLines = prev.totalLines
	boxW, _ := reportModalSize(m.width, m.height)
	state.refresh(paddedModalContentWidth(boxW))
	m.importer = state
	return m, listenCmd
}

// importEventMsg 单次 imp.Event 投递。
type importEventMsg struct {
	reqID int
	ev    imp.Event
	ch    <-chan imp.Event // 同一通道继续监听下一条
}

// importClosedMsg 事件通道关闭（导入 goroutine 停止）信号。无论停在终态还是 awaiting 处，
// 通道关闭都靠它可靠告知面板可关闭，避免只认终态导致 awaiting 停机后面板卡死。
type importClosedMsg struct {
	reqID int
}

// startImport 启动一次外部小说导入：解析参数 → 创建 modal state → 监听事件流。
func startImport(rt *host.Host, reqID int, args []string, width, height int) (*importState, tea.Cmd, error) {
	opts, err := parseImportArgs(args)
	if err != nil {
		return nil, nil, err
	}
	return startImportRun(rt, reqID, opts, width, height)
}

// startImportRun 以既定 Options 启动导入（y 确认等内部重入不经参数解析）。
// width/height 用于初始化 viewport；cancel 函数挂在 state 上供 Esc 取消。
func startImportRun(rt *host.Host, reqID int, opts imp.Options, width, height int) (*importState, tea.Cmd, error) {
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := rt.ImportFrom(ctx, opts)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	state := newImportState(reqID, opts.SourcePath, width, height, cancel)
	return state, listenImportEvent(reqID, ch), nil
}

func listenImportEvent(reqID int, ch <-chan imp.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return importClosedMsg{reqID: reqID}
		}
		return importEventMsg{reqID: reqID, ev: ev, ch: ch}
	}
}

// parseImportArgs 解析 `/import <path> [--yes] [--story=open|closed] [--continue] [--guide=<说明>]`。
// 无参数视为“从活动工作区恢复”，源路径不是恢复必需项（RFC §18）。
// --guide 是自然语言切分指导，可含空格：从 --guide= 起其后全部内容并入指导文本，须置于最后。
func parseImportArgs(args []string) (imp.Options, error) {
	var opts imp.Options
	for i := range args {
		a := args[i]
		switch {
		case a == "--yes":
			opts.AutoConfirm = true
		case a == "--continue":
			opts.ContinueAfter = true
		case strings.HasPrefix(a, "--story="):
			v := strings.TrimPrefix(a, "--story=")
			if v != "open" && v != "closed" {
				return imp.Options{}, fmt.Errorf("--story 只能是 open 或 closed：%q", v)
			}
			opts.StoryResolution = v
		case strings.HasPrefix(a, "--guide="):
			parts := append([]string{strings.TrimPrefix(a, "--guide=")}, args[i+1:]...)
			g := strings.TrimSpace(strings.Join(parts, " "))
			if g == "" {
				return imp.Options{}, fmt.Errorf("--guide 需要自然语言切分指导，例如 --guide=幕间·X 也是独立章节")
			}
			opts.Guidance = g
			return opts, nil
		case strings.HasPrefix(a, "--"):
			return imp.Options{}, fmt.Errorf("未知选项 %q（支持：--yes / --story=open|closed / --continue / --guide=<切分指导>）", a)
		default:
			if opts.SourcePath != "" {
				return imp.Options{}, fmt.Errorf("只接受一个源文件路径：多了 %q", a)
			}
			opts.SourcePath = a
		}
	}
	return opts, nil
}
