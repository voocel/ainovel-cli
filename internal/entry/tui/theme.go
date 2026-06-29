package tui

import "github.com/charmbracelet/lipgloss"

// 主题色板 — 暖调书卷气
// AdaptiveColor: Light = 亮底色值, Dark = 暗底色值
//
// 设计原则：Light 一档稳定不动（亮底已调出满意效果）；Dark 一档统一比 Light
// 提亮 ~25% lightness、略升饱和，保证暗底有足够对比度（colorDim 之前 #6b6355
// 在 #1c1c1c 黑底上几乎不可见，分隔线/辅助文字全消失）。
//
// colorAccent2 暗底从 #7a9e7e 改为青绿 #5fb8a3，跟 colorSuccess 的"健康绿"拉
// 开 — 之前两者完全同色，让 architect agent 的色标和"高命中"喜悦感混淆。
// bodyTextColor 是"中性Chính văn"的前景策略：
//   - 暗色终端 → NoColor，继承终端Mặc định前景，避免我们硬塞 #e8e0d0 米白在用户自配
//     的暖底/冷底主题上撞色（用户实测暗底Mặc định色更耐读）。
//   - 亮色终端 → 用 colorText 的 Light 档（深棕 #3d3529），保留品牌暖调；
//     亮底Mặc định黑色对比度太硬，原本调过的深棕在亮底视觉更柔和。
//
// AdaptiveColor 两端都必须给颜色值，没有"Không có色"档，所以这里启动时判一次背景，
// 之后所有Tổng quan值/ChươngChính văn/命令描述等"中性Chính văn"统一引用 bodyTextColor。
var bodyTextColor lipgloss.TerminalColor = func() lipgloss.TerminalColor {
	if lipgloss.HasDarkBackground() {
		return lipgloss.NoColor{}
	}
	return lipgloss.Color("#3d3529")
}()

var (
	colorText    = lipgloss.AdaptiveColor{Light: "#3d3529", Dark: "#e8e0d0"}
	colorDim     = lipgloss.AdaptiveColor{Light: "#8a7e6b", Dark: "#8a8175"}
	colorMuted   = lipgloss.AdaptiveColor{Light: "#7a7060", Dark: "#b8b09c"}
	colorAccent  = lipgloss.AdaptiveColor{Light: "#b8860b", Dark: "#e5b449"}
	colorAccent2 = lipgloss.AdaptiveColor{Light: "#3d7a42", Dark: "#5fb8a3"}
	colorRunning = lipgloss.AdaptiveColor{Light: "#6f8641", Dark: "#b5d075"}
	colorSuccess = lipgloss.AdaptiveColor{Light: "#3d7a42", Dark: "#7ec488"}
	colorError   = lipgloss.AdaptiveColor{Light: "#b5433a", Dark: "#e07060"}
	colorReview  = lipgloss.AdaptiveColor{Light: "#b07530", Dark: "#e09b5a"}
	colorContext = lipgloss.AdaptiveColor{Light: "#6b5a9e", Dark: "#a890d8"}
	colorTool    = lipgloss.AdaptiveColor{Light: "#3a7a8a", Dark: "#7ec5d8"}
)

// Trạng thái标签颜色映射
var statusColors = map[string]lipgloss.AdaptiveColor{
	"READY":    colorDim,
	"PAUSING":  colorAccent,
	"PAUSED":   colorAccent,
	"RUNNING":  colorRunning,
	"REVIEW":   colorReview,
	"REWRITE":  colorReview,
	"COMPLETE": colorSuccess,
	"ERROR":    colorError,
}

// Trạng thái展示：图标 + 中文标签。与整体暖调主题一致，避免实心色块突兀。
// RUNNING 的 icon 留Rỗng，由 spinner frame 动态填充，让动态感融入Trạng thái指示本身。
var statusDisplay = map[string]struct {
	icon  string
	label string
}{
	"READY":    {"○", "就绪"},
	"RUNNING":  {"", "Đang chạy"},
	"REVIEW":   {"◆", "审阅"},
	"REWRITE":  {"◆", "返工"},
	"COMPLETE": {"●", "Hoàn thành"},
	"PAUSED":   {"⏸", "Tạm dừng"},
	"PAUSING":  {"⏸", "Tạm dừng中"},
	"ERROR":    {"✕", "Lỗi"},
}

// 事件分类颜色映射
var categoryColors = map[string]lipgloss.AdaptiveColor{
	"DISPATCH": colorAccent,
	"DONE":     colorSuccess,
	"TOOL":     colorTool,
	"SYSTEM":   colorAccent,
	"USER":     colorAccent2,
	"REVIEW":   colorReview,
	"CHECK":    colorSuccess,
	"ERROR":    colorError,
	"AGENT":    colorMuted,
	"CONTEXT":  colorContext,
	"COMPACT":  colorContext,
}

// 基础样式
var (
	baseBorder = lipgloss.RoundedBorder()

	topBarStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Padding(0, 1)

	statusIconStyle = lipgloss.NewStyle().
			Bold(true)

	statusLabelStyle = lipgloss.NewStyle().
				Foreground(colorText)

	panelTitleStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	fieldLabelStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Width(10)

	// fieldValueStyle / cardContentStyle 用 bodyTextColor —— Tổng quan区的值（运行态、
	// Đã hoàn thànhChương数、字数等）、Đại cương条目、角色列表、Tóm tắt chương等"中性Chính văn内容"
	// 在暗底跟随终端Mặc định前景色（避免硬塞米白撞主题），亮底走深棕保留暖调。
	// 语义性强的元素（Tiêu đề、高亮值、Trạng thái、Lỗi、命中率染色等）仍走 colorAccent /
	// colorError 等主题色。
	fieldValueStyle = lipgloss.NewStyle().Foreground(bodyTextColor)

	highlightValueStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	contextUsageMetaStyle = lipgloss.NewStyle().
				Foreground(colorDim)

	cardTitleStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	cardContentStyle = lipgloss.NewStyle().Foreground(bodyTextColor)
)
