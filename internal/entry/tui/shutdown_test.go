package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
)

// 退出时先取消再等在途创作：跟踪的命令没结束 wait 就超时，结束后立刻返回。
func TestInflightWaitsForTrackedCommands(t *testing.T) {
	var f inflight
	release := make(chan struct{})
	cmd := f.track(func() tea.Msg { <-release; return nil })
	go cmd()
	if f.wait(20 * time.Millisecond) {
		t.Fatal("wait must time out while the command is still running")
	}
	close(release)
	if !f.wait(time.Second) {
		t.Fatal("wait must return once tracked commands finish")
	}
}

// 创作驱动命令必须被跟踪：否则退出时无人等它把任务放回队列。
func TestQuickWriteCommandIsTrackedUntilItReturns(t *testing.T) {
	m := studioModel(t, 150, 40)
	cmd := m.startQuickWriteCmd(quickParams{projectID: "letters", premise: "亡者来信", chapters: 8})
	if m.inflight.wait(10 * time.Millisecond) {
		t.Fatal("quick write must be tracked before it runs")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	<-done
	if !m.inflight.wait(time.Second) {
		t.Fatal("quick write must be released once it returns")
	}
}

// 被占用的租约用创作语言解释，不露原始英文。
func TestHeldLeaseIsExplainedInCreationLanguage(t *testing.T) {
	m := studioModel(t, 150, 40)
	updated, _ := m.Update(quickDoneMsg{gen: m.bench.gen, err: fmt.Errorf("op: %w", domainmodel.ErrOperationHeld)})
	m = updated.(model)
	if !strings.Contains(m.bench.err, "租约") || !strings.Contains(m.bench.err, "/c") || strings.Contains(m.bench.err, "held") {
		t.Fatalf("held lease must be explained: %q", m.bench.err)
	}
}
