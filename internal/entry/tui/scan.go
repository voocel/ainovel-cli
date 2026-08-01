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
	"github.com/voocel/ainovel-cli/internal/host/scan"
)

// scanState 是 /scan 命令运行期间的模态状态。
//
// 与 ripState 同形，但没有 paused 分支：扫榜没有停靠点——数据一旦提供，
// 解析到选题一路跑完，中途没有需要用户裁定的开放问题。
type scanState struct {
	reqID      int
	source     string // 数据源描述（文件/目录路径或「粘贴文本」）
	stage      scan.Stage
	current    int
	total      int
	startedAt  time.Time
	finishedAt time.Time
	history    []scanLine
	totalLines int
	err        error
	done       bool
	sparse     bool   // 有效条目不足阈值：产物可用但结论打折
	entries    int    // 有效条目数
	dir        string // 扫榜库路径
	frame      int
	cancel     context.CancelFunc
	viewport   viewport.Model
}

type scanLine struct {
	at      time.Time
	stage   scan.Stage
	current int
	total   int
	message string
	level   string
	key     string
	retryAt time.Time
	err     error

	rendered  string // 按宽度缓存，避免每 tick 全量重排
	renderedW int
}

// scanHistoryMax 与拆文同一上限：全量转录始终在 logs/scan.log。
const scanHistoryMax = 1000

func newScanState(reqID int, source string, width, height int, cancel context.CancelFunc) *scanState {
	boxW, boxH := reportModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	vp := viewport.New(contentW, boxH-4)
	s := &scanState{
		reqID:     reqID,
		source:    source,
		startedAt: time.Now(),
		stage:     scan.StageFetching,
		cancel:    cancel,
		viewport:  vp,
	}
	s.refresh(contentW)
	return s
}

func (s *scanState) appendEvent(ev scan.Event, contentW int) {
	s.stage = ev.Stage
	s.current = ev.Current
	s.total = ev.Total
	if ev.Err != nil {
		s.err = ev.Err
	}
	if ev.Stage == scan.StageDone {
		s.sparse = ev.Sparse
		s.entries = ev.Entries
		s.dir = ev.Dir
	}
	line := scanLine{
		at: ev.Time, stage: ev.Stage, current: ev.Current, total: ev.Total,
		message: ev.Message, level: ev.Level, key: ev.Key, retryAt: ev.RetryAt, err: ev.Err,
	}
	// 同 Key 且紧邻 → 原地更新（退避重试在一行跳动）；被其它行隔断则另起一行保持时间序。
	if ev.Key != "" && len(s.history) > 0 && s.history[len(s.history)-1].key == ev.Key {
		s.history[len(s.history)-1] = line
	} else {
		s.totalLines++
		s.history = append(s.history, line)
		if len(s.history) > scanHistoryMax {
			s.history = append(s.history[:0], s.history[len(s.history)-scanHistoryMax:]...)
		}
	}
	if ev.Stage == scan.StageDone || ev.Stage == scan.StageError {
		s.done = true
		s.finishedAt = ev.Time
	}
	s.refresh(contentW)
}

func (s *scanState) refresh(contentW int) {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	okStyle := lipgloss.NewStyle().Foreground(colorSuccess)
	warnStyle := lipgloss.NewStyle().Foreground(colorReview)
	errStyle := lipgloss.NewStyle().Foreground(colorError)
	stageStyle := lipgloss.NewStyle().Foreground(colorAccent2)

	var b strings.Builder
	b.WriteString(titleStyle.Render("扫榜：榜单趋势与选题"))
	b.WriteString("\n\n")
	if s.source != "" {
		b.WriteString(dimStyle.Render("数据源 "))
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
		b.WriteString(dimStyle.Render(fmt.Sprintf("(%d 条，仅显示最近 %d，全量见 logs/scan.log)", s.totalLines, len(s.history))))
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
				done: ln.stage == scan.StageDone,
			}, contentW, now)
			ln.renderedW = contentW
		}
		b.WriteString("\n")
		b.WriteString(ln.rendered)
	}

	running := !s.done
	if running {
		b.WriteString("\n\n")
		b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Bold(true).
			Render(streamCursorFrames[s.frame%len(streamCursorFrames)]))
	}

	b.WriteString("\n\n")
	switch {
	case s.err != nil:
		b.WriteString(errStyle.Render("扫榜失败"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Esc 关闭面板"))
	case s.done && s.sparse:
		b.WriteString(warnStyle.Render(fmt.Sprintf("扫榜完成，但只有 %d 条有效条目（[数据稀疏]）", s.entries)))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("选题可行性已按样本量降级；扫够样本再重扫可得更强结论；Esc 关闭面板"))
	case s.done:
		b.WriteString(okStyle.Render(fmt.Sprintf("扫榜完成：%d 条有效条目", s.entries)))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("扫榜报告.md / 选题决策.md 在扫榜库目录内；Esc 关闭面板"))
	default:
		b.WriteString(dimStyle.Render("Esc 取消扫榜"))
	}

	// 跟尾只在用户位于底部时生效：refresh 每 tick 都跑，无条件 GotoBottom 会把
	// 运行中向上翻阅的用户反复拽回底部。
	atBottom := s.viewport.AtBottom()
	s.viewport.SetContent(b.String())
	if running && atBottom {
		s.viewport.GotoBottom()
	}
}

func renderScanModal(width, height int, s *scanState, frame int) string {
	if s == nil {
		return ""
	}
	boxW, boxH := reportModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	running := !s.done
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

	hint := "  ↑↓ 滚动 · Esc 关闭"
	if running {
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
	modal := renderPaddedModalFrame(boxW, boxH, "榜单扫描", hint, body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func (m Model) handleScanKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.ranks == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		// 仍在运行 → Esc 取消，交 runner 收尾；已终态 → 关面板。
		if !m.ranks.done && m.ranks.cancel != nil {
			m.ranks.cancel()
			return m, nil
		}
		m.ranks = nil
		return m, m.textarea.Focus()
	case tea.KeyUp:
		m.ranks.viewport.ScrollUp(1)
	case tea.KeyDown:
		m.ranks.viewport.ScrollDown(1)
	case tea.KeyPgUp:
		m.ranks.viewport.HalfPageUp()
	case tea.KeyPgDown:
		m.ranks.viewport.HalfPageDown()
	}
	return m, nil
}

// scanEventMsg 单次 scan.Event 投递。
type scanEventMsg struct {
	reqID int
	ev    scan.Event
	ch    <-chan scan.Event
}

// scanClosedMsg 事件通道关闭信号。
type scanClosedMsg struct {
	reqID int
}

// startScan 启动一次扫榜：解析参数 → 创建 modal state → 监听事件流。
func startScan(rt *host.Host, reqID int, args []string, width, height int) (*scanState, tea.Cmd, error) {
	opts, err := parseScanArgs(args)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := rt.ScanRanks(ctx, opts)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return newScanState(reqID, scanSourceLabel(opts), width, height, cancel), listenScanEvent(reqID, ch), nil
}

// scanSourceLabel 回显本次数据源，与 fileFetcher 的优先级保持一致。
func scanSourceLabel(opts scan.Options) string {
	switch {
	case strings.TrimSpace(opts.PastedText) != "":
		return "粘贴文本"
	case strings.TrimSpace(opts.FilePath) != "":
		return opts.FilePath
	case strings.TrimSpace(opts.DirPath) != "":
		return opts.DirPath + "（目录）"
	default:
		return ""
	}
}

func listenScanEvent(reqID int, ch <-chan scan.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return scanClosedMsg{reqID: reqID}
		}
		return scanEventMsg{reqID: reqID, ev: ev, ch: ch}
	}
}

// parseScanArgs 解析 `/scan <榜单文件或目录> [--platform=X] [--rank=<榜单名>] [--lib=<扫榜库目录>] [--date=YYYYMMDD]`。
//
// 位置参数是文件或目录：是哪一种由 runner 侧的 fileFetcher 按路径判定，
// 这里不 stat——参数解析不该做 IO，路径不存在的错误由 fetch 阶段统一报。
func parseScanArgs(args []string) (scan.Options, error) {
	var opts scan.Options
	var path string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--platform="):
			v := strings.TrimSpace(strings.TrimPrefix(a, "--platform="))
			if v == "" {
				return scan.Options{}, fmt.Errorf("--platform 需要平台名（qidian/fanqie/qimao/jjwxc/ciweimao/other）")
			}
			opts.Platform = v
		case strings.HasPrefix(a, "--rank="):
			v := strings.TrimSpace(strings.TrimPrefix(a, "--rank="))
			if v == "" {
				return scan.Options{}, fmt.Errorf("--rank 需要榜单名")
			}
			opts.RankName = v
		case strings.HasPrefix(a, "--lib="):
			v := strings.TrimSpace(strings.TrimPrefix(a, "--lib="))
			if v == "" {
				return scan.Options{}, fmt.Errorf("--lib 需要扫榜库根目录")
			}
			opts.LibraryDir = v
		case strings.HasPrefix(a, "--date="):
			v := strings.TrimSpace(strings.TrimPrefix(a, "--date="))
			if len(v) != 8 || !isAllDigits(v) {
				return scan.Options{}, fmt.Errorf("--date 需要 YYYYMMDD 形式的采集日期：%q", v)
			}
			opts.ScanDate = v
		case strings.HasPrefix(a, "--dir="):
			v := strings.TrimSpace(strings.TrimPrefix(a, "--dir="))
			if v == "" {
				return scan.Options{}, fmt.Errorf("--dir 需要目录路径")
			}
			opts.DirPath = v
		case strings.HasPrefix(a, "--"):
			return scan.Options{}, fmt.Errorf("未知选项 %q（支持：--platform=<平台> / --rank=<榜单名> / --dir=<目录> / --lib=<扫榜库目录> / --date=YYYYMMDD）", a)
		default:
			if path != "" {
				return scan.Options{}, fmt.Errorf("只接受一个榜单文件路径：多了 %q", a)
			}
			path = a
		}
	}
	if path != "" {
		opts.FilePath = path
	}
	if opts.FilePath == "" && opts.DirPath == "" {
		return scan.Options{}, fmt.Errorf("请给出榜单文件路径，或用 --dir=<目录> 指定一批榜单文件")
	}
	return opts, nil
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
