package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/host"
)

// materialsPhase 标识素材收集 modal 的状态。
//
// 流转：collecting → selecting →（用户确认后）落盘退出 / （Esc）取消退出。
// collecting 阶段调 host.MaterialsCollect 异步收集；selecting 阶段渲染候选列表
// 让用户勾选；done 阶段是瞬态，渲染"保存了 N 条"提示后退出。
type materialsPhase int

const (
	materialsPhaseCollecting materialsPhase = iota
	materialsPhaseSelecting
	materialsPhaseDone
)

type materialsState struct {
	phase      materialsPhase
	userPrompt string

	candidates []host.MaterialsCandidate
	selected   []bool // 与 candidates 对齐；true 表示用户勾选
	cursor     int

	thinkingText string // 流式 thinking 文本（debug 用，折叠展示）
	streamText   string // 流式 reply 文本（候选 JSON 原文，渲染前用于 debug）
	rawText      string // 完整 LLM 输出

	err  error
	viewport viewport.Model
}

func newMaterialsState(userPrompt string) *materialsState {
	vp := viewport.New(60, 12)
	vp.SetContent("")
	return &materialsState{
		phase:     materialsPhaseCollecting,
		userPrompt: userPrompt,
		viewport:  vp,
	}
}

// applyCollect 把候选填充到 state，转到 selecting 阶段。
func (s *materialsState) applyCollect(candidates []host.MaterialsCandidate, raw string) {
	s.candidates = candidates
	s.rawText = raw
	s.selected = make([]bool, len(candidates))
	// 默认全选——用户主要意图是"挑掉不要的"，全选让 Space 反选成为默认操作
	for i := range s.selected {
		s.selected[i] = true
	}
	if s.cursor >= len(candidates) {
		s.cursor = 0
	}
	s.phase = materialsPhaseSelecting
}

func (s *materialsState) refreshViewport() {
	// viewport 仅用于 thinking preview 流式；selecting 阶段直接整段渲染
}

// toggleCurrent 切换当前光标条目的选中状态。
func (s *materialsState) toggleCurrent() {
	if s.cursor < 0 || s.cursor >= len(s.selected) {
		return
	}
	s.selected[s.cursor] = !s.selected[s.cursor]
}

// selectAll / unselectAll 一键切换。LLM 偶尔会产出多条无关候选，
// 给用户一个快速"全清空再单选"的入口。
func (s *materialsState) selectAll(yes bool) {
	for i := range s.selected {
		s.selected[i] = yes
	}
}

func (s *materialsState) moveCursor(delta int) {
	if len(s.candidates) == 0 {
		return
	}
	s.cursor = (s.cursor + delta + len(s.candidates)) % len(s.candidates)
}

func (s *materialsState) approved() []host.MaterialsCandidate {
	out := make([]host.MaterialsCandidate, 0, len(s.candidates))
	for i, c := range s.candidates {
		if i < len(s.selected) && s.selected[i] {
			out = append(out, c)
		}
	}
	return out
}

// ── 渲染 ──

func materialsModalSize(width, height int) (boxW, boxH int) {
	boxW = width - 4
	if boxW > 92 {
		boxW = 92
	}
	if boxW < 60 {
		boxW = 60
	}
	boxH = height - 4
	if boxH > 28 {
		boxH = 28
	}
	if boxH < 14 {
		boxH = 14
	}
	return
}

// renderMaterialsModal 渲染整个素材收集 modal。
//
// collecting：展示 spinner + 思考过程预览（让用户知道 LLM 在干活）。
// selecting：展示候选列表 + 操作提示。
// done：短暂展示"已保存 N 条"，调用方应随后清空 state。
func renderMaterialsModal(width, height int, state *materialsState, spinnerFrame string) string {
	if state == nil {
		return ""
	}
	boxW, boxH := materialsModalSize(width, height)
	contentW := paddedModalContentWidth(boxW)
	if contentW < 24 {
		contentW = 24
	}

	var body string
	switch state.phase {
	case materialsPhaseCollecting:
		body = renderMaterialsCollecting(contentW, boxH-4, state, spinnerFrame)
	case materialsPhaseSelecting:
		body = renderMaterialsSelecting(contentW, boxH-4, state)
	case materialsPhaseDone:
		body = renderMaterialsDone(contentW, state)
	}

	title := "素材收集"
	hint := ""
	switch state.phase {
	case materialsPhaseCollecting:
		hint = "Esc 取消"
	case materialsPhaseSelecting:
		hint = "↑↓ 移动 · Space 切换 · a 全选 · n 全不选 · Enter 保存 · Esc 取消"
	case materialsPhaseDone:
		hint = "Enter 关闭"
	}
	return renderPaddedModalFrame(boxW, boxH, title, hint, splitBodyLines(body, contentW))
}

func renderMaterialsCollecting(width, height int, state *materialsState, spinnerFrame string) string {
	headerStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)

	header := headerStyle.Render(spinnerFrame + " 正在收集素材…")
	prompt := "需求：" + truncateWidth(state.userPrompt, width-8)
	promptLine := mutedStyle.Render(prompt)

	// 思考预览：截尾，最多 height-4 行让 footer hint 不被挤掉
	preview := strings.TrimSpace(state.thinkingText)
	if preview == "" {
		preview = "（等待模型响应…）"
	}
	previewLines := strings.Split(preview, "\n")
	maxLines := height - 4
	if maxLines < 2 {
		maxLines = 2
	}
	if len(previewLines) > maxLines {
		previewLines = append(previewLines[:maxLines-1], "…（截断）")
	}
	previewBlock := dimStyle.Render(strings.Join(previewLines, "\n"))

	lines := []string{
		header,
		promptLine,
		"",
		previewBlock,
	}
	return strings.Join(lines, "\n")
}

func renderMaterialsSelecting(width, height int, state *materialsState) string {
	if len(state.candidates) == 0 {
		errStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
		return errStyle.Render("（LLM 未产出任何候选，按 Esc 退出重试）")
	}

	headerStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	catStyle := lipgloss.NewStyle().Foreground(colorAccent2)
	selStyle := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	dimSelStyle := lipgloss.NewStyle().Foreground(colorDim)
	titleStyle := lipgloss.NewStyle().Foreground(bodyTextColor).Bold(true)
	contentStyle := lipgloss.NewStyle().Foreground(bodyTextColor)

	header := headerStyle.Render(fmt.Sprintf("LLM 给出 %d 条候选（默认全选，Space 反选不要的）：", len(state.candidates)))

	lines := []string{header, ""}
	for i, c := range state.candidates {
		isCur := i == state.cursor
		isSel := i < len(state.selected) && state.selected[i]

		// 选中标记：✓ / · ；光标用 ›
		var mark string
		if isSel {
			mark = selStyle.Render("[✓]")
		} else {
			mark = dimSelStyle.Render("[ ]")
		}
		cursor := "  "
		if isCur {
			cursor = "› "
		}

		cat := strings.TrimSpace(c.Category)
		if cat == "" {
			cat = "misc"
		}
		catTag := catStyle.Render("[" + cat + "]")
		title := titleStyle.Render(strings.TrimSpace(c.Title))

		// 第一行：› [✓] [naming] 赛博朋克企业名候选
		line1 := cursor + mark + " " + catTag + " " + title

		// 第二行（content 预览，缩进 + 截断）
		contentPreview := truncateWidth(strings.ReplaceAll(strings.TrimSpace(c.Content), "\n", " "), width-6)
		line2 := "    " + contentStyle.Render(contentPreview)

		lines = append(lines, line1, line2)
		if i < len(state.candidates)-1 {
			lines = append(lines, mutedStyle.Render(strings.Repeat("─", width-4)))
		}
	}

	// 截断到 height 行，保留头部
	if len(lines) > height {
		lines = append(lines[:height-1], mutedStyle.Render("…（更多候选用 ↑↓ 浏览）"))
	}
	return strings.Join(lines, "\n")
}

func renderMaterialsDone(width int, state *materialsState) string {
	okStyle := lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	count := 0
	for _, v := range state.selected {
		if v {
			count++
		}
	}
	return okStyle.Render(fmt.Sprintf("✓ 已保存 %d 条素材到 meta/materials.json", count))
}

// splitBodyLines 把多行字符串拆成 []string，方便 renderPaddedModalFrame 处理。
// 保留空行（modal frame 会按 contentW 对齐补空格）。
func splitBodyLines(body string, contentW int) []string {
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// ── 键盘交互 ──

// handleMaterialsKey 处理素材 modal 内的按键。返回 (model, cmd)；
// 由 handleBlockingModalKey 统一包装成 (model, cmd, handled=true)。
// 所有 modal 内按键都被视为"已消费"——escape 取消、enter 保存、其他键忽略——
// 避免落到 textarea 改动底层输入。
func (m Model) handleMaterialsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.materials == nil {
		return m, nil
	}

	state := m.materials

	switch msg.Type {
	case tea.KeyEsc:
		// collecting 阶段取消会泄漏后台 goroutine，但 ctx cancel 会让 LLM 流尽快返回；
		// 简化处理：直接关闭 modal，host 端的 stream 自然超时。
		m.materials = nil
		return m, nil
	case tea.KeyUp:
		if state.phase == materialsPhaseSelecting {
			state.moveCursor(-1)
		}
		return m, nil
	case tea.KeyDown:
		if state.phase == materialsPhaseSelecting {
			state.moveCursor(1)
		}
		return m, nil
	case tea.KeySpace:
		if state.phase == materialsPhaseSelecting {
			state.toggleCurrent()
		}
		return m, nil
	case tea.KeyEnter:
		switch state.phase {
		case materialsPhaseSelecting:
			approved := state.approved()
			if len(approved) == 0 {
				return m, nil
			}
			saved, err := m.runtime.MaterialsApprove(approved)
			if err != nil {
				state.err = err
				return m, nil
			}
			_ = saved
			state.phase = materialsPhaseDone
			// 短暂展示后清空（用一次 tick 触发）。简化为：直接清空
			m.materials = nil
			m.applyEvent(host.Event{
				Time:     time.Now(),
				Category: "SYSTEM",
				Summary:  fmt.Sprintf("已保存 %d 条素材到素材库", len(approved)),
				Level:    "info",
			})
			m.refreshEventViewport()
			return m, nil
		case materialsPhaseDone:
			m.materials = nil
			return m, nil
		}
		return m, nil
	}

	// 字母键：a 全选 / n 全不选
	if msg.Type == tea.KeyRunes {
		switch strings.ToLower(string(msg.Runes)) {
		case "a":
			if state.phase == materialsPhaseSelecting {
				state.selectAll(true)
			}
		case "n":
			if state.phase == materialsPhaseSelecting {
				state.selectAll(false)
			}
		}
	}

	return m, nil
}

// ── tea.Cmd 工厂 ──

// materialsCollectResultMsg 是收集完成的回调消息。
type materialsCollectResultMsg struct {
	candidates []host.MaterialsCandidate
	raw        string
	err        error
}

// runMaterialsCollect 在后台 goroutine 调用 host.MaterialsCollect，避免阻塞 TUI。
// onProgress 通过单独的 channel 推到 TUI（目前 MVP 用一个简化的 partial 消息）。
func runMaterialsCollect(runtime *host.Host, userPrompt string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), materialsCollectDeadline)
		defer cancel()

		// onProgress 在 GenerateStream 内被同步调用，但本回调运行在 host 端 goroutine 里——
		// 不能直接更新 state（race）。MVP 阶段先丢掉 progress，等候选回来一次性渲染。
		candidates, raw, err := runtime.MaterialsCollect(ctx, userPrompt, nil)
		return materialsCollectResultMsg{
			candidates: candidates,
			raw:        raw,
			err:        err,
		}
	}
}

const materialsCollectDeadline = 130 * 1_000_000_000 // 130s，比 host 内部 timeout 长 10s 留余量
