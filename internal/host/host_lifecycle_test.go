package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestInterventionStopsWhenPersistenceFails(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.RunMeta.Init("default", "test", "model"); err != nil {
		t.Fatalf("RunMeta.Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "run.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{store: st, events: make(chan Event, 4)}
	err := h.doIntervention("修改主角性格", false)
	if err == nil || !strings.Contains(err.Error(), "持久化失败") {
		t.Fatalf("expected persistence error, got %v", err)
	}
	// 公共 Steer 必须等待异步任务并把同一业务错误返回给 TUI；不能只表示 goroutine
	// 启动成功，否则界面永远收不到真实失败。
	err = h.Steer("修改主角性格")
	if err == nil || !strings.Contains(err.Error(), "持久化失败") {
		t.Fatalf("Steer should return persistence error, got %v", err)
	}
}

func TestCloseWaitsForRegisteredAsyncWork(t *testing.T) {
	h := &Host{
		observer: &observer{},
		engine:   &engine{},
		events:   make(chan Event, 1),
		streamCh: make(chan string, 1),
		done:     make(chan struct{}, 1),
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if !h.launchAsync(func() {
		close(started)
		<-release
	}) {
		t.Fatal("launchAsync unexpectedly refused")
	}
	<-started
	closed := make(chan struct{})
	go func() {
		h.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned before async work finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after async work finished")
	}
}
