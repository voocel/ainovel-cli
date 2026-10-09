package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// 节拍只有一条：重复启动不再起第二条，创作停下即停，换了作品的旧节拍作废。
func TestPulseRunsOneTickOnlyWhileWriting(t *testing.T) {
	m := studioModel(t, 150, 40)
	if m.bench.pulse.start(m.bench.gen) == nil || m.bench.pulse.start(m.bench.gen) != nil {
		t.Fatal("a second start must not spawn another tick")
	}
	updated, cmd := m.Update(pulseMsg{gen: m.bench.gen})
	m = updated.(model)
	if cmd == nil || m.bench.pulse.frame != 1 {
		t.Fatalf("a tick while writing must advance and re-arm: frame %d", m.bench.pulse.frame)
	}
	if _, cmd := m.Update(pulseMsg{gen: m.bench.gen - 1}); cmd != nil {
		t.Fatal("a tick from another workbench must die")
	}
	m.bench.writing = false
	updated, cmd = m.Update(pulseMsg{gen: m.bench.gen})
	if m = updated.(model); cmd != nil || m.bench.pulse.running {
		t.Fatal("the tick must stop once creation stops")
	}
	if m.bench.pulse.start(m.bench.gen) == nil {
		t.Fatal("the next run must be able to start the tick again")
	}
}

// 光在持续收到活动时走快，安静下来缓缓放慢。
func TestPulseFlowsFasterWhileHearing(t *testing.T) {
	now := time.Now()
	var p pulseState
	p.heard = now
	for range 60 {
		p.advance(now)
	}
	if p.speed < flowFast*0.95 {
		t.Fatalf("flow must speed up while activity arrives: %.2f", p.speed)
	}
	later := now.Add(2 * time.Second)
	p.advance(later)
	if p.speed <= flowSlow || p.speed >= flowFast {
		t.Fatalf("flow must ease down, not jump: %.2f", p.speed)
	}
	for range 60 {
		p.advance(later)
	}
	if p.speed > flowSlow*1.05 {
		t.Fatalf("flow must settle to the slow pace when quiet: %.2f", p.speed)
	}
}

// 光带不改文字：标题原样、横线铺满给定宽度（最亮处加粗）；光带所到之处与别处着色不同，且会移动。
func TestFlowLineMovesLightWithoutChangingText(t *testing.T) {
	renderer := lipgloss.DefaultRenderer()
	profile, dark := renderer.ColorProfile(), renderer.HasDarkBackground()
	t.Cleanup(func() { renderer.SetColorProfile(profile); renderer.SetHasDarkBackground(dark) })
	renderer.SetColorProfile(termenv.TrueColor)
	title, width := "AI 创作现场 · 正在规划故事蓝图", 80
	want := title + " " + strings.Repeat("─", width-lipgloss.Width(title)-1)
	for _, isDark := range []bool{false, true} {
		renderer.SetHasDarkBackground(isDark)
		var frames []string
		for _, flow := range []float64{0, flowRadius + 10, flowRadius + 50} {
			line := pulseState{flow: flow}.flowLine(title, width)
			if got := strings.ReplaceAll(ansi.Strip(line), "━", "─"); got != want {
				t.Fatalf("flow changed the text:\n%q\n%q", got, want)
			}
			frames = append(frames, line)
		}
		if frames[1] == frames[2] || frames[0] == frames[1] {
			t.Fatal("the light must move along the line")
		}
		if strings.Contains(frames[0], "━") || !strings.Contains(frames[2], "━") {
			t.Fatal("the rule thickens only where the light passes")
		}
	}
	if glowLevel(0) != glowSteps || glowLevel(flowRadius) != 0 || glowLevel(flowRadius/2) <= 0 {
		t.Fatal("glow must peak at the centre and fade out at the radius")
	}
}
