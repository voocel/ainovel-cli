package tui

import "github.com/charmbracelet/lipgloss"

// 主题色板
var (
	colorText    = lipgloss.Color("#e0d8c8")
	colorDim     = lipgloss.Color("#666666")
	colorAccent  = lipgloss.Color("#d4a017") // 琥珀黄
	colorSuccess = lipgloss.Color("#2ecc71") // 冷绿
	colorError   = lipgloss.Color("#e74c3c") // 朱红
	colorReview  = lipgloss.Color("#e67e22") // 橙色
)

// 状态标签颜色映射
var statusColors = map[string]lipgloss.Color{
	"READY":    colorDim,
	"RUNNING":  colorSuccess,
	"REVIEW":   colorReview,
	"REWRITE":  colorReview,
	"COMPLETE": colorSuccess,
	"ERROR":    colorError,
}

// 事件分类颜色映射
var categoryColors = map[string]lipgloss.Color{
	"TOOL":   colorText,
	"SYSTEM": colorAccent,
	"REVIEW": colorReview,
	"CHECK":  colorSuccess,
	"ERROR":  colorError,
	"AGENT":  colorDim,
}

// 基础样式
var (
	baseBorder = lipgloss.RoundedBorder()

	topBarStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Padding(0, 1)

	statusCapsule = lipgloss.NewStyle().
			Padding(0, 1).
			Bold(true)

	panelTitleStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	fieldLabelStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Width(10)

	fieldValueStyle = lipgloss.NewStyle().
			Foreground(colorText)

	highlightValueStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	cardTitleStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Italic(true)

	cardContentStyle = lipgloss.NewStyle().
				Foreground(colorText)
)
