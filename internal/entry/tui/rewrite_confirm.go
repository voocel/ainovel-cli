package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/host"
)

// rewriteConfirmState 是重写授权 modal 的状态。
//
// 流程：用户输入 /rewrite [可选新方向] → 命令创建 state（带原始 args 作为
// pendingArgs）→ modal 渲染警告 + 输入框 → 用户输入 "yes" / "确认" →
// 调 host.RewriteFoundation() → 切回 modeNew，pendingArgs 作为初始需求填回。
type rewriteConfirmState struct {
	input        string
	pendingArgs  string
	errorMsg     string
	backupPath   string // 成功后填入，触发"已备份到 ..."的临时提示
	done         bool   // done=true 时下一次渲染只显示成功行后立即关闭
}

// rewriteConfirmKeywords 接受这些不区分大小写的输入作为授权凭证。
var rewriteConfirmKeywords = []string{"yes", "确认", "sure", "ok"}

func newRewriteConfirmState(pendingArgs string) *rewriteConfirmState {
	return &rewriteConfirmState{pendingArgs: pendingArgs}
}

func (s *rewriteConfirmState) isAuthorized() bool {
	ans := strings.ToLower(strings.TrimSpace(s.input))
	for _, k := range rewriteConfirmKeywords {
		if ans == k {
			return true
		}
	}
	return false
}

// ── 渲染 ──

func rewriteConfirmModalSize(width, height int) (boxW, boxH int) {
	boxW = width - 6
	if boxW > 88 {
		boxW = 88
	}
	if boxW < 60 {
		boxW = 60
	}
	boxH = 18
	if boxH > height-4 {
		boxH = height - 4
	}
	if boxH < 12 {
		boxH = 12
	}
	return
}

func renderRewriteConfirmModal(width, height int, state *rewriteConfirmState) string {
	if state == nil {
		return ""
	}
	boxW, boxH := rewriteConfirmModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	if contentW < 24 {
		contentW = 24
	}

	warn := lipgloss.NewStyle().Foreground(colorError).Bold(true)
	muted := lipgloss.NewStyle().Foreground(colorMuted)
	dim := lipgloss.NewStyle().Foreground(colorDim)
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	body := lipgloss.NewStyle().Foreground(bodyTextColor)

	var lines []string
	if state.done {
		ok := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
		lines = append(lines, ok.Render("✓ 已重写：项目回到未启动状态"))
		lines = append(lines, "")
		lines = append(lines, body.Render("备份目录："+state.backupPath))
		lines = append(lines, "")
		lines = append(lines, dim.Render("（输入框已切回创作模式，下方按 Esc 或 Enter 关闭此提示）"))
	} else {
		lines = append(lines, warn.Render("⚠ 即将清空全部 foundation"))
		lines = append(lines, "")
		lines = append(lines, muted.Render("此操作不可撤销，会归档以下内容到 meta/rewrite-backup-*/："))
		items := []string{
			"premise / outline / layered_outline（设定与大纲）",
			"characters / world_rules（角色与世界规则）",
			"compass / progress（故事方向与进度）",
			"chapters/ 已完成章节正文（全部）",
		}
		for _, it := range items {
			lines = append(lines, body.Render("  · "+it))
		}
		lines = append(lines, "")
		lines = append(lines, muted.Render("保留：materials.json（素材库）、meta/sessions（调用日志）"))
		lines = append(lines, "")
		lines = append(lines, hl.Render("请输入 "+strings.Join(rewriteConfirmKeywords, " / ")+" 确认："))
		inputBox := lipgloss.NewStyle().
			Width(contentW-2).
			Border(baseBorder).
			BorderForeground(colorAccent).
			Padding(0, 1).
			Render(state.input + "_")
		lines = append(lines, inputBox)
		if state.errorMsg != "" {
			lines = append(lines, warn.Render("! "+state.errorMsg))
		}
	}

	title := "重写授权"
	hint := "Esc 取消"
	if state.done {
		hint = "Enter / Esc 关闭"
	}
	return renderPaddedModalFrame(boxW, boxH, title, hint, splitBodyLines(strings.Join(lines, "\n"), contentW))
}

// ── 按键 ──

// handleRewriteConfirmKey 处理授权 modal 内的按键。返回 (model, cmd)，
// 由 handleBlockingModalKey 统一包装成 (model, cmd, handled=true)。
func (m Model) handleRewriteConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.rewriteConfirm == nil {
		return m, nil
	}
	state := m.rewriteConfirm

	if state.done {
		// 成功后任意键关闭
		m.rewriteConfirm = nil
		return m, nil
	}

	switch msg.Type {
	case tea.KeyEsc:
		m.rewriteConfirm = nil
		return m, nil
	case tea.KeyBackspace, tea.KeyCtrlH:
		if len(state.input) > 0 {
			// 处理多字节 rune
			runes := []rune(state.input)
			state.input = string(runes[:len(runes)-1])
		}
		state.errorMsg = ""
		return m, nil
	case tea.KeyCtrlU:
		state.input = ""
		state.errorMsg = ""
		return m, nil
	case tea.KeyEnter:
		if !state.isAuthorized() {
			state.errorMsg = "输入不匹配：请输入 " + strings.Join(rewriteConfirmKeywords, " / ")
			return m, nil
		}
		backup, err := m.runtime.RewriteFoundation()
		if err != nil {
			state.errorMsg = err.Error()
			m.applyEvent(host.Event{
				Time: time.Now(), Category: "ERROR",
				Summary: "重写失败：" + err.Error(), Level: "error",
			})
			m.refreshEventViewport()
			return m, nil
		}
		state.backupPath = backup
		state.done = true
		// 切回 modeNew：textarea 回到"启动需求"占位符；后续 submit 走 startup.PrepareQuick
		m.mode = modeNew
		m.snapshot.IsRunning = false
		m.snapshot.RuntimeState = ""
		m.startupMode = startupModeQuick
		m.textarea.Placeholder = placeholderForNewMode(m.startupMode)
		m.startupMode = startupModeQuick
		m.applyEvent(host.Event{
			Time: time.Now(), Category: "SYSTEM",
			Summary: fmt.Sprintf("已重写 foundation（备份在 %s）", backup), Level: "info",
		})
		m.refreshEventViewport()
		// 用户最初输入的新方向（pendingArgs）填回 textarea，避免再问一次
		if strings.TrimSpace(state.pendingArgs) != "" {
			m.textarea.SetValue(state.pendingArgs)
			m.refitTextareaHeight()
		}
		return m, tea.Batch(tea.DisableMouse, m.textarea.Focus())
	}

	// 字符输入
	if msg.Type == tea.KeyRunes {
		cleaned := cleanHumanKeyRunesForRewrite(msg)
		if cleaned != "" {
			state.input += cleaned
			state.errorMsg = ""
		}
	}
	return m, nil
}

// cleanHumanKeyRunesForRewrite 过滤粘贴流的 ANSI 残片，保留可见字符。
// 复用主输入路径的清理逻辑，但只关心文本拼接（不需要 KeyMsg 再传递）。
func cleanHumanKeyRunesForRewrite(msg tea.KeyMsg) string {
	if containsSGRFragment(string(msg.Runes)) || isCSILeak(msg.Runes) {
		return ""
	}
	cleaned, ok := cleanHumanKeyRunes(msg)
	if !ok {
		return ""
	}
	return string(cleaned.Runes)
}
