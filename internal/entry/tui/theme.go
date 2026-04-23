package tui

import "github.com/charmbracelet/lipgloss"

// 主题色板 — 暖调书卷气
// AdaptiveColor: Light = 亮底色值, Dark = 暗底色值
var (
	colorText    = lipgloss.AdaptiveColor{Light: "#3d3529", Dark: "#e8e0d0"}
	colorDim     = lipgloss.AdaptiveColor{Light: "#8a7e6b", Dark: "#6b6355"}
	colorMuted   = lipgloss.AdaptiveColor{Light: "#7a7060", Dark: "#a09880"}
	colorAccent  = lipgloss.AdaptiveColor{Light: "#b8860b", Dark: "#c9953c"}
	colorAccent2 = lipgloss.AdaptiveColor{Light: "#3d7a42", Dark: "#7a9e7e"}
	colorRunning = lipgloss.AdaptiveColor{Light: "#6f8641", Dark: "#9ab65b"}
	colorSuccess = lipgloss.AdaptiveColor{Light: "#3d7a42", Dark: "#7a9e7e"}
	colorError   = lipgloss.AdaptiveColor{Light: "#b5433a", Dark: "#c45c4a"}
	colorReview  = lipgloss.AdaptiveColor{Light: "#b07530", Dark: "#cc8844"}
	colorContext = lipgloss.AdaptiveColor{Light: "#6b5a9e", Dark: "#8b7bb5"}
	colorTool    = lipgloss.AdaptiveColor{Light: "#3a7a8a", Dark: "#6b9dad"}
)

// 状态标签颜色映射
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

// 状态展示：图标 + 中文标签。与整体暖调主题一致，避免实心色块突兀。
// RUNNING 的 icon 留空，由 spinner frame 动态填充，让动态感融入状态指示本身。
var statusDisplay = map[string]struct {
	icon  string
	label string
}{
	"READY":    {"○", "就绪"},
	"RUNNING":  {"", "运行中"},
	"REVIEW":   {"◆", "审阅"},
	"REWRITE":  {"◆", "返工"},
	"COMPLETE": {"●", "完成"},
	"PAUSED":   {"⏸", "暂停"},
	"PAUSING":  {"⏸", "暂停中"},
	"ERROR":    {"✕", "错误"},
}

// 事件分类颜色映射
var categoryColors = map[string]lipgloss.AdaptiveColor{
	"DISPATCH": colorAccent,
	"DONE":     colorSuccess,
	"TOOL":     colorTool,
	"SYSTEM":   colorAccent,
	"REVIEW":  colorReview,
	"CHECK":   colorSuccess,
	"ERROR":   colorError,
	"AGENT":   colorMuted,
	"CONTEXT": colorContext,
	"COMPACT": colorContext,
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

	fieldValueStyle = lipgloss.NewStyle().
			Foreground(colorText)

	highlightValueStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	contextUsageMetaStyle = lipgloss.NewStyle().
				Foreground(colorDim)

	cardTitleStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	cardContentStyle = lipgloss.NewStyle().
				Foreground(colorText)
)
