package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// pipelineLine 是后台管线面板（导入 / 拆文）共用的一条流程日志行。
// 两个面板的日志形态必须一致：同一套图标语义、同一套换行与缩进规则，
// 各写一份只会在窄终端裁剪、倒计时徽标这类细节上悄悄分叉。
type pipelineLine struct {
	at      time.Time
	stage   string
	current int
	total   int
	message string
	level   string    // "warn" 退避重试/校验重问
	retryAt time.Time // 非零 = 下次重试截止时刻，渲染成剩余秒数倒计时
	err     error
	done    bool // 终态行：图标用绿勾
}

// renderPipelineLine 渲染一条流程日志行：时间戳 + 语义图标列 + 阶段（+进度）+ 正文。
// 正文按扣除前缀后的剩余宽度换行，续行对齐正文起点；超宽只换行绝不裁剪——
// viewport 对超宽行是硬裁，错误里的 HTTP 状态/provider/模型正是排查依据，截掉等于白报错。
func renderPipelineLine(ln pipelineLine, contentW int, now time.Time) string {
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
	case ln.done:
		p.WriteString(okStyle.Bold(true).Render("✓"))
	default:
		p.WriteString(dimStyle.Render("·"))
	}
	p.WriteString(" ")
	p.WriteString(stageStyle.Render(ln.stage))
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
	// 多行块消息（如切分确认预览、黄金三章停靠点）：首行跟在前缀后，其余行整体浅缩进——
	// 若按前缀宽对齐续行，40+ 列的前缀会把整块内容挤到面板右半，左半全空。
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
