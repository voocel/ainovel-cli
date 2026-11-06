package tui

import "charm.land/lipgloss/v2"

// darkBackground 是终端底色是否为深色。启动时向终端询问底色（tea.RequestBackgroundColor），
// 回复到达前按深色画；配色在渲染时才按它取色，回复一到整屏随之换色。
var darkBackground = true

// adaptiveColor 是一对浅底、深底配色（十六进制），渲染时按终端底色取其一。
type adaptiveColor struct{ Light, Dark string }

func (c adaptiveColor) hex() string {
	if darkBackground {
		return c.Dark
	}
	return c.Light
}

func (c adaptiveColor) RGBA() (r, g, b, a uint32) { return lipgloss.Color(c.hex()).RGBA() }

// Keep the terminal's own surface; only selected controls paint a background.
// Separate foreground pairs preserve legibility on both light and dark themes.
var benchColors = struct {
	Text, Muted, Border, Accent      adaptiveColor
	SelectedText, SelectedBackground adaptiveColor
	Faint, InputBackground           adaptiveColor
	Warning, Error, Success, Glow    adaptiveColor
}{
	Text:               adaptiveColor{Light: "#252F30", Dark: "#DDE3DD"},
	Muted:              adaptiveColor{Light: "#56635D", Dark: "#A8B5AC"},
	Border:             adaptiveColor{Light: "#86948A", Dark: "#53645A"},
	Accent:             adaptiveColor{Light: "#356B58", Dark: "#A5C6AA"},
	SelectedText:       adaptiveColor{Light: "#244A39", Dark: "#DCE9DC"},
	SelectedBackground: adaptiveColor{Light: "#DDE7D9", Dark: "#2B3C30"},
	Faint:              adaptiveColor{Light: "#8B9294", Dark: "#8E9699"},
	InputBackground:    adaptiveColor{Light: "#ECEEEF", Dark: "#303438"},
	Warning:            adaptiveColor{Light: "#92541B", Dark: "#F0B477"},
	Error:              adaptiveColor{Light: "#AD352F", Dark: "#FF928B"},
	Success:            adaptiveColor{Light: "#32633B", Dark: "#8BCB94"},
	Glow:               adaptiveColor{Light: "#0E6646", Dark: "#E4FBE6"}, // 创作中动画的光：比强调色更亮（深底）或更深（浅底）
}

type benchStyles struct {
	Text, Muted, Border, Accent, Focus lipgloss.Style
	Selected, Warning, Error, Success  lipgloss.Style
	Title, Thought                     lipgloss.Style
	// Primary 是可以按下的主按钮：强调色底、反色字；Button 是次一级的按钮底色。Link 是可点的
	// 文字入口，像网页链接一样带下划线。
	Primary, Button, Link lipgloss.Style
}

var benchTheme = newBenchStyles()

func newBenchStyles() benchStyles {
	return benchStyles{
		Text:     lipgloss.NewStyle().Foreground(benchColors.Text),
		Muted:    lipgloss.NewStyle().Foreground(benchColors.Muted),
		Border:   lipgloss.NewStyle().Foreground(benchColors.Border),
		Accent:   lipgloss.NewStyle().Foreground(benchColors.Accent),
		Focus:    lipgloss.NewStyle().Foreground(benchColors.Accent).Bold(true),
		Selected: lipgloss.NewStyle().Foreground(benchColors.SelectedText).Background(benchColors.SelectedBackground).Bold(true),
		Warning:  lipgloss.NewStyle().Foreground(benchColors.Warning).Bold(true),
		Error:    lipgloss.NewStyle().Foreground(benchColors.Error).Bold(true),
		Success:  lipgloss.NewStyle().Foreground(benchColors.Success),
		Title:    lipgloss.NewStyle().Foreground(benchColors.Text).Bold(true),
		// 模型的思考是过程不是产出：比说明再淡一层、斜体，让大纲、设定、正文站在前面。
		Thought: lipgloss.NewStyle().Foreground(benchColors.Faint).Italic(true),
		Primary: lipgloss.NewStyle().Foreground(benchColors.SelectedBackground).Background(benchColors.Accent).Bold(true),
		Button:  lipgloss.NewStyle().Foreground(benchColors.SelectedText).Background(benchColors.SelectedBackground).Bold(true),
		Link:    lipgloss.NewStyle().Foreground(benchColors.Muted).Underline(true),
	}
}
