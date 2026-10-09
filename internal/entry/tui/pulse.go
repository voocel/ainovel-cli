package tui

import (
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	colorful "github.com/lucasb-eyer/go-colorful"
)

// 创作进行中的动画：现场标题线行首一颗呼吸的星，一道光沿标题与横线从左往右流过。光在模型
// 持续吐字时走得快、安静下来逐渐放慢——看一眼就知道此刻是在出字还是在等。动画有自己的节拍，
// 只在创作中运行；界面只重绘变了的行，开销是每帧一次 View。
const (
	pulseInterval = 60 * time.Millisecond
	flowFast      = 2.4  // 光带每帧前进的格数：最近一秒内收到过活动
	flowSlow      = 0.6  // 安静时
	flowEase      = 0.12 // 流速每帧向目标靠近的比例
	flowRadius    = 7    // 光带半宽（格）
	flowGap       = 28   // 一趟流完到下一趟进场之间的空白（格）
	glowSteps     = 8    // 光带亮度分级：同级相邻的格合并成一段着色
	starHold      = 3    // 星每 starHold 帧换一格
	spinnerHold   = 2    // 步骤行与顶栏的转圈每 spinnerHold 帧换一格
)

// starFrames 由小到大再回落，一呼一吸。
var starFrames = []string{"·", "✧", "✦", "✶", "✷", "✸", "✹", "✸", "✷", "✶", "✦", "✧"}

type pulseMsg struct{ gen int }

// pulseState 是动画的全部状态；frame 也驱动步骤行与顶栏的转圈，所有动画同一个节拍。
type pulseState struct {
	running bool
	frame   int
	flow    float64   // 光带走过的格数
	speed   float64   // 当前流速，向目标流速缓动
	heard   time.Time // 最近一次收到活动的时刻
}

// start 启动节拍；已在运行就不再起第二条。
func (p *pulseState) start(gen int) tea.Cmd {
	if p.running {
		return nil
	}
	p.running = true
	return pulseTick(gen)
}

func pulseTick(gen int) tea.Cmd {
	return tea.Tick(pulseInterval, func(time.Time) tea.Msg { return pulseMsg{gen: gen} })
}

// advance 走一帧：流速向目标缓动，光带按流速前进。
func (p *pulseState) advance(now time.Time) {
	target := flowSlow
	if now.Sub(p.heard) < time.Second {
		target = flowFast
	}
	p.speed += (target - p.speed) * flowEase
	p.flow += p.speed
	p.frame++
}

func (p pulseState) spinner() string {
	return spinnerFrames[p.frame/spinnerHold%len(spinnerFrames)]
}

// star 行首的星：形状随呼吸变大变小，颜色由强调色向辉光色同步变亮。
func (p pulseState) star() string {
	i := p.frame / starHold % len(starFrames)
	peak := len(starFrames) / 2
	t := 1 - math.Abs(float64(i-peak))/float64(peak)
	return blendStyle(benchColors.Accent, t).Bold(true).Render(starFrames[i])
}

// flowLine 标题与其后的横线铺满 width，光带流过之处，文字由次要色、横线由边框色向辉光色过渡；
// 横线在光带最亮的几格加粗，像一段发亮的笔锋。
func (p pulseState) flowLine(title string, width int) string {
	cycle := float64(width + 2*flowRadius + flowGap)
	center := math.Mod(p.flow, cycle) - flowRadius
	var out strings.Builder
	x := 0
	paint := func(text string, base lipgloss.AdaptiveColor) {
		var run strings.Builder
		level := -1
		flush := func() {
			if run.Len() > 0 {
				out.WriteString(blendStyle(base, float64(level)/glowSteps).Render(run.String()))
				run.Reset()
			}
		}
		for _, r := range text {
			w := lipgloss.Width(string(r))
			if l := glowLevel(float64(x) + float64(w)/2 - center); l != level {
				flush()
				level = l
			}
			if r == '─' && level > glowSteps/2 {
				r = '━'
			}
			run.WriteRune(r)
			x += w
		}
		flush()
	}
	paint(title+" ", benchColors.Muted)
	paint(strings.Repeat("─", max(0, width-lipgloss.Width(title)-1)), benchColors.Border)
	return out.String()
}

// glowLevel 离光带中心 d 格处的亮度级：余弦衰减，半宽之外为 0。
func glowLevel(d float64) int {
	d = math.Abs(d)
	if d >= flowRadius {
		return 0
	}
	return int(math.Round((1 + math.Cos(math.Pi*d/flowRadius)) / 2 * glowSteps))
}

// blendStyle 由 base 向辉光色混合 t（0–1）的前景样式；t 为 0 时保留自适应色。
func blendStyle(base lipgloss.AdaptiveColor, t float64) lipgloss.Style {
	if t <= 0 {
		return lipgloss.NewStyle().Foreground(base)
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(tone(base).BlendLab(tone(benchColors.Glow), t).Hex()))
}

// tone 取自适应色在当前背景下的那一个；调色板都是合法的十六进制色。
func tone(c lipgloss.AdaptiveColor) colorful.Color {
	hex := c.Light
	if lipgloss.HasDarkBackground() {
		hex = c.Dark
	}
	color, _ := colorful.Hex(hex)
	return color
}
