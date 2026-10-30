package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/voocel/ainovel-cli/internal/app/diag"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

type diagnosticsState struct {
	ctx            context.Context
	cancel         context.CancelFunc
	epoch          int
	busy           bool
	request        diag.Request
	report         diag.Report
	cursor         int
	body           viewport.Model
	text           string
	operationLines []int
	input          *textinput.Model
	notice         string
	err            string
}

type diagnosticsMsg struct {
	gen, epoch int
	report     diag.Report
	err        error
}

type diagnosticsSharedMsg struct {
	gen, epoch int
	path       string
	err        error
}

func (m model) openDiagnostics() (tea.Model, tea.Cmd) {
	return m.openOperationDiagnostics("")
}

func (m model) openOperationDiagnostics(operationID string) (tea.Model, tea.Cmd) {
	m.bench.diagEpoch++
	ctx, cancel := context.WithCancel(m.ctx)
	m.bench.diag = &diagnosticsState{ctx: ctx, cancel: cancel, epoch: m.bench.diagEpoch, request: diag.Request{ProjectID: m.bench.projectID, RunID: m.bench.run().ID, OperationID: operationID}, body: viewport.New(viewport.WithWidth(1), viewport.WithHeight(1))}
	m.layoutDiagnostics()
	return m, m.loadDiagnosticsCmd()
}

func (m model) loadDiagnosticsCmd() tea.Cmd {
	d := m.bench.diag
	if d.busy {
		return nil
	}
	d.busy, d.err = true, ""
	api, ctx, gen, epoch, request := m.api, d.ctx, m.bench.gen, d.epoch, d.request
	return func() tea.Msg {
		report, err := api.Diag.Inspect(ctx, request)
		return diagnosticsMsg{gen: gen, epoch: epoch, report: report, err: err}
	}
}

func (m model) applyDiagnostics(msg diagnosticsMsg) (tea.Model, tea.Cmd) {
	d := m.bench.diag
	if d == nil || msg.gen != m.bench.gen || msg.epoch != d.epoch {
		return m, nil
	}
	d.busy = false
	if msg.err != nil {
		d.err = msg.err.Error()
		if d.text != "" {
			d.request = d.report.Header.Scope
		}
		return m, nil
	}
	d.report = msg.report
	d.request = msg.report.Header.Scope
	d.cursor = 0
	m.formatDiagnostics()
	d.body.GotoTop()
	return m, nil
}

func (m model) formatDiagnostics() {
	d := m.bench.diag
	r := d.report
	var b strings.Builder
	fmt.Fprintf(&b, "状态  %s", diagnosticStateLabel(r.Summary.State))
	if r.Summary.Reason != "" {
		fmt.Fprintf(&b, " · %s", r.Summary.Reason)
	}
	fmt.Fprintf(&b, "\n采集 %s · 运行 %s", r.Header.CapturedAt.Format("01-02 15:04:05"), r.Header.Scope.RunID)
	fmt.Fprintf(&b, "\n任务 %d · 尝试 %d · 重试任务 %d", r.Metrics.Operations, r.Metrics.Attempts, r.Metrics.RetriedOperations)
	if r.Metrics.ToolErrors > 0 {
		fmt.Fprintf(&b, " · 工具报错 %d", r.Metrics.ToolErrors) // 常驻计数：未立发现时也要看得见
	}
	b.WriteString("\n")
	if r.Metrics.Usage != (domainmodel.Usage{}) {
		fmt.Fprintf(&b, "用量  %s\n", spendLine(r.Metrics.Usage))
	}
	if r.Summary.LastActivityAt != nil {
		fmt.Fprintf(&b, "最近进展  %s\n", r.Summary.LastActivityAt.Format("01-02 15:04:05"))
	}
	b.WriteString("\n诊断发现\n")
	if len(r.Findings) == 0 {
		b.WriteString("  当前规则未发现异常；不代表所有环节均已覆盖。\n")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "  [%s · %s] %s\n  %s\n  建议：%s\n", diagnosticLabel(f.Severity), diagnosticLabel(f.Certainty), f.Code, f.Observed, f.Suggestion)
	}
	if d.request.OperationID == "" {
		fmt.Fprintf(&b, "\n任务 · 本页 %d 项（j/k 选择，Enter 查看执行记录）\n", len(r.Operations))
		// Keep row positions in wrapped viewport coordinates for mouse selection.
		d.operationLines = nil
		line := len(readingLines(b.String(), d.body.Width())) - 1
		for i, o := range r.Operations {
			d.operationLines = append(d.operationLines, line)
			var item strings.Builder
			mark := "  "
			if i == d.cursor {
				mark = "❯ "
			}
			fmt.Fprintf(&item, "%s%s · %s · %s · 尝试 %d\n", mark, o.ID, o.Kind, o.State, o.Attempt)
			if o.Error != "" {
				fmt.Fprintf(&item, "  错误：%s\n", o.Error)
			}
			line += len(readingLines(item.String(), d.body.Width())) - 1
			b.WriteString(item.String())
		}
		if r.NextOperationID != "" {
			b.WriteString("\n还有任务，按 n 下一页。\n")
		}
	} else {
		fmt.Fprintf(&b, "\n任务 %s\n", d.request.OperationID)
		for _, o := range r.Operations {
			if o.Error != "" {
				fmt.Fprintf(&b, "错误：%s\n", o.Error)
			}
		}
		for _, a := range r.Attempts {
			b.WriteString(diagnosticAttempt(a))
		}
		if len(r.Transcript) > 0 {
			b.WriteString("\n对话记录\n")
		}
		for _, e := range r.Transcript {
			fmt.Fprintf(&b, "\n#%d · 尝试 %d · %s · %s\n", e.Sequence, e.Attempt, e.Kind, e.CreatedAt.Format("01-02 15:04:05"))
			b.WriteString(diagnosticTranscriptText(e.Text))
			b.WriteByte('\n')
			if e.PayloadTruncated {
				b.WriteString("[此记录内容已截断，完整记录保留在本地数据库]\n")
			}
		}
		if r.NextTranscriptSequence != 0 {
			b.WriteString("\n还有对话记录，按 n 下一页。\n")
		}
	}
	b.WriteString("\n观测覆盖\n")
	for _, c := range r.Coverage {
		fmt.Fprintf(&b, "  %s · %s：%s\n", diagnosticLabel(c.Source), diagnosticLabel(c.Status), c.Detail)
	}
	d.text = b.String()
	d.body.SetContent(strings.Join(readingLines(d.text, d.body.Width()), "\n"))
}

func (m model) layoutDiagnostics() {
	d := m.bench.diag
	d.body.SetWidth(max(1, m.width-4))
	d.body.SetHeight(max(1, m.height-4))
	// Reflow only on resize or receipt, never on animation/activity frames.
	if d.text != "" {
		m.formatDiagnostics()
	}
}

func (m model) handleDiagnosticsKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	d := m.bench.diag
	if d.input != nil {
		switch key.String() {
		case "esc":
			d.input = nil
			return m, nil
		case "enter":
			path := strings.TrimSpace(d.input.Value())
			if path == "" {
				d.err = "请输入分享报告保存路径"
				return m, nil
			}
			d.input = nil
			d.busy = true
			d.err = ""
			api, ctx, gen, epoch, request := m.api, d.ctx, m.bench.gen, d.epoch, d.request
			return m, func() tea.Msg {
				return diagnosticsSharedMsg{gen: gen, epoch: epoch, path: path, err: api.Diag.ExportShare(ctx, request, path)}
			}
		}
		var cmd tea.Cmd
		*d.input, cmd = d.input.Update(key)
		return m, cmd
	}
	if key.String() == "esc" {
		// Leaving invalidates the response even if the underlying read is still finishing.
		if d.request.OperationID != "" && !d.busy {
			d.request.OperationID = ""
			d.request.TranscriptAfter = 0
			d.request.After = ""
			return m, m.loadDiagnosticsCmd()
		}
		d.cancel()
		m.bench.diag = nil
		return m, nil
	}
	if d.busy {
		return m, nil
	}
	switch key.String() {
	case "r", "d":
		return m, m.loadDiagnosticsCmd()
	case "e":
		input := newInput("./diagnostics.json")
		input.Focus()
		d.input = &input
		return m, nil
	case "n":
		if d.request.OperationID != "" {
			if d.report.NextTranscriptSequence == 0 {
				return m, nil
			}
			d.request.TranscriptAfter = d.report.NextTranscriptSequence
		} else {
			if d.report.NextOperationID == "" {
				return m, nil
			}
			d.request.After = d.report.NextOperationID
		}
		return m, m.loadDiagnosticsCmd()
	case "g":
		d.request.After = ""
		d.request.TranscriptAfter = 0
		return m, m.loadDiagnosticsCmd()
	case "j", "k":
		if d.request.OperationID == "" && len(d.report.Operations) > 0 {
			delta := 1
			if key.String() == "k" {
				delta = -1
			}
			d.cursor = max(0, min(len(d.report.Operations)-1, d.cursor+delta))
			m.formatDiagnostics()
			d.body.SetYOffset(max(0, d.operationLines[d.cursor]-d.body.Height()/2))
			return m, nil
		}
	case "enter":
		if d.request.OperationID == "" && len(d.report.Operations) > 0 {
			d.request.OperationID = d.report.Operations[d.cursor].ID
			d.request.TranscriptAfter = 0
			d.request.After = ""
			return m, m.loadDiagnosticsCmd()
		}
	}
	var cmd tea.Cmd
	d.body, cmd = d.body.Update(key)
	return m, cmd
}

func (m model) applyDiagnosticsShared(msg diagnosticsSharedMsg) (tea.Model, tea.Cmd) {
	d := m.bench.diag
	if d == nil || msg.gen != m.bench.gen || msg.epoch != d.epoch {
		return m, nil
	}
	d.busy = false
	if msg.err != nil {
		d.err = msg.err.Error()
	} else {
		d.notice = "分享报告已保存：" + msg.path
	}
	return m, nil
}

func (m model) handleDiagnosticsMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	mouse := msg.Mouse()
	d := m.bench.diag
	if d.input != nil || d.busy {
		return m, nil
	}
	if mouse.Y < 2 || mouse.Y >= 2+d.body.Height() {
		return m, nil
	}
	if leftClick(msg) && d.request.OperationID == "" {
		row := mouse.Y - 2 + d.body.YOffset()
		for i, line := range d.operationLines {
			if row == line {
				d.cursor = i
				return m.handleDiagnosticsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
		}
	}
	var cmd tea.Cmd
	d.body, cmd = d.body.Update(msg)
	return m, cmd
}

func (m model) viewDiagnostics() string {
	d := m.bench.diag
	title := "运行诊断"
	if d.request.OperationID != "" {
		title += " · 执行记录"
	}
	if d.busy {
		title += " · 读取中…"
	}
	lines := []string{benchTheme.Title.Render(fitLine(title, m.width)), benchTheme.Muted.Render(fitLine("本地详情可能含私有内容 · e 按异常采集分享报告（不受页码影响）", m.width))}
	lines = append(lines, fitBlock(d.body.View(), m.width, d.body.Height())...)
	status := benchTheme.Success.Render(d.notice)
	if d.err != "" {
		status = benchTheme.Error.Render(d.err)
	}
	if d.input != nil {
		status = "保存到 " + inputView(*d.input, m.width-lipgloss.Width("保存到 "))
	}
	lines = append(lines, fitLine(status, m.width))
	help := "↑/↓ 滚动 · j/k 选任务 · Enter 查看 · n 下一页 · g 首页 · r 刷新 · e 分享 · Esc 返回"
	if d.input != nil {
		help = "仅分享状态、统计与别名证据 · 不含正文/思考/密钥/原始错误 · Enter 保存 · Esc 取消"
	}
	lines = append(lines, m.keyHint(benchTheme.Muted.Render(fitLine(help, m.width))))
	return strings.Join(fitBlock(strings.Join(lines, "\n"), max(1, m.width), max(1, m.height)), "\n")
}

// diagnosticAttempt 是一次尝试的摘要：结局、实际模型、工具报错、用量与原因。
func diagnosticAttempt(a domainmodel.OperationAttempt) string {
	var b strings.Builder
	outcome := "进行中"
	if a.Outcome != "" {
		outcome = string(a.Outcome)
	}
	if a.LeaseExpired {
		outcome += "（租约回收）"
	}
	fmt.Fprintf(&b, "\n尝试 %d · %s · %s", a.Attempt, outcome, a.StartedAt.Format("01-02 15:04:05"))
	if a.Agent.Model != "" {
		fmt.Fprintf(&b, " · %s", a.Agent.Model)
	}
	if a.ToolErrors > 0 {
		fmt.Fprintf(&b, " · 工具报错 %d", a.ToolErrors)
	}
	b.WriteByte('\n')
	if a.Usage != (domainmodel.Usage{}) {
		fmt.Fprintf(&b, "用量 %s\n", spendLine(a.Usage))
	}
	if a.Error != "" {
		fmt.Fprintf(&b, "原因：%s\n", a.Error)
	}
	return b.String()
}

// Keep terminal layout bounded even when one persisted message contains a large payload.
func diagnosticTranscriptText(text string) string {
	const limit = 2048
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && text[end]&0xc0 == 0x80 {
		end--
	}
	return text[:end] + "\n[TUI 仅展示前 2 KiB；使用 headless diag 查看本地详情]"
}

func diagnosticStateLabel(state string) string {
	switch state {
	case "waiting_user":
		return "等你决定（正常等待）"
	case "paused":
		return "已暂停（正常状态）"
	case "no_run":
		return "尚未开始创作"
	case "environment":
		return "环境信息"
	default:
		return runStateLabel(domainmodel.CreationRunState(state))
	}
}

func diagnosticLabel(value string) string {
	labels := map[string]string{
		"statistics": "统计",
		"error":      "错误", "warning": "注意", "info": "信息", "confirmed": "已确认", "signal": "调查线索", "unknown": "未知",
		"complete": "完整", "partial": "部分覆盖", "truncated": "已截断", "not_collected": "未采集", "missing": "缺失", "unavailable": "不可用",
		"database": "持久化事实", "operations": "任务", "attempts": "尝试", "transcript": "对话记录", "tool_errors": "工具错误", "usage": "用量", "models": "模型", "content_commits": "内容提交", "environment": "环境", "findings": "发现证据",
	}
	if label, ok := labels[value]; ok {
		return label
	}
	return value
}
